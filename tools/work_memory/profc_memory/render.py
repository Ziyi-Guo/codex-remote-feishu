"""Owner-only chronology and compact context, both derived from the same ledger."""

from collections import Counter, defaultdict
from datetime import datetime
from html import escape
import re
from urllib.parse import urlsplit
from .evidence import visible_text


KINDS = {'proposal': '提议', 'decision': '决定', 'progress': '进展',
         'verification': '报告验证', 'correction': '纠正', 'scope_extension': '范围扩展',
         'open_question': '未决问题', 'risk': '风险', 'source_update': '来源更新'}
STAGES = {'planning': '规划', 'discussion': '讨论', 'design': '设计', 'implementation': '实现',
          'development': '开发', 'review': '评审', 'testing': '测试', 'verification': '验证',
          'deployment': '部署', 'acceptance': '验收', 'operation': '运行', 'scope': '范围',
          'decision': '决策', 'unknown': '未知', 'reported': '来源报告', 'unspecified': '未明确'}
FACETS = {'status': '状态', 'scope': '范围', 'cost': '成本', 'risk': '风险', 'question': '问题',
          'delivery': '交付', 'billing': '计费', 'ownership': '归属', 'permission': '权限'}
PRIORITY = {'decision': 0, 'correction': 0, 'open_question': 1, 'risk': 1,
            'scope_extension': 2, 'verification': 3, 'progress': 4, 'proposal': 5, 'source_update': 6}


def _validate(snapshot):
    if snapshot.get('visibility') != 'owner' or not snapshot.get('owner_id'):
        raise PermissionError('only owner-private snapshots can be rendered')


def _escape(text):
    return escape(str(text), quote=False).replace('\\', '\\\\').replace('`', '\\`').replace('*', '\\*').replace('[', '\\[').replace(']', '\\]').replace('\n', ' ')


def _url(value):
    if not value:
        return '未提供链接'
    if any(c.isspace() or ord(c) < 32 for c in value):
        raise ValueError('source URL contains whitespace or control characters')
    parsed = urlsplit(value)
    if parsed.scheme.lower() in ('https', 'http') and parsed.netloc and not any(c in value for c in '`<>\\'):
        return '<' + value + '>'
    # Other schemes remain exact copyable evidence, never active links or markup.
    fence = '`' * (1 + max((len(m) for m in re.findall(r'`+', value)), default=0))
    return f'仅作文本：{fence} {value} {fence}'


def _source_map(snapshot):
    _validate(snapshot)
    return {(s['source_id'], s['revision']): s for s in snapshot['sources']}


def _accepted(events):
    return [e for e in events if e['status'] == 'accepted']


def _event_source(snapshot, event, sources):
    if event['work_id'] != snapshot['work_id']:
        raise ValueError('event belongs to another work')
    if event['kind'] not in KINDS:
        raise ValueError('unknown event kind')
    source = sources.get((event['source_id'], event['revision']))
    if source is None:
        raise ValueError('event source revision is missing')
    return source


def _entry(snapshot, event, sources, *, compact=False, parallel=False, state=''):
    source = _event_source(snapshot, event, sources)
    kind = KINDS[event['kind']]
    if event['kind'] == 'verification':
        kind += '（来源陈述，未经本次独立核验）'
    stage = _escape(STAGES.get(event['stage'], event['stage']))
    facet = _escape(FACETS.get(event['facet'], event['facet']))
    when = _escape(event['occurred_at'] or '时间未知')
    claim = visible_text(event['claim']) if source.get('kind') == 'document' else event['claim']
    lines = [f"- 【{kind}｜{stage}｜{facet}】{_escape(claim)}"]
    if state:
        lines.append('  ' + state)
    if parallel:
        lines.append('  并列记录：该阶段与维度有多条未明确替代的证据，不按采集时间选择结论。')
    lines.append(f'  发生：{when}')
    if not compact:
        lines.extend([f"  原话：{_escape(event['quote'])}",
                      f"  采集：{_escape(event['observed_at'])}；人物：{_escape(event['actor'])}"])
    lines.append(f"  来源 ID：{_escape(event['source_id'])}；来源版本：{_escape(event['revision'])}；事件 ID：{_escape(event['event_id'])}")
    if not compact:
        lines.append('  来源链接：' + _url(source.get('url', '')))
    return '\n'.join(lines)


def _section(title, entries, empty):
    return '## ' + title + '\n\n' + ('\n\n'.join(entries) if entries else empty)


def render_work(snapshot):
    sources = _source_map(snapshot)
    current = _accepted(snapshot['current_events'])
    history = _accepted(snapshot['events'])
    parallel = Counter((e['stage'], e['facet']) for e in current)
    main, unresolved = [], []
    for event in current:
        entry = _entry(snapshot, event, sources, parallel=parallel[(event['stage'], event['facet'])] > 1)
        (unresolved if event['kind'] in ('open_question', 'risk') else main).append(entry)
    replaced_by = defaultdict(list)
    for event in history:
        for previous in event['supersedes']:
            replaced_by[previous].append(event['event_id'])
    changes = []
    for event in history:
        replacements = replaced_by[event['event_id']]
        state = ('已被替代；替代者：' + '、'.join(_escape(e) for e in replacements)) if replacements else '当前保留'
        if event['supersedes']:
            state += '；替代记录：' + '、'.join(_escape(e) for e in event['supersedes'])
        changes.append(_entry(snapshot, event, sources, state=state))
    index = []
    for source in snapshot['sources']:
        index.append(f"- 来源 ID：{_escape(source['source_id'])}；来源版本：{_escape(source['revision'])}\n"
                     f"  发生：{_escape(source.get('occurred_at') or '时间未知')}；采集：{_escape(source.get('observed_at') or '时间未知')}\n"
                     f"  来源链接：{_url(source.get('url', ''))}")
    sections = [f"# {_escape(snapshot['title'])}\n\n事项：{_escape(snapshot['work_id'])}；仅所有者可见；版本：{snapshot['revision']}",
                _section('当前有效陈述与决定', main, '暂无已确认且未被明确替代的陈述或决定。'),
                _section('未决问题与风险', unresolved, '暂无已记录的未决问题或风险。'),
                _section('历史变化', changes, '暂无历史事件。'),
                _section('来源索引', index, '暂无来源。'),
                f"候选记录：{snapshot.get('candidates', 0)}（不进入正式上下文）。"]
    return '\n\n'.join(sections) + '\n'


def build_context(snapshot, max_chars=10000):
    if not isinstance(max_chars, int) or max_chars < 0:
        raise ValueError('max_chars must be a nonnegative integer')
    sources = _source_map(snapshot)
    current = _accepted(snapshot['current_events'])
    parallel = Counter((e['stage'], e['facet']) for e in current)
    entries, dropped, length = [], 0, 0
    # Newer occurrence wins space within the same kind, never supersession.
    # Unknown occurrence times must not become newest through collection time.
    def order(event):
        occurred = event.get('occurred_at')
        timestamp = datetime.fromisoformat(occurred.replace('Z', '+00:00')).timestamp() if occurred else float('-inf')
        return PRIORITY.get(event['kind'], 99), -timestamp, event['event_id']
    # Keep the small packet useful across stages: one latest entry from each
    # category before filling it with more decisions from the same category.
    ranked, counts = [], Counter()
    for event in sorted(current, key=order):
        category = event['kind']
        ranked.append((counts[category], *order(event), event))
        counts[category] += 1
    for *_, event in sorted(ranked, key=lambda row: row[:-1]):
        entry = _entry(snapshot, event, sources, compact=True, parallel=parallel[(event['stage'], event['facet'])] > 1)
        size = len(entry) + (2 if entries else 0)
        if length + size > max_chars:
            dropped += 1
        else:
            entries.append(entry)
            length += size
    return dict(schema_version=1, work_id=snapshot['work_id'], title=snapshot['title'],
                owner_id=snapshot['owner_id'], visibility='owner', version=str(snapshot['revision']),
                updated_at=snapshot['updated_at'], context='\n\n'.join(entries), dropped_count=dropped)
