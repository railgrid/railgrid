#!/usr/bin/env python3
"""Print the CI selection for a Prow job as JSON.

Same classifier as the GitHub Actions `changes` job (hack/ci/selection.py), fed
from Prow's job environment instead of a GitHub event payload: a presubmit is a
pull_request whose diff is PULL_BASE_SHA...PULL_PULL_SHA; anything else
(postsubmit, periodic, a local run) validates everything.
"""

import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import selection  # noqa: E402


def main():
    paths = None
    event = "push"
    if os.environ.get("JOB_TYPE") == "presubmit":
        event = "pull_request"
        paths = selection.changed_paths(os.environ.get("PULL_BASE_SHA"), os.environ.get("PULL_PULL_SHA"))
    print(json.dumps(selection.selection(event, paths, os.environ.get("REPO_OWNER", "railgrid"))))


if __name__ == "__main__":
    main()
