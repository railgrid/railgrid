#!/usr/bin/env python3
"""Require all selected jobs to succeed, including after failures or skips."""

import json
import os
import sys


# Every validation job of each managed workflow and the rule that selects it.
# Tests compare this inventory with the workflows' jobs, their `if`
# conditions and the completion job's direct needs, so a new job cannot escape
# the gate and a condition cannot drift from the policy here.
#
#   always   every mode, every event
#   ui       full and provider-ui modes (skipped by docs-only PRs)
#   full     full mode only
#   publish  full mode on events other than pull_request (the E2E build job
#            validates the hub and agent Dockerfiles on PRs)
#   images   whenever selection chose at least one provider image, on PRs and
#            manual runs (provider images are published by provider-release)
POLICIES = {
    "ci": {
        "contracts": "always",
        "provider-portals": "ui",
        "build": "full",
        "test-modules": "full",
        "test-app-studio-postgres": "full",
        "lint": "full",
        "verify-codegen": "full",
        "portal": "full",
    },
    "e2e": {
        "build": "full",
        "e2e-providers": "full",
        "e2e-standalone": "full",
        "e2e-ssh": "full",
        "e2e-oidc": "full",
        "e2e-external-kcp": "full",
        "e2e-edges-connectivity": "full",
        "e2e-cli": "full",
    },
    "images": {
        "build-and-push-hub-image": "publish",
        "build-and-push-agent-image": "publish",
        "build-and-push-provider-images": "images",
    },
}
MODES = ("full", "provider-ui", "docs")


def selected(rule, mode, event, has_images):
    return {
        "always": True,
        "ui": mode in ("full", "provider-ui"),
        "full": mode == "full",
        "publish": mode == "full" and event != "pull_request",
        "images": has_images and event in ("pull_request", "workflow_dispatch"),
    }[rule]


def expected_jobs(workflow, mode, event, has_images=False):
    if mode not in MODES:
        raise ValueError("Missing or invalid selection mode")
    if mode != "full" and event != "pull_request":
        raise ValueError("Non-PR events must use full mode")
    if mode == "docs" and has_images:
        raise ValueError("Docs-only PRs select no images")
    return {job: "success" if selected(rule, mode, event, has_images) else "skipped"
            for job, rule in POLICIES[workflow].items()}


def check_results(workflow, event, needs):
    changes = needs.get("changes", {})
    if changes.get("result") != "success":
        return ["Change detection did not succeed"]
    outputs = changes.get("outputs", {})
    expected = expected_jobs(workflow, outputs.get("mode"), event, outputs.get("has-images") == "true")
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
