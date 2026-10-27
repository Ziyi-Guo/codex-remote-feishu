import contextlib
import fcntl
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from profc_memory.cli import CLIError, execute, main
from profc_memory.ledger import Ledger

NOW = '2026-10-27T02:00:00+00:00'


def source(sid='message:om_1', content='BYOK：部署完成，尚未验收。'):
    return dict(source_id=sid, revision='1', kind='message', object_id=sid,
                occurred_at=NOW, observed_at=NOW, modified_at=None,
                content=content, url='https://example.test/source', owner_id='owner', visibility='owner')


class CLITests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.root.chmod(0o700)
        self.config = dict(schema_version=1, owner_id='owner',
                           works=[dict(work_id='byok', title='自带密钥', aliases=['BYOK'], anchors=['BYOK'])],
                           sources=[dict(id='chat', kind='chat', chat_id='oc_1', start=NOW, work_ids=['byok'])],
                           max_classification_batches=1)
        self.save()

    def tearDown(self):
        self.tmp.cleanup()

    def save(self):
        (self.root / 'config.json').write_text(json.dumps(self.config))
        (self.root / 'config.json').chmod(0o600)

    def ledger(self):
        return Ledger(self.root / 'ledger.sqlite3', 'owner')

    @patch('profc_memory.cli.collect_source')
    def test_chat_requires_anchor_not_group_membership(self, collect):
        collect.return_value = dict(records=[source(), source('message:om_2', '同群普通讨论。')], complete=True, cursor=NOW)
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual(2, result['collected'])
        with self.ledger() as ledger:
            self.assertEqual(['byok'], ledger.get_source('message:om_1', '1')['work_ids'])
            self.assertEqual([], ledger.get_source('message:om_2', '1')['work_ids'])
            self.assertEqual(NOW, ledger.get_cursor('source:chat'))

    @patch('profc_memory.cli.collect_source')
    def test_partial_source_does_not_advance_but_other_source_continues(self, collect):
        self.config['sources'].append(dict(id='doc', kind='document', doc='https://example.test/doc', work_ids=['byok']))
        self.save()
        collect.side_effect = [dict(records=[source()], complete=False, cursor=None, error=dict(type='limit_reached')),
                               dict(records=[], complete=True, cursor=NOW)]
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual(1, result['collected'])
        self.assertEqual('limit_reached', result['errors'][0]['type'])
        with self.ledger() as ledger:
            self.assertIsNone(ledger.get_cursor('source:chat'))
            self.assertEqual(NOW, ledger.get_cursor('source:doc'))

    @patch('profc_memory.cli.collect_source')
    def test_source_exception_isolated_from_following_source(self, collect):
        self.config['sources'].append(dict(id='doc', kind='document', doc='https://example.test/doc', work_ids=['byok']))
        self.save()
        collect.side_effect = [ValueError('private upstream data'), dict(records=[], complete=True, cursor=NOW)]
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual('collection_failed', result['errors'][0]['type'])
        self.assertNotIn('private upstream data', json.dumps(result))
        with self.ledger() as ledger:
            self.assertEqual(NOW, ledger.get_cursor('source:doc'))

    @patch('profc_memory.cli.classify_sources')
    @patch('profc_memory.cli.collect_source')
    def test_rejected_event_never_marks_source_processed(self, collect, classify):
        collect.return_value = dict(records=[source()], complete=True, cursor=NOW)
        execute(self.root, 'collect', until=NOW)
        classify.return_value = dict(events=[dict(source_id='message:om_1', revision='1', event={})], errors=[])
        result = execute(self.root, 'classify')
        self.assertEqual('invalid_evidence', result['errors'][0]['type'])
        with self.ledger() as ledger:
            self.assertEqual(1, len(ledger.pending_sources()))

    @patch('profc_memory.cli.classify_sources')
    @patch('profc_memory.cli.collect_source')
    def test_run_classifies_renders_private_and_retains_evidence(self, collect, classify):
        collect.return_value = dict(records=[source()], complete=True, cursor=NOW)
        ev = dict(work_id='byok', kind='progress', claim=source()['content'], quote=source()['content'],
                  occurred_at=NOW, actor='unknown', stage='deployment', facet='status', supersedes=[], status='accepted')
        classify.return_value = dict(events=[dict(source_id=source()['source_id'], revision='1', event=ev)], errors=[])
        result = execute(self.root, 'run', until=NOW)
        self.assertEqual(1, result['classified'])
        context = json.loads((self.root / 'works/byok/context.json').read_text())
        self.assertIn('尚未验收', context['context'])
        self.assertLessEqual(len(context['context']), 3200)
        for path in [self.root/'works/byok/context.json', self.root/'works/byok/timeline.md']:
            self.assertEqual(0o600, path.stat().st_mode & 0o777)
        self.assertEqual(0o700, (self.root/'works/byok').stat().st_mode & 0o777)
        self.assertNotIn(source()['content'], json.dumps(result, ensure_ascii=False))
        self.assertFalse((self.root/'manifest.json').exists())

    @patch('profc_memory.cli.classify_sources')
    @patch('profc_memory.cli.collect_source')
    def test_classification_failure_stays_pending_and_backs_off(self, collect, classify):
        collect.return_value = dict(records=[source()], complete=True, cursor=NOW)
        execute(self.root, 'collect', until=NOW)
        classify.return_value = dict(events=[], errors=[dict(type='timeout', message='not complete')])
        first = execute(self.root, 'classify')
        second = execute(self.root, 'classify')
        self.assertEqual(1, classify.call_count)
        self.assertEqual('timeout', first['errors'][0]['type'])
        self.assertEqual(1, second['deferred'])
        with self.ledger() as ledger:
            self.assertEqual(1, len(ledger.pending_sources()))

    @patch('profc_memory.cli.classify_sources')
    @patch('profc_memory.cli.collect_source')
    def test_unmatched_raw_sources_do_not_go_to_model_or_context(self, collect, classify):
        collect.return_value = dict(records=[source(content='同群讨论。')], complete=True, cursor=NOW)
        execute(self.root, 'run', until=NOW)
        classify.assert_not_called()
        with self.ledger() as ledger:
            self.assertEqual([], ledger.pending_sources())
            self.assertEqual([], ledger.get_source('message:om_1')['work_ids'])
        self.assertNotIn('同群讨论', (self.root/'works/byok/context.json').read_text())

    def test_context_requires_owner_and_owner_audience(self):
        execute(self.root, 'render')
        for requester, audience in [('other', 'owner'), ('owner', 'shared'), (None, 'owner')]:
            with self.assertRaises(CLIError):
                execute(self.root, 'context', work_id='byok', requester_id=requester, audience=audience)
        result = execute(self.root, 'context', work_id='byok', requester_id='owner', audience='owner')
        self.assertEqual('owner', result['visibility'])

    @patch('profc_memory.cli.publish_work')
    def test_publish_is_explicit_and_preserves_distinct_app_identities(self, publish):
        with self.assertRaises(CLIError):
            execute(self.root, 'publish')
        publish.assert_not_called()
        self.config['publish'] = dict(enabled=True, owner_open_id='cli-owner')
        self.save()
        publish.return_value = dict(status='published', url='https://example.test/private', published_ledger_version='1')
        result = execute(self.root, 'publish')
        self.assertEqual(1, result['published'])
        self.assertEqual('owner', publish.call_args.args[1]['owner_id'])
        self.assertEqual('cli-owner', publish.call_args.args[2])

    def test_missing_config_unsafe_permissions_and_traversal_rejected(self):
        (self.root/'config.json').chmod(0o644)
        with self.assertRaises(CLIError):
            execute(self.root, 'status')
        self.save()
        self.config['works'][0]['work_id'] = '../escape'
        self.save()
        with self.assertRaises(CLIError):
            execute(self.root, 'render')
        (self.root/'config.json').unlink()
        with self.assertRaises(CLIError):
            execute(self.root, 'status')

    def test_projection_symlink_not_followed(self):
        outside = self.root / 'outside'
        outside.write_text('untouched')
        folder = self.root/'works/byok'
        folder.mkdir(parents=True)
        (folder/'context.json').symlink_to(outside)
        with self.assertRaises(CLIError):
            execute(self.root, 'render')
        self.assertEqual('untouched', outside.read_text())

    def test_run_lock_rejects_overlap(self):
        with (self.root/'run.lock').open('w') as handle:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(CLIError) as ctx:
                execute(self.root, 'run')
            self.assertEqual('busy', ctx.exception.code)

    def test_cli_failure_output_does_not_echo_private_config(self):
        self.config['works'][0]['work_id'] = '../private-secret'
        self.save()
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            code = main(['--state-dir', str(self.root), 'status'])
        self.assertEqual(1, code)
        self.assertNotIn('private-secret', output.getvalue())


if __name__ == '__main__':
    unittest.main()
