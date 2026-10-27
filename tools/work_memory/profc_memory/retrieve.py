"""Explicit, owner-private dossier reads with paragraph and session budgets."""

from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile


SECTIONS = frozenset(('目标', '现状', '背景', '问题', '决策', '近期关注', 'home', 'sources', 'decisions','history'))
_ID = re.compile(r'[a-z0-9_-]{1,64}')
_PRONOUN = re.compile(r'^(?:它|这个|该|当前|刚才那个|上面那个)(?:项目|事项|工作)?(?:现在|目前)?(?:怎么样|进展如何|情况如何|的进展|进展|的情况|情况|呢)?[？?。！!\s]*$')


def read_dossier(state_dir, work_id, requester_id, section='home', audience='owner'):
    # Import lazily: the CLI also imports this module.
    from .archive import read_dossier as read
    return read(state_dir, work_id, requester_id, section=section, audience=audience)


def _safe(path, *, directory=False, missing=False):
    path = Path(path).absolute()
    if '..' in path.parts or any(p.is_symlink() for p in (path, *path.parents)):
        raise ValueError('unsafe_path')
    if missing and not path.exists():
        return path
    info = path.stat()
    if info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError('unsafe_permissions')
    if not (path.is_dir() if directory else path.is_file()):
        raise ValueError('unsafe_path')
    return path


def _json(path):
    path = _safe(path)
    if path.stat().st_size > 2_000_000:
        raise ValueError('state_limit')
    return json.loads(path.read_text(encoding='utf-8'))


def _catalog(root, owner, works):
    from .archive import _lock, dossier_directory
    directory = _safe(dossier_directory(root), directory=True, missing=True)
    if not directory.exists():
        return []
    with _lock(root):
        return _catalog_unlocked(root, directory, owner, works)


def _catalog_unlocked(root, directory, owner, works):
    catalog = []
    for folder in sorted(directory.iterdir()):
        work_id = folder.name
        if work_id.startswith('.backup-'):
            work_id = work_id[len('.backup-'):]
            if (directory / work_id).exists() or (directory / work_id).is_symlink():
                continue
        if not _ID.fullmatch(work_id):
            continue
        _safe(folder, directory=True)
        path = _safe(folder / 'manifest.json', missing=True)
        if not path.exists():
            continue
        manifest = _json(path)
        if manifest.get('owner_id') != owner or manifest.get('visibility') != 'owner':
            raise PermissionError('forbidden')
        if (manifest.get('schema_version') != 1 or manifest.get('work_id') != work_id
                or not isinstance(manifest.get('title'), str) or not manifest['title']
                or len(manifest['title']) > 256
                or not isinstance(manifest.get('version'), str) or not manifest['version']):
            raise ValueError('invalid_manifest')
        aliases = manifest.get('aliases', [])
        if (not isinstance(aliases, list) or len(aliases) > 30
                or not all(isinstance(a, str) and 0 < len(a) <= 128 for a in aliases)):
            raise ValueError('invalid_manifest')
        configured = next((w for w in works if w.get('work_id') == work_id), {})
        configured_aliases = configured.get('aliases', [])
        if (not isinstance(configured_aliases, list) or len(configured_aliases) > 30
                or not all(isinstance(a, str) and 0 < len(a) <= 128 for a in configured_aliases)):
            raise ValueError('invalid_config')
        summary = manifest.get('summary', '')
        if not isinstance(summary, str) or len(summary) > 240 or '\n' in summary or '\r' in summary:
            raise ValueError('invalid_manifest')
        summary = configured.get('summary', summary)
        if not isinstance(summary, str) or len(summary) > 240 or '\n' in summary or '\r' in summary:
            raise ValueError('invalid_config')
        receipt = manifest.get('receipt', {})
        updated_at = manifest.get('updated_at', receipt.get('built_at', '') if isinstance(receipt, dict) else '')
        if not isinstance(updated_at, str) or len(updated_at) > 64 or '\n' in updated_at or '\r' in updated_at:
            raise ValueError('invalid_manifest')
        aliases = list(dict.fromkeys([*aliases, *configured_aliases]))
        catalog.append(dict(work_id=work_id, title=manifest['title'], aliases=aliases,
                            summary=summary, updated_at=updated_at,
                            version=manifest['version'], path=str(folder / 'home.md')))
        if len(catalog) > 100:
            raise ValueError('catalog_limit')
    return catalog


def _matches(query, candidate):
    for alias in [candidate['work_id'], candidate['title'], *candidate['aliases']]:
        if re.fullmatch(r'[\w ._-]+', alias, re.ASCII):
            pattern = r'(?<![a-z0-9_])' + re.escape(alias.casefold()) + r'(?![a-z0-9_])'
            if re.search(pattern, query.casefold()):
                return True
        elif alias.casefold() in query.casefold():
            return True
    return False


def _candidates(query, catalog, resolver):
    broad = query.strip() in ('所有项目', '所有持续事项', '持续事项', '有哪些项目', '有哪些持续事项', '项目列表')
    matches = [c for c in catalog if broad or _matches(query, c)]
    if resolver is not None:
        try:
            # Remote facts and unknown IDs never authorize a local dossier read.
            resolved = resolver(query)
            ids = {c['work_id'] for c in resolved
                   if isinstance(c, dict) and isinstance(c.get('work_id'), str)}
            matches = [c for c in catalog if c in matches or c['work_id'] in ids]
            matches.sort(key=lambda c: c['work_id'] not in ids)
        except Exception:
            pass
    return [{key: c[key] for key in ('work_id', 'title', 'aliases', 'summary', 'updated_at', 'path')}
            for c in matches[:20]]


def _write_json(path, value):
    _safe(path, missing=True)
    descriptor, temporary = tempfile.mkstemp(prefix='.read-', dir=path.parent)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, 'w', encoding='utf-8') as handle:
            json.dump(value, handle, ensure_ascii=False)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


@contextmanager
def _state(root):
    directory = _safe(root / 'retrieval', directory=True, missing=True)
    directory.mkdir(mode=0o700, exist_ok=True)
    _safe(directory, directory=True)
    lock = _safe(directory / '.lock', missing=True)
    descriptor = os.open(lock, os.O_CREAT | os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    try:
        fcntl.flock(descriptor, fcntl.LOCK_EX)
        yield directory
    finally:
        fcntl.flock(descriptor, fcntl.LOCK_UN)
        os.close(descriptor)


def _paragraph_budget(content, maximum):
    paragraphs = re.split(r'\n\s*\n', content.strip())
    selected = []
    for paragraph in paragraphs:
        value = '\n\n'.join([*selected, paragraph])
        if len(value) > maximum:
            break
        selected.append(paragraph)
    result = '\n\n'.join(selected)
    return result, len(selected) < len(paragraphs)


def _result(status='none', **fields):
    result = dict(status=status, content='', chars=0, truncated=False, candidates=[],
                  receipt=None, budget_unit='characters')
    result.update(fields)
    return result


def retrieve(state_dir, *, requester_id, audience='owner', work_id=None, query=None,
             section='现状', session_id=None, after_compaction=False, max_chars=1600,
             resolver=None):
    """Read a confirmed work; queries return candidates for the caller to confirm.

    max_chars counts Unicode characters, not model tokens. Oversized paragraphs
    are omitted as a whole; a partial read never suppresses a later full read.
    resolver(query) may suggest existing local catalog IDs, never supply prose.
    """
    root = _safe(state_dir, directory=True)
    config = _json(root / 'config.json')
    owner = config.get('owner_id')
    if not isinstance(owner, str) or not owner:
        raise ValueError('invalid_config')
    if requester_id != owner or audience != 'owner':
        raise PermissionError('forbidden')
    decision_section = isinstance(section, str) and re.fullmatch(r'decision:[a-z0-9_-]{1,64}', section)
    source_section = isinstance(section,str) and re.fullmatch(r'source:[1-9][0-9]{0,2}',section)
    if ((section not in SECTIONS and not decision_section and not source_section) or type(max_chars) is not int or max_chars < 0
            or type(after_compaction) is not bool):
        raise ValueError('invalid_request')
    for value in (query, session_id):
        if value is not None and (not isinstance(value, str) or len(value) > 4096):
            raise ValueError('invalid_request')
    if work_id is not None and (not isinstance(work_id, str) or not _ID.fullmatch(work_id)):
        raise ValueError('invalid_work_id')
    works = config.get('works', [])
    if not isinstance(works, list) or len(works) > 100 or not all(isinstance(w, dict) for w in works):
        raise ValueError('invalid_config')
    catalog = _catalog(root, owner, works)
    with _state(root) as directory:
        session_path, session = None, {}
        if session_id:
            key = json.dumps([requester_id, audience, session_id], ensure_ascii=False)
            session_path = directory / (hashlib.sha256(key.encode()).hexdigest() + '.json')
            _safe(session_path, missing=True)
            session = _json(session_path) if session_path.exists() else {}
        if work_id is None:
            if query and _PRONOUN.fullmatch(query.strip()) and session.get('current_work'):
                work_id = session['current_work']
            else:
                candidates = _candidates(query, catalog, resolver) if query else []
                if (session_path and session.get('current_work') and
                        (len(candidates) != 1 or candidates[0]['work_id'] != session['current_work'])):
                    _write_json(session_path,{})
                if query and not candidates:
                    return _result('catalog_fallback', candidates=_candidates('所有项目', catalog, None),
                                   fallback_reason='no_candidate_match', catalog_total=len(catalog),
                                   catalog_truncated=len(catalog) > 20)
                return _result('candidates' if candidates else 'none', candidates=candidates)
        candidate = next((c for c in catalog if c['work_id'] == work_id), None)
        if candidate is None:
            if session_path and session:
                _write_json(session_path,{})
            return _result()
        read = read_dossier(root, work_id, requester_id, section=section, audience=audience)
        if read.get('status') not in ('read', 'ok', 'drift') or not isinstance(read.get('content'), str):
            return _result()
        version = read['version']
        fingerprint = hashlib.sha256(read['content'].encode()).hexdigest()
        reads = session.get('reads', {})
        if (after_compaction or session.get('current_work') != work_id
                or session.get('version') != version):
            reads = {}
        previous = reads.get(section, {})
        unchanged = (previous.get('content_hash') == fingerprint
                     and not previous.get('truncated', True))
        if read['status'] == 'drift':
            content, truncated, status = '', False, 'drift'
        elif unchanged:
            content, truncated, status = '', False, 'unchanged'
        else:
            content, truncated = _paragraph_budget(read['content'], max_chars)
            status = 'read'
        receipt = dict(status=status, work_id=work_id, version=version, section=section,
                       chars=len(content), truncated=truncated, max_chars=max_chars,
                       budget_unit='characters', requester_id=requester_id, audience=audience,
                       session_key=session_path.stem if session_path else None,
                       read_at=datetime.now(timezone.utc).isoformat())
        if status == 'drift':
            receipt['drift'] = read.get('drift', [])
        path = _safe(directory / 'read_receipts.jsonl', missing=True)
        descriptor = os.open(path, os.O_APPEND | os.O_CREAT | os.O_WRONLY | getattr(os, 'O_NOFOLLOW', 0), 0o600)
        with os.fdopen(descriptor, 'a', encoding='utf-8') as handle:
            handle.write(json.dumps(receipt, ensure_ascii=False) + '\n')
            handle.flush()
            os.fsync(handle.fileno())
        if session_path and status != 'drift':
            reads[section] = dict(truncated=truncated, content_hash=fingerprint)
            _write_json(session_path, dict(current_work=work_id, version=version, reads=reads))
        return _result(status, content=content, chars=len(content), truncated=truncated,
                       work_id=work_id, version=version, section=section, receipt=receipt)
