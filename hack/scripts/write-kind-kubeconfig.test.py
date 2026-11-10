import ctypes
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('writer', Path(__file__).with_name('write-kind-kubeconfig.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class KubeconfigTest(unittest.TestCase):
    def test_refresh_preserves_unchanged_file_and_replaces_changed_content(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'config'
            with patch.object(m.subprocess, 'check_output', return_value=b'first'):
                m.refresh('owned', path)
                identity = (path.stat().st_ino, path.stat().st_mtime_ns)
                m.refresh('owned', path)
                self.assertEqual((path.stat().st_ino, path.stat().st_mtime_ns), identity)
            with patch.object(m.subprocess, 'check_output', return_value=b'second'):
                m.refresh('owned', path)
            self.assertEqual(path.read_bytes(), b'second')
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    @unittest.skipUnless(sys.platform == 'linux', 'Linux file watcher regression')
    def test_unchanged_private_config_emits_no_watch_event(self):
        libc = ctypes.CDLL(None, use_errno=True)
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'config'
            path.write_bytes(b'working')
            path.chmod(0o600)
            fd = libc.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
            self.assertGreaterEqual(fd, 0)
            try:
                # Attribute changes, completed writes, replacement or removal.
                watch = libc.inotify_add_watch(fd, os.fsencode(path), 0x4 | 0x8 | 0x800 | 0x400)
                self.assertGreaterEqual(watch, 0)
                with patch.object(m.subprocess, 'check_output', return_value=b'working'):
                    m.refresh('owned', path)
                with self.assertRaises(BlockingIOError):
                    os.read(fd, 4096)
            finally:
                os.close(fd)

    def test_unchanged_content_still_repairs_permissions(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'config'
            path.write_bytes(b'working')
            for mode in (0o644, 0o4600):
                path.chmod(mode)
                with patch.object(m.subprocess, 'check_output', return_value=b'working'):
                    m.refresh('owned', path)
                self.assertEqual(path.stat().st_mode & 0o7777, 0o600)
                self.assertEqual(path.read_bytes(), b'working')

    def test_usage_error_has_no_traceback(self):
        result = subprocess.run([sys.executable, str(Path(m.__file__))], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('usage:', result.stderr)
        self.assertNotIn('Traceback', result.stderr)

    def test_failed_sync_keeps_existing_kubeconfig(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'config'
            path.write_bytes(b'working')
            with patch.object(m.subprocess, 'check_output', return_value=b'new'), \
                    patch.object(m.os, 'fsync', side_effect=OSError('disk error')):
                with self.assertRaises(OSError):
                    m.refresh('owned', path)
            self.assertEqual(path.read_bytes(), b'working')
            self.assertEqual(list(Path(tmp).iterdir()), [path])

    def test_failed_export_keeps_existing_kubeconfig(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'config'
            path.write_bytes(b'working')
            with patch.object(m.subprocess, 'check_output', side_effect=subprocess.CalledProcessError(1, ['kind'])):
                with self.assertRaises(subprocess.CalledProcessError):
                    m.refresh('owned', path)
            self.assertEqual(path.read_bytes(), b'working')
            with patch.object(m.subprocess, 'check_output', return_value=b''):
                with self.assertRaises(ValueError):
                    m.refresh('owned', path)
            self.assertEqual(path.read_bytes(), b'working')


if __name__ == '__main__':
    unittest.main()
