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
# Keep this in sync with projectAssistantAuditMaxTools in
# providers/app-studio/api/assistant_audit.go.
AUDIT_TOOL_CAP = 128


class InputError(ValueError):
    """A safe, user-facing validation error for report inputs."""


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
    if not evidence:
        return 0, 0
    root = Path(base).resolve()
    found = 0
    missing = 0
    for item in evidence:
        try:
            path = (root / item).resolve()
            available = path.is_relative_to(root) and path.is_file() and path.stat().st_size > 0
        except (OSError, RuntimeError, ValueError):
            available = False
        found += int(available)
        missing += int(not available)
    return found, missing


def _object(value, label):
    if not isinstance(value, dict):
        raise InputError(label + ' must be an object')
    return value


def _array(value, label):
    if not isinstance(value, list):
        raise InputError(label + ' must be an array')
    return value


def _identifier(value, label):
    if not isinstance(value, str) or not value.strip():
        raise InputError(label + ' must be a nonempty string')
    return value


def _optional_string(obj, key, label):
    value = obj.get(key)
    if value is not None and not isinstance(value, str):
        raise InputError(label + ' must be a string')


def _counter(obj, key, label):
    value = obj.get(key, 0)
    if type(value) is not int or value < 0:
        raise InputError(label + ' must be a nonnegative integer')


def _validate_audits(audits):
    audits = _array(audits, 'audits')
    run_ids = []
    for audit in audits:
        audit = _object(audit, 'each audit')
        run_ids.append(_identifier(audit.get('runID'), 'audit runID'))
        metrics = _object(audit.get('metrics'), 'audit metrics')
        tools = _array(metrics.get('tools', []), 'audit metrics.tools')
        for tool in tools:
            tool = _object(tool, 'each audit tool')
            _optional_string(tool, 'name', 'audit tool name')
            _optional_string(tool, 'status', 'audit tool status')
            diagnostic = tool.get('diagnostic')
            if diagnostic is not None:
                diagnostic = _object(diagnostic, 'audit tool diagnostic')
                _optional_string(diagnostic, 'category', 'audit diagnostic category')
                _optional_string(diagnostic, 'code', 'audit diagnostic code')
        stats = metrics.get('modelCallStats')
        if stats is not None:
            stats = _object(stats, 'audit modelCallStats')
            if 'retryAttempts' in stats:
                _counter(stats, 'retryAttempts', 'model retryAttempts')
    if len(run_ids) != len(set(run_ids)):
        raise InputError('audit runID values must be unique')
    return run_ids


def _validate_metrics(metrics):
    metrics = _object(metrics, 'metrics')
    rows = _array(metrics.get('rows'), 'metrics rows')
    run_ids = []
    for row in rows:
        row = _object(row, 'each metrics row')
        run_ids.append(_identifier(row.get('run'), 'metrics row run'))
        _counter(row, 'failedItems', 'metrics failedItems')
        _counter(row, 'recoveredItems', 'metrics recoveredItems')
    if len(run_ids) != len(set(run_ids)):
        raise InputError('metrics row run values must be unique')
    return run_ids


def _validate_timings(timings):
    timings = _object(timings, 'tool timings')
    calls = _array(timings.get('calls'), 'tool timings calls')
    identities = []
    for call in calls:
        call = _object(call, 'each timing call')
        run_id = _identifier(call.get('runID'), 'timing runID')
        call_id = _identifier(call.get('callID'), 'timing callID')
        identities.append((run_id, call_id))
        _identifier(call.get('toolName'), 'timing toolName')
        payload = call.get('payload')
        if payload is not None:
            payload = _object(payload, 'timing payload')
        else:
            payload = call
        failed = payload.get('failed')
        if failed is not None and type(failed) is not bool:
            raise InputError('timing failed flag must be a boolean')
        _optional_string(payload, 'disposition', 'timing disposition')
        for key in ('admittedAt', 'finishedAt'):
            value = call.get(key)
            if value is None:
                continue
            if not isinstance(value, str) or not value.strip():
                raise InputError('timing timestamps must be nonempty strings')
            try:
                timestamp(value)
            except (AttributeError, TypeError, ValueError, OverflowError):
                raise InputError('timing timestamps must be valid and include a timezone') from None
    if len(identities) != len(set(identities)):
        raise InputError('timing runID and callID pairs must be unique')
    return calls


def _validate_acceptance(acceptance):
    acceptance = _object(acceptance, 'acceptance manifest')
    cases = _array(acceptance.get('cases'), 'acceptance cases')
    if not cases:
        raise InputError('acceptance cases must not be empty')
    ids = []
    for case in cases:
        case = _object(case, 'each acceptance case')
        ids.append(_identifier(case.get('id'), 'acceptance case id'))
        status = case.get('status', 'unverified')
        if not isinstance(status, str) or status not in ('passed', 'failed', 'unverified'):
            raise InputError('acceptance case status is invalid')
        evidence = case.get('evidence', [])
        evidence = _array(evidence, 'acceptance case evidence')
        for item in evidence:
            if not isinstance(item, str) or not item.strip():
                raise InputError('acceptance evidence paths must be nonempty strings')
    if len(ids) != len(set(ids)):
        raise InputError('acceptance case IDs must be unique')
    return cases


def summarize(audits, metrics, timings, acceptance=None, acceptance_base=None):
    runs = _validate_audits(audits)
    metric_runs = _validate_metrics(metrics)
    calls = _validate_timings(timings)
    cases = _validate_acceptance(acceptance) if acceptance is not None else None
    run_set = set(runs)
    if set(metric_runs) != run_set:
        raise ValueError('audit and UI metrics must describe the same distinct runs')
    if any(c['runID'] not in run_set for c in calls):
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
                        'possiblyTruncatedRuns': [a['runID'] for a in audits if len(a['metrics'].get('tools', [])) >= AUDIT_TOOL_CAP],
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
    if cases is not None:
        gaps = []
        for case in cases:
            status = case.get('status', 'unverified')
            evidence = case.get('evidence', [])
            found, missing = _evidence_files(evidence, acceptance_base or Path.cwd())
            if status != 'passed' or not evidence or found == 0 or missing:
                if status == 'passed':
                    reason = 'Passing case has no available nonempty evidence files'
                    if found and missing:
                        reason = 'Some evidence files are unavailable'
                else:
                    reason = 'Case is marked ' + status + ' in the acceptance manifest'
                gaps.append({'id': case['id'], 'status': status if status != 'passed' else 'unverified',
                             'evidenceFilesFound': found, 'evidenceFilesMissing': missing,
                             'reason': reason})
        result['acceptance'] = {
            'declaredCases': len(cases), 'fullyVerified': not gaps, 'gaps': gaps,
            'limitation': 'Evidence checks only confirm that referenced files exist and are nonempty; they do not verify file contents or establish semantic acceptance.',
        }
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('--acceptance', type=Path)
    parser.add_argument('--require-acceptance', action='store_true')
    args = parser.parse_args(argv)
    if args.require_acceptance and args.acceptance is None:
        parser.error('--require-acceptance requires --acceptance')
    def read(path, label):
        try:
            return json.loads(path.read_text())
        except (OSError, UnicodeError, json.JSONDecodeError, RecursionError):
            raise InputError('cannot read ' + label + ' as JSON') from None
    try:
        audits = read(args.directory / 'audits.json', 'audits.json')
        metrics = read(args.directory / 'metrics.json', 'metrics.json')
        timings = read(args.directory / 'tool-timings.json', 'tool-timings.json')
        acceptance = read(args.acceptance, 'acceptance manifest') if args.acceptance else None
        if args.acceptance and acceptance is None:
            raise InputError('acceptance manifest must be an object')
        result = summarize(audits, metrics, timings, acceptance,
                           args.acceptance.parent if args.acceptance else None)
    except InputError as error:
        parser.error(str(error))
    except (KeyError, TypeError, ValueError, OverflowError, RecursionError):
        parser.error('invalid harness input')
    print(json.dumps(result, indent=2))
    return int(args.require_acceptance and not result['acceptance']['fullyVerified'])


if __name__ == '__main__':
    raise SystemExit(main())
