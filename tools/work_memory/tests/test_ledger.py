import concurrent.futures
import os
from pathlib import Path
import tempfile
import unittest

from profc_memory.ledger import Ledger


class LedgerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / 'private' / 'memory.sqlite3'
        self.ledger = Ledger(self.path, 'owner')
        self.ledger.register_work('byok', 'BYOK', ['自带密钥'])
        self.ledger.register_work('cli', 'CLI')

    def tearDown(self):
        self.ledger.close()
        self.tmp.cleanup()

    def source(self, source_id='s1', revision='1', **overrides):
        value = dict(source_id=source_id, kind='message', object_id=source_id,
                     revision=revision, occurred_at='2026-10-20T10:00:00Z',
                     observed_at='2026-10-27T10:00:00Z', url='https://example.test/raw?id=1',
                     content='部署完成。尚未验收。需要用户确认。', visibility='owner', owner_id='owner')
        return dict(value, **overrides)

    def event(self, **overrides):
        return dict(dict(work_id='byok', kind='progress', claim='部署完成', quote='部署完成。',
                         occurred_at='2026-10-20T10:00:00Z', actor='alice', stage='deployment',
                         facet='status', supersedes=[], status='accepted'), **overrides)

    def test_idempotent_revision_and_events(self):
        self.assertTrue(self.ledger.ingest_source(self.source(), ['byok']))
        self.assertFalse(self.ledger.ingest_source(self.source(), ['byok']))
        first = self.ledger.apply_events('s1', '1', [self.event()])
        version = self.ledger.read_work('byok', 'owner')['revision']
        self.assertEqual(first, self.ledger.apply_events('s1', '1', [self.event()]))
        snap = self.ledger.read_work('byok', 'owner')
        self.assertEqual(version, snap['revision'])
        self.assertEqual(1, len(snap['events']))
        self.assertEqual('s1', snap['events'][0]['source_id'])
        self.assertEqual(self.source()['observed_at'], snap['events'][0]['observed_at'])

    def test_same_revision_is_immutable(self):
        self.ledger.ingest_source(self.source(), ['byok'])
        with self.assertRaises(ValueError):
            self.ledger.ingest_source(self.source(content='修改后的内容'), ['byok'])
        self.assertTrue(self.ledger.ingest_source(self.source(revision='2', content='修改后的内容'), ['byok']))
        self.assertEqual(2, len(self.ledger.read_work('byok', 'owner')['sources']))

    def test_shared_source_keeps_works_separate(self):
        self.ledger.ingest_source(self.source(), ['byok', 'cli'])
        self.ledger.apply_events('s1', '1', [self.event(), self.event(work_id='cli', claim='CLI 已部署')])
        self.assertEqual(['byok'], [e['work_id'] for e in self.ledger.read_work('byok', 'owner')['events']])
        self.assertEqual(['cli'], [e['work_id'] for e in self.ledger.read_work('cli', 'owner')['events']])

    def test_unknown_work_or_unlinked_event_rejected(self):
        with self.assertRaises(ValueError):
            self.ledger.ingest_source(self.source(), ['unknown'])
        self.ledger.ingest_source(self.source(), ['byok'])
        with self.assertRaises(ValueError):
            self.ledger.apply_events('s1', '1', [self.event(work_id='cli')])

    def test_quote_must_be_nonempty_verbatim_and_batch_is_atomic(self):
        self.ledger.ingest_source(self.source(), ['byok'])
        for quote in ['', '验收成功']:
            with self.assertRaises(ValueError):
                self.ledger.apply_events('s1', '1', [self.event(), self.event(quote=quote)])
        self.assertEqual([], self.ledger.read_work('byok', 'owner')['events'])
        self.assertEqual(1, len(self.ledger.pending_sources()))

    def test_candidate_is_not_current(self):
        self.ledger.ingest_source(self.source(), ['byok'])
        self.ledger.apply_events('s1', '1', [self.event(status='candidate')])
        snap = self.ledger.read_work('byok', 'owner')
        self.assertEqual([], snap['current_events'])
        self.assertEqual(1, snap['candidates'])

    def test_event_kind_is_an_explicit_enum(self):
        self.ledger.ingest_source(self.source(), ['byok'])
        for kind in ['fact', 'arbitrary', 'completed']:
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                self.ledger.apply_events('s1', '1', [self.event(kind=kind)])
        kinds = ['proposal', 'decision', 'progress', 'verification', 'correction',
                 'scope_extension', 'open_question', 'risk', 'source_update']
        self.ledger.apply_events('s1', '1', [self.event(kind=kind) for kind in kinds])
        self.assertEqual(set(kinds), {e['kind'] for e in self.ledger.read_work('byok', 'owner')['events']})

    def test_weak_association_only_allows_candidates(self):
        self.ledger.ingest_source(self.source(), ['cli'])
        self.ledger.apply_events('s1', '1', [self.event(status='candidate')])
        snap = self.ledger.read_work('byok', 'owner')
        self.assertEqual(1, snap['candidates'])
        self.assertEqual([], snap['current_events'])
        self.assertEqual(['cli'], snap['sources'][0]['work_ids'])

    def test_unknown_occurrence_is_kept_without_using_observation_as_decision_time(self):
        self.ledger.ingest_source(self.source(occurred_at=None), ['byok'])
        unknown = self.ledger.apply_events('s1', '1', [self.event(occurred_at=None)])[0]
        known = self.ledger.apply_events('s1', '1', [self.event()])[0]
        for timestamp, target in [(None, known), ('2026-10-20T10:00:00Z', unknown)]:
            with self.assertRaises(ValueError):
                self.ledger.apply_events('s1', '1', [self.event(kind='correction', occurred_at=timestamp, supersedes=[target])])
        snap = self.ledger.read_work('byok', 'owner')
        self.assertEqual(2, len(snap['current_events']))
        self.assertIsNone(snap['sources'][0]['occurred_at'])

    def test_late_old_material_never_replaces_new_state(self):
        self.ledger.ingest_source(self.source(), ['byok'])
        new_id = self.ledger.apply_events('s1', '1', [self.event()])[0]
        self.ledger.ingest_source(self.source('old', observed_at='2026-10-28T00:00:00Z'), ['byok'])
        self.ledger.apply_events('old', '1', [self.event(occurred_at='2026-10-01T00:00:00Z', claim='旧状态')])
        self.assertIn(new_id, [e['event_id'] for e in self.ledger.read_work('byok', 'owner')['current_events']])
        with self.assertRaises(ValueError):
            self.ledger.apply_events('old', '1', [self.event(kind='correction', occurred_at='2026-10-01T00:00:00Z', supersedes=[new_id])])

    def test_supersession_is_explicit_same_work_stage_facet_and_not_earlier(self):
        self.ledger.ingest_source(self.source(), ['byok', 'cli'])
        old = self.ledger.apply_events('s1', '1', [self.event()])[0]
        for invalid in [dict(kind='progress'), dict(stage='acceptance'), dict(facet='cost'),
                        dict(work_id='cli'), dict(status='candidate'), dict(supersedes=['missing'])]:
            event = self.event(kind='correction', claim='尚未验收', quote='尚未验收。', supersedes=[old])
            event.update(invalid)
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                self.ledger.apply_events('s1', '1', [event])
        new = self.ledger.apply_events('s1', '1', [self.event(kind='correction', claim='尚未验收', quote='尚未验收。', supersedes=[old])])[0]
        snap = self.ledger.read_work('byok', 'owner')
        self.assertEqual([new], [e['event_id'] for e in snap['current_events']])
        self.assertEqual(2, len(snap['events']))

    def test_owner_access_and_storage_permissions(self):
        self.assertEqual(0o600, os.stat(self.path).st_mode & 0o777)
        self.assertEqual(0o700, os.stat(self.path.parent).st_mode & 0o777)
        for who, audience in [('other', 'owner'), ('owner', 'shared')]:
            with self.assertRaises(PermissionError):
                self.ledger.read_work('byok', who, audience)
        with self.assertRaises(PermissionError):
            Ledger(self.path, 'other')
        with self.assertRaises(PermissionError):
            self.ledger.ingest_source(self.source(owner_id='other'), ['byok'])

    def test_source_pending_cursors_and_failures(self):
        self.ledger.ingest_source(self.source(), ['byok'], cursor_key='chat', cursor_value={'offset': 1})
        self.assertEqual({'offset': 1}, self.ledger.get_cursor('chat'))
        self.assertEqual(['byok'], self.ledger.get_source('s1', '1')['work_ids'])
        self.assertEqual(1, len(self.ledger.pending_sources()))
        self.ledger.record_failure('failed', 'fetch timeout')
        self.assertEqual('failed', self.ledger.list_failures()[0]['source_id'])
        self.assertIsNone(self.ledger.get_cursor('failed'))
        with self.assertRaises(ValueError):
            self.ledger.ingest_source(self.source(content='changed'), ['byok'], cursor_key='chat', cursor_value=2)
        self.assertEqual({'offset': 1}, self.ledger.get_cursor('chat'))
        self.ledger.apply_events('s1', '1', [])
        self.assertEqual([], self.ledger.pending_sources())
        self.ledger.set_cursor('chat', 2)
        self.assertEqual(2, self.ledger.get_cursor('chat'))

    def test_parallel_duplicate_ingestion(self):
        def ingest(_):
            with Ledger(self.path, 'owner') as ledger:
                ledger.ingest_source(self.source(), ['byok'])
                return ledger.apply_events('s1', '1', [self.event()])
        with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
            results = list(pool.map(ingest, range(8)))
        self.assertTrue(all(item == results[0] for item in results))
        self.assertEqual(1, len(self.ledger.read_work('byok', 'owner')['events']))

    def test_late_old_revision_cannot_supersede_new_revision(self):
        newer = self.source(revision='hash-new', modified_at='2026-10-23T10:00:00Z', content='使用方案B。')
        older = self.source(revision='hash-old', modified_at='2026-10-22T10:00:00Z', content='更正方案B，使用方案A。')
        self.ledger.ingest_source(newer, ['byok'])
        new = self.ledger.apply_events('s1', 'hash-new', [self.event(quote='使用方案B。')])[0]
        self.ledger.ingest_source(older, ['byok'])
        self.assertEqual('hash-new', self.ledger.get_source('s1')['revision'])
        with self.assertRaises(ValueError):
            self.ledger.apply_events('s1', 'hash-old', [self.event(kind='correction', quote=older['content'], supersedes=[new])])

    def test_unordered_revisions_require_explicit_selection(self):
        self.ledger.ingest_source(self.source(revision='hash-a'), ['byok'])
        self.ledger.ingest_source(self.source(revision='hash-b'), ['byok'])
        with self.assertRaises(ValueError):
            self.ledger.get_source('s1')

    def test_canonical_ledger_rejects_negation_cut_and_credential_url(self):
        self.ledger.ingest_source(self.source(content='尚未验收成功。'), ['byok'])
        with self.assertRaises(ValueError):
            self.ledger.apply_events('s1', '1', [self.event(quote='验收成功')])
        with self.assertRaises(ValueError):
            self.ledger.ingest_source(self.source('credential', url='https://user:synthetic-private@example.test/a'), ['byok'])

    def test_ancestor_symlink_is_rejected(self):
        root = Path(self.tmp.name)
        (root / 'real').mkdir()
        (root / 'shortcut').symlink_to(root / 'real', target_is_directory=True)
        with self.assertRaises(ValueError):
            Ledger(root / 'shortcut' / 'nested' / 'store.sqlite3', 'owner')


if __name__ == '__main__':
    unittest.main()
