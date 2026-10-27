import unittest

from profc_memory.discover import discover_documents


OWNER = 'owner'
WORKS = [dict(work_id='byok', anchors=['BYOK']), dict(work_id='cli', anchors=['IPGen CLI'])]
CONFIG = dict(id='chat', kind='chat', chat_id='oc_one', work_ids=['byok', 'cli'])


def record(content, **kwargs):
    return dict(dict(source_id='message:om_one', revision='1', content=content,
                     url='https://applink.feishu.cn/client/message/open?messageId=om_one',
                     owner_id=OWNER, visibility='owner', observed_at='2026-10-27T00:00:00+00:00'), **kwargs)


class DiscoveryTests(unittest.TestCase):
    def test_only_exact_allowed_https_document_links_are_discovered(self):
        valid = ['https://bytedance.larkoffice.com/docx/Doc123?from=chat#part',
                 'https://feishu.cn/wiki/Wiki123', 'https://team.larksuite.com/docx/Lark123']
        invalid = ['http://team.feishu.cn/docx/Plain', 'https://team.feishu.cn.evil.test/docx/Fake',
                   'https://evil.test/docx/Other', 'https://user:pass@team.feishu.cn/docx/Credential',
                   'https://team.feishu.cn:8443/docx/Port', 'https://team.feishu.cn:443/docx/ExplicitPort',
                   'https://team.feishu.cn/base/NotDoc', 'https://team.feishu.cn/docx/Bad/extra',
                   'https://team.feishu.cn/docx/Bad%2FToken', 'https://team.feishu.cn/docx/Query?token=private']
        found = discover_documents(record('BYOK ' + '，'.join(valid + invalid) + '。'), CONFIG, WORKS, OWNER)
        self.assertEqual(valid, [item['url'] for item in found])
        self.assertTrue(all(item['work_ids'] == ['byok'] for item in found))

    def test_repeated_reference_keeps_one_identity_and_original_url(self):
        url = 'https://team.feishu.cn/docx/Doc123'
        found = discover_documents(record('BYOK [' + url + '](' + url + ') ' + url + '?from=another'), CONFIG, WORKS, OWNER)
        self.assertEqual(1, len(found))
        self.assertEqual(url, found[0]['url'])

    def test_ambiguous_anchor_is_only_pending(self):
        found = discover_documents(record('BYOK 与 IPGen CLI 共用 https://team.feishu.cn/wiki/Wiki123'), CONFIG, WORKS, OWNER)
        self.assertEqual([], found[0]['work_ids'])

    def test_identifier_substring_does_not_claim_project(self):
        found = discover_documents(record('BYOK_extra https://team.feishu.cn/docx/Doc123'), CONFIG, WORKS, OWNER)
        self.assertEqual([], found[0]['work_ids'])

    def test_nonowner_shared_and_deleted_sources_never_discover(self):
        for extra in [dict(owner_id='other'), dict(visibility='shared'), dict(deleted=True)]:
            self.assertEqual([], discover_documents(record('BYOK https://team.feishu.cn/docx/Doc123', **extra),
                                                    CONFIG, WORKS, OWNER))


if __name__ == '__main__':
    unittest.main()
