#!/usr/bin/env python3
"""Select the components a push to main publishes as a main build.

A main build of a component is its image at `sha-<commit>` plus its chart at
the fixed version 0.0.0-main, whose appVersion names that image tag
(.github/workflows/main-builds.yaml). The chart version never changes, so the
chart published last is also the record of what is deployed: its appVersion is
the commit the component was last built from.

A component is rebuilt when something in its build context changed between
that commit and HEAD. Comparing against what is published, rather than against
the previous push, means a run that failed or was superseded in the queue is
made up for by the next one, and a component that has never been published is
built the first time this runs.
"""

import json
import os
from pathlib import Path
import re
import subprocess

from selection import MATRICES, is_docs


ROOT = Path(__file__).resolve().parents[2]
CHART_VERSION = "0.0.0-main"
CHART_REGISTRY = "oci://ghcr.io/{owner}/charts"
# A change to the publishing machinery rebuilds every component.
REBUILD_ALL = (".github/workflows/main-builds.yaml", "hack/ci/main_builds.py", "hack/ci/matrices.json")
# The hub and the agent build from the whole repository (COPY . .) minus the
# providers, which are separate modules with their own images.
PLATFORM = [
    {"name": "hub", "image": "railgrid-hub", "dockerfile": "./deploy/Dockerfile.hub",
     "chart": "deploy/charts/railgrid-hub"},
    {"name": "agent", "image": "railgrid-agent", "dockerfile": "./deploy/Dockerfile.agent",
     "chart": "deploy/charts/railgrid-agent"},
]


def chart_name(chart_dir, root=ROOT):
    for line in (root / chart_dir / "Chart.yaml").read_text().splitlines():
        if line.startswith("name:"):
            return line.split(":", 1)[1].strip()
    raise ValueError(f"{chart_dir}/Chart.yaml has no name")


def components(root=ROOT):
    """Every component with both an image and a chart, platform first."""
    result = [dict(entry, platform=True) for entry in PLATFORM]
    for entry in MATRICES["images"]:
        chart = f"providers/{entry['name']}/deploy/chart"
        # Companion images (dev-agent, access-proxy, ...) have no chart of
        # their own and keep following the provider's release tags.
        if not (root / chart / "Chart.yaml").is_file():
            continue
        result.append({"name": entry["name"], "image": entry["image"],
                       "dockerfile": entry.get("dockerfile", f"./providers/{entry['name']}/Dockerfile"),
                       "chart": chart, "inputs": entry["inputs"], "platform": False})
    for entry in result:
        entry["chart-name"] = chart_name(entry["chart"], root)
    return result


def needs_build(component, paths):
    """paths: files changed since the published build, or None when unknown."""
    if paths is None:
        return True
    if any(path.startswith(REBUILD_ALL) for path in paths):
        return True
    if component["platform"]:
        return any(not is_docs(path) and not path.startswith("providers/") for path in paths)
    # The chart lives under providers/<name>/, so a chart-only change counts.
    return any(path.startswith(tuple(component["inputs"])) and not is_docs(path) for path in paths)


def published_revision(chart, owner):
    """The commit the published 0.0.0-main chart was built from, or None."""
    shown = subprocess.run(
        ["helm", "show", "chart", f"{CHART_REGISTRY.format(owner=owner)}/{chart}", "--version", CHART_VERSION],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
    if shown.returncode != 0:
        return None
    match = re.search(r'^appVersion:\s*"?sha-([0-9a-f]{7,40})"?\s*$', shown.stdout, re.MULTILINE)
    return match.group(1) if match else None


def changed_since(revision, cwd=None):
    """Paths changed between a published revision and HEAD; None if unreadable."""
    if not revision or not re.fullmatch(r"[0-9a-f]{7,40}", revision):
        return None
    try:
        # A revision that is not an ancestor of HEAD (a rewritten main) is not
        # a base to diff from: rebuild.
        subprocess.run(["git", "merge-base", "--is-ancestor", revision, "HEAD"], cwd=cwd, check=True,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        diff = subprocess.run(
            ["git", "diff", "--no-ext-diff", "--no-renames", "--name-only", "-z", revision, "HEAD", "--"],
            cwd=cwd, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout
    except subprocess.CalledProcessError:
        return None
    return [path.decode("utf-8", errors="surrogateescape") for path in diff.split(b"\0") if path]


def select(owner, force=False, revision=published_revision, changed=changed_since):
    matrix, report = [], []
    for component in components():
        published = None if force else revision(component["chart-name"], owner)
        paths = None if force else changed(published)
        build = needs_build(component, paths)
        report.append((component["name"], published or "none", build))
        if build:
            matrix.append({"name": component["name"], "image": f"{owner}/{component['image']}",
                           "dockerfile": component["dockerfile"], "chart": component["chart"],
                           "chart-name": component["chart-name"],
                           # Same scopes as the release workflows, so a release
                           # build starts from the layers main already built.
                           "cache": component["name"] if component["platform"] else f"{component['name']}-amd64"})
    return matrix, report


def main():
    owner = os.environ["GITHUB_REPOSITORY_OWNER"]
    matrix, report = select(owner, force=os.environ.get("FORCE_ALL") == "true")
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"matrix={json.dumps({'include': matrix})}\n")
        output.write(f"has-builds={'true' if matrix else 'false'}\n")
    rows = "\n".join(f"| {name} | {published} | {'build' if build else 'unchanged'} |"
                     for name, published, build in report)
    summary = f"### Main builds\n\n| Component | Published | This run |\n| - | - | - |\n{rows}\n"
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as output:
        output.write(summary)
    print(summary)


if __name__ == "__main__":
    main()
