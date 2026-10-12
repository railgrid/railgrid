#!/usr/bin/env python3
# Copyright 2026 The Railgrid Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import importlib.util
import json
import subprocess
import sys
from pathlib import Path
import tempfile
import unittest

sys.dont_write_bytecode = True

spec = importlib.util.spec_from_file_location('report', Path(__file__).with_name('app-studio-harness-report.py'))
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)
SCRIPT_PATH = Path(__file__).with_name('app-studio-harness-report.py')


def write_cli_inputs(directory):
    Path(directory, 'audits.json').write_text(json.dumps([
        {'runID': 'r', 'metrics': {'tools': []}},
    ]))
    Path(directory, 'metrics.json').write_text(json.dumps({
        'rows': [{'run': 'r', 'failedItems': 0, 'recoveredItems': 0}],
    }))
    Path(directory, 'tool-timings.json').write_text(json.dumps({'calls': []}))


def run_cli(directory, *arguments):
    return subprocess.run(
        [sys.executable, str(SCRIPT_PATH), str(directory), *map(str, arguments)],
        capture_output=True, text=True, check=False,
    )


class HarnessReportTest(unittest.TestCase):
    def test_populations_and_durable_failure_dimensions_stay_separate(self):
        audits = [{'runID': 'r', 'metrics': {
            'tools': [
                {'name': 'edit_file', 'status': 'failed', 'diagnostic': {'category': 'validation', 'code': 'version_conflict'}},
                {'name': 'exec_command', 'status': 'rejected'},
                {'name': 'interact', 'status': 'outcome_unknown'},
                {'name': 'edit_file', 'status': 'succeeded'},
            ],
            'modelCallStats': {'retryAttempts': 2},
        }}]
        metrics = {'rows': [{'run': 'r', 'failedItems': 1, 'recoveredItems': 1}]}
        timings = {'calls': [
            {'runID': 'r', 'callID': 'blocked', 'toolName': 'exec_command', 'failed': False,
             'disposition': 'failed', 'result': '{"status":"blocked","secret":"never print"}'},
            {'runID': 'r', 'callID': 'failed', 'toolName': 'edit_file', 'failed': True,
             'disposition': 'failed', 'result': '{"status":"failed"}'},
            {'runID': 'r', 'callID': 'unknown', 'toolName': 'browser', 'failed': False},
        ]}
        result = report.summarize(audits, metrics, timings)
        self.assertEqual(result['nativeAudit']['outcomes'], {
            'succeeded': 1, 'failed': 1, 'blocked': 1, 'unknown': 1, 'canceled': 0,
        })
        self.assertEqual(result['nativeAudit']['failedByCategory'], {'validation': 1})
        self.assertEqual(result['nativeAudit']['modelRetries']['retryAttempts'], 2)
        self.assertEqual(result['durableEvents']['outcomes'], {
            'succeeded': 0, 'failed': 1, 'blocked': 1, 'unknown': 1, 'canceled': 0,
        })
        self.assertEqual(result['durableEvents']['invocationFailures'], 1)
        self.assertNotIn('nativeFailureCountMatchesEventFlags', result['reconciliation'])
        self.assertIn('not call-level comparable', result['reconciliation']['limitation'])
        self.assertNotIn('never print', json.dumps(result))
        with self.assertRaises(ValueError):
            report.summarize(audits, {'rows': [{'run': 'unrelated'}]}, timings)

    def test_boundary_touch_is_not_overlap_and_duplicate_receipts_are_rejected(self):
        def call(identifier, start, end):
            return {'runID': 'r', 'callID': identifier, 'toolName': 'exec_command',
                    'admittedAt': f'2026-10-11T00:00:{start:02d}Z',
                    'finishedAt': f'2026-10-11T00:00:{end:02d}Z'}
        calls = [call('a', 0, 10), call('b', 10, 20)]
        self.assertEqual(report.command_concurrency(calls)['overlapMS'], 0)
        calls.append(call('c', 5, 15))
        self.assertEqual(report.command_concurrency(calls)['overlapMS'], 10000)
        self.assertEqual(report.command_concurrency(calls)['maximumOverlappingCommandIntervals'], 2)
        with self.assertRaises(ValueError):
            report.command_concurrency(calls + [calls[0]])
        calls.append({'runID': 'r', 'callID': 'pending', 'toolName': 'exec_command',
                      'admittedAt': '2026-10-11T00:00:05Z'})
        result = report.command_concurrency(calls)
        self.assertFalse(result['complete'])
        self.assertEqual(result['incompleteCommandIntervals'], 1)

    def test_evidence_gaps_are_actionable_without_echoing_paths_or_free_text(self):
        with tempfile.TemporaryDirectory() as tmp:
            Path(tmp, 'reuse.json').write_text('{}')
            manifest = {'cases': [
                {'id': 'existing-due-key', 'status': 'passed', 'evidence': ['reuse.json']},
                {'id': 'new-insert', 'status': 'unverified', 'reason': 'private diagnostic text'},
                {'id': 'missing', 'status': 'passed', 'evidence': ['private/path/missing.json']},
                {'id': 'outside', 'status': 'passed', 'evidence': ['../private.json']},
            ]}
            result = report.summarize([], {'rows': []}, {'calls': []}, manifest, tmp)
        self.assertFalse(result['acceptance']['fullyVerified'])
        self.assertEqual([c['id'] for c in result['acceptance']['gaps']], ['new-insert', 'missing', 'outside'])
        output = json.dumps(result)
        self.assertNotIn('private diagnostic text', output)
        self.assertNotIn('private/path', output)
        self.assertNotIn('../private.json', output)
        self.assertEqual(result['acceptance']['gaps'][1]['evidenceFilesMissing'], 1)
        with self.assertRaises(ValueError):
            report.summarize([], {'rows': []}, {'calls': []}, {'cases': [
                {'id': '', 'status': 'passed', 'evidence': ['unowned.json']}]})

    def test_missing_retry_rollup_is_unknown_and_malformed_status_is_not_success(self):
        audits = [{'runID': 'r', 'metrics': {
            'tools': [{'name': 'x', 'status': 'future-private-status'}],
            'modelCallStats': {},
        }}]
        result = report.summarize(audits, {'rows': [{'run': 'r'}]}, {'calls': []})
        self.assertEqual(result['nativeAudit']['outcomes']['unknown'], 1)
        self.assertEqual(result['nativeAudit']['modelRetries']['retryAttempts'], 0)
        self.assertEqual(result['nativeAudit']['modelRetries']['runsWithCounts'], 1)
        self.assertTrue(result['nativeAudit']['modelRetries']['complete'])
        missing = report.summarize(
            [{'runID': 'r', 'metrics': {'tools': []}}],
            {'rows': [{'run': 'r'}]}, {'calls': []},
        )
        self.assertEqual(missing['nativeAudit']['modelRetries']['runsWithoutCounts'], 1)

    def test_durable_disposition_precedes_conflicting_result_and_unknown_labels(self):
        self.assertEqual(report.durable_outcome({
            'disposition': 'failed', 'failed': False,
            'result': '{"status":"completed","exitCode":0}',
        }), 'failed')
        self.assertEqual(report.durable_outcome({
            'disposition': 'succeeded', 'failed': False, 'result': '{"status":"ok"}',
        }), 'succeeded')
        self.assertEqual(report.durable_outcome({
            'disposition': 'succeeded', 'failed': False, 'result': '{"status":"blocked"}',
        }), 'unknown')
        self.assertEqual(report.durable_outcome({
            'disposition': 'failed', 'failed': False, 'result': '{"status":"blocked"}',
        }), 'blocked')
        self.assertEqual(report.durable_outcome({
            'disposition': 'failed', 'failed': False, 'result': '{"status":"canceled"}',
        }), 'canceled')
        self.assertEqual(report.durable_outcome({
            'failed': False, 'result': '{"status":"outcome_unknown"}',
        }), 'unknown')

    def test_timing_timestamps_require_timezone(self):
        calls = [{'runID': 'r', 'callID': 'c', 'toolName': 'exec_command',
                  'admittedAt': '2026-10-11T00:00:00', 'finishedAt': '2026-10-11T00:00:01'}]
        with self.assertRaisesRegex(ValueError, 'timezone'):
            report.command_concurrency(calls)

    def test_audit_at_producer_tool_cap_is_marked_as_possibly_truncated(self):
        audit = {'runID': 'r', 'metrics': {'tools': [
            {'name': 'read_file', 'status': 'succeeded'} for _ in range(report.AUDIT_TOOL_CAP)
        ]}}
        result = report.summarize([audit], {'rows': [{'run': 'r'}]}, {'calls': []})
        self.assertEqual(report.AUDIT_TOOL_CAP, 128)
        self.assertEqual(result['nativeAudit']['possiblyTruncatedRuns'], ['r'])


class HarnessReportCLITest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.directory = Path(self.temp.name)
        write_cli_inputs(self.directory)

    def tearDown(self):
        self.temp.cleanup()

    def write_acceptance(self, manifest):
        path = self.directory / 'acceptance.json'
        path.write_text(json.dumps(manifest))
        return path

    def assert_invalid_cli_input(self, result, secret=None):
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertNotIn('Traceback', result.stderr)
        self.assertEqual(result.stdout, '')
        if secret:
            self.assertNotIn(secret, result.stderr)
            self.assertNotIn(secret, result.stdout)

    def test_malformed_manifest_array_and_missing_case_id_exit_two_without_echo(self):
        manifest = self.write_acceptance(['SECRET_ACCEPTANCE_VALUE'])
        result = run_cli(self.directory, '--acceptance', manifest, '--require-acceptance')
        self.assert_invalid_cli_input(result, 'SECRET_ACCEPTANCE_VALUE')

        manifest = self.write_acceptance({'cases': [
            {'status': 'passed', 'evidence': ['private/artifact.json'],
             'reason': 'SECRET_FREE_TEXT_VALUE'},
        ]})
        result = run_cli(self.directory, '--acceptance', manifest, '--require-acceptance')
        self.assert_invalid_cli_input(result, 'SECRET_FREE_TEXT_VALUE')
        self.assertNotIn('private/artifact.json', result.stderr)

    def test_null_acceptance_manifest_is_invalid_with_or_without_required_mode(self):
        manifest = self.directory / 'acceptance.json'
        manifest.write_text('null')
        for arguments in (('--acceptance', manifest),
                          ('--acceptance', manifest, '--require-acceptance')):
            with self.subTest(arguments=arguments):
                result = run_cli(self.directory, *arguments)
                self.assert_invalid_cli_input(result)

    def test_malformed_input_containers_and_counters_exit_two_without_tracebacks(self):
        bad_inputs = [
            ('audits.json', {'runID': 'SECRET_RUN'}),
            ('metrics.json', {'rows': [[]]}),
            ('tool-timings.json', {'calls': [[]]}),
        ]
        for name, value in bad_inputs:
            with self.subTest(name=name):
                write_cli_inputs(self.directory)
                Path(self.directory, name).write_text(json.dumps(value))
                result = run_cli(self.directory)
                self.assert_invalid_cli_input(result, 'SECRET_RUN' if name == 'audits.json' else None)

        write_cli_inputs(self.directory)
        Path(self.directory, 'metrics.json').write_text(json.dumps({
            'rows': [{'run': 'r', 'failedItems': True}],
        }))
        result = run_cli(self.directory)
        self.assert_invalid_cli_input(result)

    def test_malformed_json_and_missing_files_exit_two_without_tracebacks(self):
        Path(self.directory, 'metrics.json').write_text('{"private":"SECRET_JSON_VALUE"')
        result = run_cli(self.directory)
        self.assert_invalid_cli_input(result, 'SECRET_JSON_VALUE')

        write_cli_inputs(self.directory)
        Path(self.directory, 'tool-timings.json').unlink()
        result = run_cli(self.directory)
        self.assert_invalid_cli_input(result)

        write_cli_inputs(self.directory)
        result = run_cli(self.directory, '--acceptance', self.directory / 'missing-acceptance.json')
        self.assert_invalid_cli_input(result)

    def test_empty_evidence_file_is_an_acceptance_gap_with_exit_one(self):
        evidence = self.directory / 'empty.png'
        evidence.write_bytes(b'')
        manifest = self.write_acceptance({'cases': [
            {'id': 'empty-evidence', 'status': 'passed', 'evidence': ['empty.png']},
        ]})
        result = run_cli(self.directory, '--acceptance', manifest, '--require-acceptance')
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertNotIn('Traceback', result.stderr)
        self.assertEqual(result.stderr, '')
        output = json.loads(result.stdout)
        self.assertFalse(output['acceptance']['fullyVerified'])
        self.assertEqual(output['acceptance']['gaps'][0]['evidenceFilesFound'], 0)
        self.assertEqual(output['acceptance']['gaps'][0]['evidenceFilesMissing'], 1)
        self.assertIn('do not verify file contents', output['acceptance']['limitation'])


if __name__ == '__main__':
    unittest.main()
