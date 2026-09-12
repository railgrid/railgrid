#!/usr/bin/env python3
"""Require all selected jobs to succeed, including after failures or skips."""

import json
import os
import sys


# Keep this inventory aligned with each completion job's direct needs. Tests
# compare it with the workflows so new jobs cannot escape the completion gate.
POLICIES = {
    "ci": {
        "always": {"govulncheck", "verify-boilerplate", "verify-portalkit",
                   "verify-ui-conformance", "verify-design-docs",
                   "verify-tilt-browser-deployment", "provider-portals", "verify-ci-selection"},
        "full": {"build", "test-modules", "lint", "verify-codegen", "portal"},
    },
    "e2e": {
        "always": set(),
        "full": {"actions-node-sdk", "e2e-providers", "e2e-standalone", "e2e-ssh",
                 "e2e-oidc", "e2e-external-kcp", "e2e-edges-connectivity", "e2e-cli"},
    },
    "images": {
        "always": set(),
        "full": {"build-and-push-hub-image", "build-and-push-agent-image"},
        "validation": {"build-and-push-provider-images"},
    },
    "helm-images": {"always": set(), "full": {"build-hub"}},
}


def expected_jobs(workflow, mode, event):
    if mode not in ("full", "provider-ui"):
        raise ValueError("Missing or invalid selection mode")
    if mode == "provider-ui" and event != "pull_request":
        raise ValueError("Non-PR events must use full mode")
    policy = POLICIES[workflow]
    expected = {}
    for category, jobs in policy.items():
        selected = (category == "always" or (category == "full" and mode == "full")
                    or (category == "validation" and event in ("pull_request", "workflow_dispatch")))
        expected.update({job: "success" if selected else "skipped" for job in jobs})
    return expected


def check_results(workflow, event, needs):
    changes = needs.get("changes", {})
    if changes.get("result") != "success":
        return ["Change detection did not succeed"]
    expected = expected_jobs(workflow, changes.get("outputs", {}).get("mode"), event)
    errors = []
    if set(needs) != set(expected) | {"changes"}:
        errors.append("Completion dependencies do not match the validation job inventory")
    for job, required in expected.items():
        actual = needs.get(job, {}).get("result", "missing")
        if actual != required:
            errors.append(f"{job}: expected {required}, got {actual}")
    return errors


def main():
    errors = check_results(sys.argv[1], os.environ["GITHUB_EVENT_NAME"],
                           json.loads(os.environ["CI_NEEDS"]))
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print("All selected jobs succeeded; excluded jobs were skipped.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
