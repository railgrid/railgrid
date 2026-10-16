"""Per-checkout identities must never reuse another stack's host resources."""
import contextlib
import io
from concurrent.futures import ThreadPoolExecutor
import importlib.util
import json
from pathlib import Path
import tempfile
import sys
sys.dont_write_bytecode = True
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('checkout', Path(__file__).with_name('tilt-checkout.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class CheckoutTest(unittest.TestCase):
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

    def test_legacy_checks_hub_port_without_requiring_stopped_provider_databases(self):
        with m.socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            with patch.dict(m.os.environ, {'HUB_PORT': str(listener.getsockname()[1]), 'TILT_PORT': '0'}):
                with self.assertRaisesRegex(ValueError, 'HUB_PORT port .* unavailable'):
                    m.check_ports({'legacy': True, 'ports': m.PORTS})

    def test_buildvcs_override_only_applies_to_worktrees(self):
        with tempfile.TemporaryDirectory() as root:
            config = {'legacy': False, 'root': root, 'key': 'abc', 'ports': m.PORTS}
            self.assertNotIn('GOFLAGS', m.environment(config))
            (Path(root) / '.git').write_text('gitdir: /example')
            with patch.dict(m.os.environ, {'GOFLAGS': '-mod=readonly'}):
                self.assertEqual(m.environment(config)['GOFLAGS'], '-mod=readonly -buildvcs=false')

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
        config = {'legacy': False, 'root': '/checkout', 'key': 'abc'}
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

    def test_make_preserves_legacy_startup_and_scopes_edge_cleanup(self):
        def dryrun(*args):
            return m.subprocess.check_output(['make', '-n', *args], cwd=m.ROOT, text=True)
        legacy = dryrun('tilt-start', 'RAILGRID_TILT_CHECKOUT=', 'EXTERNAL_PROVIDERS_DIR=')
        self.assertIn('require_service_not_running kcp', legacy)
        self.assertNotIn('kind get clusters', legacy)
        self.assertNotIn('--context', legacy)
        isolated = dryrun('tilt-start', 'RAILGRID_TILT_CHECKOUT=abc', 'EXTERNAL_PROVIDERS_DIR=')
        self.assertIn('kind get clusters', isolated)
        self.assertIn('tilt-checkout.py dns', isolated)
        cleanup = dryrun('dev-edge-clean-credentials', 'TYPE=server', 'RAILGRID_TILT_CHECKOUT=abc')
        self.assertIn('agent-dev-edge-server-1-abc.kubeconfig', cleanup)
        self.assertNotIn('agent-dev-edge-kube', cleanup)

    def test_two_checkouts_have_disjoint_names_urls_and_ports(self):
        with tempfile.TemporaryDirectory() as a, tempfile.TemporaryDirectory() as b:
            with patch.object(m, 'allocate_ports', return_value={k: v + 1000 for k, v in m.PORTS.items()}):
                ca = m.settings(Path(a))
            with patch.object(m, 'allocate_ports', return_value={k: v + 2000 for k, v in m.PORTS.items()}):
                cb = m.settings(Path(b))
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
            config = {'legacy': False, 'ports': {'HUB_PORT': listener.getsockname()[1]}}
            with self.assertRaisesRegex(ValueError, 'HUB_PORT port .* unavailable'):
                m.check_ports(config)

    def test_dns_uses_cluster_gateway_and_is_idempotent(self):
        config = {'legacy': False, 'key': 'abc', 'root': '/checkout'}
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
        config = {'legacy': False, 'root': '/example', 'key': 'abc', 'ports': m.PORTS}
        first = m.environment(config)
        with patch.dict(m.os.environ, first):
            self.assertEqual(first, m.environment(config))

    def test_legacy_install_keeps_existing_runtime(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / '.kcp').mkdir()
            (root / '.kcp/admin.kubeconfig').touch()
            config = m.settings(root)
            self.assertTrue(config['legacy'])
            self.assertEqual(set(m.environment(config)), {'DEV_STATIC_TOKEN', 'DEV_STATIC_ADMIN'})
            self.assertEqual(m.manifests(config), {})

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
