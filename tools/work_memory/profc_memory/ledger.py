"""Append-only evidence and explicit supersession; SQLite is the canonical store."""

from contextlib import contextmanager
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import threading
from urllib.parse import urlsplit

from .collect import sanitize_content
from .evidence import complete_quote


EVENT_KINDS = frozenset({'proposal', 'decision', 'progress', 'verification', 'correction',
                         'scope_extension', 'open_question', 'risk', 'source_update'})


def _json(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':'))


def _time(value):
    if value is None:
        return None
    parsed = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if parsed.tzinfo is None:
        raise ValueError('timestamps must include a timezone')
    return parsed


def _now():
    return datetime.now(timezone.utc).isoformat()


def _text(value, label):
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f'{label} must be a nonempty string')
    return value


def _revision_order(left, right):
    if left['revision'] == right['revision']:
        return 0
    if left['kind'] == right['kind'] == 'document' and left['revision'].isdigit() and right['revision'].isdigit():
        return (int(left['revision']) > int(right['revision'])) - (int(left['revision']) < int(right['revision']))
    a, b = _time(left.get('modified_at')), _time(right.get('modified_at'))
    if a is not None and b is not None and a != b:
        return (a > b) - (a < b)
    return None


class Ledger:
    def __init__(self, db_path, owner_id):
        self.owner_id = _text(owner_id, 'owner_id')
        self.path = Path(db_path).absolute()
        # macOS exposes its root-owned temp directories through these fixed aliases.
        for alias in ('/var', '/tmp'):
            root = Path(alias)
            if self.path.is_relative_to(root) and root.is_symlink() and root.resolve() == Path('/private' + alias):
                self.path = root.resolve() / self.path.relative_to(root)
        if any(p.is_symlink() for p in (self.path, *self.path.parents)):
            raise ValueError('private ledger paths must not be symbolic links')
        self.path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(self.path.parent, 0o700)
        fd = os.open(self.path, os.O_CREAT | os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0), 0o600)
        os.fchmod(fd, 0o600)
        os.close(fd)
        self._lock = threading.RLock()
        self.db = sqlite3.connect(self.path, timeout=30, isolation_level=None, check_same_thread=False)
        self.db.row_factory = sqlite3.Row
        self.db.execute('PRAGMA foreign_keys=ON')
        self.db.executescript('''
            CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS works (
                work_id TEXT PRIMARY KEY, title TEXT NOT NULL, aliases TEXT NOT NULL,
                version INTEGER NOT NULL DEFAULT 1, updated_at TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS sources (
                source_id TEXT NOT NULL, revision TEXT NOT NULL, data TEXT NOT NULL,
                processed INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(source_id, revision));
            CREATE TABLE IF NOT EXISTS source_works (
                source_id TEXT NOT NULL, revision TEXT NOT NULL, work_id TEXT NOT NULL REFERENCES works(work_id),
                PRIMARY KEY(source_id, revision, work_id),
                FOREIGN KEY(source_id, revision) REFERENCES sources(source_id, revision));
            CREATE TABLE IF NOT EXISTS events (
                event_id TEXT PRIMARY KEY, source_id TEXT NOT NULL, revision TEXT NOT NULL,
                work_id TEXT NOT NULL REFERENCES works(work_id), data TEXT NOT NULL,
                FOREIGN KEY(source_id, revision) REFERENCES sources(source_id, revision));
            CREATE TABLE IF NOT EXISTS cursors (key TEXT PRIMARY KEY, value TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS failures (
                id INTEGER PRIMARY KEY, source_id TEXT NOT NULL, error TEXT NOT NULL, observed_at TEXT NOT NULL);
        ''')
        try:
            with self._transaction():
                existing = self.db.execute("SELECT value FROM meta WHERE key='owner_id'").fetchone()
                if existing and existing['value'] != owner_id:
                    raise PermissionError('ledger belongs to another owner')
                self.db.execute("INSERT OR IGNORE INTO meta VALUES ('owner_id', ?)", (owner_id,))
        except Exception:
            self.db.close()
            raise

    @contextmanager
    def _transaction(self):
        with self._lock:
            self.db.execute('BEGIN IMMEDIATE')
            try:
                yield
                self.db.execute('COMMIT')
            except BaseException:
                self.db.execute('ROLLBACK')
                raise

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()

    def close(self):
        self.db.close()

    def _bump(self, work_id):
        self.db.execute('UPDATE works SET version=version+1, updated_at=? WHERE work_id=?', (_now(), work_id))

    def _work(self, work_id):
        row = self.db.execute('SELECT * FROM works WHERE work_id=?', (work_id,)).fetchone()
        if row is None:
            raise ValueError(f'unknown work_id: {work_id}')
        return dict(row)

    def register_work(self, work_id, title, aliases=None):
        _text(work_id, 'work_id')
        _text(title, 'title')
        aliases = sorted(set(aliases or []))
        for alias in aliases:
            _text(alias, 'alias')
        with self._transaction():
            previous = self.db.execute('SELECT title,aliases FROM works WHERE work_id=?', (work_id,)).fetchone()
            if previous is None:
                self.db.execute('INSERT INTO works(work_id,title,aliases,updated_at) VALUES (?,?,?,?)',
                                (work_id, title, _json(aliases), _now()))
            elif previous['title'] != title or previous['aliases'] != _json(aliases):
                self.db.execute('UPDATE works SET title=?,aliases=? WHERE work_id=?', (title, _json(aliases), work_id))
                self._bump(work_id)

    def ingest_source(self, source, work_ids, *, cursor_key=None, cursor_value=None):
        source = dict(source)
        source.setdefault('owner_id', self.owner_id)
        source.setdefault('visibility', 'owner')
        if source['owner_id'] != self.owner_id or source['visibility'] != 'owner':
            raise PermissionError('only owner-private sources are supported')
        source.setdefault('occurred_at', None)
        for field in ('source_id', 'kind', 'object_id', 'revision', 'observed_at'):
            _text(source.get(field), field)
        if not isinstance(source.get('content'), str) or not isinstance(source.get('url', ''), str):
            raise ValueError('source content and url must be strings')
        source.setdefault('url', '')
        parsed_url = urlsplit(source['url'])
        if parsed_url.username is not None or parsed_url.password is not None:
            raise ValueError('source URL must not contain credentials')
        if sanitize_content(source['content'])[1] or sanitize_content(source['url'])[1]:
            raise ValueError('source must be sanitized before persistence')
        _time(source['occurred_at'])
        _time(source['observed_at'])
        _time(source.get('modified_at'))
        identity = (source['source_id'], source['revision'])
        with self._transaction():
            for work_id in work_ids:
                self._work(work_id)
            old = self.db.execute('SELECT data FROM sources WHERE source_id=? AND revision=?', identity).fetchone()
            if old:
                before = json.loads(old['data'])
                # A later fetch of identical evidence is not a new source revision.
                if {k: v for k, v in before.items() if k != 'observed_at'} != {k: v for k, v in source.items() if k != 'observed_at'}:
                    raise ValueError('source revision is immutable; provide a new revision')
            else:
                self.db.execute('INSERT INTO sources(source_id,revision,data) VALUES (?,?,?)', (*identity, _json(source)))
            for work_id in sorted(set(work_ids)):
                changed = self.db.execute('INSERT OR IGNORE INTO source_works VALUES (?,?,?)', (*identity, work_id)).rowcount
                if changed:
                    self._bump(work_id)
                    self.db.execute('UPDATE sources SET processed=0 WHERE source_id=? AND revision=?', identity)
            if cursor_key is not None:
                self._set_cursor(cursor_key, cursor_value)
            return old is None

    def _source(self, source_id, revision=None):
        if revision is None:
            rows = self.db.execute('SELECT * FROM sources WHERE source_id=?', (source_id,)).fetchall()
            row = rows[0] if rows else None
            for candidate in rows[1:]:
                order = _revision_order(json.loads(candidate['data']), json.loads(row['data']))
                if order is None:
                    raise ValueError('source revisions are unordered; select an explicit revision')
                if order > 0:
                    row = candidate
        else:
            row = self.db.execute('SELECT * FROM sources WHERE source_id=? AND revision=?', (source_id, revision)).fetchone()
        if row is None:
            raise ValueError(f'unknown source revision: {source_id}/{revision}')
        result = json.loads(row['data'])
        result['work_ids'] = [r['work_id'] for r in self.db.execute(
            'SELECT work_id FROM source_works WHERE source_id=? AND revision=? ORDER BY work_id', (source_id, row['revision']))]
        return result

    def get_source(self, source_id, revision=None):
        with self._transaction():
            return self._source(source_id, revision)

    def pending_sources(self):
        with self._transaction():
            return [self._source(r['source_id'], r['revision']) for r in self.db.execute(
                'SELECT source_id, revision FROM sources WHERE processed=0 ORDER BY rowid').fetchall()]

    def mark_source_processed(self, source_id, revision):
        with self._transaction():
            self._source(source_id, revision)
            self.db.execute('UPDATE sources SET processed=1 WHERE source_id=? AND revision=?', (source_id, revision))

    def apply_events(self, source_id, revision, events):
        ids = []
        with self._transaction():
            source = self._source(source_id, revision)
            for supplied in events:
                event = {key: supplied.get(key) for key in ('work_id', 'kind', 'claim', 'quote', 'occurred_at', 'actor', 'stage', 'facet')}
                for field, value in event.items():
                    if field != 'occurred_at':
                        _text(value, field)
                event['status'] = supplied.get('status', 'accepted')
                if event['kind'] not in EVENT_KINDS:
                    raise ValueError('unknown event kind')
                if event['status'] not in ('accepted', 'candidate'):
                    raise ValueError('unknown event status')
                self._work(event['work_id'])
                if event['status'] == 'accepted' and event['work_id'] not in source['work_ids']:
                    raise ValueError('accepted event requires a configured strong work association')
                if not complete_quote(event['quote'], source['content']):
                    raise ValueError('event quote must preserve a complete source sentence or line')
                event['claim'] = event['quote']
                occurred = _time(event['occurred_at'])
                supersedes = supplied.get('supersedes', [])
                if not isinstance(supersedes, list) or any(not isinstance(e, str) for e in supersedes):
                    raise ValueError('supersedes must be a list of event IDs')
                event['supersedes'] = sorted(set(supersedes))
                for target in event['supersedes']:
                    previous = self.db.execute('SELECT data FROM events WHERE event_id=?', (target,)).fetchone()
                    if previous is None:
                        raise ValueError('superseded event does not exist')
                    previous = json.loads(previous['data'])
                    if previous['source_id'] == source_id and previous['revision'] != revision:
                        order = _revision_order(source, self._source(source_id, previous['revision']))
                        if order is None or order <= 0:
                            raise ValueError('older or unordered source revision cannot supersede another revision')
                    if (event['kind'] not in ('correction', 'decision') or event['status'] != 'accepted'
                            or previous['status'] != 'accepted'
                            or any(event[k] != previous[k] for k in ('work_id', 'stage', 'facet'))
                            or occurred is None or previous['occurred_at'] is None
                            or occurred < _time(previous['occurred_at'])):
                        raise ValueError('supersession requires an explicit later decision/correction in the same work, stage and facet')
                event.update(source_id=source_id, revision=revision, observed_at=source['observed_at'])
                event_id = 'evt_' + hashlib.sha256(_json(event).encode()).hexdigest()
                event['event_id'] = event_id
                changed = self.db.execute('INSERT OR IGNORE INTO events VALUES (?,?,?,?,?)',
                                          (event_id, source_id, revision, event['work_id'], _json(event))).rowcount
                if changed:
                    self._bump(event['work_id'])
                ids.append(event_id)
            self.db.execute('UPDATE sources SET processed=1 WHERE source_id=? AND revision=?', (source_id, revision))
        return ids

    def read_work(self, work_id, requester_id, audience='owner'):
        if requester_id != self.owner_id or audience != 'owner':
            raise PermissionError('shared or non-owner work memory access is not supported')
        with self._transaction():
            work = self._work(work_id)
            work['aliases'] = json.loads(work['aliases'])
            events = [json.loads(r['data']) for r in self.db.execute('SELECT data FROM events WHERE work_id=?', (work_id,))]
            events.sort(key=lambda e: (e['occurred_at'] is None, _time(e['occurred_at']), e['event_id']))
            accepted = [e for e in events if e['status'] == 'accepted']
            superseded = {target for e in accepted for target in e['supersedes']}
            sources = [self._source(r['source_id'], r['revision']) for r in self.db.execute('''
                SELECT source_id,revision FROM source_works WHERE work_id=?
                UNION SELECT source_id,revision FROM events WHERE work_id=?
                ORDER BY source_id,revision''', (work_id, work_id)).fetchall()]
            return dict(work_id=work_id, title=work['title'], owner_id=self.owner_id, visibility='owner', work=work,
                        revision=work['version'], updated_at=work['updated_at'], sources=sources, events=events,
                        current_events=[e for e in accepted if e['event_id'] not in superseded],
                        candidates=sum(e['status'] == 'candidate' for e in events))

    def _set_cursor(self, key, value):
        _text(key, 'cursor key')
        self.db.execute('INSERT INTO cursors VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value', (key, _json(value)))

    def set_cursor(self, key, value):
        """Advance only after the caller has durably ingested the corresponding batch."""
        with self._transaction():
            self._set_cursor(key, value)

    def get_cursor(self, key):
        with self._transaction():
            row = self.db.execute('SELECT value FROM cursors WHERE key=?', (key,)).fetchone()
            return json.loads(row['value']) if row else None

    def record_failure(self, source_id, error):
        with self._transaction():
            self.db.execute('INSERT INTO failures(source_id,error,observed_at) VALUES (?,?,?)', (source_id, str(error), _now()))

    def list_failures(self):
        with self._transaction():
            return [dict(r) for r in self.db.execute('SELECT * FROM failures ORDER BY id')]
