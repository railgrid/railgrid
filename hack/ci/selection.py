#!/usr/bin/env python3
"""Select provider-portal CI from the complete PR diff; default to full CI."""

import json
import os
from pathlib import Path
import re
import subprocess


MATRICES = json.loads(Path(__file__).with_name("matrices.json").read_text())
PROVIDERS = {entry["provider"] for entry in MATRICES["portals"]}


def classify(event_name, paths):
    if event_name != "pull_request":
        return "full", [], "Non-PR events run full validation."
    if not paths:
        return "full", [], "No complete, nonempty PR diff is available."
    affected = set()
    for path in paths:
        parts = path.split("/")
        if (len(parts) < 4 or parts[0] != "providers"
                or parts[1] not in PROVIDERS or parts[2] != "portal"
                or any(part in ("", ".", "..") for part in parts)):
            return "full", [], "The diff includes files outside known provider portals."
        affected.add(parts[1])
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


def selection(event_name, paths, owner):
    mode, affected, reason = classify(event_name, paths)
    portals = [entry for entry in MATRICES["portals"]
               if mode == "full" or entry["provider"] in affected]
    images = [dict(entry, image=f"{owner}/{entry['image']}") for entry in MATRICES["images"]
              if mode == "full" or entry["name"] in affected]
    return {"mode": mode, "providers": affected, "reason": reason,
            "portal-matrix": {"include": portals}, "image-matrix": {"include": images}}


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
    summary = (f"### CI selection: {result['mode']}\n\n{result['reason']}\n\n"
               f"Affected providers: {', '.join(result['providers']) or 'full matrix'}\n")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as output:
        output.write(summary)
    print(summary)


if __name__ == "__main__":
    main()
