import importlib.util
from pathlib import Path
import subprocess
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
