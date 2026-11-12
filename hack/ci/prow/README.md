# Prow CI

Prow runs on the railgrid-dev cluster at https://prow-dev.railgrid.ai (log in
with GitHub; railgrid org members only). Its deployment and configuration live
in `railgrid/ops` (`cicd/apps/railgrid-dev/prow/`). This repository owns the
job definitions and what they run.

| File | What |
| ---- | ---- |
| `/.prow.yaml` | The jobs: name, trigger, image, resources. Read from the PR's own HEAD |
| `hack/ci/prow/job.sh` | What each job runs. One `case` per job, mirroring a GitHub Actions job |
| `hack/ci/prow/mode.py` | CI selection (full / provider-ui / docs) from Prow's environment, using `hack/ci/selection.py` |
| `hack/ci/prow/image/` | The CI image: `Dockerfile`, `runner.sh` (its entry point), `VERSION` (its tag) |
| `/.github/workflows/ci-image.yaml` | Builds and pushes the CI image |
| `/OWNERS` | Who can approve |

## Status: shadowing GitHub Actions

Every job is `optional: true`. Prow reports results on the PR next to the
Actions checks and blocks nothing. The plan is to compare the two for a while,
then make jobs required one at a time and drop the matching Actions job.
macOS, releases and image publishing stay on Actions.

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
approver's own PR is approved implicitly, so for a maintainer "green" means
"merges". Use `/hold` to stop that.

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
