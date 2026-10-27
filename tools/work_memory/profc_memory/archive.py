"""Owner-private, human-readable project dossiers backed by immutable sources.

All readers and writers use one flock. A fully staged directory replaces the
current dossier; a failed installation restores the old directory. Original
ledger revisions are only read, never rewritten by this module.
"""

from contextlib import closing, contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import tempfile
from urllib.parse import urlsplit,parse_qs

from .collect import sanitize_content
from .evidence import visible_text


SECTIONS = ('现状', '背景', '问题', '决策', '近期关注')
HUMAN = re.compile(r'<!-- human:start -->(.*?)<!-- human:end -->', re.S)


def _json(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + '\n'


def _id(value):
    if not isinstance(value, str) or not re.fullmatch(r'[a-z0-9_-]{1,64}', value):
        raise ValueError('unsafe dossier identifier')
    return value


def _private(path, directory=False):
    if path.is_symlink() or not path.exists():
        raise ValueError('unsafe dossier path')
    info = path.stat()
    if (info.st_uid != os.getuid() or info.st_mode & 0o077
            or (directory and not path.is_dir()) or (not directory and not path.is_file())):
        raise ValueError('dossier requires owner-private permissions')


def _root(state_dir):
    root = Path(state_dir).absolute()
    # Match the ledger's fixed macOS system aliases, never user-created links.
    for alias in ('/var', '/tmp'):
        path = Path(alias)
        if root.is_relative_to(path) and path.is_symlink() and path.resolve() == Path('/private' + alias):
            root = path.resolve() / root.relative_to(path)
    if '..' in root.parts or any(p.is_symlink() for p in (root, *root.parents)):
        raise ValueError('unsafe dossier path')
    _private(root, True)
    _private(root / 'config.json')
    config = json.loads((root / 'config.json').read_text(encoding='utf-8'))
    if config.get('schema_version') != 1 or not isinstance(config.get('owner_id'), str) or not config['owner_id']:
        raise ValueError('invalid private config')
    return root, config


def _tree(path):
    _private(path, True)
    for item in path.rglob('*'):
        _private(item, item.is_dir())


def dossier_directory(root, create=False):
    """Locate the single Markdown canonical store from the private owner config.

    A missing safe path is returned for read-only catalog discovery. Creation
    touches only missing directories; existing ancestor permissions stay intact.
    """
    root, config = _root(root)
    settings = config.get('dossiers', {})
    if not isinstance(settings, dict):
        raise ValueError('invalid dossier directory config')
    value = settings.get('directory', str(root / 'dossiers'))
    if not isinstance(value, str) or not value or not Path(value).is_absolute():
        raise ValueError('dossier directory must be an absolute path')
    directory = Path(value)
    if '..' in directory.parts or any(p.is_symlink() for p in (directory, *directory.parents)):
        raise ValueError('unsafe dossier directory')
    missing, ancestor = [], directory
    while not ancestor.exists():
        missing.append(ancestor)
        ancestor = ancestor.parent
    if not ancestor.is_dir():
        raise ValueError('dossier ancestor must be a directory')
    if create:
        for path in reversed(missing):
            path.mkdir(mode=0o700)
    if directory.exists():
        _private(directory, True)
        if directory.stat().st_mode & 0o777 != 0o700:
            raise ValueError('dossier directory requires mode 0700')
    return directory


@contextmanager
def _lock(root, create=False):
    directory = dossier_directory(root, create=create)
    _private(directory, True)
    path = directory / '.archive.lock'
    if path.is_symlink():
        raise ValueError('unsafe dossier lock')
    flags = os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0)
    descriptor = os.open(path, flags | (os.O_CREAT if create else 0), 0o600)
    try:
        _private(path)
        fcntl.flock(descriptor, fcntl.LOCK_EX if create else fcntl.LOCK_SH)
        yield directory
    finally:
        os.close(descriptor)


def _stamp(now):
    if now is None:
        return datetime.now(timezone.utc).isoformat()
    if isinstance(now, datetime):
        parsed = now
        now = now.isoformat()
    else:
        parsed = datetime.fromisoformat(now.replace('Z', '+00:00'))
    if parsed.tzinfo is None:
        raise ValueError('build time must include timezone')
    return now


def _human(text):
    matches = list(HUMAN.finditer(text))
    if (len(matches) != 1 or text.count('<!-- human:start -->') != 1
            or text.count('<!-- human:end -->') != 1):
        raise ValueError('invalid human region')
    return matches[0][1]


def _hash(text):
    _human(text)
    return hashlib.sha256(HUMAN.sub('<!-- human:start --><!-- human:end -->', text).encode()).hexdigest()


def _write(path, text):
    missing, parent = [], path.parent
    while not parent.exists():
        missing.append(parent)
        parent = parent.parent
    for directory in reversed(missing):
        directory.mkdir(mode=0o700)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    with os.fdopen(descriptor, 'w', encoding='utf-8') as handle:
        handle.write(text)
        handle.flush()
        os.fsync(handle.fileno())


def _plain(value):
    if (not isinstance(value, str) or '<' in value or re.search(r'(?im)^#{1,6}\s', value)
            or re.search(r'(?i)(?:[a-z][a-z0-9+.-]*://|\]\s*\(|\]\s*:)', value)
            or sanitize_content(value)[1]):
        raise ValueError('draft text must not contain links, metadata, headings or credentials')
    return value.strip()


def _label(value, fallback):
    value = value if isinstance(value, str) and value.strip() else fallback
    return re.sub(r'[\r\n\[\]<>`]', ' ', value)


def _source_display(source):
    value = source.get('occurred_at')
    occurred = datetime.fromisoformat(value.replace('Z', '+00:00')).strftime('%Y-%m-%d %H:%M') if value else '未知'
    person = source.get('author') or source.get('actor')
    actor = _label(person if person != 'unknown' else None, '未知')
    kind = '文档' if source.get('kind') == 'document' else '聊天消息'
    title = source.get('title')
    if not isinstance(title, str) or not title.strip():
        for tag in ('title', 'h1'):
            match = re.search(r'<' + tag + r'\b[^>]*>.*?</' + tag + r'\s*>', source.get('content', ''), re.I | re.S)
            if match and visible_text(match[0]).strip():
                title = visible_text(match[0]).strip()
                break
    return _label(title, ' · '.join((occurred, actor, kind))), actor, occurred


def _source_map(root, snapshot):
    path = root / 'ledger.sqlite3'
    _private(path)
    sources = {}
    with closing(sqlite3.connect(path.as_uri() + '?mode=ro', uri=True)) as db:
        row = db.execute("SELECT value FROM meta WHERE key='owner_id'").fetchone()
        if not row or row[0] != snapshot['owner_id']:
            raise PermissionError('ledger owner mismatch')
        if not db.execute('SELECT 1 FROM works WHERE work_id=?', (snapshot['work_id'],)).fetchone():
            raise ValueError('snapshot work is not in ledger')
        for source in snapshot.get('sources', []):
            key = (source['source_id'], source['revision'])
            row = db.execute('SELECT data FROM sources WHERE source_id=? AND revision=?', key).fetchone()
            if not row:
                raise ValueError('snapshot source is not in ledger')
            linked = db.execute('''SELECT 1 FROM source_works WHERE source_id=? AND revision=? AND work_id=?
                UNION SELECT 1 FROM events WHERE source_id=? AND revision=? AND work_id=?''',
                (*key, snapshot['work_id'], *key, snapshot['work_id'])).fetchone()
            if not linked:
                raise ValueError('snapshot source is unrelated to this work')
            stored = json.loads(row[0])
            if (stored.get('owner_id') != snapshot['owner_id'] or stored.get('visibility') != 'owner'):
                raise PermissionError('source owner mismatch')
            if any(source.get(k) != stored.get(k) for k in ('content', 'url')):
                raise ValueError('snapshot source differs from ledger')
            sources[key] = stored
    return sources


def _pages(root, snapshot, draft, config):
    sections = draft.get('sections')
    if not isinstance(sections, dict) or set(sections) != set(SECTIONS):
        raise ValueError('draft must contain exactly the five dossier sections')
    sources, used = _source_map(root, snapshot), {}

    def references(refs):
        if not isinstance(refs, list):
            raise ValueError('source_refs must be a list')
        links, metadata = [], []
        for ref in refs:
            if not isinstance(ref, dict) or set(ref) != {'source_id', 'revision'}:
                raise ValueError('exact source_id and revision required')
            key = (ref['source_id'], ref['revision'])
            if key not in sources:
                raise ValueError('unknown source revision')
            source = sources[key]
            url = source.get('url', '')
            parsed = urlsplit(url)
            if (parsed.scheme not in ('http', 'https') or not parsed.netloc or parsed.username
                    or parsed.password or re.search(r'[\s<>]', url) or sanitize_content(url)[1]):
                raise ValueError('reference requires a safe original URL')
            used[key] = source
            links.append('[' + _source_display(source)[0] + '](<' + url + '>)')
            metadata.append(ref)
        if not links:
            return ''
        return '\n\n来源：' + '；'.join(dict.fromkeys(links)) + '\n<!-- source_refs: ' + json.dumps(metadata, ensure_ascii=False).replace('--', '\\u002d\\u002d') + ' -->'

    def paragraphs(value, extra=None):
        if isinstance(value, str):
            value = [{'text': value, 'source_refs': []}]
        if not isinstance(value, list):
            raise ValueError('section must be text or structured paragraphs')
        parts = []
        for part in value:
            if not isinstance(part, dict):
                raise ValueError('paragraph must be structured')
            parts.append(_plain(part.get('text')) + references(part.get('source_refs', [])))
        if extra:
            parts.append(references(extra))
        return '\n\n'.join(parts) or '暂无可确认的资料。'

    pages = {'home.md': '# ' + _label(snapshot.get('title'), snapshot['work_id']) + '\n\n{BUILD}\n\n' +
             '\n\n'.join('## ' + name + '\n\n' + paragraphs(sections[name]) for name in SECTIONS)}
    decisions = draft.get('decisions', [])
    if not isinstance(decisions, list):
        raise ValueError('decisions must be a list')
    slugs = set()
    for decision in decisions:
        slug = _id(decision.get('slug'))
        if slug in slugs:
            raise ValueError('duplicate decision slug')
        slugs.add(slug)
        status = decision.get('status', 'current')
        if status not in ('current', 'superseded', 'proposed'):
            raise ValueError('invalid decision status')
        target = decision.get('superseded_by')
        if target:
            _id(target)
        label = {'current': '当前决策', 'superseded': '已被替代', 'proposed': '待确认'}[status]
        pages['decisions/' + slug + '.md'] = ('# ' + _plain(decision.get('title')) + '\n\n状态：' + label +
            ('；后续问题档案：[查看](' + target + '.md)' if target else '') + '\n\n' +
            paragraphs(decision.get('body'), decision.get('source_refs')))
    if any(d.get('superseded_by') and d['superseded_by'] not in slugs for d in decisions):
        raise ValueError('decision replacement must exist in this draft')
    if decisions:
        links = '；'.join('[' + _plain(d['title']) + '](decisions/' + d['slug'] + '.md)' for d in decisions)
        pages['home.md'] = pages['home.md'].replace('\n\n## 近期关注', '\n\n决策过程：' + links + '\n\n## 近期关注', 1)
    entries = []
    for key, source in sorted(used.items()):
        title, actor, occurred = _source_display(source)
        source_type = '文档' if source.get('kind') == 'document' else '聊天（类型未核实）'
        if source.get('kind') == 'document' and title.startswith(('文字记录','会议纪要','妙记')):
            source_type = '会议产物'
        params = parse_qs(urlsplit(source['url']).query)
        chat_ids = {value for name in ('openChatId','open_chat_id','openchatid') for value in params.get(name,[])}
        for definition in config.get('sources',[]):
            if definition.get('chat_id') in chat_ids and definition.get('chat_type_verified_at'):
                source_type = {'p2p':'私聊','group':'群聊'}.get(definition.get('chat_type'),source_type)
                break
        modification = ''
        if source.get('modified_at') and source.get('modified_at') != source.get('occurred_at'):
            changed = datetime.fromisoformat(source['modified_at'].replace('Z','+00:00')).strftime('%Y-%m-%d %H:%M %z')
            modification = '；此版修改时间：' + changed
        entries.append('- [' + title + '](<' + source['url'] + '>)\n  人物：' + actor +
                       '；来源类型：' + source_type +
                       '；发生时间：' + occurred + modification +
                       '\n  <!-- collected_at: ' + json.dumps(source.get('observed_at'), ensure_ascii=False) + ' -->' +
                       '\n  <!-- source_refs: ' + json.dumps([dict(source_id=key[0], revision=key[1])], ensure_ascii=False).replace('--', '\\u002d\\u002d') + ' -->')
    pages['sources.md'] = ('# 来源\n\n仅本人可见。来源类型只说明材料来自会议、群聊、私聊或文档，'
        '不能据此自动提高陈述的权威；决定以明确确认及其阶段、范围为准。'
        '已编辑消息的创建时间不能单独证明此版文字当时已经存在。\n\n'
        + ('\n\n'.join(entries) or '暂无引用来源。'))
    return {path: text + '\n\n**人工补充**\n\n<!-- human:start -->\n<!-- human:end -->\n' for path, text in pages.items()}


def _current(path, owner, work_id=None):
    _tree(path)
    manifest = json.loads((path / 'manifest.json').read_text(encoding='utf-8'))
    if manifest.get('owner_id') != owner or manifest.get('visibility') != 'owner':
        raise PermissionError('dossier owner mismatch')
    if manifest.get('work_id') != (work_id or path.name) or manifest.get('schema_version') != 1:
        raise ValueError('invalid dossier manifest')
    pages, missing = {}, []
    for relative in manifest['machine_hashes']:
        if not re.fullmatch(r'(?:home|sources)\.md|decisions/[a-z0-9_-]{1,64}\.md', relative):
            raise ValueError('unsafe manifest path')
        if not (path / relative).exists():
            missing.append(relative)
        else:
            pages[relative] = (path / relative).read_text(encoding='utf-8')
    actual = {str(p.relative_to(path)) for p in path.glob('*.md')} | {str(p.relative_to(path)) for p in (path / 'decisions').glob('*.md')}
    drift = sorted(actual ^ set(pages)) + missing
    for name, text in pages.items():
        try:
            if _hash(text) != manifest['machine_hashes'][name]:
                drift.append(name)
        except ValueError:
            drift.append(name)
    return manifest, pages, sorted(set(drift))


def _recover(directory, work_id, owner):
    target, backup = directory / work_id, directory / ('.backup-' + work_id)
    if backup.exists() or backup.is_symlink():
        previous, _, _ = _current(backup, owner, work_id)
        if not target.exists() and not target.is_symlink():
            os.replace(backup, target)
        else:
            current, _, drift = _current(target, owner)
            if drift or int(current['version']) != int(previous['version']) + 1:
                raise ValueError('unfinished dossier transaction requires review')
            shutil.rmtree(backup)


def apply_dossier(state_dir, snapshot, draft, *, now=None):
    """Validate and publish one private dossier; return updated/unchanged/drift."""
    root, config = _root(state_dir)
    work_id = _id(snapshot.get('work_id'))
    if snapshot.get('owner_id') != config['owner_id'] or snapshot.get('visibility') != 'owner':
        raise PermissionError('only matching owner-private snapshots are supported')
    stamp = _stamp(now)
    reason = _plain(draft.get('reason', '根据现有资料重建'))
    rendered = _pages(root, snapshot, draft, config)
    with _lock(root, create=True) as directory:
        _recover(directory, work_id, config['owner_id'])
        target = directory / work_id
        old, pages = None, {}
        if target.exists() or target.is_symlink():
            old, pages, drift = _current(target, config['owner_id'])
            if drift:
                return dict(status='drift', work_id=work_id, version=old['version'], paths=drift)
        for name, text in list(rendered.items()):
            if name in pages:
                rendered[name] = HUMAN.sub(lambda _: '<!-- human:start -->' + _human(pages[name]) + '<!-- human:end -->', text)
        # Human notes from retired decision files must remain available.
        for name in set(pages) - set(rendered):
            if _human(pages[name]).strip():
                rendered[name] = pages[name]
        content_hash = hashlib.sha256(_json(dict(pages={name: _hash(text) for name, text in rendered.items()}, title=snapshot.get('title'),
            aliases=snapshot.get('work', {}).get('aliases', []))).encode()).hexdigest()
        if old and content_hash == old.get('content_hash'):
            return dict(status='unchanged', work_id=work_id, version=old['version'], receipt=old['receipt'])
        version = str(int(old['version']) + 1 if old else 1)
        receipt = dict(version=version, built_at=stamp, reason=reason,
                       reconstruction='根据现有资料于 ' + stamp + ' 重建')
        manifest = dict(schema_version=1, work_id=work_id, title=snapshot.get('title', work_id),
            aliases=snapshot.get('work', {}).get('aliases', []), owner_id=config['owner_id'], visibility='owner',
            version=version, updated_at=stamp, content_hash=content_hash, receipt=receipt,
            sections={**{s: dict(path='home.md', heading=s, level=2) for s in SECTIONS},
                      'sources': dict(path='sources.md'), 'decisions': dict(path='decisions')})
        final = {name: text.replace('{BUILD}', receipt['reconstruction'], 1) if name == 'home.md' else text
                 for name, text in rendered.items()}
        manifest['machine_hashes'] = {name: _hash(text) for name, text in final.items()}
        stage = Path(tempfile.mkdtemp(prefix='.stage-', dir=directory))
        backup = directory / ('.backup-' + work_id)
        if backup.exists() or backup.is_symlink():
            shutil.rmtree(stage)
            raise ValueError('unfinished dossier transaction requires recovery')
        moved = False
        try:
            if old:
                shutil.copytree(target / 'versions', stage / 'versions')
            for name, text in final.items():
                _write(stage / name, text)
                _write(stage / 'versions' / version / name, text)
            for base in (stage, stage / 'versions' / version):
                _write(base / 'manifest.json', _json(manifest))
                _write(base / 'receipt.json', _json(receipt))
            _tree(stage)
            if old:
                os.replace(target, backup)
                moved = True
            try:
                os.replace(stage, target)
            except BaseException:
                if moved:
                    os.replace(backup, target)
                raise
            if moved:
                shutil.rmtree(backup)
        finally:
            if stage.exists():
                shutil.rmtree(stage)
        return dict(status='updated', work_id=work_id, version=version, receipt=receipt,
                    path=str(target), manifest=str(target / 'manifest.json'))


def read_dossier(state_dir, work_id, requester_id, section='home', *, audience='owner'):
    """Read one consistent private version without exposing machine identifiers."""
    root, config = _root(state_dir)
    _id(work_id)
    if requester_id != config['owner_id'] or audience != 'owner':
        raise PermissionError('shared or non-owner dossier access is not supported')
    with _lock(root) as directory:
        target = directory / work_id
        if not target.exists() and not target.is_symlink():
            target = directory / ('.backup-' + work_id)
        manifest, pages, drift = _current(target, config['owner_id'], work_id)
        if drift:
            content = ''
        elif section == 'home':
            content = pages['home.md']
        elif section == 'sources':
            content = pages['sources.md']
        elif section == 'decisions':
            content = '\n\n'.join(text for name, text in sorted(pages.items()) if name.startswith('decisions/'))
        elif section == 'history':
            entries = []
            folders = [p for p in (target / 'versions').iterdir() if p.is_dir() and p.name.isdigit()]
            for folder in sorted(folders, key=lambda p:int(p.name)):
                receipt = json.loads((folder / 'receipt.json').read_text(encoding='utf-8'))
                entries.append('版本 ' + folder.name + '｜构建时间：' + receipt.get('built_at','未知')
                               + '\n变更原因：' + receipt.get('reason','未记录'))
            content = ('# 档案版本历史\n\n这些是档案实际构建与修订的时间；首版依据现存材料重建，'
                       '不能当作过去某天已经存在的首页。原始材料的发生时间和采集时间保留在来源快照中。\n\n'
                       + '\n\n'.join(entries))
        elif isinstance(section, str) and section.startswith('decision:'):
            slug = _id(section.removeprefix('decision:'))
            content = pages.get('decisions/' + slug + '.md', '')
            if not content:
                raise ValueError('unknown decision')
        elif section in SECTIONS:
            match = re.search(r'^## ' + re.escape(section) + r'\n(.*?)(?=^## |\Z)', pages['home.md'], re.M | re.S)
            content = match[1] if match else ''
        else:
            raise ValueError('unknown dossier section')
        content = re.sub(r'<!--.*?-->', '', content, flags=re.S).strip()
        return dict(status='drift' if drift else 'ok', work_id=work_id, title=manifest['title'],
                    version=manifest['version'], updated_at=manifest['updated_at'], section=section,
                    content=content, content_hash=hashlib.sha256(content.encode()).hexdigest(),
                    receipt=manifest['receipt'], drift=drift)
