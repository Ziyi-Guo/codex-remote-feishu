"""Explicit owner-only publishing of a ledger projection; never edits the ledger.

Receipts live in state/publish/<work_id>.json. A persisted creation intent with
no returned document identity is never retried automatically. Publishing is not
part of collect/run; callers must explicitly invoke this entry point.
"""

from datetime import datetime, timezone
import fcntl
import hashlib
from html import escape
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import urlsplit
import xml.etree.ElementTree as ET

from .collect import sanitize_content
from .evidence import complete_quote, visible_text
from .render import KINDS, STAGES, FACETS

PRIVATE = dict(link_share_entity='closed', external_access=False,
               share_entity='only_full_access', security_entity='only_full_access')


class PublishError(ValueError):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def _fail(code):
    raise PublishError(code)


def _now():
    return datetime.now(timezone.utc).isoformat()


def _safe_text(value):
    text = str(value)
    if sanitize_content(text)[1] or any(ord(c) < 32 and c not in '\n\r\t' for c in text):
        _fail('unsafe_content')
    return escape(text, quote=True).replace('\n', '<br/>')


def _url(value):
    if not isinstance(value, str):
        _fail('unsafe_url')
    parsed = urlsplit(value)
    if (parsed.scheme != 'https' or not parsed.netloc or parsed.username is not None or parsed.password is not None
            or any(c.isspace() or ord(c) < 32 for c in value) or sanitize_content(value)[1]):
        _fail('unsafe_url')
    return value


def _stamp(value):
    if value is None:
        return datetime.max.replace(tzinfo=timezone.utc)
    try:
        stamp = datetime.fromisoformat(value.replace('Z', '+00:00'))
        if stamp.tzinfo is None:
            raise ValueError()
        return stamp
    except (ValueError, AttributeError):
        _fail('invalid_snapshot')


def render_xml(snapshot):
    """Render only accepted event quotations and provenance, never source bodies."""
    if snapshot.get('visibility') != 'owner' or not snapshot.get('owner_id'):
        _fail('owner_mismatch')
    sources = {(s['source_id'], s['revision']): s for s in snapshot['sources']}
    events = [e for e in snapshot['events'] if e['status'] == 'accepted']
    if len(events) > 1000:
        _fail('limit_exceeded')
    by_id = {e['event_id']: e for e in events}
    current = [e for e in snapshot['current_events'] if e['status'] == 'accepted']
    if any(e['event_id'] not in by_id for e in current):
        _fail('invalid_snapshot')
    current_ids = {e['event_id'] for e in current}
    replaced = {}
    for event in events:
        for target in event.get('supersedes', []):
            replaced.setdefault(target, []).append(event['event_id'])

    def entry(event, historical=False):
        source = sources.get((event['source_id'], event['revision']))
        if (event['work_id'] != snapshot['work_id'] or source is None or event['kind'] not in KINDS
                or not isinstance(event['quote'], str) or len(event['quote']) > 300
                or not complete_quote(event['quote'], source['content'])):
            _fail('invalid_snapshot')
        label = KINDS[event['kind']]
        if event['kind'] == 'verification':
            label += '（来源报告，未经本次独立核验）'
        label += '｜' + STAGES.get(event['stage'], event['stage']) + '｜' + FACETS.get(event['facet'], event['facet'])
        state = ''
        if historical:
            state = '已被替代：' + '、'.join(replaced[event['event_id']]) if event['event_id'] in replaced else (
                '当前保留' if event['event_id'] in current_ids else '历史版本，不在当前视图')
        quote = visible_text(event['quote']).strip() if source.get('kind') == 'document' else event['quote']
        lines = ['<p><b>' + _safe_text(label) + '</b></p>', '<blockquote>' + _safe_text(quote) + '</blockquote>']
        if state:
            lines.append('<p>' + _safe_text(state) + '</p>')
        lines.append('<p>发生：' + _safe_text(event.get('occurred_at') or '时间未知')
                     + '；采集：' + _safe_text(event.get('observed_at') or '时间未知')
                     + '；来源人物：' + _safe_text(event.get('actor') or 'unknown') + '</p>')
        lines.append('<p>event_id=' + _safe_text(event['event_id']) + '；source_id=' + _safe_text(event['source_id'])
                     + '；source_revision=' + _safe_text(event['revision']) + '</p>')
        url = source.get('url')
        if url:
            original = _url(url)
            lines.append('<p>原始来源：<a href="' + escape(original, quote=True) + '">' + escape(original) + '</a></p>')
        else:
            lines.append('<p>原始来源链接未提供。</p>')
        return '\n'.join(lines)

    marker = f"profc-memory work_id={snapshot['work_id']} ledger_version={snapshot['revision']}"
    parts = ['<title>' + _safe_text(snapshot['title']) + '｜私有事项脉络</title>',
             '<p>本页是私有账本的只读视图。编辑本页不会反向修改账本；检测到人工修改后自动更新会停止。</p>',
             '<p>' + _safe_text(marker) + '</p>',
             '<p>当前区为节选，未展示的未被判定失效；完整记录在历史区。有效陈述最多展示最近12条，未决问题与风险最多8条；时间未知的记录后置。</p>',
             '<h1>当前有效决定与陈述</h1>']
    ordered = sorted([e for e in current if e.get('occurred_at')],
                     key=lambda e: (_stamp(e['occurred_at']), e['event_id']), reverse=True)
    ordered += sorted([e for e in current if not e.get('occurred_at')], key=lambda e: e['event_id'])
    main = [e for e in ordered if e['kind'] not in ('open_question', 'risk')][:12]
    parts += [entry(e) for e in main] or ['<p>暂无已记录的有效陈述。</p>']
    parts.append('<h1>未决问题与风险</h1>')
    unresolved = [e for e in ordered if e['kind'] in ('open_question', 'risk')][:8]
    parts += [entry(e) for e in unresolved] or ['<p>暂无已记录的未决问题或风险。</p>']
    parts.append('<h1>按发生时间排列的历史</h1>')
    parts += [entry(e, True) for e in sorted(events, key=lambda e: (_stamp(e.get('occurred_at')), e['event_id']))]
    parts.append('<p>候选事件数：' + str(snapshot.get('candidates', 0)) + '；候选内容不进入本页。</p>')
    xml = '\n'.join(parts)
    if len(xml) > 300000:
        _fail('limit_exceeded')
    return xml


def _private(path, directory=False):
    if path.is_symlink() or not path.exists():
        _fail('unsafe_path')
    info = path.stat()
    if info.st_uid != os.getuid() or info.st_mode & 0o077 or (directory and not path.is_dir()):
        _fail('unsafe_path')
    if not directory and not path.is_file():
        _fail('unsafe_path')


def _save(path, receipt):
    if path.is_symlink():
        _fail('unsafe_path')
    receipt['updated_at'] = _now()
    fd, name = tempfile.mkstemp(prefix='.receipt-', dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, 'w', encoding='utf-8') as output:
            json.dump(receipt, output, ensure_ascii=False)
            output.flush()
            os.fsync(output.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def _invoke(argv, directory, runner):
    env = dict(os.environ, LARKSUITE_CLI_NO_UPDATE_NOTIFIER='1', LARKSUITE_CLI_NO_SKILLS_NOTIFIER='1')
    try:
        completed = runner(argv, cwd=str(directory), capture_output=True, text=True, timeout=60, env=env, check=False)
    except subprocess.TimeoutExpired:
        _fail('timeout')
    except OSError:
        _fail('cli_unavailable')
    if completed.returncode:
        _fail('cli_failed')
    if len(completed.stdout) > 2_000_000:
        _fail('response_limit')
    try:
        payload = json.loads(completed.stdout)
    except (ValueError, TypeError):
        _fail('invalid_response')
    if not isinstance(payload, dict):
        _fail('invalid_response')
    return payload


def _data(payload):
    if payload.get('ok') is not True or payload.get('identity') != 'user':
        _fail('invalid_response')
    current = payload
    for _ in range(4):
        if 'code' in current and (type(current['code']) is not int or current['code'] != 0):
            _fail('api_failed')
        if current is payload or 'code' in current:
            current = current.get('data')
            if not isinstance(current, dict):
                _fail('invalid_response')
        else:
            return current
    _fail('invalid_response')


def _call(arguments, directory, runner):
    return _data(_invoke(['lark-cli'] + arguments + ['--as', 'user', '--format', 'json'], directory, runner))


def _auth(owner, directory, runner):
    payload = _invoke(['lark-cli', 'auth', 'status', '--json', '--verify'], directory, runner)
    if payload.get('ok') is True and isinstance(payload.get('data'), dict):
        payload = payload['data']
    user = payload.get('identities', {}).get('user', {})
    if payload.get('identity') != 'user' or payload.get('verified') is not True or user.get('openId') != owner:
        _fail('owner_mismatch')


def _permissions(token, owner, directory, runner):
    params = json.dumps(dict(token=token, type='docx'))
    args = ['drive', 'permission.public', 'get', '--params', params]
    public = _call(args, directory, runner).get('permission_public')
    if not isinstance(public, dict):
        _fail('unsafe_permissions')
    if any(type(public.get(k)) is not type(v) or public.get(k) != v for k, v in PRIVATE.items()):
        _call(['drive', 'permission.public', 'patch', '--params', params,
               '--data', json.dumps(PRIVATE), '--yes'], directory, runner)
    public = _call(args, directory, runner).get('permission_public')
    if not isinstance(public, dict) or any(type(public.get(k)) is not type(v) or public.get(k) != v for k, v in PRIVATE.items()):
        _fail('unsafe_permissions')
    members = _call(['api', 'GET', '/open-apis/drive/v1/permissions/' + token + '/members',
                     '--params', json.dumps(dict(type='docx', fields='type'))], directory, runner)
    items = members.get('items')
    if not isinstance(items, list) or members.get('has_more'):
        _fail('unsafe_collaborators')
    if any(not isinstance(m, dict) or m.get('member_id') != owner or m.get('member_type') != 'openid'
           or m.get('type') != 'user' for m in items):
        _fail('unsafe_collaborators')


def _document(data, token=None):
    doc = data.get('document')
    if not isinstance(doc, dict) or not isinstance(doc.get('document_id'), str):
        _fail('invalid_response')
    if token is not None and doc['document_id'] != token:
        _fail('invalid_response')
    if not re.fullmatch(r'[A-Za-z0-9_-]+', doc['document_id']):
        _fail('invalid_response')
    return doc


def _revision(doc):
    value = doc.get('revision_id')
    if type(value) not in (int, str) or not str(value).isdigit():
        _fail('invalid_response')
    return str(value)


def _readback(xml, remote):
    try:
        roots = [ET.fromstring('<root>' + text + '</root>') for text in (xml, remote)]
    except (ET.ParseError, TypeError):
        _fail('readback_failed')
    text = [re.sub(r'\s+', '', ''.join(root.itertext())) for root in roots]
    links = [sorted(node.get('href', '') for node in root.iter('a')) for root in roots]
    if text[0] != text[1] or links[0] != links[1]:
        _fail('readback_failed')


def _published(receipt, doc, xml, version, path):
    _readback(xml, doc.get('content'))
    receipt.update(status='published', saved_remote_revision=_revision(doc), published_ledger_version=version,
                   saved_remote_hash=hashlib.sha256(doc['content'].encode()).hexdigest(),
                   published_payload_hash=hashlib.sha256(xml.encode()).hexdigest())
    for key in ('pending_ledger_version', 'pending_payload_hash', 'error'):
        receipt.pop(key, None)
    _save(path, receipt)
    return receipt


def publish_work(state_dir, snapshot, owner_open_id, *, runner=None):
    """Publish to a caller-verified cross-app owner mapping; never retry unknown creates."""
    receipt, path, descriptor, payload_path = dict(status='blocked'), None, None, None
    runner = runner or subprocess.run
    try:
        work_id = snapshot.get('work_id')
        if not isinstance(work_id, str) or not re.fullmatch(r'[a-z0-9_-]{1,64}', work_id):
            _fail('invalid_snapshot')
        if not isinstance(snapshot.get('owner_id'), str) or not snapshot['owner_id'] or not isinstance(owner_open_id, str) or not owner_open_id:
            _fail('owner_mismatch')
        xml, version = render_xml(snapshot), str(snapshot['revision'])
        root = Path(state_dir).absolute()
        if '..' in root.parts or any(p.is_symlink() for p in [root, *root.parents]):
            _fail('unsafe_path')
        _private(root, True)
        directory = root / 'publish'
        if directory.is_symlink():
            _fail('unsafe_path')
        directory.mkdir(mode=0o700, exist_ok=True)
        _private(directory, True)
        path = directory / (work_id + '.json')
        if path.is_symlink():
            _fail('unsafe_path')
        lock = directory / (work_id + '.lock')
        if lock.is_symlink():
            _fail('unsafe_path')
        descriptor = os.open(lock, os.O_RDWR | os.O_CREAT | getattr(os, 'O_NOFOLLOW', 0), 0o600)
        os.fchmod(descriptor, 0o600)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            _fail('busy')
        if path.exists():
            _private(path)
            if path.stat().st_size > 50000:
                _fail('invalid_receipt')
            receipt = json.loads(path.read_text())
            if receipt.get('work_id') != work_id:
                _fail('invalid_receipt')
            if receipt.get('owner_open_id') != owner_open_id or receipt.get('ledger_owner_id') != snapshot['owner_id']:
                _fail('owner_mismatch')
            if not receipt.get('document_id'):
                _fail('creation_unknown')
        else:
            receipt = dict(schema_version=1, work_id=work_id, owner_open_id=owner_open_id,
                           ledger_owner_id=snapshot['owner_id'], status='uncreated')
        receipt.update(event_count=sum(e['status'] == 'accepted' for e in snapshot['events']), xml_chars=len(xml))
        _auth(owner_open_id, directory, runner)
        if not receipt.get('document_id'):
            receipt['status'] = 'creation_pending'
            _save(path, receipt)
            try:
                created = _document(_call(['docs', '+create', '--title', '私有事项脉络页'], directory, runner))
                receipt.update(document_id=created['document_id'], status='created_blank')
                _save(path, receipt)
                receipt['url'] = _url(created.get('url'))
                _save(path, receipt)
                receipt['saved_remote_revision'] = _revision(created)
                _save(path, receipt)
            except PublishError:
                if not receipt.get('document_id'):
                    _fail('creation_unknown')
                raise
        token = receipt['document_id']
        if not re.fullmatch(r'[A-Za-z0-9_-]+', token):
            _fail('invalid_receipt')
        _url(receipt['url'])
        _permissions(token, owner_open_id, directory, runner)
        fetch_args = ['docs', '+fetch', '--doc', token, '--detail', 'simple']
        current = _document(_call(fetch_args, directory, runner), token)
        if not isinstance(current.get('content'), str):
            _fail('invalid_response')
        if receipt.get('pending_ledger_version') is not None:
            if receipt['pending_ledger_version'] != version or receipt.get('pending_payload_hash') != hashlib.sha256(xml.encode()).hexdigest():
                _fail('write_unverified')
            return _published(receipt, current, xml, version, path)
        if (_revision(current) != receipt.get('saved_remote_revision')
                or ('saved_remote_hash' in receipt and hashlib.sha256(current.get('content', '').encode()).hexdigest() != receipt['saved_remote_hash'])):
            _fail('view_drift')
        if receipt.get('published_ledger_version') == version:
            receipt.pop('error', None)
            receipt['status'] = 'published'
            _save(path, receipt)
            return receipt
        fd, name = tempfile.mkstemp(prefix='.payload-', suffix='.xml', dir=directory)
        payload_path = Path(name)
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, 'w', encoding='utf-8') as content:
            content.write(xml)
        receipt.update(status='write_pending', pending_ledger_version=version,
                       pending_payload_hash=hashlib.sha256(xml.encode()).hexdigest())
        _save(path, receipt)
        try:
            _call(['docs', '+update', '--doc', token, '--command', 'overwrite', '--revision-id', _revision(current),
                   '--content', '@' + payload_path.name], directory, runner)
        except PublishError:
            _fail('write_unknown')
        remote = _document(_call(fetch_args, directory, runner), token)
        _permissions(token, owner_open_id, directory, runner)
        return _published(receipt, remote, xml, version, path)
    except (PublishError, OSError, ValueError, KeyError, TypeError) as exc:
        code = exc.code if isinstance(exc, PublishError) else 'operation_failed'
        receipt['error'] = dict(type=code)
        if receipt.get('status') not in ('creation_pending', 'write_pending'):
            receipt['status'] = 'blocked'
        if (path is not None and descriptor is not None and code not in ('busy', 'unsafe_path', 'invalid_receipt')
                and (path.exists() or receipt.get('status') in ('creation_pending', 'write_pending'))):
            try:
                _save(path, receipt)
            except (OSError, PublishError):
                pass
        return receipt
    finally:
        if payload_path is not None:
            payload_path.unlink(missing_ok=True)
        if descriptor is not None:
            os.close(descriptor)
