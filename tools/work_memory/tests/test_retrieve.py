import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from profc_memory.retrieve import retrieve


class RetrieveTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name).resolve()
        self.root.chmod(0o700)
        self.write(self.root / 'config.json', {'schema_version': 1, 'owner_id': 'owner'})
        self.add_work('byok', '自带密钥', ['BYOK'])
        self.add_work('cli', '命令行工具', ['CLI'])
        (self.root / 'dossiers' / '.archive.lock').touch(mode=0o600)
        self.reader = patch('profc_memory.retrieve.read_dossier', side_effect=self.read_dossier)
        self.reader.start()
        self.content = '部署已完成，尚未通过验收。\n\n负责人仍需确认灰度范围。'

    def tearDown(self):
        self.reader.stop()
        self.temp.cleanup()

    def write(self, path, value):
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        for directory in (path.parent, *path.parent.parents):
            if directory == self.root.parent:
                break
            directory.chmod(0o700)
        path.write_text(json.dumps(value, ensure_ascii=False), encoding='utf-8')
        path.chmod(0o600)

    def add_work(self, work_id, title, aliases):
        self.write(self.root / 'dossiers' / work_id / 'manifest.json',
                   dict(schema_version=1, work_id=work_id, title=title, aliases=aliases,
                        owner_id='owner', visibility='owner', version='1'))

    def read_dossier(self, root, work_id, requester_id, section='home', audience='owner'):
        manifest = json.loads((self.root / 'dossiers' / work_id / 'manifest.json').read_text())
        return dict(status='read', work_id=work_id, title=manifest['title'],
                    version=manifest['version'], section=section, content=self.content,
                    receipt={'version': manifest['version'], 'built_at': 'now'})

    def call(self, **kwargs):
        return retrieve(self.root, requester_id='owner', **kwargs)

    def test_explicit_work_reads_only_selected_section(self):
        result = self.call(work_id='byok')
        self.assertEqual('read', result['status'])
        self.assertEqual(self.content, result['content'])
        self.assertEqual(len(self.content), result['chars'])
        self.assertEqual('现状', result['receipt']['section'])
        self.assertNotIn('byok', result['content'])
        self.assertEqual('characters', result['budget_unit'])

    def test_query_only_returns_relevant_catalog_without_body_read(self):
        with patch('profc_memory.retrieve.read_dossier') as reader:
            result = self.call(query='BYOK进展')
            self.assertEqual('candidates', result['status'])
            self.assertEqual(['byok'], [c['work_id'] for c in result['candidates']])
            self.assertEqual('', result['content'])
            reader.assert_not_called()

    def test_unrelated_query_is_empty_and_does_not_read_body(self):
        with patch('profc_memory.retrieve.read_dossier') as reader:
            result = self.call(query='今天北京天气')
            self.assertEqual('none', result['status'])
            self.assertEqual(0, result['chars'])
            reader.assert_not_called()

    def test_unconfirmed_topic_switch_cannot_reuse_old_project_for_pronoun(self):
        self.call(work_id='byok',session_id='switch')
        self.call(query='所有持续事项',session_id='switch')
        self.assertEqual('none',self.call(query='它怎么样',session_id='switch')['status'])

    def test_broad_query_returns_metadata_only(self):
        result = self.call(query='所有持续事项')
        self.assertEqual('candidates', result['status'])
        self.assertEqual(2, len(result['candidates']))
        self.assertEqual(0, result['chars'])

    def test_session_unchanged_compaction_version_and_switch(self):
        args = dict(work_id='byok', session_id='s')
        self.assertEqual('read', self.call(**args)['status'])
        unchanged = self.call(**args)
        self.assertEqual('unchanged', unchanged['status'])
        self.assertEqual(0, unchanged['receipt']['chars'])
        self.assertEqual('read', self.call(**args, after_compaction=True)['status'])
        path = self.root / 'dossiers/byok/manifest.json'
        manifest = json.loads(path.read_text())
        manifest['version'] = '2'
        self.write(path, manifest)
        self.assertEqual('read', self.call(**args)['status'])
        self.call(work_id='cli', session_id='s')
        self.assertEqual('read', self.call(**args)['status'])

    def test_human_content_change_refreshes_same_machine_version(self):
        self.call(work_id='byok', session_id='s')
        self.content = '人工更新：灰度尚未验收。'
        result = self.call(work_id='byok', session_id='s')
        self.assertEqual('read', result['status'])
        self.assertEqual(self.content, result['content'])
        self.assertEqual('1', result['version'])

    def test_archive_drift_is_visible_without_loading_body(self):
        with patch('profc_memory.retrieve.read_dossier', return_value=dict(
                status='drift', version='1', content='', drift=['home.md'])):
            result = self.call(work_id='byok')
        self.assertEqual('drift', result['status'])
        self.assertEqual('', result['content'])
        self.assertEqual(['home.md'], result['receipt']['drift'])

    def test_real_archive_builder_and_reader(self):
        from profc_memory.archive import apply_dossier
        from profc_memory.ledger import Ledger
        self.reader.stop()
        with Ledger(self.root / 'ledger.sqlite3', 'owner') as ledger:
            ledger.register_work('profc', 'Prof.C')
            snapshot = ledger.read_work('profc', 'owner')
        draft = dict(sections={s: '此栏目尚待核验。' for s in ['现状', '背景', '问题', '决策', '近期关注']},
                     decisions=[], reason='本地契约验证')
        apply_dossier(self.root, snapshot, draft)
        result = self.call(work_id='profc', section='背景', session_id='real')
        self.assertEqual('read', result['status'])
        self.assertEqual('此栏目尚待核验。', result['content'])
        self.assertEqual('unchanged', self.call(work_id='profc', section='背景', session_id='real')['status'])
        (self.root / 'dossiers/profc').rename(self.root / 'dossiers/.backup-profc')
        self.assertEqual('candidates', self.call(query='Prof.C')['status'])
        self.assertEqual('read', self.call(work_id='profc', after_compaction=True)['status'])

    def test_external_private_canonical_directory_catalog_and_read(self):
        from profc_memory.archive import apply_dossier
        from profc_memory.ledger import Ledger
        self.reader.stop()
        with tempfile.TemporaryDirectory() as external_name:
            external = Path(external_name).resolve()
            external.chmod(0o700)
            self.write(self.root / 'config.json', dict(schema_version=1, owner_id='owner',
                       dossiers=dict(directory=str(external))))
            with Ledger(self.root / 'ledger.sqlite3', 'owner') as ledger:
                ledger.register_work('profc', 'Prof.C')
                snapshot = ledger.read_work('profc', 'owner')
            draft = dict(sections={s: '外部正本尚待验收。' for s in ['现状', '背景', '问题', '决策', '近期关注']},
                         decisions=[], reason='正本目录验证')
            apply_dossier(self.root, snapshot, draft)
            result = self.call(query='Prof.C')
            self.assertEqual('candidates', result['status'])
            self.assertEqual(str(external / 'profc/home.md'), result['candidates'][0]['path'])
            self.assertEqual('none', self.call(query='BYOK')['status'])
            result = self.call(work_id='profc', session_id='outside')
            self.assertEqual('外部正本尚待验收。', result['content'])
            self.assertTrue((self.root / 'retrieval/read_receipts.jsonl').exists())
            self.assertFalse((external / 'retrieval').exists())
            with self.assertRaises(PermissionError):
                retrieve(self.root, requester_id='other', work_id='profc')
            manifest = external / 'profc/manifest.json'
            data = json.loads(manifest.read_text())
            data['owner_id'] = 'other'
            manifest.write_text(json.dumps(data))
            with self.assertRaises(PermissionError):
                self.call(query='Prof.C')
            alias = self.root / 'external-link'
            alias.symlink_to(external, target_is_directory=True)
            self.write(self.root / 'config.json', dict(schema_version=1, owner_id='owner',
                       dossiers=dict(directory=str(alias))))
            with self.assertRaises(ValueError):
                self.call(query='Prof.C')

    def test_same_work_remembers_each_read_section(self):
        self.call(work_id='byok', section='现状', session_id='s')
        self.call(work_id='byok', section='背景', session_id='s')
        self.assertEqual('unchanged', self.call(work_id='byok', section='现状', session_id='s')['status'])

    def test_session_and_receipt_symlinks_are_rejected(self):
        self.call(work_id='byok', session_id='s')
        directory = self.root / 'retrieval'
        session = next(directory.glob('*.json'))
        session.unlink()
        session.symlink_to(self.root / 'config.json')
        with self.assertRaises(ValueError):
            self.call(work_id='byok', session_id='s')
        session.unlink()
        receipts = directory / 'read_receipts.jsonl'
        receipts.unlink()
        receipts.symlink_to(self.root / 'config.json')
        with self.assertRaises(ValueError):
            self.call(work_id='byok')

    def test_invalid_budget_and_section_are_rejected(self):
        for args in [dict(max_chars=-1), dict(max_chars=True), dict(section='../home')]:
            with self.assertRaises(ValueError):
                self.call(work_id='byok', **args)

    def test_pronouns_inherit_only_confirmed_current_work(self):
        self.assertEqual('none', self.call(query='它现在怎么样', session_id='s')['status'])
        self.call(query='BYOK', session_id='s')
        self.assertEqual('none', self.call(query='它现在怎么样', session_id='s')['status'])
        self.call(work_id='byok', session_id='s')
        self.assertEqual('read', self.call(query='它现在怎么样', section='背景', session_id='s')['status'])
        result = self.call(query='这个CLI现在怎么样', session_id='s')
        self.assertEqual('candidates', result['status'])
        self.assertEqual(['cli'], [c['work_id'] for c in result['candidates']])
        self.assertEqual('none', self.call(query='它现在怎么样', session_id='other')['status'])

    def test_budget_preserves_complete_paragraphs_and_negation(self):
        self.content = '部署已完成，尚未通过验收。\n\n' + '不能上线。' * 20
        result = self.call(work_id='byok', max_chars=25)
        self.assertEqual('部署已完成，尚未通过验收。', result['content'])
        self.assertTrue(result['truncated'])
        self.assertLessEqual(result['chars'], 25)
        result = self.call(work_id='byok', max_chars=5)
        self.assertEqual('', result['content'])
        self.assertTrue(result['truncated'])

    def test_truncated_read_does_not_hide_later_full_read(self):
        self.call(work_id='byok', session_id='s', max_chars=5)
        self.assertEqual('read', self.call(work_id='byok', session_id='s')['status'])

    def test_owner_and_audience_denied_before_reader(self):
        with patch('profc_memory.retrieve.read_dossier') as reader:
            for args in [dict(requester_id='other'), dict(requester_id='owner', audience='shared')]:
                with self.assertRaises(PermissionError):
                    retrieve(self.root, work_id='byok', **args)
            reader.assert_not_called()

    def test_traversal_symlinks_and_foreign_manifest_are_rejected(self):
        for work_id in ['../byok', '/byok', 'a/b', '']:
            with self.assertRaises(ValueError):
                self.call(work_id=work_id)
        alias = self.root / 'alias'
        alias.symlink_to(self.root / 'dossiers', target_is_directory=True)
        with self.assertRaises(ValueError):
            retrieve(alias / 'byok', requester_id='owner')
        manifest = self.root / 'dossiers/byok/manifest.json'
        manifest.unlink()
        manifest.symlink_to(self.root / 'config.json')
        with self.assertRaises(ValueError):
            self.call(query='BYOK')
        manifest.unlink()
        self.add_work('byok', '自带密钥', ['BYOK'])
        data = json.loads(manifest.read_text())
        data['owner_id'] = 'other'
        self.write(manifest, data)
        with self.assertRaises(PermissionError):
            self.call(work_id='byok')

    def test_resolver_cannot_inject_global_facts_or_unknown_work(self):
        resolver = lambda query: [{'facts': 'unrelated global private text'}, {'work_id': 'outside'}]
        self.assertEqual('none', self.call(query='天气', resolver=resolver)['status'])
        def unavailable(query):
            raise OSError('401 private details')
        result = self.call(query='BYOK', resolver=unavailable)
        self.assertEqual('candidates', result['status'])
        self.assertNotIn('401', json.dumps(result))

    def test_resolver_can_add_known_semantic_candidate_without_reading_body(self):
        resolver = lambda query: [{'work_id': 'byok', 'facts': 'must not enter response'},
                                  {'work_id': 'outside'}, {'facts': 'global facts'}]
        with patch('profc_memory.retrieve.read_dossier') as reader:
            result = self.call(query='运营商渠道', resolver=resolver)
            self.assertEqual('candidates', result['status'])
            self.assertEqual(['byok'], [c['work_id'] for c in result['candidates']])
            self.assertEqual(0, result['chars'])
            self.assertEqual('', result['content'])
            self.assertNotIn('must not enter', json.dumps(result))
            reader.assert_not_called()

    def test_resolver_empty_result_keeps_unrelated_query_empty(self):
        result = self.call(query='天气', resolver=lambda query: [])
        self.assertEqual('none', result['status'])
        self.assertEqual(0, result['chars'])

    def test_owner_check_happens_before_resolver(self):
        with patch('profc_memory.retrieve.read_dossier') as reader:
            resolver = unittest.mock.Mock(return_value=[{'work_id': 'byok'}])
            for args in [dict(requester_id='other'), dict(requester_id='owner', audience='shared')]:
                with self.assertRaises(PermissionError):
                    retrieve(self.root, query='运营商渠道', resolver=resolver, **args)
            resolver.assert_not_called()
            reader.assert_not_called()

    def test_receipts_are_persisted_with_private_permissions(self):
        self.call(work_id='byok', session_id='s')
        self.call(work_id='byok', session_id='s')
        path = self.root / 'retrieval' / 'read_receipts.jsonl'
        receipts = [json.loads(line) for line in path.read_text().splitlines()]
        self.assertEqual(['read', 'unchanged'], [r['status'] for r in receipts])
        self.assertEqual([len(self.content), 0], [r['chars'] for r in receipts])
        self.assertEqual(0o600, path.stat().st_mode & 0o777)
        self.assertNotIn(self.content, path.read_text())


if __name__ == '__main__':
    unittest.main()
