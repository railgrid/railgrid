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

import ast
from contextlib import redirect_stderr, redirect_stdout
import importlib.util
import io
import json
import os
import sys
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True

spec = importlib.util.spec_from_file_location('health', Path(__file__).with_name('tilt-host-health.py'))
health = importlib.util.module_from_spec(spec)
spec.loader.exec_module(health)


class HostHealthTest(unittest.TestCase):
    def test_tilt_registers_health_probe_and_only_opts_explicit_logs_into_rotation(self):
        tilt_path = Path(__file__).resolve().parents[2] / 'Tiltfile'
        source = tilt_path.read_text()
        tree = ast.parse(source)
        config_index = next(
            index for index, node in enumerate(tree.body)
            if isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
            and isinstance(node.value.func, ast.Attribute)
            and isinstance(node.value.func.value, ast.Name)
            and node.value.func.value.id == 'config'
            and node.value.func.attr == 'define_string'
            and node.value.args and ast.literal_eval(node.value.args[0]) == 'host-log-path'
        )
        parse_index = next(
            index for index, node in enumerate(tree.body)
            if isinstance(node, ast.Assign) and any(
                isinstance(target, ast.Name) and target.id == 'cfg' for target in node.targets
            )
        )
        self.assertLess(config_index, parse_index)
        self.assertLess(source.index("config.define_string('host-log-path'"),
                        source.index('# Run kcp from a published image'))

        resource = next(
            node.value for node in tree.body
            if isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
            and isinstance(node.value.func, ast.Name) and node.value.func.id == 'local_resource'
            and node.value.args and ast.literal_eval(node.value.args[0]) == 'host-health'
        )
        fields = {keyword.arg: keyword.value for keyword in resource.keywords}
        self.assertEqual(ast.unparse(fields['serve_cmd']), 'host_health_args')
        self.assertEqual(ast.literal_eval(fields['deps']), ['hack/scripts/tilt-host-health.py'])
        self.assertIn("exec_action(host_health_args + ['--check'])",
                      ast.unparse(fields['readiness_probe']))
        self.assertIn("os.getenv('GOTMPDIR', '')", source)
        self.assertIn("host_build_tmp != 'off'", source)
        log_path = next(
            node.value for node in tree.body
            if isinstance(node, ast.Assign) and any(
                isinstance(target, ast.Name) and target.id == 'host_log_path'
                for target in node.targets
            )
        )
        self.assertIn("cfg.get('host-log-path', '')", ast.unparse(log_path))
        self.assertIn("os.getenv('TILT_LOG_PATH', '')", ast.unparse(log_path))

    def test_explicit_paths_do_not_hide_effective_temp_and_cache_filesystems(self):
        with patch.dict(os.environ, {'GOTMPDIR': '/go-temp', 'TMPDIR': '/os-temp',
                                     'GOCACHE': '/go-cache'}, clear=True):
            with patch.object(health.tempfile, 'gettempdir', return_value='/os-temp'):
                self.assertEqual(health.monitored_paths(['/repo', '/go-temp']),
                                 ['/repo', '/go-temp', '/os-temp', '/go-cache'])
        with patch.dict(os.environ, {}, clear=True):
            with patch.object(health.tempfile, 'gettempdir', return_value='/default-temp'):
                self.assertEqual(health.monitored_paths(['/repo']), ['/repo', '/default-temp'])
        with patch.dict(os.environ, {'GOTMPDIR': 'off'}, clear=True):
            with patch.object(health.tempfile, 'gettempdir', return_value='/default-temp'):
                self.assertEqual(health.monitored_paths(['/repo']), ['/repo', '/default-temp'])

    def test_open_writer_survives_rotation_and_sparse_prefix_does_not_loop(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'tilt.log'
            with path.open('wb') as writer:
                writer.write(b'a' * 65536 + b'diagnostic-tail')
                writer.flush()
                self.assertEqual(health.rotate_log(path, limit=32768, keep=15), 15)
                self.assertEqual(path.with_name('tilt.log.previous').read_bytes(), b'diagnostic-tail')
                self.assertLessEqual(path.with_name('tilt.log.previous').stat().st_size,
                                     health.MAX_RETAINED_TAIL)
                writer.write(b'still-running')
                writer.flush()
                self.assertGreater(path.stat().st_size, 65536)
                self.assertIsNone(health.rotate_log(path, limit=32768, keep=15))
                self.assertTrue(path.read_bytes().endswith(b'still-running'))

    def test_rotation_event_reports_short_tail_and_zero_byte_tail(self):
        class StopLoop(Exception):
            pass

        with tempfile.TemporaryDirectory() as tmp:
            short_log = Path(tmp) / 'short.log'
            short_contents = b'short-tail'
            short_log.write_bytes(short_contents)
            empty_tail_log = Path(tmp) / 'empty-tail.log'
            empty_tail_log.write_bytes(b'x' * 8192)
            original_rotate = health.rotate_log

            def rotate_for_test(path):
                if Path(path) == short_log:
                    return original_rotate(path, limit=1, keep=64)
                return original_rotate(path, limit=1, keep=0)

            output = io.StringIO()
            with patch.object(sys, 'argv', [
                    'tilt-host-health.py', '--path', tmp,
                    '--log', str(short_log), '--log', str(empty_tail_log)]), \
                    patch.object(health, 'rotate_log', side_effect=rotate_for_test), \
                    patch.object(health, 'check_disk', return_value=[]), \
                    patch.object(health.time, 'sleep', side_effect=StopLoop), \
                    redirect_stdout(output):
                with self.assertRaises(StopLoop):
                    health.main()

            events = [json.loads(line) for line in output.getvalue().splitlines()]
            rotations = {event['path']: event['retainedTailBytes'] for event in events
                         if event['event'] == 'tilt_log_rotated'}
            self.assertEqual(rotations, {str(short_log): len(short_contents),
                                         str(empty_tail_log): 0})
            self.assertEqual(short_log.with_name('short.log.previous').read_bytes(),
                             short_contents)
            self.assertEqual(empty_tail_log.with_name('empty-tail.log.previous').read_bytes(),
                             b'')

    def test_retained_diagnostic_tail_has_a_fixed_upper_bound(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'tilt.log'
            path.write_bytes(b'x' * (health.MAX_RETAINED_TAIL + 1024))
            self.assertEqual(health.rotate_log(path, limit=1), health.MAX_RETAINED_TAIL)
            self.assertEqual(path.with_name('tilt.log.previous').stat().st_size,
                             health.MAX_RETAINED_TAIL)
            with self.assertRaisesRegex(ValueError, 'managed log tail'):
                health.rotate_log(path, limit=1, keep=health.MAX_RETAINED_TAIL + 1)

    def test_does_not_follow_input_or_backup_symlinks(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            data = directory / 'database'; data.write_bytes(b'preserve')
            log = directory / 'tilt.log'; log.symlink_to(data)
            with self.assertRaises(OSError):
                health.rotate_log(log, limit=1)
            log.unlink(); log.write_bytes(b'x' * 65536)
            log.with_name('tilt.log.previous').symlink_to(data)
            self.assertEqual(health.rotate_log(log, limit=32768, keep=16), 16)
            self.assertEqual(data.read_bytes(), b'preserve')
            self.assertEqual(log.with_name('tilt.log.previous').stat().st_mode & 0o777, 0o600)

    def test_does_not_truncate_a_hardlinked_log(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            log = directory / 'tilt.log'
            alias = directory / 'kept-data'
            log.write_bytes(b'x' * 65536)
            os.link(log, alias)
            with self.assertRaisesRegex(ValueError, 'hard links'):
                health.rotate_log(log, limit=32768)
            self.assertEqual(alias.stat().st_size, 65536)

    def test_missing_log_and_low_disk_probe_do_not_mutate_data(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertIsNone(health.rotate_log(Path(tmp) / 'missing'))
            self.assertEqual(health.check_disk([tmp], minimum=1), [])
            self.assertEqual(len(health.check_disk([tmp], minimum=2**80)), 1)
            self.assertEqual(list(Path(tmp).iterdir()), [])

    def test_readiness_check_does_not_rotate_an_explicit_log(self):
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp) / 'tilt.log'
            contents = b'x' * 65536
            log.write_bytes(contents)
            with patch.object(sys, 'argv', [
                    'tilt-host-health.py', '--path', tmp, '--log', str(log), '--check']), \
                    patch.object(health, 'check_disk', return_value=[]):
                self.assertEqual(health.main(), 0)
            self.assertEqual(log.read_bytes(), contents)
            self.assertFalse(log.with_name('tilt.log.previous').exists())

    def test_one_unavailable_path_is_reported_without_skipping_other_paths(self):
        with tempfile.TemporaryDirectory() as tmp:
            missing = Path(tmp) / 'missing'
            result = health.check_disk([tmp, missing], minimum=1)
            self.assertEqual(len(result), 1)
            self.assertEqual(result[0]['path'], str(missing))
            self.assertIn('error', result[0])

    def test_nonfinite_minimum_is_rejected(self):
        for value in ('nan', 'inf', '1e308'):
            with self.subTest(value=value), patch.object(sys, 'argv', ['tilt-host-health.py', '--min-free-gib', value]), redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as raised:
                    health.main()
                self.assertEqual(raised.exception.code, 2)

    def test_log_rotation_errors_do_not_stop_monitor_loop(self):
        class StopLoop(Exception):
            pass

        output = io.StringIO()
        with patch.object(sys, 'argv', ['tilt-host-health.py', '--path', '.repo', '--log', 'tilt.log']), \
                patch.object(health, 'rotate_log', side_effect=PermissionError('permission denied')), \
                patch.object(health, 'check_disk', return_value=[]), \
                patch.object(health.time, 'sleep', side_effect=StopLoop), \
                redirect_stdout(output):
            with self.assertRaises(StopLoop):
                health.main()
        events = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertTrue(any(event['event'] == 'tilt_log_rotation_error' for event in events))
        self.assertTrue(any(event['event'] == 'host_disk_ready' for event in events))


if __name__ == '__main__':
    unittest.main()
