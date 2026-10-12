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

"""Summarize separate harness telemetry populations without inventing coverage.

Inputs are the benchmark's audits.json, metrics.json and tool-timings.json.
An optional acceptance manifest declares cases with id, status (passed, failed,
unverified), and evidence (artifact references). A passing case without evidence
is unverified. Missing fixture branches remain gaps even if other cases pass.
"""
import argparse
from collections import Counter
from datetime import datetime
import json
from pathlib import Path
import re


OUTCOMES = ('succeeded', 'failed', 'blocked', 'unknown', 'canceled')


def timestamp(value):
    parsed = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if parsed.utcoffset() is None:
        raise ValueError('timing timestamp must include a timezone')
    return parsed.timestamp()


def outcome(value):
    """Map an explicit terminal status to a small, conservative outcome class."""
    status = str(value or '').strip().lower().replace('-', '_')
    if status in ('succeeded', 'success', 'passed', 'complete', 'completed'):
        return 'succeeded'
    if status in ('failed', 'failure', 'error', 'partial_failure', 'timed_out', 'timeout'):
        return 'failed'
    if status in ('blocked', 'rejected', 'denied', 'not_executed', 'not_configured',
                  'not_ready', 'unavailable', 'skipped'):
        return 'blocked'
    if status in ('canceled', 'cancelled'):
        return 'canceled'
    return 'unknown'


def _safe_label(value):
    value = str(value or '').strip().lower()
    return value if re.fullmatch(r'[a-z0-9_.-]{1,64}', value) else 'unknown'


def _result_payload(call):
    payload = call.get('payload')
    return payload if isinstance(payload, dict) else call


def _result_status(payload):
    raw = payload.get('result')
    if isinstance(raw, str):
        try:
            raw = json.loads(raw)
        except (TypeError, ValueError):
            return None
    if not isinstance(raw, dict):
        return None
    statuses = []
    for key in ('status', 'phase', 'outcome'):
        value = raw.get(key)
        if not value:
            continue
        status = str(value).strip().lower().replace('-', '_')
        if status in ('succeeded', 'success', 'passed', 'complete', 'completed',
                      'failed', 'failure', 'error', 'partial_failure', 'timed_out', 'timeout',
                      'blocked', 'rejected', 'denied', 'not_executed', 'not_configured',
                      'not_ready', 'unavailable', 'skipped', 'canceled', 'cancelled',
                      'unknown', 'outcome_unknown', 'unverifiable', 'stale', 'unverified',
                      'running', 'pending'):
            statuses.append(outcome(status))
    if not statuses:
        return None
    return statuses[0] if len(set(statuses)) == 1 else 'unknown'


def durable_outcome(call):
    """Disposition is semantic; failed alone records an invocation error."""
    payload = _result_payload(call)
    result_status = _result_status(payload)
    disposition = str(payload.get('disposition') or '').strip().lower()
    if disposition == 'failed':
        if result_status in ('blocked', 'canceled'):
            return result_status
        return 'failed'
    if payload.get('failed') is True:
        if result_status in ('blocked', 'canceled'):
            return result_status
        return 'failed'
    if disposition == 'succeeded':
        if result_status in ('failed', 'blocked', 'unknown', 'canceled'):
            return 'unknown'
        return 'succeeded'
    if result_status is not None:
        return result_status
    return 'unknown'


def command_concurrency(calls):
    points = []
    seen = set()
    incomplete = 0
    for call in calls:
        key = (call.get('runID'), call.get('callID'))
        if key in seen:
            raise ValueError('duplicate command timing identity')
        seen.add(key)
        if call.get('toolName') != 'exec_command':
            continue
        if not call.get('finishedAt') or not call.get('admittedAt'):
            incomplete += 1
            continue
        start, end = timestamp(call['admittedAt']), timestamp(call['finishedAt'])
        if end < start:
            raise ValueError('negative command interval')
        if end > start:
            points.extend([(start, 1), (end, -1)])
    active = maximum = 0
    overlap = 0
    previous = None
    for at, delta in sorted(points):
        if previous is not None and active > 1:
            overlap += at - previous
        active += delta
        maximum = max(maximum, active)
        previous = at
    return {'maximumOverlappingCommandIntervals': maximum,
            'overlapMS': round(overlap * 1000),
            'incompleteCommandIntervals': incomplete,
            'complete': incomplete == 0,
            'limitation': 'Admission-to-result intervals include queueing; they do not prove simultaneous running processes or throughput.'}


def _model_retries(audits):
    values = []
    for audit in audits:
        stats = audit.get('metrics', {}).get('modelCallStats')
        if isinstance(stats, dict):
            retries = stats.get('retryAttempts', 0)
            if type(retries) is int and retries >= 0:
                values.append(retries)
    return {'retryAttempts': sum(values), 'runsWithCounts': len(values),
            'runsWithoutCounts': len(audits) - len(values),
            'complete': len(values) == len(audits),
            'limitation': 'Model retry rollups are separate from tool retries and are unknown when an audit has no modelCallStats count.'}


def _evidence_files(evidence, base):
    if not isinstance(evidence, list) or not evidence or not all(isinstance(item, str) and item.strip() for item in evidence):
        return 0, 0
    root = Path(base).resolve()
    found = 0
    missing = 0
    for item in evidence:
        try:
            path = (root / item).resolve()
            available = path.is_relative_to(root) and path.is_file()
        except (OSError, RuntimeError, ValueError):
            available = False
        found += int(available)
        missing += int(not available)
    return found, missing


def summarize(audits, metrics, timings, acceptance=None, acceptance_base=None):
    runs = [a['runID'] for a in audits]
    if len(runs) != len(set(runs)):
        raise ValueError('duplicate audit run')
    metric_runs = [r['run'] for r in metrics['rows']]
    if len(metric_runs) != len(set(metric_runs)) or set(metric_runs) != set(runs):
        raise ValueError('audit and UI metrics must describe the same distinct runs')
    calls = timings['calls']
    if any(c['runID'] not in set(runs) for c in calls):
        raise ValueError('timing calls include an unrelated run')
    tools = [t for a in audits for t in a['metrics'].get('tools', [])]
    tool_outcomes = Counter(outcome(t.get('status')) for t in tools)
    failed = [t for t in tools if outcome(t.get('status')) == 'failed']
    ui_failed = sum(r.get('failedItems', 0) for r in metrics['rows'])
    recovered = sum(r.get('recoveredItems', 0) for r in metrics['rows'])
    event_outcomes = Counter(durable_outcome(c) for c in calls)
    invocation_failures = sum(_result_payload(c).get('failed') is True for c in calls)
    missing_invocation_flags = sum(type(_result_payload(c).get('failed')) is not bool for c in calls)
    result = {
        'nativeAudit': {'entries': len(tools), 'outcomes': {key: tool_outcomes[key] for key in OUTCOMES},
                        'failedAttempts': tool_outcomes['failed'],
                        'failurePercent': round(100 * tool_outcomes['failed'] / len(tools), 2) if tools else None,
                        'failedByTool': dict(Counter(_safe_label(t.get('name')) for t in failed)),
                        'failedByCategory': dict(Counter(_safe_label((t.get('diagnostic') or {}).get('category')) for t in failed)),
                        'failedByCode': dict(Counter(_safe_label((t.get('diagnostic') or {}).get('code')) for t in failed)),
                        'possiblyTruncatedRuns': [a['runID'] for a in audits if len(a['metrics'].get('tools', [])) >= 128],
                        'modelRetries': _model_retries(audits)},
        'ui': {'failedItems': ui_failed, 'recoveredItems': recovered},
        'durableEvents': {'calls': len(calls), 'outcomes': {key: event_outcomes[key] for key in OUTCOMES},
                          'invocationFailures': invocation_failures,
                          'missingInvocationFailureFlags': missing_invocation_flags,
                          'limitation': 'Current durable failed flags record invocation errors; disposition records semantic success or failure. Result statuses refine blocked and unknown outcomes. Missing semantic evidence remains unknown.'},
        'reconciliation': {'nativeAuditFailedEntries': tool_outcomes['failed'],
                           'uiFailedItems': ui_failed, 'uiRecoveredItems': recovered,
                           'durableInvocationFailures': invocation_failures,
                           'limitation': 'These populations have different boundaries and are not call-level comparable. Equal counts do not establish agreement; do not mix denominators.'},
        'commandTiming': command_concurrency(calls),
    }
    if acceptance is not None:
        cases = acceptance['cases']
        ids = [case['id'] for case in cases]
        if not ids or any(not isinstance(identifier, str) or not identifier.strip() for identifier in ids) or len(ids) != len(set(ids)):
            raise ValueError('acceptance cases must have distinct IDs and must not be empty')
        gaps = []
        for case in cases:
            status = case.get('status', 'unverified')
            if status not in ('passed', 'failed', 'unverified'):
                raise ValueError('invalid acceptance status')
            found, missing = _evidence_files(case.get('evidence'), acceptance_base or Path.cwd())
            if status != 'passed' or found == 0 or missing:
                if status == 'passed':
                    reason = 'Passing case has no available evidence files'
                    if found and missing:
                        reason = 'Some evidence files are unavailable'
                else:
                    reason = 'Case is marked ' + status + ' in the acceptance manifest'
                gaps.append({'id': case['id'], 'status': status if status != 'passed' else 'unverified',
                             'evidenceFilesFound': found, 'evidenceFilesMissing': missing,
                             'reason': reason})
        result['acceptance'] = {'declaredCases': len(cases), 'fullyVerified': not gaps, 'gaps': gaps}
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('--acceptance', type=Path)
    parser.add_argument('--require-acceptance', action='store_true')
    args = parser.parse_args()
    if args.require_acceptance and args.acceptance is None:
        parser.error('--require-acceptance requires --acceptance')
    def read(name):
        return json.loads((args.directory / name).read_text())
    acceptance = json.loads(args.acceptance.read_text()) if args.acceptance else None
    result = summarize(read('audits.json'), read('metrics.json'), read('tool-timings.json'), acceptance,
                       args.acceptance.parent if args.acceptance else None)
    print(json.dumps(result, indent=2))
    return int(args.require_acceptance and not result['acceptance']['fullyVerified'])


if __name__ == '__main__':
    raise SystemExit(main())
