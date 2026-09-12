# Copyright 2026 The Faros Authors.
import hashlib
import importlib.util
import json
from pathlib import Path
import platform
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('manage', Path(__file__).with_name('manage.py'))
manage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(manage)


class InstallTest(unittest.TestCase):
    def test_upgrade_rejection_and_rollback_selection(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine(), platform.machine())
            def binary(name, protocol='runner/v1'):
                data = {'version': name, 'commit': name, 'protocolVersion': protocol,
                        'os': platform.system().lower(), 'arch': arch}
                source = root / name
                source.write_text('#!/bin/sh\ncat <<\'EOF\'\n' + json.dumps(data) + '\nEOF\n')
                return source, hashlib.sha256(source.read_bytes()).hexdigest()
            first, sha1 = binary('first')
            manage.install(root, {}, first, sha1)
            state = json.loads((root / 'selection.json').read_text())
            second, sha2 = binary('second')
            with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
                manage.install(root, state, second, '0' * 64)
            bad, badsha = binary('bad', 'runner/v99')
            with self.assertRaisesRegex(ValueError, 'protocol'):
                manage.install(root, state, bad, badsha)
            self.assertEqual(json.loads((root / 'selection.json').read_text()), state)
            manage.install(root, state, second, sha2)
            state = json.loads((root / 'selection.json').read_text())
            self.assertEqual(state, {'current': sha2, 'previous': sha1})
            self.assertEqual(manage.metadata(manage.selected(root, state, 'previous'))['version'], 'first')
            import subprocess
            import sys
            subprocess.run([sys.executable, str(Path(__file__).with_name('manage.py')),
                            '--root', directory, 'rollback'], check=True, capture_output=True)
            rolled = json.loads((root / 'selection.json').read_text())
            self.assertEqual(rolled, {'current': sha1, 'previous': sha2})
            manage.selected(root, state).write_text('corrupted')
            with self.assertRaisesRegex(ValueError, 'checksum'):
                manage.selected(root, state)

class ManagerCLITest(unittest.TestCase):
    def test_lock_rejects_install_before_reading_binary(self):
        import fcntl
        import subprocess
        import sys
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with (root / '.lock').open('a') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                result = subprocess.run([sys.executable, str(Path(__file__).with_name('manage.py')),
                                         '--root', directory, 'install', '--binary', '/missing',
                                         '--sha256', '0' * 64], capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('manager is busy', result.stderr)
                self.assertFalse((root / 'selection.json').exists())

    def test_managed_run_holds_lock_across_exec(self):
        import subprocess
        import sys
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(platform.machine(), platform.machine())
            info = {'version': 'test', 'commit': 'test', 'protocolVersion': 'runner/v1',
                    'os': platform.system().lower(), 'arch': arch}
            binary = root / 'source'
            binary.write_text('#!' + sys.executable + '\nimport sys,time\n'
                              'if sys.argv[1:] == ["--version"]: print(' + repr(json.dumps(info)) + ')\n'
                              'else:\n print("started", flush=True)\n time.sleep(20)\n')
            sha = hashlib.sha256(binary.read_bytes()).hexdigest()
            manage.install(root, {}, binary, sha)
            command = [sys.executable, str(Path(__file__).with_name('manage.py')), '--root', directory]
            child = subprocess.Popen(command + ['run'], stdout=subprocess.PIPE, text=True)
            try:
                import select
                self.assertTrue(select.select([child.stdout], [], [], 5)[0], 'runner did not start')
                self.assertEqual(child.stdout.readline().strip(), 'started')
                result = subprocess.run(command + ['rollback'], capture_output=True, text=True)
                self.assertIn('manager is busy', result.stderr)
            finally:
                child.terminate()
                child.wait(timeout=5)
                child.stdout.close()
            result = subprocess.run(command + ['status'], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)


class PackageTest(unittest.TestCase):
    def test_archives_contain_only_runner_manager_and_pinned_installer(self):
        import subprocess
        import sys
        import tarfile
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for arch in ('arm64', 'amd64'):
                (root / ('faros-runner-darwin-' + arch)).write_bytes(arch.encode())
            subprocess.run([sys.executable, str(Path(__file__).with_name('package.py')), directory],
                           check=True, capture_output=True)
            for arch in ('arm64', 'amd64'):
                archive = root / ('faros-runner-macos-' + arch + '.tar')
                checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
                self.assertEqual(archive.with_suffix('.tar.sha256').read_text(), checksum + '  ' + archive.name + '\n')
                with tarfile.open(archive, 'r:') as bundle:
                    self.assertEqual(set(bundle.getnames()), {
                        'faros-runner-install/faros-runner', 'faros-runner-install/manage.py',
                        'faros-runner-install/install.sh'})
                    script = bundle.extractfile('faros-runner-install/install.sh').read().decode()
                    self.assertIn(hashlib.sha256(arch.encode()).hexdigest(), script)


if __name__ == '__main__':
    unittest.main()
