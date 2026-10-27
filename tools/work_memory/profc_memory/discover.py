"""Bounded document discovery from configured evidence; never crawl links."""

from datetime import datetime, timedelta, timezone
import hashlib
import json
import re
import sqlite3
from urllib.parse import urlsplit

from .collect import bounded_anchor_matches, sanitize_content


CURSOR = 'discovery:documents'
DOMAINS = ('feishu.cn', 'larksuite.com', 'larkoffice.com')
MAX_DOCUMENTS = 500
MAX_PROVENANCE = 8
LINK = re.compile(r'https://[^\s<>"\'\\\]\)},;，。；、！？）】》]+', re.IGNORECASE)


def _reference(url):
    try:
        parsed = urlsplit(url)
        host = parsed.hostname or ''
        if (parsed.scheme.lower() != 'https' or parsed.username is not None or parsed.password is not None
                or parsed.port is not None or not any(host == domain or host.endswith('.' + domain) for domain in DOMAINS)
                or sanitize_content(url)[1]):
            return None
        path = re.fullmatch(r'/(docx|wiki)/([A-Za-z0-9]{1,128})/?', parsed.path)
        if path is None:
            return None
        identity = host + '/' + path[1] + '/' + path[2]
        return 'discovered-document-' + hashlib.sha256(identity.encode()).hexdigest()[:24]
    except (ValueError, TypeError):
        return None


def _scope(source):
    values = {key: source.get(key) for key in ('id', 'kind', 'chat_id', 'chat_type', 'query', 'doc', 'url', 'work_ids')}
    return hashlib.sha256(json.dumps(values, sort_keys=True).encode()).hexdigest()


def discover_documents(record, source, works, owner_id):
    """Return exact URLs and provenance; uncertain associations have no work IDs."""
    if record.get('owner_id') != owner_id or record.get('visibility') != 'owner' or record.get('deleted'):
        return []
    allowed = source.get('work_ids') or [work['work_id'] for work in works]
    matched = [work['work_id'] for work in works if work['work_id'] in allowed
               and bounded_anchor_matches(record['content'], work.get('anchors', []))]
    if source['kind'] == 'document' and len(source.get('work_ids', [])) == 1:
        linked = source['work_ids']
    else:
        linked = matched if len(matched) == 1 else []
    provenance = dict(configured_source_id=source['id'], scope=_scope(source),
                      source_id=record['source_id'], revision=record['revision'], url=record['url'],
                      work_ids=linked, discovered_at=record['observed_at'])
    found = {}
    for match in LINK.finditer(record['content']):
        url = match[0]
        identity = _reference(url)
        if identity and identity not in found:
            found[identity] = dict(id=identity, url=url, work_ids=linked, provenance=[provenance])
            if len(found) == MAX_DOCUMENTS:
                break
    return list(found.values())


def _work_ids(entry, config):
    sources = {source['id']: source for source in config['sources']}
    known = {work['work_id'] for work in config['works']}
    linked = set()
    for origin in entry['provenance']:
        source = sources.get(origin.get('configured_source_id'))
        if source and origin.get('scope') == _scope(source):
            linked.update(set(origin.get('work_ids', [])) & known & set(source.get('work_ids') or known))
    return sorted(linked)


def collect_discovered(config, ledger, discoveries, until, collector):
    """Reuse the collector for a few approved links; retain a private fair queue."""
    stats = dict(collected=0, discovered_documents=0, pending_documents=0, errors=[])
    queue = ledger.get_cursor(CURSOR) or dict(schema_version=1, documents={})
    if (not isinstance(queue, dict) or queue.get('schema_version') != 1
            or not isinstance(queue.get('documents'), dict) or len(queue['documents']) > MAX_DOCUMENTS):
        return dict(stats, errors=[dict(type='invalid_discovery_state')])
    documents = queue['documents']
    for identity, entry in documents.items():
        if (not isinstance(entry, dict) or _reference(entry.get('url')) != identity
                or not isinstance(entry.get('provenance'), list) or len(entry['provenance']) > MAX_PROVENANCE
                or not all(isinstance(origin, dict) for origin in entry['provenance'])):
            return dict(stats, errors=[dict(type='invalid_discovery_state')])
    for candidate in discoveries:
        identity = candidate['id']
        if identity not in documents:
            if len(documents) == MAX_DOCUMENTS:
                if not stats['errors']:
                    stats['errors'].append(dict(type='discovery_limit'))
                continue
            documents[identity] = candidate
            stats['discovered_documents'] += 1
        else:
            entry = documents[identity]
            for origin in candidate['provenance']:
                signature = {key: value for key, value in origin.items() if key != 'discovered_at'}
                if not any(signature == {key: value for key, value in old.items() if key != 'discovered_at'}
                           for old in entry['provenance']):
                    entry['provenance'].append(origin)
            if len(entry['provenance']) > MAX_PROVENANCE:
                entry['provenance'] = entry['provenance'][-MAX_PROVENANCE:]
                entry['provenance_truncated'] = True
    for entry in documents.values():
        entry['work_ids'] = _work_ids(entry, config)
        stats['pending_documents'] += int(not entry['work_ids'])
    ledger.set_cursor(CURSOR, queue)
    now = datetime.now(timezone.utc)
    ready = [entry for entry in documents.values() if entry['work_ids']
             and entry.get('retry_after', '') <= now.isoformat()]
    ready.sort(key=lambda entry: entry.get('attempted_at', ''))
    for entry in ready[:config.get('discovery', {}).get('max_documents_per_run', 2)]:
        entry['attempted_at'] = now.isoformat()
        source = dict(id=entry['id'], kind='document', doc=entry['url'], url=entry['url'],
                      owner_id=config['owner_id'], work_ids=entry['work_ids'])
        try:
            result = collector(source, ledger.get_cursor('source:' + entry['id']), until)
            for record in result['records']:
                if record.get('kind') != 'document' or _reference(record.get('url')) is None:
                    raise ValueError('untrusted discovered document')
                requested_kind, requested_token = urlsplit(entry['url']).path.strip('/').split('/')
                returned_kind, returned_token = urlsplit(record['url']).path.strip('/').split('/')
                if ((requested_kind == 'docx' and record.get('object_id') != requested_token)
                        or (returned_kind == 'docx' and record.get('object_id') != returned_token)
                        or (returned_kind == 'wiki' and _reference(record['url']) != entry['id'])):
                    raise ValueError('discovered document identity mismatch')
                try:
                    existing = ledger.get_source(record['source_id'], record['revision'])
                except ValueError:
                    existing = None
                if existing is not None:
                    ignored = {'url', 'observed_at', 'work_ids'}
                    if ({key: value for key, value in existing.items() if key not in ignored}
                            != {key: value for key, value in record.items() if key not in ignored}):
                        raise ValueError('source revision is immutable')
                    # Keep the stored evidence URL; the exact alias remains in provenance.
                    record = {key: value for key, value in existing.items() if key != 'work_ids'}
                stats['collected'] += int(ledger.ingest_source(record, entry['work_ids']))
            error = result.get('error', {}).get('type', 'incomplete') if not result.get('complete') else None
            if not error:
                ledger.set_cursor('source:' + entry['id'], result['cursor'])
        except (ValueError, KeyError, TypeError, OSError, sqlite3.Error):
            error = 'discovery_collection_failed'
        if error:
            stats['errors'].append(dict(type=error))
            ledger.record_failure(entry['id'], error)
            entry.update(error=error, retry_after=(now + timedelta(hours=1)).isoformat())
        else:
            entry.pop('error', None)
            entry.pop('retry_after', None)
            entry['collected_at'] = now.isoformat()
        ledger.set_cursor(CURSOR, queue)
    return stats
