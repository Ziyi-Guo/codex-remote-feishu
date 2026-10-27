"""Bounded, read-only lark-cli collection. Incomplete runs never advance cursors.

Source config: id, kind (chat/document/message_search), owner_id, start (ISO with
zone); chat_id for chat; doc URL/token and url if token-only for document; query
and/or chat_id for message_search, optional chat_type (group/p2p). Optional limits:
max_pages=10 (<=40), max_messages=500 (<=5000), timeout=30 (<=120 seconds),
overlap_seconds=120 (<=3600). start is required for the first message collection.
timezone must explicitly name an IANA zone to interpret CLI timestamps without
an offset; the CLI process receives the same TZ. Display times have minute
precision: same-minute revisions have no trustworthy order, even if hashes differ.

Message windows use creation time. Optional reconcile_days (1..365) rechecks
max(config.start, until-days) each run instead of since-overlap; coverage_start
reports that boundary, and a completed cursor means this bounded window was
checked. Without reconciliation, overlap catches boundary races and recent edits.
Neither mode covers edits outside its creation-time window, messages no longer
matching a search, or all new replies to old threads. Documents are current
snapshots, not revision history; embedded media
and linked documents are not recursively collected. No data is persisted here.
"""

from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
import re
import subprocess
from urllib.parse import urlsplit
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError


class CollectError(ValueError):
    """Safe, typed collection failure; never include upstream bodies or argv."""

    def __init__(self, code, message):
        self.code = code
        super().__init__(message)


def sanitize_content(content):
    """Return (scrubbed text, changed); reference names/IDs are not credentials."""
    if not isinstance(content, str):
        raise CollectError('invalid_field', 'content must be text')
    clean = re.sub(r'(?i)\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+', '[REDACTED]', content)
    clean = re.sub(r'(?i)https?://[^\s<>"\']+',
                   lambda m: '[REDACTED]' if '@' in m[0].split('://', 1)[1].split('/', 1)[0] else m[0], clean)
    keys = (r'authorization|password|passwd|pwd|token|access[_-]?token|refresh[_-]?token|'
            r'app[_-]?secret|client[_-]?secret|secret[_-]?access[_-]?key|'
            r'access[_-]?key(?:[_-]?id|[_-]?secret)?|api[_-]?key|secret[_-]?key|ak|sk|密码|密钥')
    pattern = (r'(?i)(?<![\w-])((?:\\*["\'])?(?:' + keys + r')(?:\\*["\'])?\s*[:=：]\s*)'
               r'(\[REDACTED\]|\\*"[^"\r\n]*"|\\*\'[^\'\r\n]*\'|[^\s,;<>}\]&]+)')
    clean = re.sub(pattern, lambda m: m[0] if m[2] == '[REDACTED]' else m[1] + '[REDACTED]', clean)
    clean = re.sub(r'\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b', '[REDACTED]', clean)
    clean = re.sub(r'\b(?:(?:AKIA|ASIA)[A-Z0-9]{16}|LTAI[A-Za-z0-9]{12,}|'
                   r'AKLT[A-Za-z0-9]{12,}|sk-[A-Za-z0-9_-]{16,})\b', '[REDACTED]', clean)
    clean = re.sub(r'-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----.*?'
                   r'-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----',
                   '[REDACTED]', clean, flags=re.DOTALL)
    return clean, clean != content


def bounded_anchor_matches(content, anchors):
    """Exact configured URL, ID, or full-name matches; never infer from chat."""
    if not isinstance(content, str) or not isinstance(anchors, list):
        raise CollectError('invalid_field', 'anchors must be a list and content text')
    found = []
    for anchor in anchors:
        if not isinstance(anchor, str) or not anchor.strip():
            raise CollectError('invalid_field', 'anchor must be nonempty text')
        # ASCII identifier boundaries prevent task_1 matching task_12. Chinese
        # full names use exact substrings because prose has no word separators.
        edge = r'A-Za-z0-9_'
        if '://' in anchor:
            edge += r'/?#&=%+.:~\-'
        if re.search(r'(?<![' + edge + r'])' + re.escape(anchor) + r'(?![' + edge + r'])', content):
            if anchor not in found:
                found.append(anchor)
    return found


def _required(obj, name):
    value = obj.get(name)
    if value is None or value == '':
        raise CollectError('missing_field', 'Missing required field: ' + name)
    return value


def _zone(name):
    try:
        if not isinstance(name, str) or not name:
            raise ValueError()
        return ZoneInfo(name)
    except (ValueError, ZoneInfoNotFoundError):
        raise CollectError('invalid_config', 'timezone must name an available IANA zone') from None


def _precision(value):
    if isinstance(value, str) and re.fullmatch(r'\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2})?', value):
        return 'minute'
    if str(value).isdigit() and float(value) > 100_000_000_000:
        return 'millisecond'
    return 'second'


def _time(value, timezone_name=None):
    try:
        if isinstance(value, (int, float)) or (isinstance(value, str) and value.isdigit()):
            seconds = float(value)
            if seconds > 100_000_000_000:
                seconds /= 1000
            return datetime.fromtimestamp(seconds, timezone.utc)
        parsed = datetime.fromisoformat(value.replace('Z', '+00:00'))
        if parsed.tzinfo is None:
            if timezone_name is None:
                raise ValueError('timezone required')
            local = parsed.replace(tzinfo=_zone(timezone_name))
            if (local.utcoffset() != local.replace(fold=1).utcoffset()
                    or local.astimezone(timezone.utc).astimezone(local.tzinfo).replace(tzinfo=None) != parsed):
                raise ValueError('ambiguous or nonexistent local time')
            parsed = local
        return parsed
    except (ValueError, TypeError, AttributeError, OverflowError, OSError):
        raise CollectError('invalid_time', 'Timestamp must be valid and timezone-aware') from None


def _url(value):
    if not isinstance(value, str) or not urlsplit(value).scheme or not urlsplit(value).netloc:
        raise CollectError('invalid_field', 'Source URL must be an absolute URL')
    if urlsplit(value).username is not None or urlsplit(value).password is not None:
        raise CollectError('unsafe_url', 'Source URL contains userinfo credentials')
    if sanitize_content(value)[1]:
        raise CollectError('secret_url', 'Source URL contains credential material')
    return value


def _record(kind, object_id, revision, occurred_at, observed_at, url, content, config):
    clean, changed = sanitize_content(content)
    owner = _required(config, 'owner_id')
    if not isinstance(object_id, str) or not isinstance(owner, str):
        raise CollectError('invalid_field', 'Object and owner identifiers must be text')
    return dict(source_id=kind + ':' + object_id, kind=kind, object_id=object_id,
                revision=str(revision), occurred_at=occurred_at, observed_at=observed_at,
                url=_url(url), content=clean, visibility='owner', owner_id=owner, redacted=changed)


def normalize_message(item, config, observed_at):
    mid = _required(item, 'message_id')
    content = item.get('content')
    if content is None:
        raise CollectError('missing_field', 'Missing required field: content')
    clean, _ = sanitize_content(content)
    zone = config.get('timezone')
    updated = item.get('update_time')
    modified = _time(updated, zone).isoformat() if updated else None
    revision = updated
    if revision is None or revision == '':
        revision = 'sha256:' + hashlib.sha256(clean.encode()).hexdigest()
    else:
        if _precision(updated) == 'minute':
            revision = 'minute:' + modified + ':sha256:' + hashlib.sha256(clean.encode()).hexdigest()
    created = _required(item, 'create_time')
    occurred = _time(created, zone).isoformat()
    record = _record('message', mid, revision, occurred, observed_at,
                     _required(item, 'message_app_link'), content, config)
    record.update(modified_at=modified, occurred_at_precision=_precision(created),
                  modified_at_precision=_precision(updated) if updated else None)
    sender = item.get('sender') if isinstance(item.get('sender'), dict) else {}
    name = sender.get('name') if isinstance(sender.get('name'), str) and sender['name'].strip() else 'unknown'
    user_id = sender.get('id') if isinstance(sender.get('id'), str) and sender['id'].strip() else None
    name, name_redacted = sanitize_content(name)
    if user_id is not None:
        user_id, id_redacted = sanitize_content(user_id)
    else:
        id_redacted = False
    record.update(actor=name, actor_user_id=user_id,
                  redacted=record['redacted'] or name_redacted or id_redacted)
    if item.get('msg_type') == 'merge_forward':
        record.update(actor='unknown', actor_user_id=None, forwarded_by=dict(name=name, user_id=user_id))
    if item.get('deleted'):
        record['deleted'] = True
    return record


def normalize_document(item, config, observed_at):
    token = _required(item, 'document_id')
    revision = _required(item, 'revision_id')
    if not isinstance(revision, (str, int)) or not str(revision).isdigit():
        raise CollectError('invalid_field', 'Document revision must be a real revision number')
    url = item.get('url') or config.get('url')
    if not url and isinstance(config.get('doc'), str) and '://' in config['doc']:
        url = config['doc']
    if not url:
        raise CollectError('missing_field', 'Missing required field: document URL')
    occurred = item.get('update_time') or item.get('create_time')
    occurred = _time(occurred).isoformat() if occurred else None
    if item.get('content') is None:
        raise CollectError('missing_field', 'Missing required field: content')
    return _record('document', token, revision, occurred, observed_at, url, item['content'], config)


def _run_cli(argv, timeout, timezone_name=None):
    env = dict(os.environ, LARKSUITE_CLI_NO_UPDATE_NOTIFIER='1', LARKSUITE_CLI_NO_SKILLS_NOTIFIER='1')
    if timezone_name is not None:
        env['TZ'] = timezone_name
    try:
        result = subprocess.run(argv, capture_output=True, text=True, timeout=timeout, env=env, check=False)
    except subprocess.TimeoutExpired:
        raise CollectError('timeout', 'Read-only CLI call timed out') from None
    except OSError:
        raise CollectError('cli_unavailable', 'Read-only CLI could not be started') from None
    if result.returncode:
        raise CollectError('cli_failed', 'Read-only CLI returned a nonzero status')
    try:
        return json.loads(result.stdout)
    except (json.JSONDecodeError, TypeError):
        raise CollectError('invalid_response', 'CLI did not return a JSON response') from None


def _data(payload):
    if not isinstance(payload, dict) or payload.get('ok') is not True:
        raise CollectError('cli_failed', 'CLI response did not confirm success')
    if payload.get('identity') != 'user':
        raise CollectError('identity_mismatch', 'CLI did not confirm user identity')
    if not isinstance(payload.get('data'), dict):
        raise CollectError('invalid_response', 'CLI response is missing data')
    return payload['data']


def _limit(config, key, default, maximum):
    value = config.get(key, default)
    if type(value) is not int or not 1 <= value <= maximum:
        raise CollectError('invalid_config', 'Invalid bound: ' + key)
    return value


def collect_source(config, since, until):
    """Return records/cursor/complete and a safe typed error on partial failure."""
    result = dict(records=[], cursor=since, complete=False)
    try:
        _required(config, 'id')
        _required(config, 'owner_id')
        # The search endpoint rejects fractional seconds. Keep the durable
        # watermark at the exact rounded boundary actually requested.
        end = _time(until).replace(microsecond=0)
        until = end.isoformat(timespec='seconds')
        observed = datetime.now(timezone.utc).isoformat()
        timeout = _limit(config, 'timeout', 30, 120)
        zone = config.get('timezone')
        if zone is not None:
            _zone(zone)
        kind = _required(config, 'kind')
        common = ['--as', 'user', '--format', 'json']
        if kind == 'document':
            payload = _data(_run_cli(['lark-cli', 'docs', '+fetch', '--doc', _required(config, 'doc'),
                                     '--detail', 'simple'] + common, timeout, timezone_name=zone))
            item = _required(payload, 'document')
            if not isinstance(item, dict):
                raise CollectError('invalid_response', 'Document must be an object')
            result['records'].append(normalize_document(item, config, observed))
        elif kind in ('chat', 'message_search'):
            max_pages = _limit(config, 'max_pages', 10, 40)
            max_messages = _limit(config, 'max_messages', 500, 5000)
            overlap = config.get('overlap_seconds', 120)
            if type(overlap) is not int or not 0 <= overlap <= 3600:
                raise CollectError('invalid_config', 'Invalid overlap_seconds')
            if 'reconcile_days' in config:
                days = _limit(config, 'reconcile_days', 30, 365)
                start = max(_time(_required(config, 'start')), end - timedelta(days=days))
            else:
                start = _time(since or _required(config, 'start')) - timedelta(seconds=overlap if since else 0)
            start = start.replace(microsecond=0)
            if start >= end:
                raise CollectError('invalid_time', 'Collection start must precede end')
            result['coverage_start'] = start.isoformat()
            argv = ['lark-cli', 'im', '+chat-messages-list' if kind == 'chat' else '+messages-search']
            if kind == 'chat':
                argv += ['--chat-id', _required(config, 'chat_id'), '--order', 'asc']
            else:
                if not config.get('query') and not config.get('chat_id'):
                    raise CollectError('invalid_config', 'Search must specify query or chat_id')
                argv += ['--query', config.get('query', '')]
                for key in ('chat_id', 'chat_type'):
                    if config.get(key):
                        argv += ['--' + key.replace('_', '-'), config[key]]
            argv += ['--start', start.isoformat(), '--end', until, '--page-size', '50', '--no-reactions'] + common
            token, visited, seen, count = None, set(), set(), 0
            for page in range(max_pages):
                data = _data(_run_cli(argv + (['--page-token', token] if token else []), timeout, timezone_name=zone))
                messages, more = data.get('messages'), data.get('has_more')
                if not isinstance(messages, list) or type(more) is not bool:
                    raise CollectError('invalid_response', 'Message page requires messages and has_more')
                for item in messages:
                    if count >= max_messages:
                        raise CollectError('limit_reached', 'Message limit reached; cursor retained')
                    count += 1
                    if not isinstance(item, dict):
                        raise CollectError('invalid_response', 'Message must be an object')
                    record = normalize_message(item, config, observed)
                    key = (record['source_id'], record['revision'])
                    if key not in seen:
                        result['records'].append(record)
                        seen.add(key)
                if not more:
                    break
                token = data.get('page_token')
                if not isinstance(token, str) or not token or token in visited:
                    raise CollectError('invalid_response', 'Missing or repeating continuation token')
                visited.add(token)
                if page == max_pages - 1 or count >= max_messages:
                    raise CollectError('limit_reached', 'Page or message limit reached; cursor retained')
        else:
            raise CollectError('invalid_config', 'Unsupported source kind')
        result.update(cursor=until, complete=True)
    except CollectError as exc:
        result['error'] = dict(type=exc.code, message=str(exc))
    return result
