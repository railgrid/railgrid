"""Per-checkout identities must never reuse another stack's host resources."""
import contextlib
import io
from concurrent.futures import ThreadPoolExecutor
import importlib.util
import json
import os
import signal
import subprocess
from pathlib import Path
import tempfile
import sys
sys.dont_write_bytecode = True
import unittest
from unittest.mock import patch, Mock

spec = importlib.util.spec_from_file_location('checkout', Path(__file__).with_name('tilt-checkout.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class CheckoutTest(unittest.TestCase):
    def capture_make_tilt(self, root, env_contents, make_values=None, inherited=None):
        """Capture the environment of `make tilt` without running the launcher."""
        root = Path(root)
        env_file = root / '.env'
        env_file.write_text(env_contents)
        fake_bin = root / 'fake-bin'
        fake_bin.mkdir(exist_ok=True)
        capture = root / 'make-tilt-env.json'
        fake_python = fake_bin / 'python3'
        captured_names = [
            'RAILGRID_APP_BASE_DOMAIN',
            'RAILGRID_APP_PUBLIC_PORT',
            'RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE',
            'RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE',
        ]
        fake_python.write_text(
            '#!' + sys.executable + '\n'
            'import json, os, sys\n'
            'from pathlib import Path\n'
            'names = ' + repr(captured_names) + '\n'
            "payload = {'argv': sys.argv[1:], 'env': {name: os.environ.get(name) for name in names}}\n"
            "Path(os.environ['TILT_MAKE_CAPTURE']).write_text(json.dumps(payload))\n"
        )
        fake_python.chmod(0o755)

        command = [
            'make', '--no-print-directory', '-f', str(m.ROOT / 'Makefile'), 'tilt',
            'ENV_FILES=' + str(env_file),
        ]
        command.extend(name + '=' + value for name, value in (make_values or {}).items())
        env = dict(os.environ)
        for name in (
                'MAKEFLAGS', 'MFLAGS', 'GNUMAKEFLAGS', 'MAKELEVEL',
                'RAILGRID_APP_BASE_DOMAIN', 'RAILGRID_APP_PUBLIC_PORT',
                'RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE',
                'RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'):
            env.pop(name, None)
        env['PATH'] = str(fake_bin) + os.pathsep + env.get('PATH', '')
        env['TILT_MAKE_CAPTURE'] = str(capture)
        if inherited:
            env.update(inherited)
        result = subprocess.run(command, cwd=m.ROOT, env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        payload = json.loads(capture.read_text())
        self.assertEqual(payload['argv'], ['hack/scripts/tilt-checkout.py'])
        return payload['env']

    def test_attached_launch_runs_tilt_in_the_terminal_session_and_cleans_up(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(m, 'ROOT', Path(tmp)):
            root = Path(tmp)
            (root / '.kcp').mkdir()
            config = {'root': tmp, 'key': 'first', 'ports': m.PORTS}
            child = Mock(pid=1234)
            child.wait.return_value = 3
            with patch.object(m, 'settings', return_value=config), patch.object(m, 'manifests', return_value={}), \
                    patch.object(m, 'check_ports'), patch.object(m, 'process_identity', return_value='birth'), \
                    patch.object(m.signal, 'signal'), patch.object(m, 'release_checkout') as release, \
                    patch.object(m.subprocess, 'Popen', return_value=child) as launch, \
                    patch.object(m.sys, 'argv', ['tilt-checkout.py']):
                with self.assertRaises(SystemExit) as raised:
                    m.main()
            self.assertEqual(raised.exception.code, 3)
            args = launch.call_args.kwargs
            # Tilt's HUD needs the controlling terminal and Ctrl-C must reach it:
            # no new session, no redirected stdio.
            self.assertNotIn('start_new_session', args)
            self.assertNotIn('stdin', args)
            # Owner was recorded before launch, then cleanup ran after exit.
            release.assert_called_once_with(config, root / '.kcp/tilt-owner.json')

    def test_detached_launch_returns_with_private_log_and_durable_owner(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(m, 'ROOT', Path(tmp)):
            root = Path(tmp)
            (root / '.kcp').mkdir()
            config = {'root': tmp, 'key': 'first', 'ports': m.PORTS}
            child = Mock(pid=1234)
            child.wait.side_effect = subprocess.TimeoutExpired("make", 3)
            with patch.dict(os.environ, {'TILT_DETACHED': '1'}), patch.object(m, 'settings', return_value=config), \
                    patch.object(m, 'manifests', return_value={}), patch.object(m, 'check_ports'), \
                    patch.object(m, 'process_identity', return_value='birth'), patch.object(m.signal, 'signal'), \
                    patch.object(m, 'release_checkout') as release, \
                    patch.object(m.subprocess, 'Popen', return_value=child) as launch, patch.object(m.sys, 'argv', ['tilt-checkout.py']):
                m.main()
            child.wait.assert_called_once_with(timeout=3)
            args = launch.call_args.kwargs
            self.assertTrue(args['start_new_session'])
            self.assertEqual(args['stdin'], m.subprocess.DEVNULL)
            self.assertEqual(args['stderr'], m.subprocess.STDOUT)
            self.assertEqual((root / '.kcp/tilt.log').stat().st_mode & 0o777, 0o600)
            owner = json.loads((root / '.kcp/tilt-owner.json').read_text())
            self.assertEqual((owner['pid'], owner['birth']), (1234, 'birth'))
            # A detached stack is not cleaned by the launcher that returns.
            release.assert_not_called()

    def test_detached_launch_reports_early_exit(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(m, 'ROOT', Path(tmp)):
            (Path(tmp) / '.kcp').mkdir()
            config = {'root': tmp, 'key': 'first', 'ports': m.PORTS}
            child = Mock(pid=1234, returncode=2)
            with patch.dict(os.environ, {'TILT_DETACHED': '1'}), patch.object(m, 'settings', return_value=config), patch.object(m, 'manifests', return_value={}), patch.object(m, 'check_ports'), patch.object(m, 'process_identity', return_value='birth'), patch.object(m.signal, 'signal'), patch.object(m.subprocess, 'Popen', return_value=child), patch.object(m.sys, 'argv', ['tilt-checkout.py']):
                with self.assertRaisesRegex(ValueError, 'status 2'):
                    m.main()
            self.assertTrue((Path(tmp) / '.kcp/tilt-owner.json').exists())

    def test_leftovers_of_a_stopped_stack_are_reaped_before_the_port_check(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(m, 'ROOT', Path(tmp)):
            root = Path(tmp)
            (root / '.kcp').mkdir()
            config = {'root': tmp, 'key': 'first', 'ports': m.PORTS}
            record = root / '.kcp/tilt-owner.json'
            record.write_text(json.dumps({'root': tmp, 'pid': 4321, 'birth': 'gone',
                                          'resources': m.owned_resources(config)}))
            order = []
            child = Mock(pid=1234)
            child.wait.return_value = 0
            identities = {4321: None, 1234: 'birth'}
            with patch.object(m, 'settings', return_value=config), patch.object(m, 'manifests', return_value={}), \
                    patch.object(m, 'check_ports', side_effect=lambda c: order.append('check')), \
                    patch.object(m, 'process_identity', side_effect=lambda pid: identities.get(pid)), \
                    patch.object(m.signal, 'signal'), \
                    patch.object(m, 'release_checkout', side_effect=lambda *a, **k: order.append('release')), \
                    patch.object(m.subprocess, 'Popen', return_value=child), patch.object(m.sys, 'argv', ['tilt-checkout.py']):
                with self.assertRaises(SystemExit):
                    m.main()
            # Stale record reaped first, then the port check, then exit cleanup.
            self.assertEqual(order, ['release', 'check', 'release'])

    def test_release_removes_owned_containers_and_stack_listeners_only(self):
        config = {'root': '/checkout', 'key': 'abc', 'ports': {k: 20000 + i for i, k in enumerate(m.PORTS)}}
        ssh = m.owned_resources(config)['containers'][1]
        removed, killed = [], []
        def run(args, **kwargs):
            if args[:3] == ['docker', 'rm', '-f']:
                removed.append(args[3])
            return Mock(returncode=0, stdout='')
        listeners = {config['ports']['HUB_PORT']: {4242, 4343}}
        with tempfile.TemporaryDirectory() as tmp:
            record = Path(tmp) / 'tilt-owner.json'
            record.write_text('{}')
            with patch.object(m.subprocess, 'check_output', return_value=ssh + '\nother\n'), \
                    patch.object(m.subprocess, 'run', side_effect=run), \
                    patch.object(m, 'port_listeners', side_effect=lambda port: listeners.get(port, set())), \
                    patch.object(m, 'is_container_runtime', side_effect=lambda pid: pid == 4343), \
                    patch.object(m, 'process_identity', return_value=None), \
                    patch.object(m.os, 'kill', side_effect=lambda pid, sig: killed.append((pid, sig))):
                m.release_checkout(config, record)
            self.assertEqual(removed, [ssh])
            self.assertEqual(killed, [(4242, m.signal.SIGTERM)])
            self.assertFalse(record.exists())

    def test_existing_claim_is_kept_and_new_keys_filled(self):
        with tempfile.TemporaryDirectory() as root, patch.object(m, 'ephemeral_range', return_value=(20000, 20009)):
            registry = m.registry_directory() / 'tilt-ports.json'
            registry.parent.mkdir(parents=True, exist_ok=True)
            # A checkout that predates per-checkout allocation keeps its fixed
            # ports: its URLs must not move under it.
            fixed = {k: v for k, v in m.PORTS.items() if not k.startswith('METERING_')}
            registry.write_text(json.dumps({root: fixed}))
            ports = m.allocate_ports(Path(root))
            self.assertEqual({k: ports[k] for k in fixed}, fixed)
            for key in ('METERING_PORT', 'METERING_WEBHOOK_PORT', 'METERING_AUDIT_PORT'):
                self.assertIn(key, ports)
                self.assertNotIn(ports[key], fixed.values())
            self.assertEqual(m.allocate_ports(Path(root)), ports)

    def setUp(self):
        self.registry_directory = m.registry_directory
        state = tempfile.TemporaryDirectory()
        self.addCleanup(state.cleanup)
        env = patch.object(m, 'registry_directory', return_value=Path(state.name) / 'railgrid')
        env.start()
        self.addCleanup(env.stop)

    def test_registry_ignores_harness_home_and_xdg_overrides(self):
        # The helper itself must use the account identity, even when the
        # runner deliberately moves HOME and XDG state into its own area.
        account_home = Path(m.pwd.getpwuid(m.os.getuid()).pw_dir)
        with patch.dict(m.os.environ, {'HOME': '/private-runner-home', 'XDG_STATE_HOME': '/private-runner-state'}):
            self.assertEqual(self.registry_directory(), account_home / '.local/state/railgrid')

    def test_registry_reserves_stopped_checkouts_and_serializes_allocations(self):
        with tempfile.TemporaryDirectory() as a, tempfile.TemporaryDirectory() as b:
            with patch.object(m, 'ephemeral_range', return_value=(20000, 20009)):
                with ThreadPoolExecutor(max_workers=2) as pool:
                    ca, cb = list(pool.map(m.settings, [Path(a), Path(b)]))
            self.assertFalse(set(ca['ports'].values()) & set(cb['ports'].values()))
            self.assertTrue(all(20010 <= port < 30000 for port in ca['ports'].values()))
            self.assertEqual(m.settings(Path(a))['ports'], ca['ports'])
            self.assertEqual(m.settings(Path(b))['ports'], cb['ports'])
            with tempfile.TemporaryDirectory() as c:
                cc = m.settings(Path(c))
                self.assertFalse(set(cc['ports'].values()) & set(ca['ports'].values()))

    def test_allocation_fails_if_dedicated_range_is_ephemeral(self):
        with tempfile.TemporaryDirectory() as root, \
                patch.object(m, 'ephemeral_range', return_value=(20000, 29999)):
            with self.assertRaisesRegex(ValueError, 'outside the host ephemeral range'):
                m.settings(Path(root))

    def test_buildvcs_override_only_applies_to_worktrees(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            self.assertNotIn('GOFLAGS', m.environment(config))
            (Path(root) / '.git').write_text('gitdir: /example')
            with patch.dict(m.os.environ, {'GOFLAGS': '-mod=readonly'}):
                self.assertEqual(m.environment(config)['GOFLAGS'], '-mod=readonly -buildvcs=false')

    def test_preview_settings_default_to_checkout_domain_and_gateway_port(self):
        with tempfile.TemporaryDirectory() as root, patch.dict(m.os.environ, {}, clear=True):
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            env = m.environment(config)
            self.assertEqual(env['RAILGRID_APP_BASE_DOMAIN'], 'apps-abc.127.0.0.1.sslip.io')
            self.assertEqual(env['RAILGRID_APP_PUBLIC_PORT'], str(m.PORTS['PREVIEW_GATEWAY_PORT']))
            self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'default')
            self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'default')

    def test_preview_settings_use_shell_values_ahead_of_root_env(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            (Path(root) / '.env').write_text(
                'RAILGRID_APP_BASE_DOMAIN=apps.file.example\n'
                'RAILGRID_APP_PUBLIC_PORT=443\n'
            )
            with patch.dict(m.os.environ, {
                    'RAILGRID_APP_BASE_DOMAIN': 'apps.shell.example',
                    'RAILGRID_APP_PUBLIC_PORT': '',
            }, clear=True):
                env = m.environment(config)
            self.assertEqual(env['RAILGRID_APP_BASE_DOMAIN'], 'apps.shell.example')
            self.assertEqual(env['RAILGRID_APP_PUBLIC_PORT'], '')
            self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'environment')
            self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'environment')

    def test_preview_settings_read_root_env_and_preserve_empty_public_port(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            env_file = Path(root) / '.env'
            env_file.write_text(
                'RAILGRID_APP_BASE_DOMAIN=apps.dev.example\n'
                'RAILGRID_APP_PUBLIC_PORT=\n'
            )
            with patch.dict(m.os.environ, {}, clear=True):
                env = m.environment(config)
            self.assertEqual(env['RAILGRID_APP_BASE_DOMAIN'], 'apps.dev.example')
            self.assertEqual(env['RAILGRID_APP_PUBLIC_PORT'], '')
            self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'dotenv')
            self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'dotenv')

    def test_blank_preview_domain_falls_back_and_keeps_empty_port(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            env_file = Path(root) / '.env'
            env_file.write_text(
                'RAILGRID_APP_BASE_DOMAIN=apps.file.example\n'
                'RAILGRID_APP_PUBLIC_PORT=\n'
            )
            for inherited in (
                    {'RAILGRID_APP_BASE_DOMAIN': '  \t'},
                    {'RAILGRID_APP_BASE_DOMAIN': '  \t',
                     'RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE': 'environment'},
            ):
                with self.subTest(inherited=inherited), patch.dict(m.os.environ, inherited, clear=True):
                    env = m.environment(config)
                self.assertEqual(env['RAILGRID_APP_BASE_DOMAIN'], 'apps.file.example')
                self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'dotenv')
                self.assertEqual(env['RAILGRID_APP_PUBLIC_PORT'], '')

            env_file.write_text('RAILGRID_APP_BASE_DOMAIN=  \t\nRAILGRID_APP_PUBLIC_PORT=\n')
            with patch.dict(m.os.environ, {'RAILGRID_APP_BASE_DOMAIN': '  \t'}, clear=True):
                env = m.environment(config)
            self.assertEqual(env['RAILGRID_APP_BASE_DOMAIN'], 'apps-abc.127.0.0.1.sslip.io')
            self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'default')
            self.assertEqual(env['RAILGRID_APP_PUBLIC_PORT'], '')

            with patch.dict(m.os.environ, {}, clear=True):
                env = m.environment(config)
            self.assertEqual(env['RAILGRID_APP_BASE_DOMAIN'], 'apps-abc.127.0.0.1.sslip.io')
            self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'default')

    def test_preview_dotenv_and_default_values_are_reloaded_from_source(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            env_file = Path(root) / '.env'
            env_file.write_text(
                'RAILGRID_APP_BASE_DOMAIN=apps.first.example\n'
                'RAILGRID_APP_PUBLIC_PORT=\n'
            )
            with patch.dict(m.os.environ, {}, clear=True):
                first = m.environment(config)
            with patch.dict(m.os.environ, first, clear=True):
                same = m.environment(config)
            self.assertEqual(same['RAILGRID_APP_BASE_DOMAIN'], 'apps.first.example')
            self.assertEqual(same['RAILGRID_APP_PUBLIC_PORT'], '')

            env_file.write_text('RAILGRID_APP_BASE_DOMAIN=apps.second.example\n')
            with patch.dict(m.os.environ, first, clear=True):
                changed = m.environment(config)
            self.assertEqual(changed['RAILGRID_APP_BASE_DOMAIN'], 'apps.second.example')
            self.assertEqual(changed['RAILGRID_APP_PUBLIC_PORT'], str(m.PORTS['PREVIEW_GATEWAY_PORT']))
            self.assertEqual(changed['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'default')

            env_file.unlink()
            with patch.dict(m.os.environ, changed, clear=True):
                defaulted = m.environment(config)
            self.assertEqual(defaulted['RAILGRID_APP_BASE_DOMAIN'], 'apps-abc.127.0.0.1.sslip.io')

    def test_preview_dotenv_replaces_an_unmarked_inherited_checkout_default(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            (Path(root) / '.env').write_text('RAILGRID_APP_BASE_DOMAIN=apps.dev.example\n')
            with patch.dict(m.os.environ, {
                    'RAILGRID_APP_BASE_DOMAIN': 'apps-abc.127.0.0.1.sslip.io',
            }, clear=True):
                env = m.environment(config)
            self.assertEqual(env['RAILGRID_APP_BASE_DOMAIN'], 'apps.dev.example')
            self.assertEqual(env['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'dotenv')

    def test_supported_make_tilt_preserves_cli_values_equal_to_dotenv_on_reload(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            shared = {
                'RAILGRID_APP_BASE_DOMAIN': 'apps.shared.example',
                'RAILGRID_APP_PUBLIC_PORT': '8443',
            }
            captured = self.capture_make_tilt(
                root,
                'RAILGRID_APP_BASE_DOMAIN=apps.shared.example\n'
                'RAILGRID_APP_PUBLIC_PORT=8443\n',
                make_values=shared,
                inherited={
                    'RAILGRID_APP_BASE_DOMAIN': 'apps.inherited.example',
                    'RAILGRID_APP_PUBLIC_PORT': '7443',
                },
            )
            self.assertEqual(captured['RAILGRID_APP_BASE_DOMAIN'], shared['RAILGRID_APP_BASE_DOMAIN'])
            self.assertEqual(captured['RAILGRID_APP_PUBLIC_PORT'], shared['RAILGRID_APP_PUBLIC_PORT'])
            self.assertEqual(captured['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'commandline')
            self.assertEqual(captured['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'commandline')

            (Path(root) / '.env').write_text(
                'RAILGRID_APP_BASE_DOMAIN=apps.edited.example\n'
                'RAILGRID_APP_PUBLIC_PORT=9443\n'
            )
            with patch.dict(m.os.environ, captured, clear=True):
                reloaded = m.environment(config)
            self.assertEqual(reloaded['RAILGRID_APP_BASE_DOMAIN'], shared['RAILGRID_APP_BASE_DOMAIN'])
            self.assertEqual(reloaded['RAILGRID_APP_PUBLIC_PORT'], shared['RAILGRID_APP_PUBLIC_PORT'])
            self.assertEqual(reloaded['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'commandline')
            self.assertEqual(reloaded['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'commandline')

    def test_supported_make_tilt_keeps_cli_value_equal_to_default_over_dotenv(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            defaults = {
                'RAILGRID_APP_BASE_DOMAIN': 'apps-abc.127.0.0.1.sslip.io',
                'RAILGRID_APP_PUBLIC_PORT': str(m.PORTS['PREVIEW_GATEWAY_PORT']),
            }
            captured = self.capture_make_tilt(
                root,
                'RAILGRID_APP_BASE_DOMAIN=apps.file.example\n'
                'RAILGRID_APP_PUBLIC_PORT=443\n',
                make_values=defaults,
            )
            self.assertEqual(captured['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'commandline')
            self.assertEqual(captured['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'commandline')
            with patch.dict(m.os.environ, captured, clear=True):
                reloaded = m.environment(config)
            self.assertEqual(reloaded['RAILGRID_APP_BASE_DOMAIN'], defaults['RAILGRID_APP_BASE_DOMAIN'])
            self.assertEqual(reloaded['RAILGRID_APP_PUBLIC_PORT'], defaults['RAILGRID_APP_PUBLIC_PORT'])

    def test_supported_make_tilt_keeps_blank_cli_domain_and_port_semantics(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            captured = self.capture_make_tilt(
                root,
                'RAILGRID_APP_BASE_DOMAIN=apps.file.example\n'
                'RAILGRID_APP_PUBLIC_PORT=7443\n',
                make_values={
                    'RAILGRID_APP_BASE_DOMAIN': '',
                    'RAILGRID_APP_PUBLIC_PORT': '',
                },
            )
            self.assertEqual(captured['RAILGRID_APP_BASE_DOMAIN'], '')
            self.assertEqual(captured['RAILGRID_APP_PUBLIC_PORT'], '')
            self.assertEqual(captured['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'commandline')
            self.assertEqual(captured['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'commandline')
            with patch.dict(m.os.environ, captured, clear=True):
                reloaded = m.environment(config)
            self.assertEqual(reloaded['RAILGRID_APP_BASE_DOMAIN'], 'apps-abc.127.0.0.1.sslip.io')
            self.assertEqual(reloaded['RAILGRID_APP_PUBLIC_PORT'], '')
            self.assertEqual(reloaded['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'commandline')
            self.assertEqual(reloaded['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'commandline')

    def test_supported_make_tilt_marks_dotenv_and_reloads_edited_values(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            captured = self.capture_make_tilt(
                root,
                'RAILGRID_APP_BASE_DOMAIN=apps.first.example\n'
                'RAILGRID_APP_PUBLIC_PORT=443\n',
                inherited={
                    'RAILGRID_APP_BASE_DOMAIN': 'apps.inherited.example',
                    'RAILGRID_APP_PUBLIC_PORT': '9443',
                },
            )
            self.assertEqual(captured['RAILGRID_APP_BASE_DOMAIN'], 'apps.first.example')
            self.assertEqual(captured['RAILGRID_APP_PUBLIC_PORT'], '443')
            self.assertEqual(captured['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'dotenv')
            self.assertEqual(captured['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'dotenv')

            (Path(root) / '.env').write_text(
                'RAILGRID_APP_BASE_DOMAIN=apps.second.example\n'
                'RAILGRID_APP_PUBLIC_PORT=8443\n'
            )
            with patch.dict(m.os.environ, captured, clear=True):
                reloaded = m.environment(config)
            self.assertEqual(reloaded['RAILGRID_APP_BASE_DOMAIN'], 'apps.second.example')
            self.assertEqual(reloaded['RAILGRID_APP_PUBLIC_PORT'], '8443')
            self.assertEqual(reloaded['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'dotenv')
            self.assertEqual(reloaded['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'dotenv')

    def test_supported_make_tilt_marks_default_and_inherited_environment_origins(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'root': root, 'key': 'abc', 'ports': m.PORTS}
            defaults = self.capture_make_tilt(root, '')
            self.assertEqual(defaults['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'default')
            self.assertEqual(defaults['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'default')
            with patch.dict(
                    m.os.environ,
                    {name: value for name, value in defaults.items() if value is not None},
                    clear=True):
                defaulted = m.environment(config)
            self.assertEqual(defaulted['RAILGRID_APP_BASE_DOMAIN'], 'apps-abc.127.0.0.1.sslip.io')
            self.assertEqual(defaulted['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'default')

            inherited_values = {
                'RAILGRID_APP_BASE_DOMAIN': 'apps.shell.example',
                'RAILGRID_APP_PUBLIC_PORT': '9443',
            }
            inherited = self.capture_make_tilt(root, '', inherited=inherited_values)
            self.assertEqual(inherited['RAILGRID_APP_BASE_DOMAIN'], inherited_values['RAILGRID_APP_BASE_DOMAIN'])
            self.assertEqual(inherited['RAILGRID_APP_PUBLIC_PORT'], inherited_values['RAILGRID_APP_PUBLIC_PORT'])
            self.assertEqual(inherited['RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE'], 'environment')
            self.assertEqual(inherited['RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'], 'environment')
            with patch.dict(m.os.environ, inherited, clear=True):
                preserved = m.environment(config)
            self.assertEqual(preserved['RAILGRID_APP_BASE_DOMAIN'], inherited_values['RAILGRID_APP_BASE_DOMAIN'])
            self.assertEqual(preserved['RAILGRID_APP_PUBLIC_PORT'], inherited_values['RAILGRID_APP_PUBLIC_PORT'])

    def test_env_does_not_generate_manifests_or_update_state(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            config = m.settings(root)
            before = {p: p.stat().st_mtime_ns for p in root.rglob('*')}
            with patch.object(m, 'ROOT', root), patch.object(sys, 'argv', ['launcher', 'env']), \
                    contextlib.redirect_stdout(io.StringIO()) as output:
                m.main()
            self.assertEqual(json.loads(output.getvalue())['HUB_PORT'], str(config['ports']['HUB_PORT']))
            self.assertEqual(before, {p: p.stat().st_mtime_ns for p in root.rglob('*')})

    def test_dns_missing_or_ambiguous_networks_fail_clearly(self):
        config = {'root': '/checkout', 'key': 'abc'}
        for networks in ({}, {'a': {'Gateway': '172.18.0.1'}, 'b': {'Gateway': '172.19.0.1'}}):
            with patch.object(m.subprocess, 'run') as run, \
                    patch.object(m.subprocess, 'check_output', return_value=json.dumps([{'NetworkSettings': {'Networks': networks}}])), \
                    patch.dict(m.os.environ, {'KIND_EXPERIMENTAL_DOCKER_NETWORK': ''}):
                run.return_value.returncode = 1
                with self.assertRaisesRegex(ValueError, 'expected one network gateway'):
                    m.configure_host_dns(config)

    def test_cli_reports_missing_tools_and_failed_commands_without_traceback(self):
        for error in (FileNotFoundError('docker not found'), m.subprocess.CalledProcessError(1, ['kubectl', 'get'])):
            with patch.object(m, 'main', side_effect=error), self.assertRaises(SystemExit) as raised:
                m.cli()
            self.assertEqual(str(raised.exception), str(error))

    def test_make_prepares_checkout_cluster_and_scopes_edge_cleanup(self):
        def dryrun(*args):
            return m.subprocess.check_output(['make', '-n', *args], cwd=m.ROOT, text=True)
        isolated = dryrun('tilt-start', 'RAILGRID_TILT_CHECKOUT=abc', 'EXTERNAL_PROVIDERS_DIR=')
        self.assertIn('kind get clusters', isolated)
        self.assertIn('tilt-checkout.py dns', isolated)
        self.assertIn('--context', isolated)
        cleanup = dryrun('dev-edge-clean-credentials', 'TYPE=server', 'RAILGRID_TILT_CHECKOUT=abc')
        self.assertIn('agent-dev-edge-server-1-abc.kubeconfig', cleanup)
        self.assertNotIn('agent-dev-edge-kube', cleanup)

    def test_two_checkouts_have_disjoint_names_urls_and_ports(self):
        with tempfile.TemporaryDirectory() as a, tempfile.TemporaryDirectory() as b:
            with patch.object(m, 'allocate_ports', return_value={k: v + 1000 for k, v in m.PORTS.items()}):
                ca = m.settings(Path(a))
            with patch.object(m, 'allocate_ports', return_value={k: v + 2000 for k, v in m.PORTS.items()}):
                cb = m.settings(Path(b))
            # This is a default-isolation fixture. The repository's root .env
            # is exported by Make, so remove inherited preview overrides while
            # keeping the checkout-specific domain assertion meaningful.
            env = dict(m.os.environ)
            for name in ('RAILGRID_APP_BASE_DOMAIN', 'RAILGRID_APP_PUBLIC_PORT',
                         'RAILGRID_TILT_CHECKOUT_APP_BASE_DOMAIN_SOURCE',
                         'RAILGRID_TILT_CHECKOUT_APP_PUBLIC_PORT_SOURCE'):
                env.pop(name, None)
            with patch.dict(m.os.environ, env, clear=True):
                ea, eb = m.environment(ca), m.environment(cb)
                for key in list(m.PORTS) + ['KRO_KIND_NAME', 'KUBECONFIG', 'DEV_AGENT_KIND',
                                           'AGENTS_POSTGRES_CONTAINER', 'DEV_HUB_URL',
                                           'DEV_SSH_CONTAINER', 'RAILGRID_APP_BASE_DOMAIN']:
                    self.assertNotEqual(ea[key], eb[key], key)
            self.assertEqual(m.settings(Path(a)), ca)
            self.assertIn(ea['HUB_PORT'], ea['VITE_HUB_URL'])
            self.assertIn(ea['AGENTS_POSTGRES_PORT'], ea['AGENTS_DEV_DATABASE_URL'])

    def test_occupied_saved_port_is_reported(self):
        with m.socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            listener.listen(1)
            config = {'ports': {'HUB_PORT': listener.getsockname()[1]}}
            with self.assertRaisesRegex(ValueError, 'HUB_PORT port .* unavailable'):
                m.check_ports(config)

    def test_dns_uses_cluster_gateway_and_is_idempotent(self):
        config = {'key': 'abc', 'root': '/checkout'}
        cm = {'data': {'Corefile': '.:53 {\n    forward . /etc/resolv.conf\n}\n'}}
        inspect = [{'NetworkSettings': {'Networks': {'custom-network': {'Gateway': '172.18.0.1'}}}}]
        def output(args, **kwargs):
            return json.dumps(inspect if args[0] == 'docker' else cm)
        with patch.object(m.subprocess, 'check_output', side_effect=output), \
                patch.object(m.subprocess, 'run') as run:
            run.return_value.returncode = 1
            m.configure_host_dns(config)
            patches = [c.args[0] for c in run.call_args_list if 'patch' in c.args[0]]
            self.assertEqual(len(patches), 1)
            self.assertIn('/checkout/.railgrid-kro.kubeconfig', patches[0])
            cm = json.loads(patches[0][-1])
            self.assertIn('172.18.0.1 host.docker.internal', cm['data']['Corefile'])
            run.reset_mock()
            m.configure_host_dns(config)
            self.assertFalse(any('patch' in c.args[0] for c in run.call_args_list))

    def test_environment_is_idempotent(self):
        config = {'root': '/example', 'key': 'abc', 'ports': m.PORTS}
        first = m.environment(config)
        with patch.dict(m.os.environ, first):
            self.assertEqual(first, m.environment(config))

    def test_existing_runtime_state_still_gets_isolated_resources(self):
        with tempfile.TemporaryDirectory() as d, patch.object(m, 'ephemeral_range', return_value=(20000, 20009)):
            root = Path(d)
            (root / '.kcp').mkdir()
            (root / '.kcp/admin.kubeconfig').touch()
            config = m.settings(root)
            self.assertNotIn('legacy', config)
            self.assertTrue(all(20010 <= port < 30000 for port in config['ports'].values()))
            self.assertEqual(m.environment(config)['KRO_KIND_NAME'], 'railgrid-kro-' + config['key'])

    def test_copied_runtime_state_is_not_silently_reused(self):
        with tempfile.TemporaryDirectory() as a, tempfile.TemporaryDirectory() as b:
            with patch.object(m, 'allocate_ports', return_value=m.PORTS):
                config = m.settings(Path(a))
            (Path(b) / '.kcp').mkdir()
            (Path(b) / '.kcp/tilt-checkout.json').write_text(json.dumps(config))
            with self.assertRaisesRegex(ValueError, 'another checkout'):
                m.settings(Path(b))

    def test_runtime_manifests_follow_selected_ports_without_modifying_sources(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            with patch.object(m, 'allocate_ports', return_value={k: v + 1000 for k, v in m.PORTS.items()}):
                config = m.settings(root)
            for name, port in [('quickstart',8081),('infrastructure',8082),('code',8083),
                               ('kuery',8084),('app-studio',8085),('agents',8087),('edges',8088)]:
                path = root / 'providers' / name / 'manifest.yaml'
                path.parent.mkdir(parents=True)
                path.write_text(f'url: http://localhost:{port}\n')
            env = m.manifests(config)
            self.assertIn('localhost:9083', Path(env['CODE_MANIFEST']).read_text())
            self.assertIn('localhost:8083', (root / 'providers/code/manifest.yaml').read_text())
            code = root / 'providers/code/manifest.yaml'
            code.write_text('url: http://localhost:8083/path\nother: http://localhost:80830\n')
            m.manifests(config)
            self.assertIn('localhost:9083/path', Path(env['CODE_MANIFEST']).read_text())
            self.assertIn('localhost:80830', Path(env['CODE_MANIFEST']).read_text())
            code.write_text('url: http://service.internal:8083\n')
            with self.assertRaisesRegex(ValueError, 'no loopback:8083 URL'):
                m.manifests(config)


if __name__ == '__main__':
    unittest.main()
