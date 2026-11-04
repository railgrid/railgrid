"""Shared host backends must not accept tenant traffic outside the Hub relay."""
import ast
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
HOST_PROVIDERS = {'quickstart', 'code', 'app-studio', 'agents', 'kuery', 'infrastructure', 'edges', 'code-2', 'kuery-2', 'edges-2'}

class PrivateProviderListeners(unittest.TestCase):
    def test_every_host_backend_uses_private_default(self):
        tree = ast.parse((ROOT / 'Tiltfile').read_text())
        bindings = [node for node in ast.walk(tree) if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == 'host_provider_listen_host' for target in node.targets)]
        self.assertEqual(len(bindings), 1)
        self.assertEqual(ast.literal_eval(bindings[0].value.args[1]), '127.0.0.1')
        found = set()
        for node in ast.walk(tree):
            if not (isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == 'local_resource' and node.args and isinstance(node.args[0], ast.Constant)):
                continue
            name = node.args[0].value
            if name not in HOST_PROVIDERS:
                continue
            found.add(name)
            environment = next(keyword.value for keyword in node.keywords if keyword.arg == 'serve_env')
            self.assertIsInstance(environment, ast.Dict)
            host = next(value for key, value in zip(environment.keys, environment.values) if isinstance(key, ast.Constant) and key.value == 'RAILGRID_LISTEN_HOST')
            self.assertIsInstance(host, ast.Name)
            self.assertEqual(host.id, 'host_provider_listen_host', name)
        self.assertEqual(found, HOST_PROVIDERS)

if __name__ == '__main__':
    unittest.main()
