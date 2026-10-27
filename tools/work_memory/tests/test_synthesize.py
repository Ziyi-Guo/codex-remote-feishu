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


if __name__ == '__main__':unittest.main()
