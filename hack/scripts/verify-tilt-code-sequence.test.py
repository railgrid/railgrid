"""Guard the embedded Tilt Code build/init/serve transaction."""
import ast
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]
TREE = ast.parse((ROOT / 'Tiltfile').read_text())
RESOURCE = next(node.value for node in TREE.body if isinstance(node, ast.Expr)
                and isinstance(node.value, ast.Call)
                and isinstance(node.value.func, ast.Name)
                and node.value.func.id == 'local_resource'
                and node.value.args and isinstance(node.value.args[0], ast.Constant)
                and node.value.args[0].value == 'code')
FIELDS = {item.arg: item.value for item in RESOURCE.keywords}


class CodeSequence(unittest.TestCase):
    def test_update_initializes_before_serving_and_watches_only_inputs(self):
        self.assertEqual(ast.literal_eval(FIELDS['cmd']), 'make init-provider-code')
        self.assertEqual(ast.literal_eval(FIELDS['serve_cmd']), 'make serve-provider-code')
        self.assertIn('code-register', ast.literal_eval(FIELDS['resource_deps']))
        deps = ast.literal_eval(FIELDS['deps'])
        self.assertIn('.kcp/admin.kubeconfig', deps)
        self.assertNotIn('.kcp/code-runtime.kubeconfig', deps)
        self.assertIn('providers/code/deploy/chart/files/schemas', deps)
        probe = ast.unparse(FIELDS['readiness_probe'])
        self.assertIn("path='/readyz'", probe)

    def test_make_builds_once_then_initializes_then_serves(self):
        result = subprocess.run(['make', '-n', 'init-provider-code', 'serve-provider-code'],
                                cwd=ROOT, text=True, capture_output=True, check=True)
        commands = result.stdout
        self.assertIn('npm ci --include=dev', commands)
        self.assertNotIn('npm install', commands)
        self.assertEqual(commands.count('cd providers/code && go build'), 1)
        self.assertLess(commands.index('cd providers/code && go build'),
                        commands.index('bin/code-provider init'))
        self.assertLess(commands.index('bin/code-provider init'),
                        commands.index('bin/code-provider serve'))
        serve = subprocess.run(['make', '-n', 'serve-provider-code'], cwd=ROOT,
                               text=True, capture_output=True, check=True).stdout
        self.assertNotIn('go build', serve)
        self.assertNotIn('npm ', serve)


if __name__ == '__main__':
    unittest.main()
