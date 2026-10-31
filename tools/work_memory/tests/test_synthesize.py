import json
from pathlib import Path
import subprocess
import unittest

from profc_memory.synthesize import synthesize


def snapshot():
    source = dict(source_id='message:s1', revision='1', content='MVP仅支持官方Key，移动渠道在MVP后评估。',
                  owner_id='owner', visibility='owner', url='https://example.test/source')
    event = dict(source_id='message:s1', revision='1', kind='decision', claim=source['content'],
                 quote=source['content'], occurred_at='2026-10-26T12:00:00+08:00', actor='unknown',
                 status='accepted', stage='planning', facet='scope', supersedes=[])
    return dict(work_id='byok', title='BYOK', owner_id='owner', visibility='owner', revision=2,
                sources=[source], events=[event], current_events=[event])


def draft():
    ref = dict(source_id='message:s1', revision='1')
    paragraph = dict(text='官方Key MVP保留，移动渠道属于后续评估范围。', source_refs=[ref])
    return dict(complete=True, sections={k:[paragraph] for k in ('目标','现状','背景','问题','决策','近期关注')},
                decisions=[dict(slug='channel-scope', title='渠道范围', status='current', superseded_by='',
                                body=[paragraph])], reason='根据当前证据重建')


def large_snapshot(text_size=130):
    data = snapshot()
    source = data['sources'][0]
    source['source_id'] = 'message:' + 'synthetic-source-' * 5
    data['events'] = [dict(data['events'][0], event_id='evt_' + f'{i:064x}',
                          source_id=source['source_id'],
                          claim='虚构约定' * (text_size // 4) + str(i),
                          quote='虚构依据' * (text_size // 4) + str(i))
                      for i in range(180)]
    data['current_events'] = list(data['events'])
    output = draft()
    for paragraphs in [*output['sections'].values(), output['decisions'][0]['body']]:
        for paragraph in paragraphs:
            paragraph['source_refs'][0]['source_id'] = source['source_id']
    return data, output


class SynthesisTests(unittest.TestCase):
    def runner(self, data, hook=None):
        def run(argv, **kwargs):
            if hook:
                hook(argv, kwargs)
            Path(argv[argv.index('-o')+1]).write_text(json.dumps(data, ensure_ascii=False))
            return subprocess.CompletedProcess(argv, 0)
        return run

    def test_structured_dossier_with_exact_refs_and_isolated_runner(self):
        def inspect(argv, kwargs):
            self.assertIn('--ignore-user-config', argv)
            self.assertIn('MVP仅支持官方Key', kwargs['input'])
            self.assertIn('不要把提议写成已定', kwargs['input'])
            self.assertNotIn('http', kwargs['input'])
        result = synthesize(snapshot(), runner=self.runner(draft(), inspect))
        self.assertEqual([], result['errors'])
        self.assertEqual(2, result['source_revision'])
        self.assertEqual('current', result['draft']['decisions'][0]['status'])

    def test_unknown_reference_and_incomplete_never_return_publishable_draft(self):
        for change in ('source', 'incomplete', 'url'):
            data = draft()
            if change == 'source':data['sections']['现状'][0]['source_refs'][0]['revision'] = '999'
            if change == 'incomplete':data['complete'] = False
            if change == 'url':data['sections']['现状'][0]['text'] = '请看 https://invented.test'
            result = synthesize(snapshot(), runner=self.runner(data))
            self.assertTrue(result['errors'])
            self.assertNotIn('draft', result)

    def test_candidate_and_secret_never_reach_synthesis(self):
        data = snapshot()
        data['current_events'].append(dict(data['events'][0], status='candidate', claim='未确认的候选'))
        def inspect(argv, kwargs):self.assertNotIn('未确认的候选', kwargs['input'])
        self.assertFalse(synthesize(data, runner=self.runner(draft(), inspect))['errors'])
        data['events'][0]['claim'] = 'password=synthetic-secret'
        self.assertTrue(synthesize(data, runner=self.runner(draft()))['errors'])

    def test_proven_older_source_cannot_support_current_dossier_sections(self):
        data=snapshot()
        data['sources'][0]['kind']='document'
        data['sources'].append(dict(data['sources'][0],revision='2',content='新版决定：移动渠道仍待评估。'))
        result=synthesize(data,runner=self.runner(draft()))
        self.assertTrue(result['errors'])
        self.assertNotIn('draft',result)

    def test_large_supported_snapshot_reaches_runner_with_all_evidence_intact(self):
        data, output = large_snapshot()
        original = json.dumps(data, ensure_ascii=False)
        seen = []
        previous = '此前已保存的虚构项目摘要。' * 100
        def inspect(argv, kwargs):
            payload = json.loads(kwargs['input'].split('输入开始：\n', 1)[1])
            rows = [dict(zip(payload['event_fields'], row)) for row in payload['events']]
            self.assertEqual(180, len(rows))
            for event, row in zip(data['events'], rows):
                for field in ('event_id', 'source_id', 'revision', 'claim', 'quote',
                              'occurred_at', 'actor', 'kind', 'stage', 'facet', 'supersedes'):
                    self.assertEqual(event.get(field), row[field])
                self.assertTrue(row['is_current'])
                self.assertFalse(row['historical_only_source'])
            sources = [dict(zip(payload['source_fields'], row)) for row in payload['source_metadata']]
            for field in payload['source_fields']:
                self.assertEqual(data['sources'][0].get(field), sources[0][field])
            self.assertEqual(previous, payload['previous'])
            self.assertLessEqual(len(kwargs['input'].split('输入开始：\n', 1)[1]), 100000)
            self.assertIn('event_fields', kwargs['input'].split('输入开始：\n', 1)[0])
            seen.append(True)
        result = synthesize(data, previous=previous, runner=self.runner(output, inspect))
        self.assertEqual([], result['errors'])
        self.assertEqual([True], seen)
        self.assertEqual(original, json.dumps(data, ensure_ascii=False))

    def test_truly_oversized_snapshot_reports_capacity_without_calling_model(self):
        data, output = large_snapshot(text_size=600)
        seen = []
        result = synthesize(data, runner=self.runner(output, lambda *_: seen.append(True)))
        self.assertEqual('synthesis_input_limit', result['errors'][0]['type'])
        self.assertGreater(result['errors'][0]['input_chars'], 100000)
        self.assertEqual(100000, result['errors'][0]['max_input_chars'])
        self.assertEqual([], seen)
        self.assertNotIn('draft', result)

    def test_compacted_snapshot_still_rejects_stale_current_evidence(self):
        data, output = large_snapshot()
        data['sources'][0]['kind'] = 'document'
        data['sources'].append(dict(data['sources'][0], revision='2', content='虚构新版依据。'))
        seen = []
        result = synthesize(data, runner=self.runner(output, lambda *_: seen.append(True)))
        self.assertEqual([True], seen)
        self.assertTrue(result['errors'])
        self.assertNotIn('draft', result)


if __name__ == '__main__':unittest.main()
