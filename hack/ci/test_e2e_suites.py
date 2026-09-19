"""Check every e2e suite is actually executed, not merely runnable.

result.py guards the job inventory: a new workflow job cannot escape the
completion gate. Nothing guarded the layer below it — whether a suite under
test/e2e/suites/ is reached by any job at all. A suite with a working Makefile
target and no caller is invisible: it compiles, `make` runs it on demand, and
CI is green forever without it. That is how providerflags went dark while
.github/copilot-instructions.md still named it a suite reviewers must protect.

The check walks the real tree rather than a second plan: suite directories from
disk, target-to-suite from the Makefile recipes, invocations from the workflow
files. A suite that should not run on every change belongs in MANUAL_SUITES
with the reason, so the exclusion is a decision on the record instead of an
oversight.
"""

import re
import unittest
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[2]
SUITES = ROOT / "test/e2e/suites"
WORKFLOWS = ROOT / ".github/workflows"

# Suites deliberately not wired into per-change CI, each with the reason it is
# excluded and how it is meant to run instead. Removing an entry is how you
# promote a suite back into CI; adding one requires justifying the gap here.
MANUAL_SUITES = {
    "tiltcluster": (
        "Tilt/kind cluster suites are driven by tilt-e2e.yaml, which is "
        "workflow_dispatch-only on purpose: they need a live Tilt cluster and "
        "cloud credentials (GCP/Config Connector, Terraform)."
    ),
    "installembedded": (
        "Helm install path; E2E_INSTALL_TIMEOUT is 45m, too heavy for every PR. "
        "TODO: wire into a nightly `schedule:` workflow — the repo currently has "
        "no scheduled trigger, so this suite runs nowhere."
    ),
    "installexternal": (
        "External-kcp Helm install path; same 45m cost as installembedded. "
        "TODO: nightly, as above."
    ),
}


def suite_dirs():
    """Every suite directory that actually holds Go tests."""
    return {path.name for path in SUITES.iterdir()
            if path.is_dir() and any(path.glob("*_test.go"))}


def target_suites():
    """Map each `e2e-*` Makefile target to the suite directories it runs.

    Recipes are the source of truth: a target counts as running a suite only if
    its own recipe names the package path. Aggregate targets (e2e-provider-all)
    contribute through their prerequisites, resolved below.
    """
    makefile = (ROOT / "Makefile").read_text()
    direct, prereqs = {}, {}
    for match in re.finditer(r"^(e2e-[\w-]+):([^\n]*)\n((?:\t[^\n]*\n|\n)*)", makefile, re.M):
        target, deps, recipe = match.group(1), match.group(2), match.group(3)
        direct[target] = set(re.findall(r"test/e2e/suites/(\w+)", recipe))
        prereqs[target] = [d for d in deps.split() if d.startswith("e2e-")]

    def resolve(target, seen=None):
        seen = seen or set()
        if target in seen:
            return set()
        seen.add(target)
        return direct.get(target, set()).union(
            *(resolve(dep, seen) for dep in prereqs.get(target, [])), set())

    return {target: resolve(target) for target in direct}


def runs_automatically(workflow):
    """True when a workflow fires on its own, rather than only by hand.

    workflow_dispatch alone means a human has to remember, which for coverage
    purposes is the same as not running. PyYAML reads the `on:` key as the
    boolean True, so both spellings are checked.
    """
    triggers = workflow.get("on", workflow.get(True)) or {}
    if isinstance(triggers, str):
        triggers = {triggers: None}
    return bool({"pull_request", "push", "schedule"} & set(triggers))


def invoked_targets():
    """Every `make e2e-*` target reached by a workflow that runs on its own."""
    targets = set()
    for path in WORKFLOWS.glob("*.y*ml"):
        text = path.read_text()
        if runs_automatically(yaml.safe_load(text) or {}):
            targets |= set(re.findall(r"make\s+(e2e-[\w-]+)", text))
    return targets


def covered_suites():
    resolved = target_suites()
    return set().union(*(resolved.get(t, set()) for t in invoked_targets()), set())


class SuiteCoverageTests(unittest.TestCase):
    def test_every_suite_runs_in_ci_or_is_declared_manual(self):
        dark = sorted(suite_dirs() - covered_suites() - set(MANUAL_SUITES))
        self.assertEqual(dark, [], (
            f"e2e suites reached by no workflow: {dark}. Add a CI step that runs "
            f"the suite's make target, or record it in MANUAL_SUITES with the "
            f"reason it is excluded."))

    def test_manual_suites_exist_and_are_genuinely_excluded(self):
        """Keep the allowlist honest: no stale names, no needless suppression."""
        suites = suite_dirs()
        for name, reason in MANUAL_SUITES.items():
            with self.subTest(suite=name):
                self.assertIn(name, suites, f"MANUAL_SUITES lists {name!r}, which is not a suite")
                self.assertTrue(reason.strip(), f"{name} needs a reason")
                self.assertNotIn(name, covered_suites(),
                                 f"{name} runs in CI; remove it from MANUAL_SUITES")

    def test_every_suite_has_a_make_target(self):
        """A suite no target names cannot be run by anyone, in CI or locally."""
        runnable = set().union(*target_suites().values(), set())
        self.assertEqual(sorted(suite_dirs() - runnable), [])


if __name__ == "__main__":
    unittest.main()
