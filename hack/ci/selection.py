#!/usr/bin/env python3
"""Select CI from the complete PR diff; default to full CI.

Three modes, decided from every path the PR changes relative to its merge base:

  full         anything else, every non-PR event, and any diff we cannot read
  provider-ui  every path is inside a known provider's portal/ directory
  docs         every path is under docs/ or is a Markdown file

The image matrix is narrowed in every mode: an image is built only when the
PR touched one of the build-context inputs matrices.json lists for it (or the
selection machinery itself). Non-PR events and unreadable diffs select every
image, so a manual run still validates all of them.
"""

import json
import os
from pathlib import Path
import re
import subprocess


MATRICES = json.loads(Path(__file__).with_name("matrices.json").read_text())
PROVIDERS = {entry["provider"] for entry in MATRICES["portals"]}
# A change to the selection itself re-selects every image.
SELECTION_INPUTS = ("hack/ci/", ".github/workflows/images.yaml", ".github/actions/ci-changes/")


def unsafe(path):
    return any(part in ("", ".", "..") for part in path.split("/"))


def is_docs(path):
    return path.startswith("docs/") or path.endswith(".md")


def portal_provider(path):
    parts = path.split("/")
    if len(parts) >= 4 and parts[0] == "providers" and parts[1] in PROVIDERS and parts[2] == "portal":
        return parts[1]
    return None


def classify(event_name, paths):
    if event_name != "pull_request":
        return "full", [], "Non-PR events run full validation."
    if not paths:
        return "full", [], "No complete, nonempty PR diff is available."
    if any(unsafe(path) for path in paths):
        return "full", [], "The diff includes a path that cannot be classified."
    if all(is_docs(path) for path in paths):
        return "docs", [], "Every changed file is documentation."
    affected = {portal_provider(path) for path in paths}
    if None in affected:
        return "full", [], "The diff includes files outside known provider portals."
    return "provider-ui", sorted(affected), "Every changed file belongs to a known provider portal."


def changed_paths(base, head, cwd=None):
    # Only immutable commit IDs from the event payload are accepted as revisions.
    if not all(re.fullmatch(r"[0-9a-fA-F]{40}", sha or "") for sha in (base, head)):
        return None
    try:
        merge_base = subprocess.run(
            ["git", "merge-base", base, head], cwd=cwd, check=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        ).stdout.strip().decode("ascii")
        # Disable rename detection so BOTH old and new paths are classified.
        # NUL separation preserves whitespace/newlines and avoids API pagination limits.
        diff = subprocess.run(
            ["git", "diff", "--no-ext-diff", "--no-renames", "--name-only", "-z",
             merge_base, head, "--"], cwd=cwd, check=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        ).stdout
    except subprocess.CalledProcessError:
        return None
    return [path.decode("utf-8", errors="surrogateescape") for path in diff.split(b"\0") if path]


def image_selected(entry, mode, affected, paths):
    if mode == "docs":
        return False
    if mode == "provider-ui":
        # Portal edits select the provider's main image, never its companions.
        return entry["name"] in affected
    if not paths:
        return True
    inputs = tuple(entry["inputs"]) + SELECTION_INPUTS
    return any(path.startswith(inputs) for path in paths)


def selection(event_name, paths, owner):
    if event_name != "pull_request":
        paths = None  # non-PR events validate everything, whatever they changed
    mode, affected, reason = classify(event_name, paths)
    portals = [entry for entry in MATRICES["portals"]
               if mode == "full" or entry["provider"] in affected]
    images = [{key: value for key, value in entry.items() if key != "inputs"} | {"image": f"{owner}/{entry['image']}"}
              for entry in MATRICES["images"] if image_selected(entry, mode, affected, paths)]
    return {"mode": mode, "providers": affected, "reason": reason,
            "portal-matrix": {"include": portals}, "image-matrix": {"include": images},
            "has-images": bool(images)}


def main():
    event_name = os.environ["GITHUB_EVENT_NAME"]
    paths = None
    if event_name == "pull_request":
        event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
        pr = event.get("pull_request", {})
        paths = changed_paths(pr.get("base", {}).get("sha"), pr.get("head", {}).get("sha"))
    result = selection(event_name, paths, os.environ["GITHUB_REPOSITORY_OWNER"])
    # JSON encoding keeps outputs single-line even for unusual data. Unexpected
    # execution/payload/output errors propagate and fail the detection job.
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        for key, value in result.items():
            output.write(f"{key}={value if isinstance(value, str) else json.dumps(value)}\n")
    images = ", ".join(entry["name"] for entry in result["image-matrix"]["include"]) or "none"
    summary = (f"### CI selection: {result['mode']}\n\n{result['reason']}\n\n"
               f"Affected providers: {', '.join(result['providers']) or 'full matrix'}\n\n"
               f"Provider images: {images}\n")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as output:
        output.write(summary)
    print(summary)


if __name__ == "__main__":
    main()
