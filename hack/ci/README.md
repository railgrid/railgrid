# CI selection

The CI, E2E, container-image, and Helm workflows always start on their existing
PR triggers. Each runs the shared `ci-changes` action with full checkout history.
`selection.py` compares the PR head with its merge base against the event's base
SHA; validation jobs continue using the normal PR merge checkout.

Only a nonempty diff entirely inside the eight known providers' `portal/`
directories enables `provider-ui` mode. Both sides of renames and deleted paths
count. Everything else, including unavailable history, root portal, canonical
PortalKit/AgentKit, backend changes, and non-PR events, selects `full` mode.
Unexpected classifier execution errors fail detection.

In UI mode, CI retains affected portal checks, every Go vulnerability scan,
boilerplate, PortalKit/AgentKit parity, UI conformance, design metadata, Tilt
wiring, and CI-policy verification. The image workflow builds the affected
providers' main images. The E2E and Helm jobs skip. Existing split, template,
example, release, and manual Tilt workflows retain their own policies.

`matrices.json` owns the portal flags and provider image metadata formerly
inline in the workflows. Image names are relative to the repository owner.
Companion images remain in the full matrix but are not selected by portal edits.
Update the matrix and its regression expectations together when adding providers.

Each workflow's result job requires successful detection, success for all
selected jobs, and skipped status for intentionally excluded jobs. `result.py`
owns this inventory; tests compare it with the actual workflow jobs, direct
dependencies, and selection conditions so newly added jobs cannot escape it.
No repository rulesets are changed. If required checks are introduced later,
use the stable `CI result`, `E2E result`, `Images result`, and `Helm result` names.

Manual runs of each of these four workflows select full validation. Manual
container-image and Helm runs do not log in or publish. Push/tag/release behavior
is preserved, including provider-image publishing remaining in provider-release.
Only superseded PR runs are cancelled; non-PR runs have unique concurrency groups.

Run `make verify-ci-selection` for Python unit, real-Git, and workflow contract
tests (requires Python 3 and the pinned PyYAML in `requirements-test.txt`; CI
installs it with pip). Run
`make verify-workflows` for pinned Actionlint syntax/expression validation of the
four managed workflows. Both targets are part of `make verify` and run in CI.
Actionlint's optional external ShellCheck/Pyflakes integrations are disabled to
keep this gate focused on workflow syntax and independent of unpinned tools.

The workflow-change PR runs full CI. After merging, confirm actual GitHub job
selection on a provider-portal-only PR; local policy checks do not prove hosted
runner behavior. An App Studio-only change should retain its portal/image jobs
and existing split validation while skipping KCP, SSH, OIDC, backend test
matrices, unrelated portal/images, codegen, and Helm validation.
