"""Test proof validation and isolation, not provider inference or product behavior."""

import copy
import json
from pathlib import Path
import unittest

import neopi_e2e as proof


class ProofEvidenceTest(unittest.TestCase):
    def setUp(self):
        self.selector = 'neoproxy/proof-chat'
        self.observed = {'path': '/v1/chat/completions', 'model': self.selector,
                         'status': 200, 'request_id': 'req-one'}
        self.fault = [{'status': 503, 'model': 'fail-chat'}]
        self.request = {'id': 'req-one', 'asked': self.selector, 'outcome': 'ok',
                        'tokens': {'in': 10, 'out': 8},
                        'decision': {'stageIndex': 1, 'accountId': 'real-account',
                                     'model': proof.REAL_PROVIDER + '/fallback',
                                     'trail': [{'kind': 'stage', 'index': 0,
                                                'skipped': 'upstream-error'}]}}
        self.assistant = {'model': self.selector, 'usage': {'input': 10, 'output': 8}}

    def validate(self):
        proof.validate_evidence('chat', self.selector, self.observed, self.fault,
                                self.request, self.assistant, 'real-account')

    def test_consistent_evidence_is_accepted(self):
        self.validate()

    def test_preexisting_unhealthy_stage_is_not_upstream_failure(self):
        self.request['decision']['trail'][0]['skipped'] = 'unhealthy'
        with self.assertRaisesRegex(proof.ProofError, 'upstream-error'):
            self.validate()

    def test_trail_without_actual_fault_upstream_call_is_rejected(self):
        self.fault.clear()
        with self.assertRaisesRegex(proof.ProofError, 'actually called'):
            self.validate()

    def test_uncorrelated_request_is_rejected(self):
        self.request['id'] = 'req-another'
        with self.assertRaisesRegex(proof.ProofError, 'correlation'):
            self.validate()

    def test_wrong_protocol_is_rejected(self):
        self.observed['path'] = '/v1/responses'
        with self.assertRaisesRegex(proof.ProofError, 'endpoint/model'):
            self.validate()

    def test_direct_or_other_account_success_is_not_fallback_proof(self):
        for field, value in [('stageIndex', 0), ('accountId', 'different-account')]:
            with self.subTest(field=field):
                previous = copy.deepcopy(self.request['decision'])
                self.request['decision'][field] = value
                with self.assertRaisesRegex(proof.ProofError, 'fallback stage/account'):
                    self.validate()
                self.request['decision'] = previous

    def test_missing_real_usage_is_rejected(self):
        self.request['tokens']['out'] = 0
        with self.assertRaisesRegex(proof.ProofError, 'usage missing'):
            self.validate()

    def test_neopi_must_retain_virtual_selector(self):
        self.assistant['model'] = 'fallback'
        with self.assertRaisesRegex(proof.ProofError, 'retain'):
            self.validate()


class IsolationAndDiscoveryTest(unittest.TestCase):
    def test_known_credentials_are_redacted_before_capture(self):
        raw = b'Authorization: Bearer example-test-key\\nreply: public fact\\n'
        captured = proof.redact_bytes(raw, ['example-test-key', ''])
        self.assertNotIn(b'example-test-key', captured)
        self.assertIn(b'[REDACTED]', captured)
        self.assertIn(b'public fact', captured)

    def test_receipt_redaction_preserves_nonsecret_evidence(self):
        receipt = {'reply': 'a public fact', 'detail': 'example-test-token', 'usage': 5}
        redacted = proof.redact_json(receipt, ['example-test-token'])
        self.assertEqual(redacted, {'reply': 'a public fact', 'detail': '[REDACTED]', 'usage': 5})

    def test_environment_is_an_allowlist(self):
        environment = proof.safe_environment(Path('/scratch/client'))
        self.assertEqual(environment['HOME'], '/scratch/client')
        self.assertEqual(environment['PI_CODING_AGENT_DIR'], '/scratch/client/agent')
        self.assertEqual(environment['TMPDIR'], '/scratch/client/tmp')
        for name in ['OMP_PROFILE', 'OMP_AUTH_BROKER_URL', 'GROQ_API_KEY',
                     'ANTHROPIC_API_KEY', 'PI_CONFIG_DIR', 'SSH_AUTH_SOCK']:
            self.assertNotIn(name, environment)

    def test_scratch_rejects_owner_profile_and_root(self):
        for path in ['/home/shayna/.omp/agent', str(proof.SCRATCH)]:
            with self.subTest(path=path), self.assertRaises(proof.ProofError):
                proof.scratch_path(path)

    def test_direct_model_does_not_count_as_virtual_discovery(self):
        models = {'data': [{'id': 'neoproxy/' + proof.REAL_PROVIDER + '/fallback'}]}
        with self.assertRaisesRegex(proof.ProofError, 'not discovered'):
            proof.discover_virtual(models, 'neoproxy/proof-chat')

    def test_discovery_uses_server_returned_selector(self):
        selector = 'neoproxy/proof-chat'
        self.assertEqual(proof.discover_virtual({'data': [{'id': selector}]}, selector), selector)

    def test_error_or_empty_assistant_is_not_success(self):
        for message in [{'role': 'assistant', 'stopReason': 'error', 'content': []},
                        {'role': 'assistant', 'stopReason': 'stop', 'content': []}]:
            event = json.dumps({'type': 'message_end', 'message': message})
            with self.subTest(message=message), self.assertRaises(proof.ProofError):
                proof.assistant_result(event)

    def test_only_completed_assistant_text_is_captured(self):
        events = [
            {'type': 'message_update', 'partial': 'not a completed answer'},
            {'type': 'message_end', 'message': {
                'role': 'assistant', 'stopReason': 'stop', 'model': 'neoproxy/proof-chat',
                'usage': {'input': 5, 'output': 3},
                'content': [{'type': 'thinking', 'thinking': 'not receipt text'},
                            {'type': 'text', 'text': 'A planet fact.'}]}}]
        result = proof.assistant_result('\n'.join(json.dumps(event) for event in events))
        self.assertEqual(result['text'], 'A planet fact.')
        self.assertEqual(result['usage'], {'input': 5, 'output': 3})


if __name__ == '__main__':
    unittest.main()
