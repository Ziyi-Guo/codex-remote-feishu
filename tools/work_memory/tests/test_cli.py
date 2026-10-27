import contextlib
import fcntl
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from profc_memory.cli import CLIError, execute, main
from profc_memory.classify import INSTRUCTIONS
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

    @patch('profc_memory.classify.subprocess.run')
    def test_cli_classifies_38519_character_meeting_once_with_only_runner_mocked(self, runner):
        quote = 'IPGen CLI 会议范围仍待确认。'
        content = quote + '\n' + ('会议记录尚待确认。\n' * 4000)
        content = content[:38518] + '\n'
        self.assertEqual(38519, len(content))
        self.config.update(works=[dict(work_id='ipgen-cli', title='IPGen CLI', aliases=['CLI'])], sources=[])
        self.save()
        meeting = dict(source('document:synthetic_cli_meeting', content), kind='document',
                       object_id='synthetic_cli_meeting', occurred_at=None, redacted=False)
        meeting.pop('modified_at')
        with self.ledger() as ledger:
            ledger.register_work('ipgen-cli', 'IPGen CLI', ['CLI'])
            ledger.ingest_source(meeting, ['ipgen-cli'])
        def run(argv, **kwargs):
            payload = json.loads(kwargs['input'][len(INSTRUCTIONS):])
            self.assertEqual(content, payload['sources'][0]['content'])
            self.assertLessEqual(len(kwargs['input']) - len(INSTRUCTIONS), 64000)
            event = dict(work_id='ipgen-cli', kind='open_question', claim=quote, quote=quote,
                         occurred_at=None, actor='unknown', stage='scope', facet='status',
                         supersedes=[], status='accepted')
            output = dict(complete=True, events=[dict(source_id=meeting['source_id'], revision='1', event=event)])
            Path(argv[argv.index('-o') + 1]).write_text(json.dumps(output), encoding='utf-8')
            return subprocess.CompletedProcess(argv, 0, '', '')
        runner.side_effect = run
        result = execute(self.root, 'classify')
        self.assertEqual([], result['errors'])
        self.assertEqual(1, result['classified'])
        self.assertEqual(0, execute(self.root, 'classify')['classified'])
        self.assertEqual(1, runner.call_count)
        with self.ledger() as ledger:
            self.assertEqual([], ledger.pending_sources())
            self.assertEqual(quote, ledger.read_work('ipgen-cli', 'owner')['current_events'][0]['claim'])

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

    @patch('profc_memory.cli.retrieve')
    def test_recall_delegates_without_opening_ledger_or_collecting(self, read):
        read.return_value = dict(status='none',chars=0,content='')
        result = execute(self.root,'recall',requester_id='owner',query='天气怎么样',session_id='cold')
        self.assertEqual(0,result['chars'])
        self.assertFalse((self.root/'ledger.sqlite3').exists())
        self.assertEqual('cold',read.call_args.kwargs['session_id'])

    @patch('profc_memory.cli.synthesize')
    @patch('profc_memory.cli.apply_dossier')
    def test_dossier_build_records_input_revision_and_skips_unchanged(self, apply, synth):
        synth.return_value = dict(draft={'sections':{}},errors=[])
        apply.return_value = dict(status='updated',version='1')
        result = execute(self.root,'dossier',work_id='byok')
        self.assertEqual(1,result['dossiers_updated'])
        self.assertEqual(0,execute(self.root,'dossier',work_id='byok')['dossiers_updated'])
        self.assertEqual(1,synth.call_count)

    def enable_two_dossiers(self):
        self.config['works'].append(dict(work_id='cli', title='IPGen CLI', aliases=['CLI'], anchors=['IPGen CLI']))
        self.config.update(sources=[], max_classification_batches=0,
                           dossiers=dict(enabled=True, work_ids=['byok', 'cli']))
        self.save()

    @patch('profc_memory.cli.synthesize')
    @patch('profc_memory.cli.apply_dossier')
    def test_run_skips_unchanged_first_dossier_and_builds_second(self, apply, synth):
        self.enable_two_dossiers()
        synth.return_value = dict(draft={'sections': {}}, errors=[])
        apply.return_value = dict(status='updated', version='1')
        self.assertEqual(1, execute(self.root, 'run', until=NOW)['dossiers_updated'])
        self.assertEqual(1, execute(self.root, 'run', until=NOW)['dossiers_updated'])
        self.assertEqual(['byok', 'cli'], [call.args[1]['work_id'] for call in apply.call_args_list])
        self.assertEqual(0, execute(self.root, 'run', until=NOW)['dossiers_updated'])

    @patch('profc_memory.cli.synthesize')
    @patch('profc_memory.cli.apply_dossier')
    def test_failed_first_dossier_backs_off_and_second_runs_next_round(self, apply, synth):
        self.enable_two_dossiers()
        synth.side_effect = [dict(draft=None, errors=[dict(type='timeout')]),
                             dict(draft={'sections': {}}, errors=[])]
        apply.return_value = dict(status='updated', version='1')
        first = execute(self.root, 'run', until=NOW)
        second = execute(self.root, 'run', until=NOW)
        self.assertEqual('timeout', first['errors'][0]['type'])
        self.assertEqual(1, second['dossiers_updated'])
        self.assertEqual('cli', apply.call_args.args[1]['work_id'])
        self.assertEqual(2, synth.call_count)

    @patch('profc_memory.cli.synthesize')
    @patch('profc_memory.cli.apply_dossier')
    def test_frequently_changed_first_dossier_does_not_starve_second(self, apply, synth):
        self.enable_two_dossiers()
        synth.return_value = dict(draft={'sections': {}}, errors=[])
        apply.return_value = dict(status='updated', version='1')
        execute(self.root, 'run', until=NOW)
        with self.ledger() as ledger:
            ledger.ingest_source(source(), ['byok'])
        execute(self.root, 'run', until=NOW)
        self.assertEqual(['byok', 'cli'], [call.args[1]['work_id'] for call in apply.call_args_list])

    @patch('profc_memory.cli.synthesize')
    @patch('profc_memory.cli.apply_dossier')
    def test_dossier_exception_is_private_and_does_not_block_other_project(self, apply, synth):
        self.enable_two_dossiers()
        synth.side_effect = [OSError('private upstream body'), dict(draft={'sections': {}}, errors=[])]
        apply.return_value = dict(status='updated', version='1')
        first = execute(self.root, 'run', until=NOW)
        self.assertEqual('dossier_failed', first['errors'][0]['type'])
        self.assertNotIn('private upstream body', json.dumps(first))
        self.assertEqual(1, execute(self.root, 'run', until=NOW)['dossiers_updated'])
        self.assertEqual('cli', apply.call_args.args[1]['work_id'])

    @patch('profc_memory.cli.synthesize')
    @patch('profc_memory.cli.apply_dossier')
    @patch('profc_memory.cli.read_dossier')
    def test_unreadable_first_dossier_does_not_block_second(self, read, apply, synth):
        self.enable_two_dossiers()
        (self.root / 'dossiers' / 'byok').mkdir(parents=True, mode=0o700)
        (self.root / 'dossiers').chmod(0o700)
        read.side_effect = ValueError('invalid existing dossier')
        synth.return_value = dict(draft={'sections': {}}, errors=[])
        apply.return_value = dict(status='updated', version='1')
        result = execute(self.root, 'run', until=NOW)
        self.assertEqual(1, result['dossiers_updated'])
        self.assertEqual('cli', apply.call_args.args[1]['work_id'])
        self.assertEqual('invalid_dossier', result['errors'][0]['type'])

    @patch('profc_memory.cli.collect_source')
    def test_discovery_collects_anchored_document_and_saves_original_provenance(self, collect):
        url = 'https://bytedance.larkoffice.com/docx/Doc123?from=chat#section-1'
        parent = source(content='BYOK 设计文档：' + url)
        document = dict(source('document:Doc123', '设计正文，另有 https://team.feishu.cn/docx/DoNotRecurse'),
                        kind='document', object_id='Doc123', url=url)
        self.config['discovery'] = dict(enabled=True, max_documents_per_run=2)
        self.save()
        collect.side_effect = [dict(records=[parent], complete=True, cursor=NOW),
                               dict(records=[document], complete=True, cursor=NOW)]
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual(2, result['collected'])
        self.assertEqual(1, result['discovered_documents'])
        self.assertEqual(2, collect.call_count)
        self.assertEqual(url, collect.call_args.args[0]['doc'])
        self.assertEqual('owner', collect.call_args.args[0]['owner_id'])
        with self.ledger() as ledger:
            self.assertEqual(['byok'], ledger.get_source('document:Doc123')['work_ids'])
            queue = ledger.get_cursor('discovery:documents')
            entry = next(iter(queue['documents'].values()))
            self.assertEqual(url, entry['url'])
            provenance = entry['provenance'][0]
            self.assertEqual(parent['source_id'], provenance['source_id'])
            self.assertEqual(parent['revision'], provenance['revision'])
            self.assertEqual(parent['url'], provenance['url'])
            self.assertEqual('chat', provenance['configured_source_id'])

    @patch('profc_memory.cli.collect_source')
    def test_discovery_keeps_unmatched_chat_link_pending_without_fetch(self, collect):
        self.config['discovery'] = dict(enabled=True)
        self.save()
        collect.return_value = dict(records=[source(content='普通讨论 https://team.feishu.cn/wiki/Wiki123')],
                                    complete=True, cursor=NOW)
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual(1, collect.call_count)
        self.assertEqual(1, result['pending_documents'])
        with self.ledger() as ledger:
            entry = next(iter(ledger.get_cursor('discovery:documents')['documents'].values()))
            self.assertEqual([], entry['work_ids'])

    @patch('profc_memory.cli.collect_source')
    def test_discovery_uses_single_document_binding_but_quarantines_ambiguous_binding(self, collect):
        self.config['works'].append(dict(work_id='cli', title='IPGen CLI', anchors=['IPGen CLI']))
        self.config['sources'] = [dict(id='design', kind='document', doc='https://team.feishu.cn/docx/Parent',
                                       work_ids=['byok', 'cli'])]
        self.config['discovery'] = dict(enabled=True)
        self.save()
        parent = dict(source('document:Parent', '参见 https://team.feishu.cn/docx/Child'),
                      kind='document', object_id='Parent', url=self.config['sources'][0]['doc'])
        collect.return_value = dict(records=[parent], complete=True, cursor=NOW)
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual(1, result['pending_documents'])
        self.assertEqual(1, collect.call_count)
        self.config['sources'][0]['work_ids'] = ['byok']
        self.save()
        collect.reset_mock()
        collect.side_effect = [dict(records=[parent], complete=True, cursor=NOW),
                               dict(records=[], complete=True, cursor=NOW)]
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual(0, result['pending_documents'])
        self.assertEqual(2, collect.call_count)

    @patch('profc_memory.cli.collect_source')
    def test_discovery_failure_backs_off_and_does_not_starve_other_document(self, collect):
        self.config['discovery'] = dict(enabled=True, max_documents_per_run=1)
        self.save()
        parent = source(content='BYOK https://team.feishu.cn/docx/First https://team.feishu.cn/docx/Second')
        collect.side_effect = [dict(records=[parent], complete=True, cursor=NOW),
                               dict(records=[], complete=False, error=dict(type='cli_failed')),
                               dict(records=[parent], complete=True, cursor=NOW),
                               dict(records=[], complete=True, cursor=NOW)]
        first = execute(self.root, 'collect', until=NOW)
        self.assertEqual('cli_failed', first['errors'][0]['type'])
        self.assertEqual('https://team.feishu.cn/docx/First', collect.call_args.args[0]['doc'])
        execute(self.root, 'collect', until=NOW)
        self.assertEqual('https://team.feishu.cn/docx/Second', collect.call_args.args[0]['doc'])
        self.assertEqual(4, collect.call_count)

    @patch('profc_memory.cli.collect_source')
    def test_discovery_disabled_leaves_no_queue_or_extra_read(self, collect):
        collect.return_value = dict(records=[source(content='BYOK https://team.feishu.cn/docx/Doc123')],
                                    complete=True, cursor=NOW)
        execute(self.root, 'collect', until=NOW)
        self.assertEqual(1, collect.call_count)
        with self.ledger() as ledger:
            self.assertIsNone(ledger.get_cursor('discovery:documents'))

    def test_discovery_rejects_invalid_bounds_before_reading_sources(self):
        for settings in [dict(enabled='yes'), dict(enabled=True, max_documents_per_run=0),
                         dict(enabled=True, max_documents_per_run=6)]:
            self.config['discovery'] = settings
            self.save()
            with self.assertRaises(CLIError):
                execute(self.root, 'collect', until=NOW)

    @patch('profc_memory.cli.collect_source')
    def test_discovery_rejects_unrelated_document_returned_for_safe_url(self, collect):
        self.config['discovery'] = dict(enabled=True)
        self.save()
        returned = dict(source('document:Wrong', '无关正文'), kind='document', object_id='Wrong',
                        url='https://team.feishu.cn/docx/Wrong')
        collect.side_effect = [dict(records=[source(content='BYOK https://team.feishu.cn/docx/Requested')],
                                   complete=True, cursor=NOW),
                               dict(records=[returned], complete=True, cursor=NOW)]
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual(1, result['collected'])
        self.assertEqual('discovery_collection_failed', result['errors'][0]['type'])
        with self.ledger() as ledger:
            with self.assertRaises(ValueError):
                ledger.get_source('document:Wrong')

    @patch('profc_memory.cli.collect_source')
    def test_removing_origin_source_stops_discovered_document_reads(self, collect):
        self.config['discovery'] = dict(enabled=True)
        self.save()
        collect.side_effect = [dict(records=[source(content='BYOK https://team.feishu.cn/docx/Doc123')],
                                   complete=True, cursor=NOW),
                               dict(records=[], complete=True, cursor=NOW)]
        execute(self.root, 'collect', until=NOW)
        self.config['sources'] = []
        self.save()
        collect.reset_mock()
        execute(self.root, 'collect', until=NOW)
        collect.assert_not_called()

    @patch('profc_memory.cli.collect_source')
    def test_discovered_alias_keeps_existing_immutable_document_revision(self, collect):
        url = 'https://team.feishu.cn/docx/Doc123'
        alias = url + '?from=chat#part'
        self.config['sources'].append(dict(id='design', kind='document', doc=url, work_ids=['byok']))
        self.config['discovery'] = dict(enabled=True)
        self.save()
        document = dict(source('document:Doc123', '已有设计正文'), kind='document', object_id='Doc123', url=url)
        collect.side_effect = [dict(records=[source(content='BYOK ' + alias)], complete=True, cursor=NOW),
                               dict(records=[document], complete=True, cursor=NOW),
                               dict(records=[dict(document, url=alias)], complete=True, cursor=NOW)]
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual([], result['errors'])
        self.assertEqual(2, result['collected'])
        with self.ledger() as ledger:
            self.assertEqual(url, ledger.get_source('document:Doc123')['url'])
            entry = next(iter(ledger.get_cursor('discovery:documents')['documents'].values()))
            self.assertEqual(alias, entry['url'])

    @patch('profc_memory.cli.collect_source')
    def test_tampered_discovery_queue_cannot_request_external_domain(self, collect):
        self.config['discovery'] = dict(enabled=True)
        self.config['sources'] = []
        self.save()
        with self.ledger() as ledger:
            ledger.set_cursor('discovery:documents', dict(schema_version=1, documents={
                'fake': dict(id='fake', url='https://evil.test/docx/Doc123', work_ids=['byok'], provenance=[])}))
        result = execute(self.root, 'collect', until=NOW)
        self.assertEqual('invalid_discovery_state', result['errors'][0]['type'])
        collect.assert_not_called()

    def test_run_deadline_interrupts_native_open_in_real_subprocess(self):
        fifo = self.root / 'blocked-open'
        os.mkfifo(fifo)
        script = '''
import os, sys
from profc_memory import cli
cli.RUN_TIMEOUT_SECONDS = 0.15
def blocked(**kwargs):
    try:
        os.open(sys.argv[2], os.O_RDONLY)
    except Exception:
        return dict(errors=[dict(type='swallowed_timeout')])
cli.execute = blocked
raise SystemExit(cli.main(['--state-dir', sys.argv[1], 'run']))
'''
        try:
            completed = subprocess.run([sys.executable, '-c', script, str(self.root), str(fifo)],
                                       capture_output=True, text=True, timeout=3)
        except subprocess.TimeoutExpired:
            self.fail('run deadline did not interrupt a native blocking open')
        self.assertEqual(1, completed.returncode)
        self.assertEqual({'errors': [{'type': 'run_timeout'}]}, json.loads(completed.stdout))

    @patch('profc_memory.cli.execute')
    def test_run_deadline_restores_existing_alarm_and_keeps_business_error(self, execute_call):
        original_handler = signal.getsignal(signal.SIGALRM)
        previous = signal.setitimer(signal.ITIMER_REAL, 30)
        execute_call.side_effect = CLIError('existing_business_error')
        output = io.StringIO()
        try:
            with contextlib.redirect_stdout(output):
                self.assertEqual(1, main(['--state-dir', str(self.root), 'run']))
            remaining, _ = signal.getitimer(signal.ITIMER_REAL)
            self.assertEqual(original_handler, signal.getsignal(signal.SIGALRM))
            self.assertTrue(29 < remaining <= 30)
            self.assertEqual('existing_business_error', json.loads(output.getvalue())['errors'][0]['type'])
        finally:
            signal.setitimer(signal.ITIMER_REAL, *previous)

    def test_recall_does_not_use_run_deadline(self):
        script = '''
import time, sys
from profc_memory import cli
cli.RUN_TIMEOUT_SECONDS = 0.03
def slow(**kwargs):
    time.sleep(0.10)
    return dict(errors=[])
cli.execute = slow
raise SystemExit(cli.main(['--state-dir', sys.argv[1], 'recall', '--requester-id', 'owner', '--query', 'BYOK']))
'''
        completed = subprocess.run([sys.executable, '-c', script, str(self.root)],
                                   capture_output=True, text=True, timeout=3)
        self.assertEqual(0, completed.returncode, completed.stderr)
        self.assertEqual({'errors': []}, json.loads(completed.stdout))

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
