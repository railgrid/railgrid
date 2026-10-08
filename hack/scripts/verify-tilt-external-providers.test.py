"""Exercise both Tilt loaders without starting workloads or touching a cluster."""
import ast
import os
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[2]


def loader(filename):
    tree = ast.parse((ROOT / filename).read_text())
    start = next(i for i, node in enumerate(tree.body)
                 if isinstance(node, ast.Assign)
                 and any(isinstance(t, ast.Name) and t.id == 'external_providers_dirs'
                         for t in node.targets))
    end = next(i for i in range(start, len(tree.body))
               if isinstance(tree.body[i], ast.For))
    return compile(ast.Module(body=tree.body[start:end + 1], type_ignores=[]), filename, 'exec')


class ExternalProviders(unittest.TestCase):
    def run_loader(self, filename, value=None, env='', exists=True, context='kind-railgrid-kro'):
        configure = Mock(return_value=['example'])
        load = Mock(return_value={'railgrid_providers': configure})

        def fail(message):
            raise ValueError(message)

        bindings = {
            'cfg': {} if value is None else {'external-providers-dir': value},
            'os': os, 'load_dynamic': load, 'fail': fail, 'print': Mock(),
            'k8s_context': lambda: context, 'allow_k8s_contexts': Mock(),
            'preview_kro_context': 'kind-railgrid-kro',
            'hub_namespace': 'railgrid-system', 'KCP_ADMIN_KUBECONFIG': '/admin.kubeconfig',
        }
        with patch.dict(os.environ, {'RAILGRID_EXTERNAL_PROVIDERS_DIR': env}, clear=True), \
                patch('os.path.exists', return_value=exists):
            exec(loader(filename), bindings)
        return load, configure

    def test_paths_and_environment_in_both_stacks(self):
        for filename in ('Tiltfile', 'Tiltfile.cluster'):
            for value, env, expected in [
                (None, '', []),
                ('../providers', '', ['../providers']),
                (' ../providers, ../experimental-providers, ,../providers/. ', '',
                 ['../providers', '../experimental-providers']),
                (None, '../providers,../experimental-providers',
                 ['../providers', '../experimental-providers']),
                ('', '../providers,../experimental-providers',
                 ['../providers', '../experimental-providers']),
                ('../with spaces', '../ignored', ['../with spaces']),
            ]:
                with self.subTest(filename=filename, value=value, env=env):
                    load, configure = self.run_loader(filename, value, env)
                    self.assertEqual([call.args[0] for call in load.call_args_list],
                                     [os.path.abspath(p) + '/hack/tilt/providers.tilt' for p in expected])
                    self.assertEqual(configure.call_count, len(expected))
                    for call in configure.call_args_list:
                        self.assertEqual(call.kwargs['selection'], 'all')
                        self.assertTrue(call.kwargs['skip_unconfigured'])
                        self.assertIn('resource_deps', call.kwargs)

    def test_missing_library_fails_with_path(self):
        for filename in ('Tiltfile', 'Tiltfile.cluster'):
            with self.subTest(filename=filename), self.assertRaisesRegex(ValueError, 'missing has no hack/tilt/providers.tilt'):
                self.run_loader(filename, '../missing', exists=False)

    def test_host_stack_rejects_wrong_context(self):
        with self.assertRaisesRegex(ValueError, 'External providers run in kind-railgrid-kro'):
            self.run_loader('Tiltfile', '../providers,../experimental-providers', context='wrong')


if __name__ == '__main__':
    unittest.main()
