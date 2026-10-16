import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch, Mock

spec = importlib.util.spec_from_file_location('browser', Path(__file__).with_name('dev-browser.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class BrowserTest(unittest.TestCase):
    def test_external_provider_catalog_is_read_from_its_own_workspace(self):
        config = {'root': str(m.ROOT), 'key': 'abc', 'legacy': False, 'ports': m.checkout.PORTS}
        setup = m.Setup(config)
        external = {'metadata': {'name': 'planner'}, 'spec': {'serving': {'backend': {'url': 'http://127.0.0.1:20341'}}}}
        setup.command = Mock(side_effect=[json.dumps({'items': []}), json.dumps(external)])
        self.assertEqual(setup.catalog(['planner']), {'planner': external['spec']})
        args = setup.command.call_args.args[0]
        self.assertIn('--server=https://localhost:6443/clusters/root:railgrid:providers:planner', args)
        self.assertNotIn('apply', args)

    def test_down_does_not_wait_for_ready_lock(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(m, 'ROOT', Path(tmp)), patch.object(m.sys, 'argv', ['dev-browser.py', 'down']):
            state = Path(tmp) / '.kcp'
            state.mkdir()
            (state / 'tilt-checkout.json').write_text(json.dumps({'root': tmp, 'legacy': False}))
            with (state / 'dev-browser.lock').open('a') as held:
                m.fcntl.flock(held, m.fcntl.LOCK_EX)
                with patch.object(m.checkout, 'stop_owned') as stop:
                    m.main()
                    stop.assert_called_once()

    def test_signal_during_launch_is_forwarded_after_owner_is_durable(self):
        c = m.checkout
        with tempfile.TemporaryDirectory() as tmp, patch.object(c, 'ROOT', Path(tmp)):
            (Path(tmp) / '.kcp').mkdir()
            config = {'root': tmp, 'key': 'first', 'legacy': False, 'ports': c.PORTS}
            handlers = {}
            child = Mock(pid=1234)
            child.poll.return_value = None
            child.wait.return_value = 0
            def launch(*args, **kwargs):
                handlers[c.signal.SIGTERM](c.signal.SIGTERM, None)
                return child
            def kill(pid, signum):
                record = json.loads((Path(tmp) / '.kcp/tilt-owner.json').read_text())
                self.assertEqual((record['pid'], record['birth']), (1234, 'birth'))
            with patch.object(c, 'settings', return_value=config), patch.object(c, 'manifests', return_value={}), patch.object(c, 'check_ports'), patch.object(c, 'process_identity', return_value='birth'), patch.object(c.signal, 'signal', side_effect=lambda signum, fn: handlers.update({signum: fn})), patch.object(c.subprocess, 'Popen', side_effect=launch), patch.object(c.os, 'killpg', side_effect=kill) as signal_group, patch.object(c.sys, 'argv', ['tilt-checkout.py']):
                with self.assertRaises(SystemExit):
                    c.main()
                signal_group.assert_called_once_with(1234, c.signal.SIGTERM)

    def test_timeout_and_url_ignore_host_overrides(self):
        config = {'root': str(m.ROOT), 'key': 'abc', 'legacy': False, 'ports': m.checkout.PORTS}
        with patch.dict(os.environ, {'DEV_HUB_URL': 'https://production.example', 'DEV_BROWSER_TIMEOUT': 'bad'}):
            with self.assertRaisesRegex(ValueError, 'DEV_BROWSER_TIMEOUT'):
                m.Setup(config)
        with patch.dict(os.environ, {'DEV_HUB_URL': 'https://production.example'}):
            self.assertEqual(m.Setup(config).url, 'https://console-abc.127.0.0.1.sslip.io:9443')

    def test_cleanup_continues_after_cluster_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / '.kcp').mkdir()
            config = {'root': tmp, 'key': 'first', 'legacy': False, 'ports': m.checkout.PORTS}
            record = root / '.kcp/tilt-owner.json'
            m.checkout.write_owner(record, {'root': tmp, 'resources': m.checkout.owned_resources(config)})
            def remove(args, **kwargs):
                if args[:2] == ['kind', 'delete']:
                    raise m.subprocess.CalledProcessError(1, args)
            with patch.object(m.checkout.subprocess, 'run', side_effect=remove) as run, patch.object(m.checkout.subprocess, 'check_output', return_value='railgrid-agents-pg-first\n'):
                with self.assertRaisesRegex(ValueError, 'kind cluster railgrid-kro-first'):
                    m.checkout.stop_owned(config)
            self.assertIn(['docker', 'rm', '-f', 'railgrid-agents-pg-first'], [c.args[0] for c in run.call_args_list])
            self.assertTrue(record.exists())

    def test_dependency_order_and_missing_dependency(self):
        catalog = {'app': {'requires': [{'provider': 'code'}]}, 'code': {}}
        self.assertEqual(m.provider_order(catalog, ['app', 'code']), ['code', 'app'])
        with self.assertRaisesRegex(ValueError, 'missing or cyclic'):
            m.provider_order(catalog, ['app'])

    def test_consent_includes_foreign_verbs_and_platform_claims(self):
        spec = {'requires': [
            {'provider': 'code', 'group': 'code.railgrid.ai', 'resources': [{'name': 'repositories'}, {'name': 'repositories/snapshot'}]},
            {'resources': [{'name': 'secrets'}]}],
            'hub': {'access': [{'capability': 'memberships.read', 'scope': 'org', 'reason': 'inspect'}]}}
        body = m.enable_body(spec)
        self.assertEqual(len(body['acceptedClaims']), 3)
        self.assertEqual(len(body['acceptedCompositions']), 2)
        self.assertEqual(body['acceptedHubAccess'], [{'capability': 'memberships.read', 'scope': 'org'}])

    def test_private_file_replaces_link_without_touching_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / 'other'
            target.write_text('untouched')
            token = Path(tmp) / 'token'
            token.symlink_to(target)
            m.write_private(token, 'secret')
            self.assertEqual(target.read_text(), 'untouched')
            self.assertEqual(token.stat().st_mode & 0o777, 0o600)
            self.assertFalse(token.is_symlink())

    def test_ready_uses_assigned_url_without_integration_credentials(self):
        config = {'root': str(m.ROOT), 'key': 'abc', 'legacy': False, 'ports': dict(m.checkout.PORTS, HUB_PORT=20341)}
        setup = m.Setup(config)
        self.assertEqual(setup.url, 'https://console-abc.127.0.0.1.sslip.io:20341')
        setup.deadline = 0
        with self.assertRaisesRegex(TimeoutError, 'timed out'):
            setup.remaining()

    def test_failed_tilt_task_reports_resource_without_waiting_for_timeout(self):
        config = {'root': str(m.ROOT), 'key': 'abc', 'legacy': False, 'ports': m.checkout.PORTS}
        setup = m.Setup(config)
        setup.tilt = Mock(return_value=json.dumps({'status': {'updateStatus': 'error'}}))
        with self.assertRaisesRegex(ValueError, 'tilt logs planner-init --port'):
            setup.wait_resource('planner-init')

    def test_fresh_and_repeat_setup_and_failure(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(m, 'ROOT', Path(tmp)):
            config = {'root': tmp, 'key': 'test', 'legacy': False, 'ports': m.checkout.PORTS}
            setup = m.Setup(config)
            completed = set()
            triggered = []
            def tilt(*args):
                if args[0] == 'trigger':
                    triggered.append(args[1])
                    completed.add(args[1])
                    return ''
                if args[1] == 'uiresources':
                    return json.dumps({'items': [{'metadata': {'name': n}} for n in ['planner', 'planner-register', 'planner-init']]})
                return json.dumps({'status': {'conditions': [{'type': 'Ready', 'status': 'True' if args[2] in completed else 'False'}]}})
            def api(path, body=None):
                if path == '/auth/token-login': return {'kubeconfig': 'private'}
                if path == '/api/orgs': return {'items': [{'uuid': 'org', 'personal': True}]}
                if path.endswith('/workspaces'): return {'items': [{'uuid': 'ws', 'clusterName': 'cluster'}]}
                if path.endswith('/enable'): return {'bindingName': 'planner'}
                if path.endswith('/enabled'): return {'bindingNamesByProvider': {'planner': 'planner'}}
                return {'items': [{'metadata': {'name': 'planner'}, 'status': {'conditions': [{'type': 'InitialBindingCompleted', 'status': 'True'}]}}]}
            setup.tilt = Mock(side_effect=tilt)
            setup.api = Mock(side_effect=api)
            setup.wait_resource = Mock()
            setup.command = Mock(return_value=json.dumps({'items': [{'metadata': {'name': 'planner'}, 'spec': {}}]}))
            setup.ready()
            setup.ready()
            self.assertEqual(triggered, ['planner-register', 'planner-init'])
            info = json.loads((Path(tmp) / '.kcp/dev-browser.json').read_text())
            self.assertEqual(info['providers'], ['planner'])
            self.assertIn('/ui/org/ws/', info['portalURL'])
            self.assertNotIn('dev-token', json.dumps(info))
            setup.wait_resource.side_effect = ValueError('provider failed readiness')
            with self.assertRaisesRegex(ValueError, 'readiness'):
                setup.ready()

    def test_cleanup_only_deletes_recorded_checkout_resources(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / '.kcp').mkdir()
            config = {'root': tmp, 'key': 'first', 'legacy': False, 'ports': m.checkout.PORTS}
            resources = m.checkout.owned_resources(config)
            record = root / '.kcp/tilt-owner.json'
            record.write_text(json.dumps({'root': tmp, 'resources': resources}))
            artifacts = root / 'artifacts/screenshots'
            artifacts.mkdir(parents=True)
            (artifacts / 'result.png').write_bytes(b'evidence')
            with patch.object(m.checkout.subprocess, 'run') as run, patch.object(m.checkout.subprocess, 'check_output', return_value='railgrid-agents-pg-first\nrailgrid-agents-pg-second\nrailgrid-agents-pg\n'):
                m.checkout.stop_owned(config)
            args = [c.args[0] for c in run.call_args_list]
            self.assertEqual(args, [
                ['kind', 'delete', 'cluster', '--name', 'railgrid-kro-first'],
                ['kind', 'delete', 'cluster', '--name', 'railgrid-agent-first'],
                ['docker', 'rm', '-f', 'railgrid-agents-pg-first']])
            self.assertTrue((artifacts / 'result.png').exists())
            self.assertFalse(record.exists())
            m.checkout.stop_owned(config)  # Already cleaned up.

    def test_cleanup_refuses_foreign_owner_and_reused_pid(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / '.kcp').mkdir()
            config = {'root': tmp, 'key': 'first', 'legacy': False, 'ports': m.checkout.PORTS}
            record = root / '.kcp/tilt-owner.json'
            record.write_text(json.dumps({'root': '/other', 'resources': {}}))
            with self.assertRaisesRegex(ValueError, 'ownership'):
                m.checkout.stop_owned(config)
            record.write_text(json.dumps({'root': tmp, 'resources': m.checkout.owned_resources(config), 'pid': 42, 'birth': 'old'}))
            with patch.object(m.checkout, 'process_identity', return_value='new'), patch.object(m.checkout.os, 'killpg') as kill:
                with self.assertRaisesRegex(ValueError, 'identity'):
                    m.checkout.stop_owned(config)
                kill.assert_not_called()


if __name__ == '__main__':
    unittest.main()
