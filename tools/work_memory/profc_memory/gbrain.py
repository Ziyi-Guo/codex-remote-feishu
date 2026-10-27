"""Optional loopback MCP entity identifiers; never a source of project prose."""

from contextlib import closing
import ipaddress
import json
import os
from pathlib import Path
import re
import tomllib
from urllib.parse import urlsplit, urlunsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener

from .collect import sanitize_content


LIMIT = 256_000
IDENTIFIERS = frozenset(('entity_slug', 'slug', 'canonical_slug'))
BODY_FIELDS = frozenset(('text', 'body', 'content', 'facts', 'suggestions'))


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, newurl):
        return None


def _configuration(config_path):
    path = Path(config_path) if config_path is not None else Path.home() / '.codex/config.toml'
    path = path.absolute()
    if '..' in path.parts or any(p.is_symlink() for p in (path, *path.parents)):
        raise ValueError('unsafe private MCP configuration')
    info = path.stat()
    if not path.is_file() or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_size > LIMIT:
        raise ValueError('unsafe private MCP configuration')
    with path.open('rb') as handle:
        config = tomllib.load(handle)['mcp_servers']['gbrain']
    url = config['url']
    if not isinstance(url, str):
        raise ValueError('invalid loopback endpoint')
    parsed = urlsplit(url)
    host, port = parsed.hostname, parsed.port
    if (parsed.scheme != 'http' or parsed.username is not None or parsed.password is not None
            or parsed.fragment or not host or re.search(r'[\s\\]', url) or sanitize_content(url)[1]):
        raise ValueError('invalid loopback endpoint')
    if host == 'localhost':
        # Pin localhost rather than trusting DNS or the caller's proxy settings.
        url = urlunsplit((parsed.scheme, '127.0.0.1' + (':' + str(port) if port else ''), parsed.path, parsed.query, ''))
    elif not ipaddress.ip_address(host).is_loopback:
        raise ValueError('endpoint is not loopback')
    if port is not None and not 1 <= port <= 65535:
        raise ValueError('invalid loopback port')
    headers = config.get('http_headers', {})
    if not isinstance(headers, dict) or len(headers) > 32:
        raise ValueError('invalid MCP headers')
    reserved = {'host', 'connection', 'content-length', 'transfer-encoding', 'upgrade',
                'proxy-authorization', 'mcp-session-id', 'mcp-protocol-version', 'content-type', 'accept'}
    for name, value in headers.items():
        if (not isinstance(name, str) or not re.fullmatch(r'[A-Za-z0-9_-]+', name)
                or name.lower() in reserved or not isinstance(value, str)
                or len(value) > 8192 or any(ord(char) < 32 or ord(char) > 126 for char in value)):
            raise ValueError('invalid MCP headers')
    return url, headers


def _rpc_message(raw, request_id):
    text = raw.decode('utf-8')
    if text.lstrip().startswith('{'):
        candidates = [json.loads(text)]
    else:
        candidates, data = [], []
        for line in [*text.splitlines(), '']:
            if line.startswith('data:'):
                data.append(line[5:].lstrip(' '))
            elif not line and data:
                try:
                    candidates.append(json.loads('\n'.join(data)))
                except ValueError:
                    pass
                data = []
    for message in candidates:
        if isinstance(message, dict) and message.get('id') == request_id:
            if message.get('jsonrpc') != '2.0' or 'error' in message or not isinstance(message.get('result'), dict):
                raise ValueError('MCP request failed')
            return message['result']
    raise ValueError('missing MCP response')


def _request(open_request, url, headers, method, params, request_id=None):
    body = dict(jsonrpc='2.0', method=method, params=params)
    if request_id is not None:
        body['id'] = request_id
    request = Request(url, data=json.dumps(body, ensure_ascii=False).encode(),
                      headers=headers, method='POST')
    with closing(open_request(request, timeout=2)) as response:
        if not 200 <= response.status < 300:
            raise ValueError('MCP request failed')
        raw = response.read(LIMIT + 1)
        if len(raw) > LIMIT:
            raise ValueError('MCP response limit')
        result = _rpc_message(raw, request_id) if request_id is not None else None
        return result, response.headers.get('Mcp-Session-Id')


def _entity(result):
    if result.get('isError'):
        return {}
    if isinstance(result.get('structuredContent'), dict):
        return result['structuredContent']
    blocks = result.get('content', [])
    if not isinstance(blocks, list):
        return {}
    for block in blocks[:8]:
        if not isinstance(block, dict) or block.get('type') != 'text':
            continue
        text = block.get('text')
        if not isinstance(text, str) or len(text) > LIMIT:
            continue
        try:
            value = json.loads(text)
        except ValueError:
            continue
        # Only a complete structured JSON object is admitted, never prose IDs.
        if isinstance(value, dict) and 'found' in value:
            return value
    return {}


def _slugs(entity):
    if entity.get('found') is not True:
        return set()
    found = {entity[key] for key in IDENTIFIERS if isinstance(entity.get(key), str)}
    pending = [(entity[key], 0) for key in ('entity', 'card') if key in entity]
    visited = 0
    while pending and visited < 512:
        value, depth = pending.pop()
        visited += 1
        if depth > 8:
            continue
        if isinstance(value, dict):
            found.update(value[key] for key in IDENTIFIERS if isinstance(value.get(key), str))
            pending.extend((item, depth + 1) for key, item in list(value.items())[:64]
                           if key not in BODY_FIELDS and key not in IDENTIFIERS)
        elif isinstance(value, list):
            pending.extend((item, depth + 1) for item in value[:64])
    return found


def gbrain_candidates(query, works, *, config_path=None, opener=None):
    """Return configured work IDs from a private MCP entity lookup, or [].

    opener is an optional callable(Request, timeout=seconds), for offline tests.
    No failure includes upstream response bodies, configuration or credentials.
    """
    if (not isinstance(query, str) or not query.strip() or len(query) > 512
            or sanitize_content(query)[1]):
        return []
    try:
        known = [work['work_id'] for work in works if isinstance(work, dict)
                 and isinstance(work.get('work_id'), str) and re.fullmatch(r'[a-z0-9_-]{1,64}', work['work_id'])]
        if not known:
            return []
        url, credentials = _configuration(config_path)
        headers = dict(credentials, **{'Content-Type': 'application/json', 'Accept': 'application/json, text/event-stream'})
        open_request = opener or build_opener(ProxyHandler({}), _NoRedirect()).open
        initialized, session = _request(open_request, url, headers, 'initialize',
            dict(protocolVersion='2025-03-26', capabilities={}, clientInfo=dict(name='profc-memory-candidates', version='1')), 1)
        protocol = initialized.get('protocolVersion')
        if not isinstance(protocol, str) or not re.fullmatch(r'\d{4}-\d{2}-\d{2}', protocol):
            return []
        headers['Mcp-Protocol-Version'] = protocol
        if session is not None:
            if not isinstance(session, str) or not re.fullmatch(r'[!-~]{1,256}', session):
                return []
            headers['Mcp-Session-Id'] = session
        _request(open_request, url, headers, 'notifications/initialized', {})
        result, _ = _request(open_request, url, headers, 'tools/call',
                             dict(name='entity', arguments=dict(name=query.strip())), 2)
        slugs = _slugs(_entity(result))
        return [dict(work_id=work_id) for work_id in dict.fromkeys(known)
                if work_id in slugs or 'projects/' + work_id in slugs]
    except (OSError, ValueError, TypeError, KeyError, AttributeError, RecursionError):
        return []
