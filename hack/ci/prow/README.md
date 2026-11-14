# Prow CI

Prow runs on the railgrid-dev cluster at https://prow-dev.railgrid.ai (log in
with GitHub; railgrid org members only). Its deployment and configuration live
in `railgrid/ops` (`cicd/apps/railgrid-dev/prow/`). This repository owns the
job definitions and what they run.

| File | What |
| ---- | ---- |
| `/.prow.yaml` | The jobs: name, trigger, image, resources. Read from the PR's own HEAD |
| `hack/ci/prow/job.sh` | What each job runs. One `case` per job |
| `hack/ci/prow/mode.py` | CI selection (full / provider-ui / docs) from Prow's environment, using `hack/ci/selection.py` |
| `hack/ci/prow/image/` | The CI image: `Dockerfile`, `runner.sh` (its entry point), `VERSION` (its tag) |
| `/.github/workflows/ci-image.yaml` | Builds and pushes the CI image |
| `/OWNERS` | Who can approve |

## Status: the pull request gate

Prow validates pull requests to `main`. No Prow job is `optional`, so Tide
does not merge past a red one; an approver can waive a job on one PR with
`/override pull-railgrid-<job>`. Tide tests a pull request against the current
`main` before it merges it, so there is no separate post-merge run.

What is left on GitHub Actions:

| Workflow | Why it stays |
| -------- | ------------ |
| `goreleaser.yaml`, `images.yaml`, `helm-images.yaml`, `provider-release.yaml`, `actions-node-release.yaml`, `auth-node-release.yaml` | Releases: binaries, images, charts, packages. `goreleaser.yaml` also dry-runs on a pull request that changes the release configuration |
| `split-*.yaml`, `infrastructure-examples.yaml` | Publishing on pushes to `main`: the provider mirrors and the example images |
| `ci-image.yaml` | Builds the image the Prow jobs run in |
| `macos.yaml` | Needs a macOS runner. The only validation that still starts on pull requests |
| `ci.yaml`, `e2e.yaml`, `infrastructure-templates-e2e.yaml`, `tilt-e2e.yaml` | Manual dispatch only: the fallback when the Prow cluster is unavailable |

`hack/ci/test_workflows.py` fails if any other workflow starts on
`pull_request`. Every check those workflows ran on pull requests has a Prow
job:

| Was, on pull requests | Prow job |
| --------------------- | -------- |
| `images.yaml` provider images, `infrastructure-examples.yaml` | `pull-railgrid-images` |
| `infrastructure-templates-e2e.yaml` | `pull-railgrid-e2e-infrastructure-templates` |
| `split-*.yaml` dry run | `pull-railgrid-split` |

No job is optional or path-triggered: all of them start on every pull request
that is not docs-only, and Tide waits for each. These three narrow their work
inside `job.sh` to what the pull request touched (the workflows' old path
filters and image selection) and succeed at once when that is nothing.

## Working with it

| Comment on a PR | Effect |
| --------------- | ------ |
| `/test all` or `/test pull-railgrid-build` | Run all jobs, or one |
| `/retest` | Re-run failed jobs |
| `/ok-to-test` | Org member: allow tests on a PR from outside the org |
| `/approve`, `/approve cancel` | Approver from `OWNERS`: add or remove `approved` |
| `/hold`, `/hold cancel` | Stop or resume merging |

Tide merges a PR when it has `approved`, no `do-not-merge/*` label, and every
status and check on it is green, the GitHub Actions ones included. An
approver's own PR needs `/approve` too; nothing is approved implicitly. Use
`/hold` to stop a merge.

**`/ok-to-test` is a security decision.** The kind suites run privileged on
the same nodes as the dev platform. Before allowing an outside PR, read its
diff, including `.prow.yaml` and everything under `hack/ci/prow/`.

## Differences from the Actions jobs

- One pod per job, no matrix and no artifacts between jobs: the module matrix
  is a loop (`test-providers`), and each kind suite builds `bin/` and the
  images itself.
- No service containers: `test-providers` starts PostgreSQL 16 from the image.
- No shared Go caches yet: every job downloads modules and compiles cold.
- Jobs run as user `ci` with passwordless sudo; Docker for the kind suites
  runs inside the job pod, and `runner.sh` keeps its containers inside the
  pod's cgroup so the job's limits apply to builds and kind nodes too.
- The cluster is three small machines: Prow runs a few jobs at a time, so a
  full run takes much longer than on Actions.

## Running a job locally

```bash
docker build -t railgrid-ci hack/ci/prow/image
docker run --rm -v "$PWD:/src:ro" railgrid-ci bash -c \
  'git clone -q /src /work && cd /work && runner.sh hack/ci/prow/job.sh build'
# kind suites: add --privileged -e DOCKER_IN_DOCKER_ENABLED=true -v /var/lib/docker
# (dockerd needs /var/lib/docker on a volume, as the Prow preset provides)
```

Uncommitted changes are not in the clone; commit first.

## Changing the image

Bump `hack/ci/prow/image/VERSION` and the image tag in `.prow.yaml` together.
The image is built only after merge (`ci-image.yaml`), so jobs on that PR and
on main fail to pull the new tag until the workflow finishes. Keep `GO_VERSION`
in the Dockerfile on the root `go.mod`'s `go` line. The package
`ghcr.io/railgrid/railgrid-ci` must be public: the cluster pulls it
anonymously.
