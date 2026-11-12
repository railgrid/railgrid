import ctypes
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('writer', Path(__file__).with_name('write-kind-kubeconfig.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

# inotify event bits (linux/inotify.h). IN_MODIFY is left out on purpose: the
# writer never edits in place, and a completed write shows up as CLOSE_WRITE.
IN_ATTRIB = 0x4
IN_CLOSE_WRITE = 0x8
IN_DELETE_SELF = 0x400
IN_MOVE_SELF = 0x800


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

    def test_unchanged_private_config_keeps_inode_metadata(self):
        # Runs on every platform: a no-op chmod or utime still bumps ctime, and
        # macOS FSEvents reports that just as inotify reports IN_ATTRIB.
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'config'
            path.write_bytes(b'working')
            path.chmod(0o600)
            before = path.stat()
            time.sleep(0.05)  # past the filesystem's timestamp granularity
            with patch.object(m.subprocess, 'check_output', return_value=b'working'):
                m.refresh('owned', path)
            after = path.stat()
            self.assertEqual((after.st_ino, after.st_ctime_ns, after.st_mtime_ns),
                             (before.st_ino, before.st_ctime_ns, before.st_mtime_ns))
            self.assertEqual(list(Path(tmp).iterdir()), [path])

    @unittest.skipUnless(sys.platform == 'linux', 'inotify is Linux-only')
    def test_unchanged_private_config_emits_no_watch_event(self):
        libc = ctypes.CDLL(None, use_errno=True)
        libc.inotify_init1.argtypes = [ctypes.c_int]
        libc.inotify_init1.restype = ctypes.c_int
        libc.inotify_add_watch.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_uint32]
        libc.inotify_add_watch.restype = ctypes.c_int
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'config'
            path.write_bytes(b'working')
            path.chmod(0o600)
            fd = libc.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
            self.assertGreaterEqual(fd, 0, f'inotify_init1: {os.strerror(ctypes.get_errno())}')
            try:
                mask = IN_ATTRIB | IN_CLOSE_WRITE | IN_MOVE_SELF | IN_DELETE_SELF
                watch = libc.inotify_add_watch(fd, os.fsencode(path), mask)
                self.assertGreaterEqual(watch, 0, f'inotify_add_watch: {os.strerror(ctypes.get_errno())}')
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
