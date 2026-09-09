"""Check the effective Tilt sandbox policy and App Studio launch command."""
import ast
import os
from pathlib import Path
import unittest
from unittest.mock import patch

TREE = ast.parse((Path(__file__).resolve().parents[2] / 'Tiltfile').read_text())
ASSIGNMENTS = {
    target.id: node.value
    for node in TREE.body if isinstance(node, ast.Assign)
    for target in node.targets if isinstance(target, ast.Name)
}
APP_STUDIO = next(
    node.value for node in TREE.body
    if isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
    and isinstance(node.value.func, ast.Name) and node.value.func.id == 'local_resource'
    and node.value.args and isinstance(node.value.args[0], ast.Constant)
    and node.value.args[0].value == 'app-studio'
)
SERVE = next(kw.value for kw in APP_STUDIO.keywords if kw.arg == 'serve_cmd')


def evaluate(expression, bindings):
    return eval(compile(ast.Expression(expression), 'Tiltfile', 'eval'), bindings)


class SandboxDefaults(unittest.TestCase):
    def test_default_and_explicit_modes_control_graph_and_process_together(self):
        for raw, expected in [(None, 'off'), ('', 'off'), ('  ', 'off'),
                              ('OFF', 'off'), ('force', 'force'), ('byo-only', 'byo-only')]:
            with self.subTest(raw=raw), patch.dict(os.environ, {}, clear=True):
                if raw is not None:
                    os.environ['APP_STUDIO_RUN_SANDBOX_MODE'] = raw
                bindings = {'os': os, 'preview_hub_public_url': 'https://localhost:9443'}
                mode = evaluate(ASSIGNMENTS['app_studio_sandbox_mode'], bindings)
                self.assertEqual(mode, expected)
                bindings['app_studio_sandbox_mode'] = mode
                self.assertEqual(evaluate(ASSIGNMENTS['app_studio_sandbox_force'], bindings), expected == 'force')
                command = evaluate(SERVE, bindings)
                self.assertIn(f'APP_STUDIO_RUN_SANDBOX_MODE={expected} ', command)
                self.assertNotIn('${APP_STUDIO_RUN_SANDBOX_MODE', command)


if __name__ == '__main__':
    unittest.main()
