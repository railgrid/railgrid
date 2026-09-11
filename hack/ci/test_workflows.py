"""Check completion behavior and the actual workflow wiring, not a second plan."""

import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

import yaml

from result import POLICIES, check_results, expected_jobs


ROOT = Path(__file__).resolve().parents[2]


def expression(value, mode, event):
    """Evaluate the small condition vocabulary used by these workflows in tests."""
    if isinstance(value, bool):
        return value
    value = value.removeprefix("${{").removesuffix("}}").strip()
    for variable, replacement in {
        "github.event_name": event, "needs.changes.outputs.mode": mode,
    }.items():
        value = value.replace(variable, repr(replacement))
    value = value.replace("&&", " and ").replace("||", " or ")
    return eval(value, {"__builtins__": {}}, {})


def valid_needs(workflow, mode, event):
    return {"changes": {"result": "success", "outputs": {"mode": mode}},
            **{job: {"result": result} for job, result in expected_jobs(workflow, mode, event).items()}}


class CompletionTests(unittest.TestCase):
    def test_every_selected_job_must_succeed_and_excluded_jobs_must_skip(self):
        scenarios = [("full", event) for event in ("pull_request", "push", "release", "workflow_dispatch")]
        scenarios.append(("provider-ui", "pull_request"))
        for workflow in POLICIES:
            for mode, event in scenarios:
                with self.subTest(workflow=workflow, mode=mode, event=event):
                    needs = valid_needs(workflow, mode, event)
                    self.assertEqual(check_results(workflow, event, needs), [])
                    for job, required in expected_jobs(workflow, mode, event).items():
                        for state in ("success", "skipped", "failure", "cancelled"):
                            changed = copy.deepcopy(needs)
                            changed[job]["result"] = state
                            self.assertEqual(bool(check_results(workflow, event, changed)), state != required,
                                             (workflow, mode, event, job, state))
                    for state in ("skipped", "failure", "cancelled"):
                        changed = copy.deepcopy(needs)
                        changed["changes"]["result"] = state
                        self.assertTrue(check_results(workflow, event, changed))

    def test_missing_or_untracked_dependency_and_invalid_mode_fail_closed(self):
        needs = valid_needs("ci", "full", "pull_request")
        del needs["lint"]
        self.assertTrue(check_results("ci", "pull_request", needs))
        needs = valid_needs("ci", "full", "pull_request")
        needs["forgotten-job"] = {"result": "success"}
        self.assertTrue(check_results("ci", "pull_request", needs))
        for mode, event in [("", "pull_request"), ("unknown", "pull_request"), ("provider-ui", "push")]:
            with self.assertRaises(ValueError):
                expected_jobs("ci", mode, event)

    def test_completion_cli_returns_failure(self):
        needs = valid_needs("e2e", "provider-ui", "pull_request")
        command = [sys.executable, str(ROOT / "hack/ci/result.py"), "e2e"]
        env = dict(os.environ, GITHUB_EVENT_NAME="pull_request", CI_NEEDS=json.dumps(needs))
        self.assertEqual(subprocess.run(command, env=env, capture_output=True).returncode, 0)
        needs["e2e-external-kcp"]["result"] = "failure"
        env["CI_NEEDS"] = json.dumps(needs)
        self.assertNotEqual(subprocess.run(command, env=env, capture_output=True).returncode, 0)


class WorkflowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflows = {name: yaml.safe_load((ROOT / f".github/workflows/{name}.yaml").read_text())
                         for name in POLICIES}

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
                self.assertEqual(set(changes["outputs"]), {"mode", "providers", "portal-matrix", "image-matrix"})
                concurrency = workflow["concurrency"]
                self.assertEqual(concurrency["cancel-in-progress"], "${{ github.event_name == 'pull_request' }}")
                self.assertEqual(concurrency["group"], "${{ github.workflow }}-${{ github.event_name == 'pull_request' && github.event.pull_request.number || github.run_id }}")
        action = yaml.safe_load((ROOT / ".github/actions/ci-changes/action.yml").read_text())
        self.assertEqual(action["runs"]["using"], "composite")
        self.assertEqual(action["runs"]["steps"][0]["run"], "python3 hack/ci/selection.py")

    def test_completion_covers_every_job_and_conditions_match_policy(self):
        for name, workflow in self.workflows.items():
            jobs = workflow["jobs"]
            with self.subTest(workflow=name):
                self.assertEqual(set(jobs["result"]["needs"]), set(jobs) - {"result"})
                self.assertEqual(set().union(*POLICIES[name].values()), set(jobs) - {"result", "changes"})
                self.assertEqual(jobs["result"]["if"], "always()")
                step = jobs["result"]["steps"][-1]
                self.assertEqual(step["env"]["CI_NEEDS"], "${{ toJSON(needs) }}")
                self.assertEqual(step["run"], f"python3 hack/ci/result.py {name}")
                for mode, event in [("provider-ui", "pull_request"), ("full", "pull_request"),
                                    ("full", "push"), ("full", "release"), ("full", "workflow_dispatch")]:
                    for job, result in expected_jobs(name, mode, event).items():
                        config = jobs[job]
                        self.assertEqual(bool(expression(config.get("if", True), mode, event)), result == "success",
                                         (name, mode, event, job))
                        if "needs.changes" in str(config):
                            self.assertIn("changes", config["needs"])

    def test_dynamic_matrices_and_retained_security(self):
        ci = self.workflows["ci"]["jobs"]
        images = self.workflows["images"]["jobs"]
        self.assertEqual(ci["provider-portals"]["strategy"]["matrix"], "${{ fromJSON(needs.changes.outputs.portal-matrix) }}")
        self.assertEqual(images["build-and-push-provider-images"]["strategy"]["matrix"], "${{ fromJSON(needs.changes.outputs.image-matrix) }}")
        self.assertNotIn("if", ci["govulncheck"])
        self.assertEqual(ci["govulncheck"]["strategy"]["matrix"]["module"], [
            ".", "provider-sdk", "providers/agents", "providers/app-studio", "providers/code",
            "providers/databricks", "providers/edges", "providers/infrastructure",
            "providers/kuery", "providers/quickstart"])
        for mode in ("provider-ui", "full"):
            self.assertEqual(expected_jobs("ci", mode, "pull_request")["govulncheck"], "success")
        verification = ci["verify-ci-selection"]
        self.assertNotIn("if", verification)
        self.assertEqual([s["run"] for s in verification["steps"] if "run" in s],
                         ["python3 -m pip install -r hack/ci/requirements-test.txt",
                          "make verify-ci-selection", "make verify-workflows"])

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
        helm = self.workflows["helm-images"]["jobs"]["build-hub"]
        for event in ("pull_request", "workflow_dispatch", "push"):
            for step in helm["steps"]:
                if step.get("uses", "").startswith("docker/login-action"):
                    self.assertEqual(bool(expression(step["if"], "full", event)), event == "push")
                if "PUSH" in step.get("env", {}):
                    self.assertEqual(bool(expression(step["env"]["PUSH"], "full", event)), event == "push")


if __name__ == "__main__":
    unittest.main()
