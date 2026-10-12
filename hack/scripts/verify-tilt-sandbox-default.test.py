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
MODE_CONFIG = next(
    node for node in TREE.body
    if isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
    and isinstance(node.value.func, ast.Attribute)
    and isinstance(node.value.func.value, ast.Name) and node.value.func.value.id == 'config'
    and node.value.func.attr == 'define_string'
    and node.value.args and isinstance(node.value.args[0], ast.Constant)
    and node.value.args[0].value == 'app-studio-sandbox-mode'
)
MODE_VALIDATION = next(
    node for node in TREE.body
    if isinstance(node, ast.If) and 'app_studio_sandbox_mode' in ast.unparse(node.test)
    and any(isinstance(call, ast.Call) and isinstance(call.func, ast.Name)
            and call.func.id == 'fail' for call in ast.walk(node))
)
APP_STUDIO = next(
    node.value for node in TREE.body
    if isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
    and isinstance(node.value.func, ast.Name) and node.value.func.id == 'local_resource'
    and node.value.args and isinstance(node.value.args[0], ast.Constant)
    and node.value.args[0].value == 'app-studio'
)
APP_STUDIO_FIELDS = {kw.arg: kw.value for kw in APP_STUDIO.keywords}
SERVE = APP_STUDIO_FIELDS['serve_cmd']


def evaluate(expression, bindings):
    return eval(compile(ast.Expression(expression), 'Tiltfile', 'eval'), bindings)


class SandboxDefaults(unittest.TestCase):
    def test_config_flag_precedes_env_fallback_and_drives_graph_and_process(self):
        cases = [
            (None, None, 'off'),
            ('', 'force', 'force'),
            ('force', 'off', 'force'),
            ('off', 'force', 'off'),
            ('byo-only', 'force', 'byo-only'),
            (None, 'BYO-ONLY', 'byo-only'),
            (None, '  ', 'off'),
        ]
        for configured, environment, expected in cases:
            with self.subTest(configured=configured, environment=environment), patch.dict(os.environ, {}, clear=True):
                if environment is not None:
                    os.environ['APP_STUDIO_RUN_SANDBOX_MODE'] = environment
                cfg = {} if configured is None else {'app-studio-sandbox-mode': configured}
                bindings = {
                    'cfg': cfg,
                    'os': os,
                    'preview_hub_public_url': 'https://localhost:9443',
                }
                mode = evaluate(ASSIGNMENTS['app_studio_sandbox_mode'], bindings)
                self.assertEqual(mode, expected)
                bindings['app_studio_sandbox_mode'] = mode
                force = evaluate(ASSIGNMENTS['app_studio_sandbox_force'], bindings)
                self.assertEqual(force, expected == 'force')
                bindings['app_studio_sandbox_force'] = force
                command = evaluate(SERVE, bindings)
                self.assertIn(f'APP_STUDIO_RUN_SANDBOX_MODE={expected} ', command)
                self.assertNotIn('${APP_STUDIO_RUN_SANDBOX_MODE', command)
                dependencies = evaluate(APP_STUDIO_FIELDS['resource_deps'], bindings)
                self.assertEqual('universal-dev-image' in dependencies, expected == 'force')

    def test_flag_is_defined_before_parse_and_mode_is_validated_before_resources(self):
        config_index = TREE.body.index(MODE_CONFIG)
        parse_index = next(
            index for index, node in enumerate(TREE.body)
            if isinstance(node, ast.Assign) and any(
                isinstance(target, ast.Name) and target.id == 'cfg' for target in node.targets
            )
        )
        mode_index = next(
            index for index, node in enumerate(TREE.body)
            if isinstance(node, ast.Assign) and any(
                isinstance(target, ast.Name) and target.id == 'app_studio_sandbox_mode'
                for target in node.targets
            )
        )
        validation_index = TREE.body.index(MODE_VALIDATION)
        first_resource_index = next(
            index for index, node in enumerate(TREE.body)
            if isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
            and isinstance(node.value.func, ast.Name) and node.value.func.id == 'local_resource'
        )
        self.assertLess(config_index, parse_index)
        self.assertLess(parse_index, mode_index)
        self.assertLess(validation_index, first_resource_index)

    def test_invalid_mode_fails_with_supported_choices(self):
        def fail(message):
            raise ValueError(message)

        def validate(mode):
            bindings = {'app_studio_sandbox_mode': mode, 'fail': fail}
            module = ast.Module(body=[MODE_VALIDATION], type_ignores=[])
            exec(compile(module, 'Tiltfile', 'exec'), bindings)

        for mode in ('off', 'byo-only', 'force'):
            with self.subTest(mode=mode):
                validate(mode)
        with self.assertRaisesRegex(ValueError, 'off, byo-only, force'):
            validate('invalid')


if __name__ == '__main__':
    unittest.main()
