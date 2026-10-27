"""Private local collection -> bounded classification -> derived projections."""

import argparse
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import tempfile

from .classify import classify_sources
from .collect import bounded_anchor_matches, collect_source
from .ledger import Ledger
from .render import build_context, render_work
from .publish import publish_work


class CLIError(ValueError):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def _now():
    return datetime.now(timezone.utc).isoformat()


def _private(path, directory=False):
    if path.is_symlink() or not path.exists():
        raise CLIError('unsafe_path')
    info = path.stat()
    if info.st_uid != os.getuid() or info.st_mode & 0o077 or (directory and not path.is_dir()):
        raise CLIError('unsafe_permissions')
    if not directory and not path.is_file():
        raise CLIError('unsafe_path')


def _read(path, default=None):
    if not path.exists() and not path.is_symlink():
        return default
    _private(path)
    if path.stat().st_size > 2_000_000:
        raise CLIError('state_limit')
    try:
        return json.loads(path.read_text(encoding='utf-8'))
    except (ValueError, UnicodeError):
        raise CLIError('invalid_state') from None


def _config(state_dir):
    root = Path(state_dir).absolute()
    if '..' in root.parts or any(p.is_symlink() for p in [root, *root.parents]):
        raise CLIError('unsafe_path')
    _private(root, directory=True)
    config = _read(root / 'config.json')
    if not isinstance(config, dict) or config.get('schema_version') != 1 or not isinstance(config.get('owner_id'), str) or not config['owner_id']:
        raise CLIError('invalid_config')
    works, sources = config.get('works'), config.get('sources')
    if not isinstance(works, list) or not isinstance(sources, list) or len(works) > 100 or len(sources) > 50:
        raise CLIError('invalid_config')
    work_ids, source_ids = set(), set()
    for work in works:
        if (not isinstance(work, dict) or not isinstance(work.get('work_id'), str)
                or not re.fullmatch('[a-z0-9_-]+', work['work_id']) or len(work['work_id']) > 64
                or not isinstance(work.get('title'), str) or not work['title'] or work['work_id'] in work_ids):
            raise CLIError('invalid_config')
        work_ids.add(work['work_id'])
        for key in ('aliases', 'anchors'):
            if not isinstance(work.get(key, []), list) or not all(isinstance(s, str) and s for s in work.get(key, [])):
                raise CLIError('invalid_config')
    for source in sources:
        if (not isinstance(source, dict) or not isinstance(source.get('id'), str) or not source['id']
                or source['id'] in source_ids or source.get('kind') not in ('document', 'chat', 'message_search')
                or not isinstance(source.get('work_ids', []), list)
                or any(w not in work_ids for w in source.get('work_ids', []))):
            raise CLIError('invalid_config')
        source_ids.add(source['id'])
    maximum = config.get('max_classification_batches', 1)
    if type(maximum) is not int or not 0 <= maximum <= 5:
        raise CLIError('invalid_config')
    publishing = config.get('publish', {})
    if (not isinstance(publishing, dict) or type(publishing.get('enabled', False)) is not bool
            or (publishing.get('enabled') and not isinstance(publishing.get('owner_open_id'), str))
            or (publishing.get('enabled') and not publishing['owner_open_id'])):
        raise CLIError('invalid_config')
    return root, config


def _mkdir(path):
    if path.is_symlink():
        raise CLIError('unsafe_path')
    path.mkdir(mode=0o700, exist_ok=True)
    if not path.is_dir() or path.stat().st_uid != os.getuid():
        raise CLIError('unsafe_path')
    path.chmod(0o700)


def _write(path, value):
    if path.is_symlink() or (path.exists() and not path.is_file()):
        raise CLIError('unsafe_path')
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False, indent=2) + '\n'
    fd, temporary = tempfile.mkstemp(prefix='.memory-', dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, 'w', encoding='utf-8') as handle:
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


@contextmanager
def _lock(root):
    path = root / 'run.lock'
    if path.is_symlink():
        raise CLIError('unsafe_path')
    descriptor = os.open(path, os.O_RDWR | os.O_CREAT | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    try:
        os.fchmod(descriptor, 0o600)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise CLIError('busy') from None
        yield
    finally:
        os.close(descriptor)


def _collect(root, config, ledger, until):
    stats = dict(collected=0, errors=[])
    history = _read(root / 'collection_status.json', {})
    if not isinstance(history, dict):
        raise CLIError('invalid_state')
    for source in config['sources']:
        key = 'source:' + source['id']
        try:
            result = collect_source(dict(source, owner_id=config['owner_id']), ledger.get_cursor(key), until)
        except (ValueError, KeyError, TypeError, OSError):
            result = dict(records=[], complete=False, error=dict(type='collection_failed'))
        error = result.get('error', {}).get('type') if not result.get('complete') else None
        try:
            for record in result['records']:
                if source['kind'] == 'document':
                    linked = source.get('work_ids', [])
                else:
                    allowed = source.get('work_ids') or [w['work_id'] for w in config['works']]
                    linked = [w['work_id'] for w in config['works'] if w['work_id'] in allowed
                              and bounded_anchor_matches(record['content'], w.get('anchors', []))]
                stats['collected'] += int(ledger.ingest_source(record, linked))
            if result.get('complete'):
                ledger.set_cursor(key, result['cursor'])
        except (ValueError, PermissionError, KeyError, TypeError, sqlite3.Error):
            error = 'ingest_failed'
        if error or not result.get('complete'):
            error = error or 'incomplete'
            stats['errors'].append(dict(type=error))
            ledger.record_failure(source['id'], error)
        history[source['id']] = dict(attempted_at=_now(), complete=not bool(error), error=error,
                                     cursor=ledger.get_cursor(key), coverage_start=result.get('coverage_start'))
    _write(root / 'collection_status.json', history)
    return stats


def _retry_key(source):
    raw = source['source_id'] + '\0' + source['revision']
    return 'classify:retry:' + hashlib.sha256(raw.encode()).hexdigest()


def _backoff(ledger, source, code):
    key = _retry_key(source)
    old = ledger.get_cursor(key) or {}
    attempts = min(int(old.get('attempts', 0)) + 1, 6)
    retry = datetime.now(timezone.utc) + timedelta(hours=min(2 ** (attempts - 1), 24))
    ledger.set_cursor(key, dict(attempts=attempts, next_after=retry.isoformat(), error=code))
    ledger.record_failure(source['source_id'], code)


def _existing(config, ledger):
    result, size = [], 0
    for work in config['works']:
        snapshot = ledger.read_work(work['work_id'], config['owner_id'])
        for event in reversed(snapshot['current_events']):
            length = len(json.dumps(event, ensure_ascii=False))
            if len(result) < 100 and size + length <= 6000:
                result.append(event)
                size += length
    return result


def _classify(config, ledger):
    stats = dict(classified=0, deferred=0, raw_candidates=0, errors=[])
    eligible = []
    for source in ledger.pending_sources():
        if not source['work_ids'] or not source['content'] or source.get('deleted'):
            ledger.mark_source_processed(source['source_id'], source['revision'])
            stats['raw_candidates'] += 1
            continue
        retry = ledger.get_cursor(_retry_key(source))
        if retry and datetime.fromisoformat(retry['next_after']) > datetime.now(timezone.utc):
            stats['deferred'] += 1
            continue
        eligible.append(source)
    maximum = config.get('max_classification_batches', 1)
    for _ in range(maximum):
        batch, size = [], 0
        while eligible and len(batch) < 8:
            source = eligible[0]
            length = len(json.dumps(source, ensure_ascii=False))
            if length > 36000:
                eligible.pop(0)
                _backoff(ledger, source, 'limit_exceeded')
                stats['errors'].append(dict(type='limit_exceeded'))
                continue
            if size + length > 36000:
                break
            batch.append(eligible.pop(0))
            size += length
        if not batch:
            break
        result = classify_sources(batch, config['works'], _existing(config, ledger))
        grouped = {(s['source_id'], s['revision']): [] for s in batch}
        invalid = False
        for row in result.get('events', []):
            key = (row.get('source_id'), row.get('revision'))
            if key not in grouped:
                invalid = True
                break
            grouped[key].append(row.get('event'))
        errors = result.get('errors', [])
        if invalid or errors:
            code = 'invalid_evidence' if invalid else errors[0].get('type', 'classification_failed')
            for source in batch:
                _backoff(ledger, source, code)
            stats['errors'].append(dict(type=code))
            continue
        for source in batch:
            key = (source['source_id'], source['revision'])
            try:
                ledger.apply_events(*key, grouped[key])
                ledger.set_cursor(_retry_key(source), None)
                stats['classified'] += 1
            except (ValueError, PermissionError, KeyError, TypeError):
                _backoff(ledger, source, 'invalid_evidence')
                stats['errors'].append(dict(type='invalid_evidence'))
    stats['deferred'] += len(eligible)
    return stats


def _coverage(root, config, pending, work_id):
    history = _read(root / 'collection_status.json', {})
    related = [s for s in config['sources'] if not s.get('work_ids') or work_id in s['work_ids']]
    failures = sum(not history.get(s['id'], {}).get('complete', False) for s in related)
    missing = sum(s['id'] not in history for s in related)
    waiting = sum(work_id in s['work_ids'] for s in pending)
    return dict(configured_sources=len(related), incomplete_sources=failures, never_collected_sources=missing,
                pending_sources=waiting)


def _projection(root, config, ledger, work_id):
    snapshot = ledger.read_work(work_id, config['owner_id'])
    coverage = _coverage(root, config, ledger.pending_sources(), work_id)
    note = (f"收集状态：配置来源{coverage['configured_sources']}个，其中未完整收集{coverage['incomplete_sources']}个；"
            f"待分类来源{coverage['pending_sources']}条。仅代表已记录证据。")
    note += f"\n完整脉络可按需读取本机文件：{root / 'works' / work_id / 'timeline.md'}。"
    context = build_context(snapshot, max_chars=3200 - len(note) - 2)
    if context['dropped_count']:
        note += f"\n上下文为节选，另有{context['dropped_count']}条记录；阶段、范围和发布结论需结合完整脉络核对。"
        context = build_context(snapshot, max_chars=3200 - len(note) - 2)
    context['context'] += ('\n\n' if context['context'] else '') + note
    context.update(coverage=coverage, generated_at=_now())
    return snapshot, context, note


def _render(root, config, ledger):
    _mkdir(root / 'works')
    versions = {}
    for work in config['works']:
        snapshot, context, note = _projection(root, config, ledger, work['work_id'])
        folder = root / 'works' / work['work_id']
        _mkdir(folder)
        _write(folder / 'context.json', context)
        _write(folder / 'timeline.md', render_work(snapshot) + '\n' + note + '\n')
        versions[work['work_id']] = context['version']
    return dict(rendered=len(versions), versions=versions, errors=[])


def _publish(root, config, ledger):
    publishing = config.get('publish', {})
    if not publishing.get('enabled'):
        raise CLIError('publishing_disabled')
    result = dict(published=0, pages={}, errors=[])
    for work in config['works']:
        snapshot = ledger.read_work(work['work_id'], config['owner_id'])
        receipt = publish_work(root, snapshot, publishing['owner_open_id'])
        if receipt.get('error'):
            result['errors'].append(dict(type=receipt['error']['type']))
        if receipt.get('status') == 'published':
            result['published'] += 1
            result['pages'][work['work_id']] = dict(url=receipt['url'], version=receipt['published_ledger_version'])
    return result


def execute(state_dir, command, *, until=None, requester_id=None, audience='owner', work_id=None):
    root, config = _config(state_dir)
    if command == 'context' and (requester_id != config['owner_id'] or audience != 'owner'):
        raise CLIError('forbidden')
    if command == 'context' and work_id not in {w['work_id'] for w in config['works']}:
        raise CLIError('unknown_work')
    with _lock(root), Ledger(root / 'ledger.sqlite3', config['owner_id']) as ledger:
        for work in config['works']:
            ledger.register_work(work['work_id'], work['title'], work.get('aliases', []))
        result = dict(errors=[])
        if command in ('collect', 'run'):
            result.update(_collect(root, config, ledger, until or _now()))
        if command in ('classify', 'run'):
            classified = _classify(config, ledger)
            result['errors'] += classified.pop('errors')
            result.update(classified)
        if command in ('render', 'run'):
            rendered = _render(root, config, ledger)
            rendered.pop('errors')
            result.update(rendered)
        if command == 'publish' or (command == 'run' and config.get('publish', {}).get('enabled')):
            published = _publish(root, config, ledger)
            result['errors'] += published.pop('errors')
            result.update(published)
        if command == 'status':
            result.update(pending=len(ledger.pending_sources()), failures=len(ledger.list_failures()),
                          versions={w['work_id']: str(ledger.read_work(w['work_id'], config['owner_id'])['revision']) for w in config['works']})
        if command == 'context':
            return _projection(root, config, ledger, work_id)[1]
        if command not in ('collect', 'classify', 'render', 'publish', 'run', 'status'):
            raise CLIError('invalid_command')
        return result


def main(argv=None):
    parser = argparse.ArgumentParser(description='Owner-private work evidence memory')
    parser.add_argument('--state-dir', required=True)
    commands = parser.add_subparsers(dest='command', required=True)
    for name in ('collect', 'classify', 'render', 'publish', 'run', 'status', 'context'):
        command = commands.add_parser(name)
        if name in ('collect', 'run'):
            command.add_argument('--until')
        if name == 'context':
            command.add_argument('--work-id', required=True)
            command.add_argument('--requester-id', '--owner-id', dest='requester_id', required=True)
            command.add_argument('--audience', required=True)
    args = vars(parser.parse_args(argv))
    try:
        result = execute(**args)
        print(json.dumps(result, ensure_ascii=False))
        return int(bool(result.get('errors')))
    except (CLIError, PermissionError, OSError, ValueError, KeyError, TypeError, sqlite3.Error) as exc:
        print(json.dumps(dict(errors=[dict(type=exc.code if isinstance(exc, CLIError) else 'operation_failed')])))
        return 1
