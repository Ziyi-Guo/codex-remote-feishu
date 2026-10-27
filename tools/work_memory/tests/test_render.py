import unittest

from profc_memory.render import build_context, render_work


class RenderTests(unittest.TestCase):
    def snapshot(self):
        return dict(work_id='byok', title='BYOK', owner_id='owner', visibility='owner',
                    revision=3, updated_at='2026-10-27T10:00:00Z', candidates=1,
                    sources=[dict(source_id='s1', revision='rev1', kind='message', occurred_at=None,
                                  observed_at='2026-10-27T10:00:00Z', url='https://example.test/raw?a=1&b=2')],
                    events=[], current_events=[dict(event_id='e1', source_id='s1', revision='rev1',
                    work_id='byok', kind='progress', claim='部署完成，尚未验收', quote='尚未验收',
                    stage='deployment', facet='status', actor='alice', status='accepted',
                    occurred_at='2026-10-20T10:00:00Z', observed_at='2026-10-27T10:00:00Z', supersedes=[])])

    def test_render_preserves_provenance_and_excludes_candidate_claims(self):
        snapshot = self.snapshot()
        snapshot['current_events'].append(dict(snapshot['current_events'][0], status='candidate', claim='候选不能出现在正式上下文'))
        rendered = render_work(snapshot)
        self.assertIn('部署完成，尚未验收', rendered)
        self.assertIn('s1', rendered)
        self.assertIn('rev1', rendered)
        self.assertIn('https://example.test/raw?a=1&b=2', rendered)
        self.assertNotIn('候选不能出现在正式上下文', rendered)
        context = build_context(snapshot)
        self.assertEqual(1, context['schema_version'])
        self.assertEqual('owner', context['visibility'])
        self.assertEqual('3', context['version'])
        self.assertIn('s1', context['context'])

    def test_budget_drops_whole_facts(self):
        snapshot = self.snapshot()
        snapshot['current_events'][0]['claim'] = '完整事实' * 100
        result = build_context(snapshot, max_chars=100)
        self.assertLessEqual(len(result['context']), 100)
        self.assertNotIn('完整事实', result['context'])
        self.assertEqual(1, result['dropped_count'])

    def test_budget_prefers_recent_occurrence_without_replacing_history(self):
        snapshot = self.snapshot()
        old = dict(snapshot['current_events'][0], kind='decision', claim='旧的决定', event_id='old')
        new = dict(old, claim='新的决定', event_id='new', occurred_at='2026-10-26T10:00:00Z')
        unknown = dict(old, claim='未知决定', event_id='unknown', occurred_at=None,
                       observed_at='2026-10-28T10:00:00Z')
        snapshot['current_events'] = [old, unknown, new]
        rendered = build_context(snapshot)
        self.assertLess(rendered['context'].index('新的决定'), rendered['context'].index('旧的决定'))
        self.assertLess(rendered['context'].index('旧的决定'), rendered['context'].index('未知决定'))
        budget = len(build_context(dict(snapshot, current_events=[new]))['context']) + 70
        compact = build_context(snapshot, max_chars=budget)
        self.assertIn('新的决定', compact['context'])
        self.assertNotIn('旧的决定', compact['context'])
        self.assertEqual(2, compact['dropped_count'])

    def test_missing_source_is_never_fabricated(self):
        snapshot = self.snapshot()
        snapshot['sources'] = []
        with self.assertRaises(ValueError):
            build_context(snapshot)

    def test_non_owner_snapshot_rejected(self):
        snapshot = self.snapshot()
        snapshot['visibility'] = 'shared'
        with self.assertRaises(PermissionError):
            render_work(snapshot)

    def test_parallel_evidence_is_labeled_without_choosing_latest_observation(self):
        snapshot = self.snapshot()
        snapshot['current_events'].append(dict(snapshot['current_events'][0], event_id='e2', claim='并列的另一说法'))
        context = build_context(snapshot)['context']
        self.assertIn('并列记录', context)
        self.assertIn('e1', context)
        self.assertIn('e2', context)

    def test_evidence_markup_is_rendered_as_data(self):
        snapshot = self.snapshot()
        snapshot['current_events'][0]['quote'] = '<script>alert(1)</script>'
        rendered = render_work(snapshot)
        self.assertNotIn('<script>', rendered)
        self.assertIn('&lt;script&gt;', rendered)

    def test_page_keeps_superseded_history_and_replacement_with_exact_sources(self):
        snapshot = self.snapshot()
        old = dict(snapshot['current_events'][0], event_id='old', claim='旧决定：暂不部署', kind='decision')
        new = dict(old, event_id='new', source_id='s2', revision='rev2', claim='新决定：部署待验收',
                   supersedes=['old'], occurred_at='2026-10-22T10:00:00Z')
        unknown = dict(new, event_id='unknown', kind='proposal', claim='未知时间的提议', supersedes=[], occurred_at=None)
        snapshot['events'] = [old, new, unknown]
        snapshot['current_events'] = [new, unknown]
        snapshot['sources'].append(dict(snapshot['sources'][0], source_id='s2', revision='rev2', url='https://example.test/revision/2'))
        page = render_work(snapshot)
        for expected in ['当前有效陈述与决定', '历史变化', '未决问题与风险', '来源索引',
                         '旧决定：暂不部署', '新决定：部署待验收', '已被替代', '替代者：new',
                         '时间未知', 's1', 'rev1', 's2', 'rev2', 'https://example.test/revision/2']:
            self.assertIn(expected, page)
        context = build_context(snapshot)['context']
        self.assertNotIn('旧决定：暂不部署', context)
        self.assertIn('s2', context)
        self.assertIn('rev2', context)

    def test_context_prioritizes_decisions_questions_and_risks_without_raw_material(self):
        snapshot = self.snapshot()
        base = snapshot['current_events'][0]
        snapshot['sources'][0]['content'] = '巨量无关原始材料' * 1000
        snapshot['current_events'] = [dict(base, event_id='p', claim='低优先级进展'),
                                      dict(base, event_id='q', kind='open_question', claim='还需谁验收'),
                                      dict(base, event_id='d', kind='decision', claim='保持私有'),
                                      dict(base, event_id='r', kind='risk', claim='共享有泄漏风险')]
        only_important = dict(snapshot, current_events=snapshot['current_events'][1:])
        budget = len(build_context(only_important)['context'])
        result = build_context(snapshot, max_chars=budget)
        for claim in ['保持私有', '还需谁验收', '共享有泄漏风险']:
            self.assertIn(claim, result['context'])
        self.assertNotIn('低优先级进展', result['context'])
        self.assertNotIn('巨量无关原始材料', result['context'])
        self.assertEqual(1, result['dropped_count'])

    def test_chat_and_document_verification_are_only_reported_validation(self):
        for source_kind in ['message', 'chat', 'document']:
            snapshot = self.snapshot()
            snapshot['sources'][0]['kind'] = source_kind
            snapshot['current_events'][0].update(kind='verification', claim='测试已通过')
            for output in [render_work(snapshot), build_context(snapshot)['context']]:
                self.assertIn('报告验证', output)
                self.assertIn('未经本次独立核验', output)

    def test_unsafe_url_scheme_is_not_an_active_link(self):
        for url in ['javascript:alert(1)', 'data:text/html,<script>alert(1)</script>', 'https://x.test/`[click](javascript:bad)']:
            snapshot = self.snapshot()
            snapshot['sources'][0]['url'] = url
            for output in [render_work(snapshot)]:
                self.assertIn(url, output)
                self.assertNotIn('<' + url + '>', output)
                self.assertIn('仅作文本', output)
            self.assertNotIn(url, build_context(snapshot)['context'])

    def test_source_index_keeps_each_revision_url_even_without_events(self):
        snapshot = self.snapshot()
        snapshot['current_events'] = []
        snapshot['sources'].append(dict(snapshot['sources'][0], revision='rev2', url='https://example.test/raw?revision=2'))
        page = render_work(snapshot)
        for source in snapshot['sources']:
            self.assertIn(source['source_id'], page)
            self.assertIn(source['revision'], page)
            self.assertIn(source['url'], page)


if __name__ == '__main__':
    unittest.main()
