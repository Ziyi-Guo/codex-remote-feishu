import json
import subprocess
import unittest
from unittest.mock import patch

from profc_memory.collect import (CollectError, bounded_anchor_matches, collect_source,
                                  normalize_message, sanitize_content)

START = '2026-10-26T00:00:00+00:00'
END = '2026-10-27T00:00:00+00:00'
CONFIG = dict(id='chat-one', kind='chat', chat_id='oc_one', owner_id='ou_owner', start=START)


def message(mid='om_one', **kwargs):
    return dict(dict(message_id=mid, content='自带密钥事项已经部署', create_time='1792972800000',
                     message_app_link='https://applink.feishu.cn/client/message/open?messageId='+mid,
                     deleted=False), **kwargs)


def envelope(messages, more=False, token=None):
    return dict(ok=True, identity='user', data=dict(messages=messages, has_more=more, page_token=token))


class CollectorTests(unittest.TestCase):
    @patch('profc_memory.collect.subprocess.run')
    def test_search_rounds_fractional_clock_to_supported_watermark(self, run):
        run.return_value = subprocess.CompletedProcess([], 0, json.dumps(envelope([])), '')
        result = collect_source(dict(CONFIG, kind='message_search', query='BYOK'),
                                START, '2026-10-27T00:00:00.123456+00:00')
        self.assertTrue(result['complete'])
        self.assertEqual(END, result['cursor'])
        argv = run.call_args.args[0]
        self.assertEqual(END, argv[argv.index('--end') + 1])

    def test_secret_scrub_preserves_reference_names(self):
        secret = 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTYifQ.abcdefghijklmnop'
        raw = ('token="sensitive-value" password: "a long secret" Authorization: Bearer abc123\n'
               'secretName=prod/api credential_id=cred-12 AKIA1234567890123456 '+secret)
        clean, changed = sanitize_content(raw)
        self.assertTrue(changed)
        for forbidden in ['sensitive-value', 'a long secret', 'abc123', 'AKIA1234567890123456', secret]:
            self.assertNotIn(forbidden, clean)
        self.assertIn('secretName=prod/api', clean)
        self.assertIn('credential_id=cred-12', clean)
        self.assertEqual(('部署完成', False), sanitize_content('部署完成'))
        self.assertEqual((clean, False), sanitize_content(clean))

    def test_secret_scrub_handles_chinese_and_escaped_json(self):
        raw = '密码：sensitive-value 密钥名称=prod/api ' + json.dumps({'text': json.dumps({'token': 'nested-secret'})})
        clean, changed = sanitize_content(raw)
        self.assertTrue(changed)
        self.assertNotIn('sensitive-value', clean)
        self.assertNotIn('nested-secret', clean)
        self.assertIn('密钥名称=prod/api', clean)

    def test_anchor_matching_is_explicit_and_bounded(self):
        anchors = ['IPGen 自带密钥', 'task_123', 'https://x.test/doc/1']
        self.assertEqual(anchors, bounded_anchor_matches('IPGen 自带密钥：task_123，https://x.test/doc/1。', anchors))
        self.assertEqual([], bounded_anchor_matches('同群聊到了密钥和IPGen task_1234 https://x.test/doc/12', anchors))
        self.assertEqual([], bounded_anchor_matches('我们正在讨论密钥', ['IPGen 自带密钥']))

    def test_normalize_keeps_link_and_revision(self):
        item = message(update_time='1792972810000')
        record = normalize_message(item, CONFIG, END)
        self.assertEqual(item['message_app_link'], record['url'])
        self.assertEqual('message:om_one', record['source_id'])
        self.assertEqual('1792972810000', record['revision'])
        self.assertEqual('owner', record['visibility'])
        self.assertEqual('ou_owner', record['owner_id'])
        self.assertNotEqual(normalize_message(message(content='before'), CONFIG, END)['revision'],
                            normalize_message(message(content='after'), CONFIG, END)['revision'])

    def test_missing_url_is_typed_error(self):
        item = message()
        del item['message_app_link']
        with self.assertRaises(CollectError) as ctx:
            normalize_message(item, CONFIG, END)
        self.assertEqual('missing_field', ctx.exception.code)

    def test_credentials_in_url_are_not_persisted(self):
        url = 'https://alice:synthetic-private@example.test/a'
        clean, changed = sanitize_content('请看 ' + url)
        self.assertTrue(changed)
        self.assertNotIn('synthetic-private', clean)
        with self.assertRaises(CollectError) as ctx:
            normalize_message(message(message_app_link=url), CONFIG, END)
        self.assertEqual('unsafe_url', ctx.exception.code)

    def test_modified_time_is_real_update_time_or_unknown(self):
        edited = normalize_message(message(update_time='1792972810000'), CONFIG, END)
        self.assertEqual('2026-10-26T00:00:10+00:00', edited['modified_at'])
        self.assertIsNone(normalize_message(message(), CONFIG, END)['modified_at'])

    @patch('profc_memory.collect.subprocess.run')
    def test_cli_minute_time_requires_explicit_zone_and_sets_process_tz(self, run):
        raw = message(create_time='2026-10-26 20:56', update_time='2026-10-26 20:57')
        run.return_value = subprocess.CompletedProcess([], 0, json.dumps(envelope([raw])), '')
        result = collect_source(dict(CONFIG, timezone='Asia/Shanghai'), START, END)
        self.assertTrue(result['complete'])
        row = result['records'][0]
        self.assertEqual('2026-10-26T20:56:00+08:00', row['occurred_at'])
        self.assertEqual('2026-10-26T20:57:00+08:00', row['modified_at'])
        self.assertEqual('minute', row['occurred_at_precision'])
        self.assertEqual('minute', row['modified_at_precision'])
        self.assertEqual('Asia/Shanghai', run.call_args.kwargs['env']['TZ'])
        unconfigured = collect_source(CONFIG, START, END)
        self.assertFalse(unconfigured['complete'])
        self.assertEqual('invalid_time', unconfigured['error']['type'])
        self.assertEqual(START, unconfigured['cursor'])

    def test_same_minute_edits_are_distinct_unordered_revisions(self):
        cfg = dict(CONFIG, timezone='Asia/Shanghai')
        first = normalize_message(message(create_time='2026-10-26 20:56', update_time='2026-10-26 20:57', content='原决定。'), cfg, END)
        second = normalize_message(message(create_time='2026-10-26 20:56', update_time='2026-10-26 20:57', content='纠正后的决定。'), cfg, END)
        self.assertNotEqual(first['revision'], second['revision'])
        self.assertEqual(first['modified_at'], second['modified_at'])
        self.assertEqual('minute', second['modified_at_precision'])

    def test_explicit_offset_is_not_reinterpreted_by_configured_zone(self):
        cfg = dict(CONFIG, timezone='Asia/Shanghai')
        row = normalize_message(message(create_time='2026-10-26T01:02:03-04:00'), cfg, END)
        self.assertEqual('2026-10-26T01:02:03-04:00', row['occurred_at'])

    def test_sender_identity_is_source_metadata_not_body_guess(self):
        row = normalize_message(message(sender=dict(name='张三', id='ou_sender'), content='李四说已完成。'), CONFIG, END)
        self.assertEqual('张三', row['actor'])
        self.assertEqual('ou_sender', row['actor_user_id'])
        unknown = normalize_message(message(content='李四说已完成。'), CONFIG, END)
        self.assertEqual('unknown', unknown['actor'])
        self.assertIsNone(unknown['actor_user_id'])

    def test_merge_forward_preserves_forwarder_without_attributing_original_speech(self):
        row = normalize_message(message(msg_type='merge_forward', sender=dict(name='转发者', id='ou_forwarder')),
                                CONFIG, END)
        self.assertEqual('unknown', row['actor'])
        self.assertIsNone(row['actor_user_id'])
        self.assertEqual(dict(name='转发者', user_id='ou_forwarder'), row['forwarded_by'])

    @patch('profc_memory.collect._run_cli')
    def test_invalid_timezone_rejected_before_cli(self, run):
        result = collect_source(dict(CONFIG, timezone='Invalid/Zone'), START, END)
        self.assertEqual('invalid_config', result['error']['type'])
        run.assert_not_called()

    @patch('profc_memory.collect._run_cli')
    def test_paginate_dedupe_keep_edited_revision(self, run):
        old = message(update_time='1792972800000')
        edited = message(content='已部署，尚未验收', update_time='1792972810000')
        run.side_effect = [envelope([old], True, 'opaque+token='), envelope([old, edited])]
        result = collect_source(CONFIG, START, END)
        self.assertTrue(result['complete'])
        self.assertEqual(END, result['cursor'])
        self.assertEqual(2, len(result['records']))
        argv = run.call_args_list[1].args[0]
        self.assertEqual('opaque+token=', argv[argv.index('--page-token')+1])
        first = run.call_args_list[0].args[0]
        self.assertEqual('2026-10-25T23:58:00+00:00', first[first.index('--start')+1])
        self.assertEqual('user', first[first.index('--as')+1])

    @patch('profc_memory.collect._run_cli')
    def test_limits_do_not_advance_cursor(self, run):
        for extra in [dict(max_pages=1), dict(max_messages=1)]:
            run.return_value = envelope([message(), message('om_two')], True, 'next')
            result = collect_source(dict(CONFIG, **extra), START, END)
            self.assertFalse(result['complete'])
            self.assertEqual(START, result['cursor'])
            self.assertEqual('limit_reached', result['error']['type'])

    @patch('profc_memory.collect._run_cli')
    def test_partial_failure_keeps_records_and_cursor(self, run):
        run.side_effect = [envelope([message()], True, 'next'), CollectError('cli_failed', 'CLI failed')]
        result = collect_source(CONFIG, START, END)
        self.assertFalse(result['complete'])
        self.assertEqual(START, result['cursor'])
        self.assertEqual(1, len(result['records']))

    @patch('profc_memory.collect._run_cli')
    def test_empty_complete_advances_cursor(self, run):
        run.return_value = envelope([])
        self.assertEqual(dict(records=[], cursor=END, complete=True, coverage_start='2026-10-25T23:58:00+00:00'),
                         collect_source(CONFIG, START, END))

    @patch('profc_memory.collect._run_cli')
    def test_reconciliation_rediscovers_old_message_edits_without_duplicate_revisions(self, run):
        cfg = dict(CONFIG, start='2026-09-01T00:00:00+00:00', reconcile_days=30, timezone='Asia/Shanghai')
        old = message(create_time='2026-10-05 09:00', update_time='2026-10-26 20:57', content='原决定。')
        edited = dict(old, content='同分钟纠正后的决定。')
        fresh = message('om_new', create_time='2026-10-26 22:00', content='新增消息。')
        run.return_value = envelope([old, old, edited, fresh])
        result = collect_source(cfg, START, END)
        self.assertTrue(result['complete'])
        self.assertEqual(END, result['cursor'])
        self.assertEqual('2026-09-27T00:00:00+00:00', result['coverage_start'])
        argv = run.call_args.args[0]
        self.assertEqual(result['coverage_start'], argv[argv.index('--start') + 1])
        self.assertEqual(3, len(result['records']))
        self.assertNotEqual(result['records'][0]['revision'], result['records'][1]['revision'])
        self.assertEqual(result['records'][0]['modified_at'], result['records'][1]['modified_at'])

    @patch('profc_memory.collect._run_cli')
    def test_reconciliation_respects_configured_start_and_retains_cursor_on_limit(self, run):
        cfg = dict(CONFIG, kind='message_search', query='BYOK', reconcile_days=30, max_pages=1)
        run.return_value = envelope([message()], True, 'next')
        result = collect_source(cfg, START, END)
        self.assertEqual(START, result['coverage_start'])
        self.assertFalse(result['complete'])
        self.assertEqual(START, result['cursor'])
        self.assertEqual('limit_reached', result['error']['type'])

    @patch('profc_memory.collect._run_cli')
    def test_reconciliation_bound_is_validated_before_read(self, run):
        result = collect_source(dict(CONFIG, reconcile_days=0), START, END)
        self.assertEqual('invalid_config', result['error']['type'])
        run.assert_not_called()

    @patch('profc_memory.collect._run_cli')
    def test_document_real_revision_unknown_occurrence(self, run):
        run.return_value = dict(ok=True, identity='user', data=dict(document=dict(
            document_id='doc_real', revision_id=12, content='<p>password=private</p>')))
        cfg = dict(CONFIG, kind='document', doc='https://tenant.feishu.cn/docx/doc_real')
        result = collect_source(cfg, START, END)
        self.assertTrue(result['complete'])
        row = result['records'][0]
        self.assertEqual('document:doc_real', row['source_id'])
        self.assertEqual('12', row['revision'])
        self.assertIsNone(row['occurred_at'])
        self.assertTrue(row['redacted'])
        self.assertEqual(cfg['doc'], row['url'])
        self.assertNotIn('private', json.dumps(result))

    @patch('profc_memory.collect._run_cli')
    def test_search_is_scoped_and_manual_paginated(self, run):
        run.return_value = envelope([])
        result = collect_source(dict(CONFIG, kind='message_search', query='BYOK', chat_type='group'), START, END)
        self.assertTrue(result['complete'])
        argv = run.call_args.args[0]
        self.assertIn('+messages-search', argv)
        self.assertIn('--query', argv)
        self.assertNotIn('--page-all', argv)

    @patch('profc_memory.collect._run_cli')
    def test_malformed_pagination_or_identity_not_complete(self, run):
        for payload in [envelope([], True), dict(ok=True, identity='bot', data=dict(messages=[], has_more=False)),
                        dict(ok=True, identity='user', data=dict(messages=[]))]:
            run.return_value = payload
            result = collect_source(CONFIG, START, END)
            self.assertFalse(result['complete'])
            self.assertEqual(START, result['cursor'])

    @patch('profc_memory.collect.subprocess.run')
    def test_cli_failure_does_not_expose_stderr(self, run):
        from profc_memory.collect import _run_cli
        run.return_value = subprocess.CompletedProcess([], 1, '', 'password=do-not-emit')
        with self.assertRaises(CollectError) as ctx:
            _run_cli(['lark-cli', 'docs', '+fetch'], 3)
        self.assertNotIn('do-not-emit', str(ctx.exception))
        self.assertEqual(3, run.call_args.kwargs['timeout'])
        self.assertFalse(run.call_args.kwargs.get('shell', False))


if __name__ == '__main__':
    unittest.main()
