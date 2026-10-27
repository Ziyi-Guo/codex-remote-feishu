from io import BytesIO
import json
from pathlib import Path
import tempfile
import unittest
from urllib.error import HTTPError

from profc_memory.gbrain import gbrain_candidates


class Response(BytesIO):
    def __init__(self, payload, headers=None, status=200, sse=False):
        body = json.dumps(payload) if payload is not None else ''
        if sse:
            body = 'event: message\ndata: ' + body + '\n\n'
        super().__init__(body.encode())
        self.headers = headers or {}
        self.status = status


class GbrainTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.config = self.root / 'config.toml'
        self.endpoint = 'http://127.0.0.1:8765/mcp'
        self.write_config()
        self.works = [{'work_id': 'byok'}, {'work_id': 'cli'}]
        self.calls = []
        self.entity = {'found': True, 'card': {'entity_slug': 'projects/byok'}}
        self.sse = False

    def tearDown(self):
        self.tmp.cleanup()

    def write_config(self, endpoint=None):
        self.config.write_text('[mcp_servers.gbrain]\nurl = ' + json.dumps(endpoint or self.endpoint) +
                               '\n[mcp_servers.gbrain.http_headers]\nAuthorization = "Bearer synthetic-local"\n')
        self.config.chmod(0o600)

    def opener(self, request, timeout):
        payload = json.loads(request.data)
        self.calls.append((request, payload, timeout))
        if payload['method'] == 'initialize':
            return Response({'jsonrpc': '2.0', 'id': 1, 'result': {'protocolVersion': '2025-03-26'}},
                            headers={'Mcp-Session-Id': 'local-session'}, sse=self.sse)
        if payload['method'] == 'notifications/initialized':
            return Response(None, status=202)
        return Response({'jsonrpc': '2.0', 'id': 2, 'result': {
            'content': [{'type': 'text', 'text': json.dumps(self.entity)}]}}, sse=self.sse)

    def call(self, query='自带密钥项目', **kwargs):
        return gbrain_candidates(query, self.works, config_path=self.config,
                                 opener=kwargs.get('opener', self.opener))

    def test_three_readonly_rpc_calls_return_only_known_identifier(self):
        self.assertEqual([{'work_id': 'byok'}], self.call())
        self.assertEqual(['initialize', 'notifications/initialized', 'tools/call'],
                         [payload['method'] for _, payload, _ in self.calls])
        request, payload, timeout = self.calls[-1]
        self.assertEqual({'name': 'entity', 'arguments': {'name': '自带密钥项目'}}, payload['params'])
        self.assertEqual(self.endpoint, request.full_url)
        headers = {name.lower(): value for name, value in request.header_items()}
        self.assertEqual('local-session', headers['mcp-session-id'])
        self.assertEqual('2025-03-26', headers['mcp-protocol-version'])
        self.assertEqual('Bearer synthetic-local', headers['authorization'])
        self.assertLessEqual(timeout, 3)

    def test_sse_and_structured_card_identifiers(self):
        self.sse = True
        self.entity = {'found': True, 'entity': {'details': {'canonical_slug': 'cli'}}}
        self.assertEqual([{'work_id': 'cli'}], self.call())

    def test_no_hit_unknown_id_suggestions_facts_and_prose_are_not_candidates(self):
        for entity in [
                {'found': False, 'entity_slug': 'byok', 'suggestions': [{'slug': 'byok'}]},
                {'found': True, 'entity_slug': 'unknown'},
                {'found': True, 'text': 'entity_slug=projects/byok'},
                {'found': True, 'suggestions': [{'slug': 'byok'}], 'facts': [{'slug': 'byok'}]},
                {'found': True, 'card': {'text': '{"slug":"projects/byok"}'}},
                {'card': {'slug': 'byok'}},
        ]:
            with self.subTest(entity=entity):
                self.entity = entity
                self.assertEqual([], self.call())

    def test_query_credentials_length_and_empty_are_rejected_before_network(self):
        for query in ['', 'x' * 513, 'api_key=synthetic-private', 'Bearer synthetic-private']:
            self.assertEqual([], self.call(query))
        self.assertEqual([], self.calls)

    def test_remote_userinfo_https_and_redirect_style_endpoints_never_receive_headers(self):
        for endpoint in ['http://example.test/mcp', 'http://127.0.0.1.example.test/mcp',
                         'http://user@127.0.0.1/mcp', 'https://127.0.0.1/mcp',
                         'http://127.0.0.1/mcp#redirect', 'http://127.0.0.1:99999/mcp']:
            self.write_config(endpoint)
            self.assertEqual([], self.call())
        self.assertEqual([], self.calls)

    def test_private_config_and_ancestor_symlinks_are_required(self):
        self.config.chmod(0o644)
        self.assertEqual([], self.call())
        self.config.chmod(0o600)
        link = self.root / 'linked.toml'
        link.symlink_to(self.config)
        self.assertEqual([], gbrain_candidates('byok', self.works, config_path=link, opener=self.opener))
        directory = self.root / 'linked-dir'
        directory.symlink_to(self.root, target_is_directory=True)
        self.assertEqual([], gbrain_candidates('byok', self.works, config_path=directory / 'config.toml', opener=self.opener))
        self.assertEqual([], self.calls)

    def test_timeout_unauthorized_and_rpc_errors_fall_back_to_empty(self):
        for error in [TimeoutError(), HTTPError(self.endpoint, 401, 'Unauthorized', {}, None)]:
            def broken(*args, **kwargs):
                raise error
            self.assertEqual([], self.call(opener=broken))
        self.assertEqual([], self.call(opener=lambda *a, **k: Response({'jsonrpc': '2.0', 'id': 1, 'error': {'code': -1}})))

    def test_depth_and_response_size_are_bounded(self):
        nested = {'slug': 'byok'}
        for _ in range(20):
            nested = {'nested': nested}
        self.entity = {'found': True, 'entity': nested}
        self.assertEqual([], self.call())
        self.entity = {'found': True, 'entity_slug': 'byok', 'text': 'x' * 300_000}
        self.assertEqual([], self.call())


if __name__ == '__main__':
    unittest.main()
