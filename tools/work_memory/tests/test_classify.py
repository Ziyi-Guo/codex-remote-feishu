import copy
import io
import json
from pathlib import Path
import subprocess
import unittest
from contextlib import redirect_stdout, redirect_stderr

from profc_memory.classify import classify_sources

SOURCE = dict(source_id='message:om_1', revision='1', content='部署完成，尚未验收。',
              occurred_at='2026-10-27T01:00:00+00:00', observed_at='2026-10-27T02:00:00+00:00',
              work_ids=['byok'])
WORKS = [dict(work_id='byok', title='自带密钥', aliases=['BYOK'])]


def event(**changes):
    ev = dict(work_id='byok', kind='progress', claim='部署完成，尚未验收。',
              quote='部署完成，尚未验收。', occurred_at=SOURCE['occurred_at'], actor='unknown',
              stage='deployment', facet='status', supersedes=[], status='accepted')
    ev.update(changes)
    return dict(source_id=SOURCE['source_id'], revision='1', event=ev)


def fake(payload, check=None):
    def run(argv, **kwargs):
        if check:
            check(argv, kwargs)
        Path(argv[argv.index('-o') + 1]).write_text(json.dumps(payload), encoding='utf-8')
        return subprocess.CompletedProcess(argv, 0, '', '')
    return run


class ClassifyTests(unittest.TestCase):
    def classify(self, payload=None, sources=None, existing=None, **kwargs):
        return classify_sources(sources or [copy.deepcopy(SOURCE)], WORKS, existing or [],
                                runner=fake(payload or dict(complete=True, events=[event()])), **kwargs)

    def test_runner_uses_private_stdin_and_cleans_temp(self):
        paths = []
        def check(argv, kwargs):
            self.assertEqual(['codex', 'exec'], argv[:2])
            for flag in ['--ignore-user-config', '--ephemeral', '--skip-git-repo-check', '--sandbox', '--output-schema']:
                self.assertIn(flag, argv)
            self.assertEqual('read-only', argv[argv.index('--sandbox') + 1])
            self.assertEqual('-', argv[-1])
            self.assertNotIn('--model', argv)
            self.assertNotIn(SOURCE['content'], ' '.join(argv))
            self.assertIn(SOURCE['content'], kwargs['input'])
            self.assertEqual(120, kwargs['timeout'])
            self.assertFalse(kwargs.get('shell', False))
            self.assertFalse((Path(kwargs['cwd']) / 'AGENTS.md').exists())
            schema = json.loads(Path(argv[argv.index('--output-schema') + 1]).read_text())
            self.assertFalse(schema['additionalProperties'])
            paths.append(Path(kwargs['cwd']))
        output = io.StringIO()
        with redirect_stdout(output), redirect_stderr(output):
            result = classify_sources([SOURCE], WORKS, [], runner=fake(dict(complete=True, events=[event()]), check))
        self.assertFalse(result['errors'])
        self.assertEqual('', output.getvalue())
        self.assertFalse(paths[0].exists())

    def test_hallucinated_quote_or_reference_rejects_batch(self):
        for field, value in [('quote', '已经验收成功'), ('work_id', 'unknown'), ('kind', 'fact')]:
            result = self.classify(dict(complete=True, events=[event(**{field: value})]))
            self.assertFalse(result['events'])
            self.assertEqual('invalid_evidence', result['errors'][0]['type'])
        for key in ['source_id', 'revision']:
            row = event()
            row[key] = 'invented'
            self.assertTrue(self.classify(dict(complete=True, events=[row]))['errors'])

    def test_weak_association_cannot_be_accepted(self):
        source = dict(SOURCE, work_ids=[])
        result = self.classify(sources=[source])
        self.assertFalse(result['errors'])
        self.assertEqual('candidate', result['events'][0]['event']['status'])

    def test_actor_and_claim_cannot_upgrade_evidence(self):
        result = self.classify(dict(complete=True, events=[event(actor='Alice', claim='全部验收成功')]))
        ev = result['events'][0]['event']
        self.assertEqual('unknown', ev['actor'])
        self.assertEqual(SOURCE['content'], ev['claim'])
        self.assertIn('尚未验收', ev['claim'])

    def test_verified_sender_metadata_reaches_runner_and_overrides_model_guess(self):
        source = dict(SOURCE, actor='真实发送者', actor_user_id='ou_sender')
        def check(argv, kwargs):
            self.assertIn('"actor_user_id": "ou_sender"', kwargs['input'])
        result = classify_sources([source], WORKS, [], runner=fake(dict(complete=True, events=[event(actor='臆测作者')]), check))
        self.assertFalse(result['errors'])
        self.assertEqual('真实发送者', result['events'][0]['event']['actor'])

    def test_forwarded_speech_actor_remains_unknown(self):
        source = dict(SOURCE, actor='unknown', actor_user_id=None,
                      forwarded_by=dict(name='转发者', user_id='ou_forwarder'))
        result = self.classify(dict(complete=True, events=[event(actor='转发者')]), sources=[source])
        self.assertEqual('unknown', result['events'][0]['event']['actor'])

    def test_quote_cannot_drop_the_negation_or_sentence_context(self):
        source = dict(SOURCE, content='尚未验收成功。')
        result = self.classify(dict(complete=True, events=[event(quote='验收成功', claim='验收成功')]), sources=[source])
        self.assertEqual('invalid_evidence', result['errors'][0]['type'])
        self.assertFalse(result['events'])

    def test_missing_occurrence_never_uses_observation(self):
        result = self.classify(dict(complete=True, events=[event(occurred_at=None)]),
                               sources=[dict(SOURCE, occurred_at=None)])
        self.assertIsNone(result['events'][0]['event']['occurred_at'])
        bad = self.classify(dict(complete=True, events=[event(occurred_at=SOURCE['observed_at'])]),
                            sources=[dict(SOURCE, occurred_at=None)])
        self.assertTrue(bad['errors'])

    def test_unproven_supersession_rejected(self):
        previous = dict(event()['event'], event_id='evt_old', occurred_at='2026-10-26T00:00:00+00:00')
        result = self.classify(dict(complete=True, events=[event(kind='correction', supersedes=['evt_old'])]),
                               existing=[previous])
        self.assertTrue(result['errors'])
        self.assertFalse(result['events'])

    def test_explicit_matching_supersession_allowed(self):
        previous = dict(event()['event'], claim='计划十月底上线', quote='计划十月底上线', event_id='evt_old',
                        occurred_at='2026-10-26T00:00:00+00:00')
        text = '更正“计划十月底上线”：延期至十一月。'
        source = dict(SOURCE, content=text)
        row = event(kind='correction', quote=text, claim=text, supersedes=['evt_old'])
        result = self.classify(dict(complete=True, events=[row]), sources=[source], existing=[previous])
        self.assertFalse(result['errors'])
        self.assertEqual(['evt_old'], result['events'][0]['event']['supersedes'])

    def test_incomplete_and_timeout_keep_pending(self):
        result = self.classify(dict(complete=False, events=[event()]))
        self.assertFalse(result['events'])
        self.assertEqual('incomplete', result['errors'][0]['type'])
        def timeout(*args, **kwargs):
            raise subprocess.TimeoutExpired('private prompt not printed', 1, output='secret raw')
        result = classify_sources([SOURCE], WORKS, [], runner=timeout)
        self.assertEqual('timeout', result['errors'][0]['type'])
        self.assertNotIn('secret raw', json.dumps(result))

    def test_failed_runner_has_safe_provider_guidance(self):
        def failure(*args, **kwargs):
            return subprocess.CompletedProcess([], 1, '', 'token=should-not-leak')
        result = classify_sources([SOURCE], WORKS, [], runner=failure)
        self.assertEqual('runner_failed', result['errors'][0]['type'])
        self.assertIn('provider', result['errors'][0]['message'])
        self.assertNotIn('should-not-leak', json.dumps(result))

    def test_malformed_and_missing_output_rejected(self):
        for payload in [dict(events=[]), dict(complete=True, events='oops'), dict(complete=True, events=[], extra=True)]:
            self.assertTrue(self.classify(payload)['errors'])
        def missing(*args, **kwargs):
            return subprocess.CompletedProcess([], 0, '', '')
        result = classify_sources([SOURCE], WORKS, [], runner=missing)
        self.assertEqual('invalid_output', result['errors'][0]['type'])

    def test_limits_reject_before_runner(self):
        def forbidden(*args, **kwargs):
            self.fail('runner should not be called')
        result = classify_sources([dict(SOURCE, content='x' * 64001)], WORKS, [], runner=forbidden)
        self.assertEqual('limit_exceeded', result['errors'][0]['type'])

    def test_source_below_64000_character_total_can_reach_runner(self):
        content = ('会议范围尚未确认。' * 7000)[:60000]
        self.assertEqual(60000, len(content))
        called = []
        def check(argv, kwargs):
            called.append(True)
            self.assertIn(content[:300], kwargs['input'])
        result = classify_sources([dict(SOURCE, content=content)], WORKS, [],
                                  runner=fake(dict(complete=True, events=[]), check))
        self.assertEqual([], result['errors'])
        self.assertEqual([True], called)

    def test_combined_input_above_64000_characters_rejected_before_runner(self):
        def forbidden(*args, **kwargs):
            self.fail('combined input must not reach runner')
        sources = [dict(SOURCE, source_id='message:' + str(index), content='待确认。\n' * 6600)
                   for index in range(2)]
        self.assertTrue(all(len(item['content']) < 64000 for item in sources))
        result = classify_sources(sources, WORKS, [], runner=forbidden)
        self.assertEqual('limit_exceeded', result['errors'][0]['type'])

    def test_unscrubbed_input_rejected_before_runner(self):
        def forbidden(*args, **kwargs):
            self.fail('secret must not reach runner')
        result = classify_sources([dict(SOURCE, content='password=not-safe')], WORKS, [], runner=forbidden)
        self.assertEqual('unsafe_input', result['errors'][0]['type'])
        self.assertNotIn('not-safe', json.dumps(result))

    def test_already_redacted_input_is_allowed(self):
        source = dict(SOURCE, content='password=[REDACTED]，已替换。')
        result = self.classify(dict(complete=True, events=[]), sources=[source])
        self.assertFalse(result['errors'])


if __name__ == '__main__':
    unittest.main()
