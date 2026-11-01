# CI selection

The CI, E2E and container-image workflows always start on their existing PR
triggers. Each runs the shared `ci-changes` action with full checkout history.
`selection.py` compares the PR head with its merge base against the event's base
SHA; validation jobs continue using the normal PR merge checkout.

Three modes. A nonempty diff entirely inside the known providers' `portal/`
directories selects `provider-ui`. A diff made only of files under `docs/` and
Markdown files selects `docs`. Everything else, including unavailable history,
root portal, canonical PortalKit/AgentKit, backend changes, and non-PR events,
selects `full`. Both sides of renames and deleted paths count. Unexpected
classifier execution errors fail detection.

In every mode CI runs the `contracts` job: boilerplate, design metadata, Tilt
wiring, PortalKit/AgentKit parity, UI conformance, the provider contract, the
Actions Node SDK package test, the harness scripts, this policy's own tests,
Actionlint, and `helm lint` of every chart. In `provider-ui` mode CI adds the
affected portal checks and the image workflow builds the affected providers'
main images; E2E skips. In `docs` mode nothing else runs. In `full` mode
everything runs, but the image workflow still builds only the provider images
whose build-context inputs (`matrices.json`) the PR touched; a PR whose diff
cannot be read, and every manual run, builds them all. The hub and agent
images are validated on PRs by the E2E workflow's `build` job and built by the
image workflow only where it publishes (tags and releases).

`matrices.json` owns the portal flags and provider image metadata formerly
inline in the workflows. Image names are relative to the repository owner.
Each image lists the path prefixes that make up its Docker build context.
Update the matrix and its regression expectations together when adding
providers or changing what a Dockerfile copies.

Each workflow's result job requires successful detection, success for all
selected jobs, and skipped status for intentionally excluded jobs. `result.py`
owns this inventory as one selection rule per job; tests compare it with the
actual workflow jobs, direct dependencies, and `if` conditions so newly added
jobs cannot escape it. No repository rulesets are changed. If required checks
are introduced later, use the stable `CI result`, `E2E result` and
`Images result` names. The Helm workflow runs only where it publishes (push to
main and `v*` tags) and has no result job; chart validation on PRs is the
`contracts` job.

## Cost shape

Each job bills in whole minutes and queues against the plan's concurrency cap,
so the layout keeps the job count down and never repeats expensive setup:

- Go jobs use `.github/actions/setup-go-cache`: one shared module cache for
  every module in the repo, written by the CI `build` job on main after
  `go mod download` in each module, plus a build-cache scope per kind of
  compile (`build`, `race`, `lint`, `darwin`), each written by one job on main.
  PR refs restore through `restore-keys` and never save, so the repository's
  10 GB cache budget holds only entries every run reads. (Previously nineteen
  jobs shared one setup-go key that the first job to finish saved: a 92 MB
  "cache hit" behind which every job compiled cold.)
- The E2E `build` job compiles the binaries and builds the hub and agent
  images once and ships them to the four kind suites as one artifact; the
  suites run the `make e2e-<suite>-run` halves against it.
- There is no disk-cleanup step; ubuntu-latest runners report ~86 GB free.
- Provider image validation on PRs writes no layer cache; PR refs writing
  twelve `mode=max` scopes is what kept the cache budget full.
- The macOS job runs only the Darwin-specific compile and focused tests, on a
  path filter; the Edges provider suite and portal checks run on Linux in CI.

Manual runs of each managed workflow select full validation. Manual
container-image and Helm runs do not log in or publish. Push/tag/release
behavior is preserved, including provider-image publishing remaining in
provider-release. Only superseded PR runs are cancelled; non-PR runs have
unique concurrency groups.

Run `make verify-ci-selection` for Python unit, real-Git, and workflow contract
tests (requires Python 3 and the pinned PyYAML in `requirements-test.txt`; CI
installs it with pip). Run `make verify-workflows` for pinned Actionlint
syntax/expression validation of the managed workflows. Both targets are part of
`make verify` and run in CI. Actionlint's optional external ShellCheck/Pyflakes
integrations are disabled to keep this gate focused on workflow syntax and
independent of unpinned tools.

The workflow-change PR runs full CI. After merging, confirm actual GitHub job
selection on a provider-portal-only PR and on a docs-only PR; local policy
checks do not prove hosted runner behavior. An App Studio-only change should
retain its portal/image jobs and existing split validation while skipping KCP,
SSH, OIDC, backend test matrices, unrelated portal/images, and codegen.
