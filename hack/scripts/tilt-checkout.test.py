"""Per-checkout identities must never reuse another stack's host resources."""
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
        config = {'legacy': False, 'key': 'abc'}
        cm = {'data': {'Corefile': '.:53 {\n    forward . /etc/resolv.conf\n}\n'}}
        inspect = [{'NetworkSettings': {'Networks': {'kind': {'Gateway': '172.18.0.1'}}}}]
        def output(args, **kwargs):
            return json.dumps(inspect if args[0] == 'docker' else cm)
        with patch.object(m.subprocess, 'check_output', side_effect=output), \
                patch.object(m.subprocess, 'run') as run:
            run.return_value.returncode = 1
            m.configure_host_dns(config)
            patches = [c.args[0] for c in run.call_args_list if 'patch' in c.args[0]]
            self.assertEqual(len(patches), 1)
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
            self.assertEqual(m.environment(config), {})
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


if __name__ == '__main__':
    unittest.main()
