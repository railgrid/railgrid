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
import sys
from pathlib import Path
import tempfile
import unittest

sys.dont_write_bytecode = True

spec = importlib.util.spec_from_file_location('report', Path(__file__).with_name('app-studio-harness-report.py'))
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)


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


if __name__ == '__main__':
    unittest.main()
