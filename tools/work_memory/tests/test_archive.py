import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from profc_memory.archive import apply_dossier, dossier_directory, read_dossier
from profc_memory.ledger import Ledger


class ArchiveTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name) / 'private'
        self.root.mkdir(mode=0o700)
        config = self.root / 'config.json'
        config.write_text(json.dumps({'schema_version': 1, 'owner_id': 'owner'}))
        config.chmod(0o600)
        self.ledger = Ledger(self.root / 'ledger.sqlite3', 'owner')
        self.ledger.register_work('profc', 'Prof.C')
        for revision in ['1', '2']:
            self.ledger.ingest_source(dict(source_id='doc:one', revision=revision, kind='document',
                object_id='one', observed_at='2026-10-27T01:00:00Z', occurred_at=None,
                url='https://example.test/original', title='项目方案', author='小王',
                content='授权范围待确认。', owner_id='owner', visibility='owner'), ['profc'])
        self.snapshot = self.ledger.read_work('profc', 'owner')
        self.ref = {'source_id': 'doc:one', 'revision': '1'}
        self.draft = {'sections': {s: [{'text': s + '的总结。', 'source_refs': [self.ref]}]
                                  for s in ['现状', '背景', '问题', '决策', '近期关注']},
                      'decisions': [{'slug': 'permission', 'title': '授权范围',
                          'body': [{'text': '授权范围待确认。', 'source_refs': [self.ref]}],
                          'status': 'proposed'}], 'reason': '根据现有资料重建'}

    def tearDown(self):
        self.ledger.close()
        self.tmp.cleanup()

    def apply(self, **kwargs):
        return apply_dossier(self.root, self.snapshot, self.draft,
                             now='2026-10-27T02:00:00Z', **kwargs)

    def configure_directory(self, directory):
        path = self.root / 'config.json'
        config = json.loads(path.read_text())
        config['dossiers'] = {'directory': str(directory)}
        path.write_text(json.dumps(config))

    def test_explicit_goal_and_project_update_history_keep_sources_and_versions(self):
        self.draft['sections']['目标'] = [{'text': '本期完成官方 Key MVP；以真实使用验收为完成条件。', 'source_refs': [self.ref]}]
        config_path = self.root / 'config.json'
        config = json.loads(config_path.read_text())
        config['works'] = [{'work_id': 'profc', 'summary': '客户自带模型资源的接入与验收'}]
        config_path.write_text(json.dumps(config))
        self.apply()
        goal = read_dossier(self.root, 'profc', 'owner', section='目标')
        self.assertIn('完成条件', goal['content'])
        self.assertIn('https://example.test/original', goal['content'])
        manifest = json.loads((self.root / 'dossiers/profc/manifest.json').read_text())
        self.assertEqual('客户自带模型资源的接入与验收', manifest['summary'])
        self.assertTrue((self.root / 'dossiers/profc/updates.md').is_file())
        self.draft['reason'] = '新增真实使用的验收边界，保留原 MVP 目标。'
        self.draft['sections']['现状'][0]['text'] = '完成定向测试，真实使用仍待验收。'
        self.apply()
        history = read_dossier(self.root, 'profc', 'owner', section='history')['content']
        self.assertIn('新增真实使用的验收边界', history)
        self.assertIn('versions/1/home.md', history)
        self.assertIn('versions/2/home.md', history)
        self.assertLess(history.index('versions/2/home.md'), history.index('versions/1/home.md'))
        self.assertEqual('unchanged', self.apply()['status'])
        updates = self.root / 'dossiers/profc/updates.md'
        updates.write_text(updates.read_text().replace('<!-- human:start -->', '<!-- human:start -->\n人工说明：等待客户确认。'))
        self.draft['sections']['问题'][0]['text'] = '仍待客户确认，来源结论保持不变。'
        self.apply()
        self.assertIn('人工说明：等待客户确认。', read_dossier(self.root, 'profc', 'owner', section='history')['content'])

    def test_external_directory_is_the_only_canonical_store_and_keeps_versions(self):
        with tempfile.TemporaryDirectory() as temporary:
            ancestor = Path(temporary).resolve() / 'existing'
            ancestor.mkdir(mode=0o755)
            ancestor.chmod(0o755)
            directory = ancestor / 'new-parent' / 'project-history'
            self.configure_directory(directory)
            self.assertEqual('updated', self.apply()['status'])
            self.assertEqual(directory, dossier_directory(self.root))
            self.assertFalse((self.root / 'dossiers').exists())
            self.assertEqual(0o755, ancestor.stat().st_mode & 0o777)
            self.assertEqual(0o700, directory.parent.stat().st_mode & 0o777)
            self.assertEqual('1', read_dossier(self.root, 'profc', 'owner')['version'])
            self.draft['sections']['现状'][0]['text'] = '外部正本更新。'
            self.assertEqual('2', self.apply()['version'])
            self.assertIn('外部正本更新', read_dossier(self.root, 'profc', 'owner')['content'])
            self.assertTrue((directory / 'profc/versions/1/home.md').is_file())
            self.assertTrue((directory / 'profc/versions/2/home.md').is_file())
            self.assertFalse(list(directory.rglob('*.sqlite3')))
            self.assertTrue((self.root / 'ledger.sqlite3').is_file())
            for item in (directory, *directory.rglob('*')):
                self.assertEqual(0o700 if item.is_dir() else 0o600, item.stat().st_mode & 0o777)

    def test_external_directory_rejects_relative_traversal_symlink_and_permissions(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary).resolve()
            unsafe = base / 'public'
            unsafe.mkdir(mode=0o755)
            unsafe.chmod(0o755)
            real = base / 'real'
            real.mkdir(mode=0o700)
            link = base / 'link'
            link.symlink_to(real, target_is_directory=True)
            for directory in ['relative/dossiers', base / 'real/../elsewhere', link / 'nested', unsafe]:
                with self.subTest(directory=str(directory)):
                    self.configure_directory(directory)
                    with self.assertRaises(ValueError):
                        self.apply()
            self.assertEqual(0o755, unsafe.stat().st_mode & 0o777)

    def test_external_directory_rejects_nonowner_and_managed_file_symlink(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary).resolve() / 'archive'
            self.configure_directory(directory)
            self.apply()
            with self.assertRaises(PermissionError):
                read_dossier(self.root, 'profc', 'other')
            home = directory / 'profc/home.md'
            home.unlink()
            home.symlink_to(self.root / 'config.json')
            with self.assertRaises(ValueError):
                read_dossier(self.root, 'profc', 'owner')
            with self.assertRaises(ValueError):
                self.apply()

    def test_readable_canonical_pages_exact_refs_and_private_manifest(self):
        receipt = self.apply()
        self.assertEqual('updated', receipt['status'])
        home = self.root / 'dossiers/profc/home.md'
        text = home.read_text()
        self.assertEqual(['现状', '背景', '问题', '决策', '近期关注'],
                         [line[3:] for line in text.splitlines() if line.startswith('## ')])
        self.assertIn('根据现有资料于 2026-10-27T02:00:00Z 重建', text)
        self.assertIn('https://example.test/original', text)
        source = (home.parent / 'sources.md').read_text()
        self.assertIn('项目方案', source)
        self.assertIn('小王', source)
        self.assertIn('发生时间：未知', source)
        visible = __import__('re').sub(r'<!--.*?-->', '', source, flags=__import__('re').S)
        self.assertNotIn('doc:one', visible)
        self.assertNotIn('revision', visible)
        raw=read_dossier(self.root,'profc','owner','source:1')
        self.assertIn('授权范围待确认',raw['content'])
        self.assertEqual('1',raw['source_revision'])
        self.assertEqual('1', read_dossier(self.root, 'profc', 'owner')['version'])
        self.assertIn('decisions/permission.md', text)
        detail = read_dossier(self.root, 'profc', 'owner', section='decision:permission')
        self.assertIn('授权范围待确认', detail['content'])
        with self.assertRaises(ValueError):
            read_dossier(self.root, 'profc', 'owner', section='decision:../../outside')
        self.assertNotIn('source_refs', read_dossier(self.root, 'profc', 'owner')['content'])
        self.assertNotIn('doc:one', read_dossier(self.root, 'profc', 'owner')['content'])
        manifest = json.loads((home.parent / 'manifest.json').read_text())
        self.assertEqual({'path': 'home.md', 'heading': '现状', 'level': 2}, manifest['sections']['现状'])
        for path in (home.parent, *home.parent.rglob('*')):
            self.assertEqual(0o700 if path.is_dir() else 0o600, path.stat().st_mode & 0o777)

    def test_decision_level_refs_apply_to_structured_body(self):
        decision = self.draft['decisions'][0]
        decision['body'] = [{'text': '需要确认新版范围。'}]
        decision['source_refs'] = [{'source_id': 'doc:one', 'revision': '2'}]
        self.apply()
        source_page = (self.root / 'dossiers/profc/sources.md').read_text()
        self.assertIn('"revision": "2"', source_page)

    def test_empty_superseded_by_is_no_replacement(self):
        self.draft['decisions'][0]['superseded_by'] = ''
        self.assertEqual('updated', self.apply()['status'])

    def test_untitled_sources_have_human_labels_and_collection_time_is_metadata(self):
        source = dict(source_id='message:two', revision='1', kind='message', object_id='two',
                      observed_at='2026-10-27T10:32:41.123456Z', occurred_at='2026-10-20T10:22:34Z',
                      actor='Alice', content='范围已说明。', url='https://example.test/message',
                      owner_id='owner', visibility='owner')
        self.ledger.ingest_source(source, ['profc'])
        self.snapshot = self.ledger.read_work('profc', 'owner')
        self.draft['sections']['现状'][0]['source_refs'] = [{'source_id': 'message:two', 'revision': '1'}]
        self.apply()
        home = read_dossier(self.root, 'profc', 'owner')['content']
        self.assertIn('[2026-10-20 10:22 · Alice · 聊天消息]', home)
        source_page = read_dossier(self.root, 'profc', 'owner', 'sources')['content']
        self.assertIn('发生时间：2026-10-20 10:22', source_page)
        self.assertNotIn('2026-10-27T10:32:41.123456Z', source_page)
        self.assertIn('2026-10-27T10:32:41.123456Z', (self.root / 'dossiers/profc/sources.md').read_text())

    def test_source_html_title_is_readable_plain_text_without_modifying_ledger(self):
        for index, content in enumerate([
                '<h1>其他标题</h1><title>【PRD】<b>BYOK</b> 模式</title><p>正文。</p>',
                '<h1>【讨论稿】BYOK 模式</h1><p>正文。</p>']):
            source = dict(source_id='title-' + str(index), revision='1', kind='document', object_id=str(index),
                          observed_at='2026-10-27T10:00:00Z', occurred_at=None,
                          content=content, url='https://example.test/title/' + str(index),
                          owner_id='owner', visibility='owner')
            self.ledger.ingest_source(source, ['profc'])
            self.snapshot = self.ledger.read_work('profc', 'owner')
            self.draft['sections']['现状'][0]['source_refs'] = [{'source_id': source['source_id'], 'revision': '1'}]
            self.apply()
            text = read_dossier(self.root, 'profc', 'owner')['content']
            self.assertIn('[【PRD】BYOK 模式]' if index == 0 else '[【讨论稿】BYOK 模式]', text)
            self.assertNotIn('<b>', text)
            self.assertEqual(content, self.ledger.get_source(source['source_id'], '1')['content'])

    def test_no_content_change_keeps_version_and_human_region_survives(self):
        self.apply()
        home = self.root / 'dossiers/profc/home.md'
        home.write_text(home.read_text().replace('<!-- human:start -->\n', '<!-- human:start -->\n人工补充。\n'))
        result = self.apply()
        self.assertEqual('unchanged', result['status'])
        self.assertEqual('1', result['version'])
        self.draft['sections']['现状'][0]['text'] = '已确认下一步。'
        updated = self.apply()
        self.assertEqual('2', updated['version'])
        self.assertIn('人工补充。', home.read_text())
        versions = home.parent / 'versions'
        self.assertIn('现状的总结', (versions / '1/home.md').read_text())
        self.assertIn('已确认下一步', (versions / '2/home.md').read_text())
        self.assertEqual('根据现有资料重建', json.loads((versions / '2/receipt.json').read_text())['reason'])
        history = read_dossier(self.root,'profc','owner',section='history')['content']
        self.assertIn('首版依据现存材料重建', history)
        self.assertIn('版本 1',history)
        self.assertIn('版本 2',history)

    def test_machine_edit_returns_drift_without_overwrite(self):
        self.apply()
        home = self.root / 'dossiers/profc/home.md'
        home.write_text(home.read_text().replace('现状的总结', '手动修改的机器区'))
        before = home.read_bytes()
        self.assertEqual('drift', self.apply()['status'])
        self.assertEqual(before, home.read_bytes())

    def test_human_notes_are_visible_in_selected_sections_and_credentials_are_redacted(self):
        self.apply()
        home=self.root/'dossiers/profc/home.md'
        home.write_text(home.read_text().replace('<!-- human:start -->\n','<!-- human:start -->\n授权范围有更新，先核对。password=synthetic-value\n'))
        content=read_dossier(self.root,'profc','owner','现状')['content']
        self.assertIn('授权范围有更新，先核对',content)
        self.assertNotIn('synthetic-value',content)
        self.assertIn('synthetic-value',home.read_text())

    def test_old_build_snapshot_cannot_replace_newer_source_state(self):
        self.apply()
        before=(self.root/'dossiers/profc/home.md').read_bytes()
        source=dict(self.snapshot['sources'][0],revision='3',content='更新的授权范围。')
        source.pop('work_ids',None)
        self.ledger.ingest_source(source,['profc'])
        self.draft['sections']['现状'][0]['text']='基于旧快照的旧结论。'
        with self.assertRaises(ValueError):self.apply()
        self.assertEqual(before,(self.root/'dossiers/profc/home.md').read_bytes())

    def test_omitting_a_decision_does_not_withdraw_its_record_or_sources(self):
        self.draft['decisions'][0]['body'][0]['source_refs']=[dict(source_id='doc:one',revision='2')]
        self.apply()
        self.draft['decisions']=[]
        self.draft['sections']['决策'][0]['text']='本轮仅更新项目首页。'
        self.apply()
        path=self.root/'dossiers/profc/decisions/permission.md'
        self.assertTrue(path.exists())
        self.assertIn('授权范围待确认',read_dossier(self.root,'profc','owner','decision:permission')['content'])
        self.assertIn('decisions/permission.md',read_dossier(self.root,'profc','owner')['content'])
        self.assertIn('"revision": "2"',(self.root/'dossiers/profc/sources.md').read_text())

    def test_missing_managed_page_is_drift_and_changed_clock_is_not_a_revision(self):
        self.apply()
        result = apply_dossier(self.root, self.snapshot, self.draft, now='2026-10-28T00:00:00Z')
        self.assertEqual('unchanged', result['status'])
        (self.root / 'dossiers/profc/sources.md').unlink()
        self.assertEqual('drift', self.apply()['status'])

    def test_forged_snapshot_and_non_http_draft_link_are_rejected(self):
        self.snapshot['sources'][0]['url'] = 'https://fake.test/source'
        with self.assertRaises(ValueError):
            self.apply()
        self.snapshot = self.ledger.read_work('profc', 'owner')
        self.draft['sections']['现状'][0]['text'] = '原文：ftp://fake.test/source'
        with self.assertRaises(ValueError):
            self.apply()

    def test_source_from_another_work_is_not_a_valid_reference(self):
        self.ledger.register_work('other', 'Other')
        source = dict(self.snapshot['sources'][0], source_id='other-doc', object_id='other')
        source.pop('work_ids', None)
        self.ledger.ingest_source(source, ['other'])
        self.snapshot['sources'].append(source)
        self.draft['sections']['现状'][0]['source_refs'] = [{'source_id': 'other-doc', 'revision': '1'}]
        with self.assertRaises(ValueError):
            self.apply()

    def test_verified_chat_type_is_visible_without_rewriting_source_snapshot(self):
        cfg=self.root/'config.json';data=json.loads(cfg.read_text())
        data['sources']=[dict(chat_id='oc_private',chat_type='p2p',chat_type_verified_at='2026-10-27T01:00:00Z')]
        cfg.write_text(json.dumps(data))
        message=dict(self.snapshot['sources'][0],source_id='message:p1',object_id='p1',kind='message',revision='1',
                     url='https://example.test/chat?openChatId=oc_private')
        message.pop('work_ids',None)
        self.ledger.ingest_source(message,['profc']);self.snapshot=self.ledger.read_work('profc','owner')
        self.draft['sections']['现状'][0]['source_refs']=[dict(source_id='message:p1',revision='1')]
        self.apply()
        content=read_dossier(self.root,'profc','owner','sources')['content']
        self.assertIn('私聊',content)
        self.assertIn('来源类型：私聊',content)
        self.assertNotIn('chat_type',self.ledger.get_source('message:p1','1'))

    def test_bad_reference_owner_unsafe_url_and_traversal_do_not_publish(self):
        for mutate, exception in [
            (lambda: self.draft['sections']['现状'][0]['source_refs'][0].update(revision='missing'), ValueError),
            (lambda: self.snapshot.update(owner_id='stranger'), PermissionError),
            (lambda: self.snapshot.update(work_id='../outside'), ValueError),
            (lambda: self.draft['decisions'][0].update(slug='../outside'), ValueError),
            (lambda: self.draft['sections']['现状'][0].update(text='[伪造](https://fake.test)'), ValueError),
        ]:
            original = copy.deepcopy((self.snapshot, self.draft))
            mutate()
            with self.assertRaises(exception):
                self.apply()
            self.assertFalse((self.root / 'dossiers/profc').exists())
            self.snapshot, self.draft = original

    def test_nonowner_public_and_symlink_reads_rejected(self):
        self.apply()
        for requester, audience in [('other', 'owner'), ('owner', 'public')]:
            with self.assertRaises(PermissionError):
                read_dossier(self.root, 'profc', requester, audience=audience)
        home = self.root / 'dossiers/profc/home.md'
        home.unlink()
        home.symlink_to(self.root / 'config.json')
        with self.assertRaises(ValueError):
            read_dossier(self.root, 'profc', 'owner')
        with self.assertRaises(ValueError):
            self.apply()

    def test_failed_swap_restores_previous_complete_version(self):
        self.apply()
        home = self.root / 'dossiers/profc/home.md'
        before = home.read_bytes()
        self.draft['sections']['现状'][0]['text'] = '更新失败。'
        real_replace = os.replace
        def fail_install(source, destination):
            if Path(source).name.startswith('.stage-') and Path(destination).name == 'profc':
                raise OSError('simulated installation failure')
            return real_replace(source, destination)
        with patch('profc_memory.archive.os.replace', side_effect=fail_install):
            with self.assertRaises(OSError):
                self.apply()
        self.assertEqual(before, home.read_bytes())
        self.assertEqual('1', read_dossier(self.root, 'profc', 'owner')['version'])

    def test_interrupted_rename_can_be_read_and_next_write_recovers(self):
        self.apply()
        directory = self.root / 'dossiers'
        (directory / 'profc').rename(directory / '.backup-profc')
        self.assertEqual('1', read_dossier(self.root, 'profc', 'owner')['version'])
        self.assertFalse((directory / 'profc').exists())
        self.assertEqual('unchanged', self.apply()['status'])
        self.assertTrue((directory / 'profc/home.md').exists())
        self.assertFalse((directory / '.backup-profc').exists())


if __name__ == '__main__':
    unittest.main()
