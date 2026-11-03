# Copyright 2026 The Railgrid Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Guard Edges' credential/API init before server replacement without a cluster."""
import ast
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]
TREE = ast.parse((ROOT / 'Tiltfile').read_text())


def resource_fields(name):
    resource = next(node.value for node in TREE.body if isinstance(node, ast.Expr)
                    and isinstance(node.value, ast.Call)
                    and isinstance(node.value.func, ast.Name)
                    and node.value.func.id == 'local_resource'
                    and node.value.args and isinstance(node.value.args[0], ast.Constant)
                    and node.value.args[0].value == name)
    return {item.arg: item.value for item in resource.keywords}


class EdgesSequence(unittest.TestCase):
    def test_update_initializes_before_serving_and_watches_only_inputs(self):
        fields = resource_fields('edges')
        self.assertEqual(ast.literal_eval(fields['cmd']), 'make init-provider-edges')
        serve = ast.unparse(fields['serve_cmd'])
        self.assertIn('make serve-provider-edges', serve)
        self.assertNotIn('make run-provider-edges', serve)
        self.assertIn('edges-register', ast.literal_eval(fields['resource_deps']))
        deps = ast.literal_eval(fields['deps'])
        for path in ('.kcp/admin.kubeconfig', 'provider-sdk', 'go.work',
                     'providers/edges/deploy/chart/files', 'providers/edges/assets.go',
                     'providers/edges/go.sum', 'providers/edges/portal/package-lock.json'):
            self.assertIn(path, deps)
        for output in ('.kcp/edges-runtime.kubeconfig', 'providers/edges/portal/dist',
                       'providers/edges/portal/node_modules', 'bin/edges-provider'):
            self.assertNotIn(output, deps)
        self.assertIn("path='/readyz'", ast.unparse(fields['readiness_probe']))

    def test_make_builds_once_then_initializes_then_serves(self):
        commands = subprocess.run(['make', '-n', 'init-provider-edges', 'serve-provider-edges'],
                                  cwd=ROOT, text=True, capture_output=True, check=True).stdout
        self.assertIn('npm ci --include=dev', commands)
        self.assertNotIn('npm install', commands)
        self.assertEqual(commands.count('cd providers/edges && go build'), 1)
        self.assertLess(commands.index('cd providers/edges && go build'),
                        commands.index('bin/edges-provider init'))
        self.assertLess(commands.index('bin/edges-provider init'),
                        commands.index('bin/edges-provider serve'))
        serve = subprocess.run(['make', '-n', 'serve-provider-edges'], cwd=ROOT,
                               text=True, capture_output=True, check=True).stdout
        self.assertNotIn('go build', serve)
        self.assertNotIn('npm ', serve)
        self.assertNotIn('bin/edges-provider init', serve)

    def test_replica_uses_initialized_binary_and_real_readiness(self):
        fields = resource_fields('edges-2')
        serve = ast.unparse(fields['serve_cmd'])
        self.assertIn('make serve-provider-edges', serve)
        self.assertNotIn('make run-provider-edges', serve)
        self.assertEqual(ast.literal_eval(fields['resource_deps']), ['edges'])
        self.assertIn("path='/readyz'", ast.unparse(fields['readiness_probe']))


if __name__ == '__main__':
    unittest.main()
