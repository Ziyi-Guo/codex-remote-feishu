import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import xml.etree.ElementTree as ET

from profc_memory.publish import publish_work, render_xml

OWNER = 'ou_owner'
PRIVATE = dict(link_share_entity='closed', external_access=False,
               share_entity='only_full_access', security_entity='only_full_access')


def snapshot():
    event = dict(event_id='evt_one', source_id='message:om_one', revision='rev1', work_id='byok',
                 kind='progress', claim='部署完成，尚未验收。', quote='部署完成，尚未验收。',
                 occurred_at='2026-10-27T01:00:00+00:00', observed_at='2026-10-27T02:00:00+00:00',
                 actor='张三', stage='deployment', facet='status', status='accepted', supersedes=[])
    source = dict(source_id='message:om_one', revision='rev1', content=event['quote']+'\n不要发布这一段私聊全文。',
                  url='https://example.test/source?a=1&b=2')
    return dict(work_id='byok', title='自带密钥', revision=3, owner_id=OWNER, visibility='owner',
                sources=[source], events=[event], current_events=[event], candidates=0)


class Lark:
    def __init__(self):
        self.calls = []
        self.public = dict(PRIVATE)
        self.members = []
        self.revision = 1
        self.content = '<title>私有事项脉络页</title>'
        self.create_count = self.update_count = 0
        self.patch_works = True
        self.create_unknown = False
        self.bad_readback = False
        self.raw_code = 0
        self.auth_owner = OWNER
        self.update_unknown = False

    def __call__(self, argv, **kwargs):
        self.calls.append(argv)
        assert kwargs.get('shell', False) is False
        assert kwargs['timeout'] > 0
        if argv[1:3] == ['auth', 'status']:
            payload = dict(identity='user', verified=True, identities=dict(user=dict(openId=self.auth_owner, status='authorized')))
            return subprocess.CompletedProcess(argv, 0, json.dumps(payload), '')
        if '+create' in argv:
            self.create_count += 1
            assert '--content' not in argv
            assert argv[argv.index('--title')+1] == '私有事项脉络页'
            if self.create_unknown:
                raise subprocess.TimeoutExpired('no raw output', 30)
            data = dict(document=dict(document_id='doc_real', revision_id=1, url='https://tenant.feishu.cn/docx/doc_real'))
        elif 'permission.public' in argv:
            if 'patch' in argv:
                assert '--yes' in argv
                if self.patch_works:
                    self.public = json.loads(argv[argv.index('--data')+1])
            data = dict(permission_public=self.public)
        elif argv[1:3] == ['api', 'GET']:
            assert '/permissions/doc_real/members' in argv[3]
            params = json.loads(argv[argv.index('--params')+1])
            assert params == dict(type='docx', fields='type')
            data = dict(code=self.raw_code, data=dict(items=self.members))
        elif '+update' in argv:
            self.update_count += 1
            expected = str(self.revision)
            assert argv[argv.index('--revision-id')+1] == expected
            content_arg = argv[argv.index('--content')+1]
            assert content_arg.startswith('@') and not os.path.isabs(content_arg[1:])
            path = Path(kwargs['cwd']) / content_arg[1:]
            assert path.stat().st_mode & 0o777 == 0o600
            self.content = path.read_text()
            assert '不要发布这一段私聊全文' not in self.content
            self.revision += 1
            if self.update_unknown:
                raise subprocess.TimeoutExpired('unknown write', 30)
            data = {}
        elif '+fetch' in argv:
            content = '<title>读取缺失</title>' if self.bad_readback and self.update_count else self.content
            data = dict(document=dict(document_id='doc_real', revision_id=self.revision, content=content))
        else:
            raise AssertionError(argv)
        return subprocess.CompletedProcess(argv, 0, json.dumps(dict(ok=True, identity='user', data=data)), '')


class PublishTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.root.chmod(0o700)
        self.lark = Lark()

    def tearDown(self):
        self.tmp.cleanup()

    def publish(self, value=None):
        return publish_work(self.root, value or snapshot(), OWNER, runner=self.lark)

    def test_publish_verifies_private_acl_and_readback_before_receipt(self):
        self.lark.public['link_share_entity'] = 'tenant_readable'
        result = self.publish()
        self.assertEqual('published', result['status'])
        self.assertEqual('3', result['published_ledger_version'])
        self.assertEqual('2', result['saved_remote_revision'])
        self.assertEqual('https://tenant.feishu.cn/docx/doc_real', result['url'])
        receipt = self.root/'publish/byok.json'
        self.assertEqual(0o600, receipt.stat().st_mode & 0o777)
        self.assertEqual('published', json.loads(receipt.read_text())['status'])
        self.assertEqual([], list((self.root/'publish').glob('*.xml')))
        self.assertNotIn('部署完成', json.dumps(result, ensure_ascii=False))
        self.assertEqual(1, self.lark.update_count)
        self.assertGreaterEqual(sum('permission.public' in a and 'get' in a for a in self.lark.calls), 2)

    def test_unsafe_public_permissions_never_receive_body(self):
        self.lark.public['external_access'] = True
        self.lark.patch_works = False
        result = self.publish()
        self.assertEqual('unsafe_permissions', result['error']['type'])
        self.assertEqual(0, self.lark.update_count)

    def test_unknown_creation_is_never_repeated(self):
        self.lark.create_unknown = True
        first = self.publish()
        second = self.publish()
        self.assertEqual('creation_unknown', first['error']['type'])
        self.assertEqual('creation_unknown', second['error']['type'])
        self.assertEqual(1, self.lark.create_count)

    def test_external_or_unknown_collaborators_reject_body(self):
        for members in [[dict(member_type='openid', member_id='ou_other', type='user')],
                        [dict(member_id=OWNER)], [dict(member_type='chatid', member_id=OWNER, type='chat')]]:
            with self.subTest(members=members):
                self.lark.members = members
                result = self.publish()
                self.assertEqual('unsafe_collaborators', result['error']['type'])
                self.assertEqual(0, self.lark.update_count)

    def test_owner_only_collaborator_and_nested_raw_success_are_supported(self):
        self.lark.members = [dict(member_type='openid', member_id=OWNER, type='user')]
        self.assertEqual('published', self.publish()['status'])

    def test_raw_nested_nonzero_code_is_not_ignored(self):
        self.lark.raw_code = 999
        result = self.publish()
        self.assertEqual('api_failed', result['error']['type'])
        self.assertEqual(0, self.lark.update_count)

    def test_remote_human_edit_blocks_overwrite(self):
        self.assertEqual('published', self.publish()['status'])
        self.lark.revision += 1
        self.lark.content += '<p>人工补充</p>'
        updated = dict(snapshot(), revision=4)
        result = self.publish(updated)
        self.assertEqual('view_drift', result['error']['type'])
        self.assertEqual(1, self.lark.update_count)

    def test_unchanged_ledger_checks_permissions_without_rewriting(self):
        self.publish()
        self.lark.calls = []
        result = self.publish()
        self.assertEqual('published', result['status'])
        self.assertEqual(1, self.lark.update_count)
        self.assertTrue(any('permission.public' in a for a in self.lark.calls))
        self.assertTrue(any(a[1:3] == ['api', 'GET'] for a in self.lark.calls))

    def test_failed_readback_is_not_marked_published(self):
        self.lark.bad_readback = True
        result = self.publish()
        self.assertEqual('readback_failed', result['error']['type'])
        self.assertNotEqual('published', result['status'])
        self.assertNotIn('published_ledger_version', result)

    def test_wrong_logged_in_owner_cannot_create(self):
        self.lark.auth_owner = 'ou_someone_else'
        result = self.publish()
        self.assertEqual('owner_mismatch', result['error']['type'])
        self.assertEqual(0, self.lark.create_count)

    def test_auth_failure_before_create_does_not_poison_future_attempt(self):
        self.lark.auth_owner = 'ou_someone_else'
        self.publish()
        self.lark.auth_owner = OWNER
        self.assertEqual('published', self.publish()['status'])
        self.assertEqual(1, self.lark.create_count)

    def test_unknown_write_can_be_verified_without_repeating_overwrite(self):
        self.lark.update_unknown = True
        first = self.publish()
        self.assertEqual('write_unknown', first['error']['type'])
        self.assertEqual('published', self.publish()['status'])
        self.assertEqual(1, self.lark.update_count)

    def test_verified_mapping_allows_distinct_app_scoped_owner_ids(self):
        value = dict(snapshot(), owner_id='ou_runtime_app_owner')
        result = self.publish(value)
        self.assertEqual('published', result['status'])
        self.assertEqual('ou_runtime_app_owner', result['ledger_owner_id'])
        self.assertEqual(OWNER, result['owner_open_id'])
        changed = dict(value, owner_id='ou_another_ledger_owner')
        result = self.publish(changed)
        self.assertEqual('owner_mismatch', result['error']['type'])
        self.assertEqual(1, self.lark.update_count)

    def test_xml_escapes_quote_and_url_without_emitting_whole_source(self):
        value = snapshot()
        text = '<script>危险 & 文本</script>。'
        value['events'][0].update(quote=text, claim=text)
        value['sources'][0]['content'] = text + '\n不要发布这一段私聊全文。'
        xml = render_xml(value)
        root = ET.fromstring('<root>'+xml+'</root>')
        self.assertNotIn('<script>', xml)
        self.assertIn(text, ''.join(root.itertext()))
        self.assertIn(value['sources'][0]['url'], [a.get('href') for a in root.iter('a')])
        self.assertNotIn('不要发布这一段私聊全文', xml)
        self.assertIn('work_id=byok', xml)
        self.assertIn('ledger_version=3', xml)
        self.assertIn('evt_one', xml)

    def test_current_sections_are_bounded_but_history_keeps_every_event(self):
        value = snapshot()
        base = value['events'][0]
        events = [dict(base, event_id='evt_' + str(i), occurred_at=f'2026-10-{i+1:02d}T01:00:00+00:00') for i in range(16)]
        events += [dict(base, event_id='risk_' + str(i), kind='risk', occurred_at=None) for i in range(10)]
        value.update(events=events, current_events=events)
        xml = render_xml(value)
        main, tail = xml.split('<h1>未决问题与风险</h1>')
        unresolved, history = tail.split('<h1>按发生时间排列的历史</h1>')
        self.assertEqual(12, main.count('event_id='))
        self.assertEqual(8, unresolved.count('event_id='))
        self.assertEqual(26, history.count('event_id='))
        self.assertLess(main.index('event_id=evt_15'), main.index('event_id=evt_14'))
        self.assertLess(history.index('event_id=evt_0'), history.index('event_id=evt_15'))
        self.assertIn('未展示的未被判定失效', xml)

    def test_document_xml_quote_shows_visible_text_only(self):
        value = snapshot()
        quote = '<p>当前<b>尚未验收</b>。</p>'
        value['sources'][0].update(kind='document', content=quote)
        value['events'][0].update(quote=quote, claim=quote)
        xml = render_xml(value)
        self.assertNotIn('&lt;p&gt;', xml)
        self.assertIn('当前尚未验收。', xml)

    def test_history_marks_explicitly_superseded_event(self):
        value = snapshot()
        old = value['events'][0]
        newer = dict(old, event_id='evt_two', kind='correction', supersedes=['evt_one'],
                     occurred_at='2026-10-28T00:00:00+00:00')
        value.update(events=[newer, old], current_events=[newer])
        xml = render_xml(value)
        self.assertIn('已被替代：evt_two', xml)

    def test_symlink_receipt_rejected_without_external_calls(self):
        publish = self.root/'publish'
        publish.mkdir(mode=0o700)
        (publish/'byok.json').symlink_to(self.root/'external.json')
        result = self.publish()
        self.assertEqual('unsafe_path', result['error']['type'])
        self.assertEqual([], self.lark.calls)


if __name__ == '__main__':
    unittest.main()
