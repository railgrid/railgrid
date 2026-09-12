"""Behavioral and real-Git coverage for the conservative UI fast path."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from selection import changed_paths, classify, selection


STUDIO = "providers/app-studio/portal/"
ROOT = Path(__file__).resolve().parents[2]


class SelectionTests(unittest.TestCase):
    def test_pr_695_and_multiple_portals(self):
        result = selection("pull_request", [STUDIO + "src/App.vue", STUDIO + "src/NewProjectWizard.vue"], "faroshq")
        self.assertEqual(result["mode"], "provider-ui")
        self.assertEqual(result["providers"], ["app-studio"])
        self.assertEqual(result["portal-matrix"]["include"], [
            {"provider": "app-studio", "test": True, "typecheck": True}])
        self.assertEqual(result["image-matrix"]["include"], [
            {"name": "app-studio", "image": "faroshq/faros/app-studio-provider"}])
        paths = [STUDIO + "package-lock.json", "providers/agents/portal/src/App.vue"]
        result = selection("pull_request", paths, "fork-owner")
        self.assertEqual(result["providers"], ["agents", "app-studio"])
        self.assertEqual({x["provider"] for x in result["portal-matrix"]["include"]}, {"agents", "app-studio"})
        self.assertEqual({x["name"] for x in result["image-matrix"]["include"]}, {"agents", "app-studio"})
        self.assertTrue(all(x["image"].startswith("fork-owner/") for x in result["image-matrix"]["include"]))

    def test_portal_configuration_and_unusual_filenames(self):
        for suffix in ["package.json", "package-lock.json", "vite.config.ts", "tsconfig.json",
                       "src/space and\nnewline.vue", "src/$(echo unsafe).vue", "src/日本語.vue"]:
            with self.subTest(suffix=suffix):
                self.assertEqual(classify("pull_request", [STUDIO + suffix])[:2], ("provider-ui", ["app-studio"]))

    def test_full_fallback_and_mixed_changes(self):
        for path in ["portal/src/App.vue", "provider-sdk/portalkit/tenant.ts",
                     "provider-sdk/agentkit/styles.ts", "provider-sdk/agentkit-vue/Chat.vue",
                     "providers/app-studio/main.go", "providers/app-studio/go.mod",
                     "go.mod", "go.sum", "go.work", "providers/app-studio/Dockerfile",
                     "Makefile", ".github/workflows/ci.yaml", "docs/design/README.md",
                     "unknown.txt", "providers/new-provider/portal/App.vue",
                     STUDIO + "../main.go", STUDIO + "src//App.vue"]:
            with self.subTest(path=path):
                self.assertEqual(classify("pull_request", [path])[0], "full")
                self.assertEqual(classify("pull_request", [STUDIO + "src/App.vue", path])[0], "full")
        for paths in ([], None):
            self.assertEqual(classify("pull_request", paths)[0], "full")

    def test_no_changed_file_limit(self):
        paths = [STUDIO + f"src/component-{n}.vue" for n in range(3501)]
        self.assertEqual(classify("pull_request", paths)[0], "provider-ui")
        self.assertEqual(classify("pull_request", paths + ["pkg/hub/server.go"])[0], "full")

    def test_full_matrices_preserve_original_metadata(self):
        expected_portals = {
            "agents": (True, True), "app-studio": (True, True), "code": (False, False),
            "databricks": (True, True), "edges": (True, True), "infrastructure": (False, False),
            "kuery": (True, True), "linear": (True, True), "quickstart": (False, True),
        }
        expected_images = [
            {"name": name, "image": f"faroshq/faros-{name}-provider"}
            for name in ["quickstart", "infrastructure", "code", "kuery"]
        ] + [{"name": "app-studio", "image": "faroshq/faros/app-studio-provider"}] + [
            {"name": name, "image": f"faroshq/faros-{name}-provider"}
            for name in ["databricks", "agents", "edges", "linear"]
        ] + [
            {"name": "infrastructure/dev-agent", "image": "faroshq/faros-dev-agent",
             "context": "./providers/infrastructure/dev-agent"},
            {"name": "infrastructure/universal-dev", "image": "faroshq/faros-universal-dev",
             "context": "./providers/infrastructure/dev-agent",
             "dockerfile": "./providers/infrastructure/dev-agent/Dockerfile.universal"},
            {"name": "access-proxy", "image": "faroshq/faros-access-proxy", "context": ".",
             "dockerfile": "./providers/infrastructure/Dockerfile.access-proxy"},
        ]
        for event in ("pull_request", "push", "release", "workflow_dispatch"):
            with self.subTest(event=event):
                result = selection(event, ["Makefile"] if event == "pull_request" else [STUDIO + "App.vue"], "faroshq")
                self.assertEqual(result["mode"], "full")
                self.assertEqual(result["image-matrix"]["include"], expected_images)
                self.assertEqual(result["portal-matrix"]["include"], [
                    {"provider": name, "test": flags[0], "typecheck": flags[1]}
                    for name, flags in expected_portals.items()])

    def test_infrastructure_ui_excludes_companion_images(self):
        result = selection("pull_request", ["providers/infrastructure/portal/src/App.vue"], "faroshq")
        self.assertEqual([entry["name"] for entry in result["image-matrix"]["include"]], ["infrastructure"])

    def test_unavailable_history_falls_back_but_execution_errors_propagate(self):
        self.assertIsNone(changed_paths(None, "a" * 40))
        self.assertIsNone(changed_paths("--help", "a" * 40))
        with patch("selection.subprocess.run", side_effect=subprocess.CalledProcessError(128, "git")):
            self.assertIsNone(changed_paths("a" * 40, "b" * 40))
        with patch("selection.subprocess.run", side_effect=OSError("cannot execute git")):
            with self.assertRaises(OSError):
                changed_paths("a" * 40, "b" * 40)


class GitDiffTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="ci-selection-test-")
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        self.git("init", "-q")
        self.git("config", "user.name", "CI test")
        self.git("config", "user.email", "ci@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.write(STUDIO + "src/App.vue", "original\n")
        self.write("backend.go", "original\n")
        self.base = self.commit()

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.repo, stderr=subprocess.PIPE).decode().strip()

    def write(self, path, content):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(content)

    def commit(self):
        self.git("add", "-A")
        self.git("commit", "-qm", "fixture")
        return self.git("rev-parse", "HEAD")

    def test_entire_pr_and_merge_base_not_last_commit_or_base_tip(self):
        self.write("backend.go", "base branch advanced\n")
        advanced_base = self.commit()
        self.git("checkout", "--detach", self.base)
        self.write(STUDIO + "src/App.vue", "first PR commit\n")
        self.commit()
        self.write(STUDIO + "src/NewProjectWizard.vue", "second PR commit\n")
        head = self.commit()
        paths = changed_paths(advanced_base, head, self.repo)
        self.assertEqual(set(paths), {STUDIO + "src/App.vue", STUDIO + "src/NewProjectWizard.vue"})
        self.assertEqual(classify("pull_request", paths)[0], "provider-ui")
        self.assertEqual(self.git("rev-parse", "HEAD"), head)
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_deletion_and_newline_path(self):
        (self.repo / (STUDIO + "src/App.vue")).unlink()
        unusual = STUDIO + "src/a\nb file.vue"
        self.write(unusual, "new\n")
        paths = changed_paths(self.base, self.commit(), self.repo)
        self.assertEqual(set(paths), {STUDIO + "src/App.vue", unusual})
        self.assertEqual(classify("pull_request", paths)[0], "provider-ui")

    def test_rename_across_boundary_in_both_directions(self):
        for old, new in [("backend.go", STUDIO + "backend.go"), (STUDIO + "src/App.vue", "App.vue")]:
            with self.subTest(old=old):
                self.git("reset", "--hard", self.base)
                (self.repo / old).rename(self.repo / new)
                paths = changed_paths(self.base, self.commit(), self.repo)
                self.assertEqual(set(paths), {old, new})
                self.assertEqual(classify("pull_request", paths)[0], "full")

    def test_earlier_backend_commit_is_not_missed(self):
        self.write("backend.go", "backend change\n")
        self.commit()
        self.write(STUDIO + "src/App.vue", "last commit UI only\n")
        self.assertEqual(classify("pull_request", changed_paths(self.base, self.commit(), self.repo))[0], "full")

    def test_empty_diff_and_unavailable_commit_select_full(self):
        for base in (self.base, "f" * 40):
            paths = changed_paths(base, self.base, self.repo)
            self.assertEqual(classify("pull_request", paths)[0], "full")

    def test_cli_outputs_summary_and_invalid_payload_failure(self):
        self.write(STUDIO + "src/App.vue", "changed\n")
        head = self.commit()
        event, output, summary = [self.repo / name for name in ("event.json", "output", "summary")]
        event.write_text(json.dumps({"pull_request": {"base": {"sha": self.base}, "head": {"sha": head}}}))
        env = dict(os.environ, GITHUB_EVENT_NAME="pull_request", GITHUB_EVENT_PATH=str(event),
                   GITHUB_OUTPUT=str(output), GITHUB_STEP_SUMMARY=str(summary), GITHUB_REPOSITORY_OWNER="faroshq")
        command = [sys.executable, str(ROOT / "hack/ci/selection.py")]
        subprocess.run(command, cwd=self.repo, env=env, check=True, capture_output=True)
        outputs = dict(line.split("=", 1) for line in output.read_text().splitlines())
        self.assertEqual(outputs["mode"], "provider-ui")
        self.assertEqual(json.loads(outputs["providers"]), ["app-studio"])
        self.assertIn("app-studio", summary.read_text())
        event.write_text("invalid json")
        self.assertNotEqual(subprocess.run(command, cwd=self.repo, env=env, capture_output=True).returncode, 0)


if __name__ == "__main__":
    unittest.main()
