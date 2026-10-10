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

"""Guard App Studio's portal/build/serve dependencies without starting Tilt."""
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


class AppStudioSequence(unittest.TestCase):
    def test_portal_watches_build_inputs_and_ignores_node_tests(self):
        fields = resource_fields('app-studio-portal')
        self.assertEqual(ast.literal_eval(fields['cmd']), 'make build-app-studio-provider-portal')
        deps = ast.literal_eval(fields['deps'])
        for path in ('src', 'scripts', 'package.json', 'package-lock.json', 'vite.config.ts'):
            self.assertIn('providers/app-studio/portal/' + path, deps)
        self.assertNotIn('providers/app-studio/portal/dist', deps)
        self.assertNotIn('providers/app-studio/portal/node_modules', deps)
        self.assertEqual(ast.literal_eval(fields['ignore']),
                         ['providers/app-studio/portal/**/*.test.mjs'])

    def test_provider_builds_go_and_serves_existing_binary(self):
        fields = resource_fields('app-studio')
        self.assertEqual(ast.literal_eval(fields['cmd']), 'make build-app-studio-provider-go')
        self.assertIn('make run-provider-app-studio-prebuilt', ast.unparse(fields['serve_cmd']))
        self.assertIn('app-studio-portal', ast.unparse(fields['resource_deps']))
        deps = ast.literal_eval(fields['deps'])
        self.assertIn('providers/app-studio/portal/dist', deps)
        self.assertNotIn('providers/app-studio/portal/src', deps)
        self.assertEqual(ast.literal_eval(fields['ignore']),
                         ['providers/app-studio/**/*_test.go'])

    def test_production_builds_portal_and_go_once(self):
        commands = subprocess.run(['make', '-n', 'build-app-studio-provider'], cwd=ROOT,
                                  text=True, capture_output=True, check=True).stdout
        self.assertEqual(commands.count('npm run build'), 1)
        self.assertEqual(commands.count('cd providers/app-studio && go build'), 1)
        self.assertLess(commands.index('npm run build'),
                        commands.index('cd providers/app-studio && go build'))
        self.assertIn('node scripts/ensure-dependencies.mjs', commands)

    def test_serve_does_not_rebuild(self):
        commands = subprocess.run(['make', '-n', 'run-provider-app-studio-prebuilt'], cwd=ROOT,
                                  text=True, capture_output=True, check=True).stdout
        self.assertNotIn('go build', commands)
        self.assertNotIn('npm run build', commands)
        self.assertNotIn('npm install', commands)
        self.assertNotIn('npm ci', commands)


if __name__ == '__main__':
    unittest.main()
