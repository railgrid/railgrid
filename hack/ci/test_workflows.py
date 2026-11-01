"""Check completion behavior and the actual workflow wiring, not a second plan."""

import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

import yaml

from result import MODES, POLICIES, check_results, expected_jobs


ROOT = Path(__file__).resolve().parents[2]
# (mode, event, has-images) combinations the workflows must handle.
SCENARIOS = [("full", "pull_request", True), ("full", "pull_request", False),
             ("provider-ui", "pull_request", True), ("docs", "pull_request", False),
             ("full", "push", True), ("full", "release", True), ("full", "workflow_dispatch", True)]
GO_WORKFLOWS = ("ci", "e2e", "images", "macos")


def expression(value, mode, event, has_images=False):
    """Evaluate the small condition vocabulary used by these workflows in tests."""
    if isinstance(value, bool):
        return value
    value = value.removeprefix("${{").removesuffix("}}").strip()
    for variable, replacement in {
        "github.event_name": event, "needs.changes.outputs.mode": mode,
        "needs.changes.outputs.has-images": "true" if has_images else "false",
    }.items():
        value = value.replace(variable, repr(replacement))
    value = value.replace("&&", " and ").replace("||", " or ")
    return eval(value, {"__builtins__": {}}, {})


def valid_needs(workflow, mode, event, has_images):
    outputs = {"mode": mode, "has-images": "true" if has_images else "false"}
    return {"changes": {"result": "success", "outputs": outputs},
            **{job: {"result": result}
               for job, result in expected_jobs(workflow, mode, event, has_images).items()}}


def load(name):
    return yaml.safe_load((ROOT / f".github/workflows/{name}.yaml").read_text())


def steps_using(workflow, prefix):
    for job in workflow["jobs"].values():
        for step in job.get("steps", []):
            if step.get("uses", "").startswith(prefix):
                yield job, step


class CompletionTests(unittest.TestCase):
    def test_every_selected_job_must_succeed_and_excluded_jobs_must_skip(self):
        for workflow in POLICIES:
            for mode, event, has_images in SCENARIOS:
                with self.subTest(workflow=workflow, mode=mode, event=event, has_images=has_images):
                    needs = valid_needs(workflow, mode, event, has_images)
                    self.assertEqual(check_results(workflow, event, needs), [])
                    for job, required in expected_jobs(workflow, mode, event, has_images).items():
                        for state in ("success", "skipped", "failure", "cancelled"):
                            changed = copy.deepcopy(needs)
                            changed[job]["result"] = state
                            self.assertEqual(bool(check_results(workflow, event, changed)), state != required,
                                             (workflow, mode, event, job, state))
                    for state in ("skipped", "failure", "cancelled"):
                        changed = copy.deepcopy(needs)
                        changed["changes"]["result"] = state
                        self.assertTrue(check_results(workflow, event, changed))

    def test_selection_rules(self):
        ci = expected_jobs("ci", "docs", "pull_request")
        self.assertEqual({job for job, result in ci.items() if result == "success"}, {"contracts"})
        ui = expected_jobs("ci", "provider-ui", "pull_request", True)
        self.assertEqual({job for job, result in ui.items() if result == "success"}, {"contracts", "provider-portals"})
        self.assertTrue(all(result == "skipped" for result in expected_jobs("e2e", "docs", "pull_request").values()))
        self.assertTrue(all(result == "skipped" for result in expected_jobs("e2e", "provider-ui", "pull_request", True).values()))
        self.assertEqual(expected_jobs("images", "full", "pull_request", True),
                         {"build-and-push-hub-image": "skipped", "build-and-push-agent-image": "skipped",
                          "build-and-push-provider-images": "success"})
        self.assertEqual(expected_jobs("images", "full", "pull_request", False)["build-and-push-provider-images"], "skipped")
        self.assertEqual(expected_jobs("images", "full", "push", True),
                         {"build-and-push-hub-image": "success", "build-and-push-agent-image": "success",
                          "build-and-push-provider-images": "skipped"})
        self.assertTrue(all(result == "success" for result in expected_jobs("images", "full", "workflow_dispatch", True).values()))

    def test_missing_or_untracked_dependency_and_invalid_mode_fail_closed(self):
        needs = valid_needs("ci", "full", "pull_request", True)
        del needs["lint"]
        self.assertTrue(check_results("ci", "pull_request", needs))
        needs = valid_needs("ci", "full", "pull_request", True)
        needs["forgotten-job"] = {"result": "success"}
        self.assertTrue(check_results("ci", "pull_request", needs))
        for mode, event, has_images in [("", "pull_request", False), ("unknown", "pull_request", False),
                                        ("provider-ui", "push", True), ("docs", "push", False),
                                        ("docs", "pull_request", True)]:
            with self.assertRaises(ValueError):
                expected_jobs("ci", mode, event, has_images)
        self.assertEqual(set(MODES), {"full", "provider-ui", "docs"})

    def test_completion_cli_returns_failure(self):
        needs = valid_needs("e2e", "provider-ui", "pull_request", True)
        command = [sys.executable, str(ROOT / "hack/ci/result.py"), "e2e"]
        env = dict(os.environ, GITHUB_EVENT_NAME="pull_request", CI_NEEDS=json.dumps(needs))
        self.assertEqual(subprocess.run(command, env=env, capture_output=True).returncode, 0)
        needs = valid_needs("e2e", "full", "pull_request", True)
        needs["e2e-external-kcp"]["result"] = "failure"
        env["CI_NEEDS"] = json.dumps(needs)
        self.assertNotEqual(subprocess.run(command, env=env, capture_output=True).returncode, 0)


class WorkflowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflows = {name: load(name) for name in POLICIES}
        cls.helm = load("helm-images")
        cls.macos = load("macos")
        cls.cache_action = yaml.safe_load((ROOT / ".github/actions/setup-go-cache/action.yml").read_text())

    def test_full_history_shared_action_and_unfiltered_dispatch(self):
        for name, workflow in self.workflows.items():
            with self.subTest(workflow=name):
                events = workflow.get("on", workflow.get(True))  # PyYAML's YAML 1.1 'on' boolean
                self.assertIn("workflow_dispatch", events)
                self.assertNotIn("paths", events["pull_request"])
                self.assertNotIn("paths-ignore", events["pull_request"])
                changes = workflow["jobs"]["changes"]
                self.assertEqual(changes["permissions"], {"contents": "read"})
                self.assertEqual(changes["steps"][0]["with"]["fetch-depth"], 0)
                self.assertEqual(changes["steps"][1]["uses"], "./.github/actions/ci-changes")
                self.assertEqual(set(changes["outputs"]),
                                 {"mode", "providers", "portal-matrix", "image-matrix", "has-images"})
                concurrency = workflow["concurrency"]
                self.assertEqual(concurrency["cancel-in-progress"], "${{ github.event_name == 'pull_request' }}")
                self.assertEqual(concurrency["group"], "${{ github.workflow }}-${{ github.event_name == 'pull_request' && github.event.pull_request.number || github.run_id }}")
        action = yaml.safe_load((ROOT / ".github/actions/ci-changes/action.yml").read_text())
        self.assertEqual(action["runs"]["using"], "composite")
        self.assertEqual(action["runs"]["steps"][0]["run"], "python3 hack/ci/selection.py")
        self.assertEqual(set(action["outputs"]), {"mode", "providers", "portal-matrix", "image-matrix", "has-images"})

    def test_completion_covers_every_job_and_conditions_match_policy(self):
        for name, workflow in self.workflows.items():
            jobs = workflow["jobs"]
            with self.subTest(workflow=name):
                self.assertEqual(set(jobs["result"]["needs"]), set(jobs) - {"result"})
                self.assertEqual(set(POLICIES[name]), set(jobs) - {"result", "changes"})
                self.assertEqual(jobs["result"]["if"], "always()")
                step = jobs["result"]["steps"][-1]
                self.assertEqual(step["env"]["CI_NEEDS"], "${{ toJSON(needs) }}")
                self.assertEqual(step["run"], f"python3 hack/ci/result.py {name}")
                for mode, event, has_images in SCENARIOS:
                    for job, result in expected_jobs(name, mode, event, has_images).items():
                        config = jobs[job]
                        self.assertEqual(bool(expression(config.get("if", True), mode, event, has_images)),
                                         result == "success", (name, mode, event, has_images, job))
                        if "needs.changes" in str(config):
                            self.assertIn("changes", config["needs"])

    def test_dynamic_matrices(self):
        ci = self.workflows["ci"]["jobs"]
        images = self.workflows["images"]["jobs"]
        self.assertEqual(ci["provider-portals"]["strategy"]["matrix"], "${{ fromJSON(needs.changes.outputs.portal-matrix) }}")
        self.assertEqual(images["build-and-push-provider-images"]["strategy"]["matrix"], "${{ fromJSON(needs.changes.outputs.image-matrix) }}")
        modules = {entry["module"] for entry in ci["test-modules"]["strategy"]["matrix"]["include"]}
        self.assertEqual(modules, {
            ".", "provider-sdk", "providers/agents", "providers/app-studio", "providers/code",
            "providers/edges", "providers/infrastructure", "providers/infrastructure/dev-agent",
            "providers/kuery", "providers/quickstart"})

    def test_contracts_job_carries_every_cheap_gate(self):
        contracts = self.workflows["ci"]["jobs"]["contracts"]
        self.assertNotIn("if", contracts)
        self.assertNotIn("needs", contracts)
        commands = "\n".join(step["run"] for step in contracts["steps"] if "run" in step)
        for command in ("make verify-boilerplate", "make verify-design-docs", "make verify-tilt-browser-deployment",
                        "make verify-portalkit", "make verify-ui-conformance", "make verify-provider-contract",
                        "npm test && npm run test:install", "make test-app-studio-harness-scripts",
                        "make test-tilt-checkout", "make test-tilt-app-studio-sequence",
                        "python3 hack/scripts/verify-tilt-sandbox-default.test.py",
                        "python3 -m pip install -r hack/ci/requirements-test.txt",
                        "make verify-ci-selection", "make verify-workflows", "make verify-charts"):
            self.assertIn(command, commands)
        self.assertIn("make lint-app-studio-harness",
                      [step["run"] for step in self.workflows["ci"]["jobs"]["lint"]["steps"] if "run" in step])

    def test_go_jobs_share_the_cached_toolchain_and_only_main_writes_caches(self):
        savers = {}
        for name in GO_WORKFLOWS:
            workflow = self.workflows.get(name) or self.macos
            with self.subTest(workflow=name):
                self.assertEqual(list(steps_using(workflow, "actions/setup-go")), [],
                                 "use ./.github/actions/setup-go-cache so every job shares the caches")
                self.assertEqual(list(steps_using(workflow, "./.github/actions/free-disk-space")), [],
                                 "the disk-cleanup action was removed; runners have ~86 GB free")
            for job_name, job in workflow["jobs"].items():
                for step in job.get("steps", []):
                    if step.get("uses") == "./.github/actions/setup-go-cache":
                        inputs = step.get("with", {})
                        if str(inputs.get("save-mod", "false")) != "false":
                            savers.setdefault("mod", []).append((name, job_name))
                        if str(inputs.get("save-build", "false")) != "false":
                            savers.setdefault(inputs.get("build-scope", "build"), []).append((name, job_name))
        self.assertEqual(savers, {"mod": [("ci", "build")], "build": [("ci", "build")],
                                  "race": [("ci", "test-modules")], "lint": [("ci", "lint")],
                                  "darwin": [("macos", "darwin")]})
        self.assertEqual(self.cache_action["runs"]["using"], "composite")
        for step in self.cache_action["runs"]["steps"]:
            if step.get("uses", "").startswith("actions/cache@"):
                self.assertIn("github.ref == 'refs/heads/main'", step["if"])
            if step.get("uses", "").startswith("actions/cache"):
                self.assertIn("restore-keys", step["with"])

    def test_e2e_kind_suites_consume_the_shared_build_artifact(self):
        e2e = self.workflows["e2e"]["jobs"]
        self.assertIn("make e2e-images", [step["run"] for step in e2e["build"]["steps"] if "run" in step])
        upload = next(step for step in e2e["build"]["steps"] if step.get("uses", "").startswith("actions/upload-artifact"))
        self.assertEqual(upload["with"]["name"], "e2e-build")
        for job, target in [("e2e-standalone", "e2e-standalone-run"), ("e2e-ssh", "e2e-ssh-run"),
                            ("e2e-oidc", "e2e-oidc-run"), ("e2e-external-kcp", "e2e-external-kcp-run")]:
            with self.subTest(job=job):
                self.assertIn("build", e2e[job]["needs"])
                download = next(step for step in e2e[job]["steps"] if step.get("uses", "").startswith("actions/download-artifact"))
                self.assertEqual(download["with"]["name"], "e2e-build")
                commands = [step["run"] for step in e2e[job]["steps"] if "run" in step]
                self.assertTrue(any(f"make {target} " in command for command in commands), commands)
                self.assertTrue(any("docker load -i images.tar" in command for command in commands))
                self.assertFalse(any("make build" in command or "docker build" in command for command in commands))

    def test_image_and_helm_publishing_only_on_original_events(self):
        images = self.workflows["images"]["jobs"]
        for event in ("pull_request", "workflow_dispatch", "push", "release"):
            for name in ("build-and-push-hub-image", "build-and-push-agent-image", "build-and-push-provider-images"):
                job = images[name]
                publish = name != "build-and-push-provider-images" and event in ("push", "release")
                for step in job["steps"]:
                    if step.get("uses", "").startswith("docker/login-action"):
                        self.assertEqual(bool(expression(step["if"], "full", event)), publish)
                    if step.get("uses", "").startswith("docker/build-push-action"):
                        self.assertEqual(bool(expression(step["with"]["push"], "full", event)), publish)
                        platforms = step["with"]["platforms"]
                        if platforms.startswith("${{"):
                            platforms = expression(platforms, "full", event)
                        self.assertEqual(platforms, "linux/amd64,linux/arm64" if publish else "linux/amd64")
                        if name == "build-and-push-provider-images":
                            self.assertNotIn("cache-to", step["with"], "PR refs must not write the layer cache")
        events = self.helm.get("on", self.helm.get(True))
        self.assertEqual(set(events), {"push", "workflow_dispatch"})
        self.assertNotIn("changes", self.helm["jobs"])
        for event in ("workflow_dispatch", "push"):
            for step in self.helm["jobs"]["publish-charts"]["steps"]:
                if step.get("uses", "").startswith("docker/login-action"):
                    self.assertEqual(bool(expression(step["if"], "full", event)), event == "push")
                if "PUSH" in step.get("env", {}):
                    self.assertEqual(bool(expression(step["env"]["PUSH"], "full", event)), event == "push")

    def test_macos_runs_only_the_darwin_specific_half_on_relevant_paths(self):
        events = self.macos.get("on", self.macos.get(True))
        for event in ("push", "pull_request"):
            self.assertEqual(events[event]["paths"], events["push"]["paths"])
            self.assertTrue({"cmd/**", "pkg/**", "go.mod", "go.sum", "Makefile"} <= set(events[event]["paths"]))
        commands = [step["run"] for step in self.macos["jobs"]["darwin"]["steps"] if "run" in step]
        self.assertEqual(commands, ["make build-macos-agent", "make build-macos-stub", "make test-macos-agent"])
        self.assertEqual(list(steps_using(self.macos, "actions/setup-node")), [])


if __name__ == "__main__":
    unittest.main()
