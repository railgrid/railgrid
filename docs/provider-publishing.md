# Publishing providers

**Last updated:** 2026-11-16

Every provider under `providers/`, and the SDK under `provider-sdk/`, lives in
this repository and is published from it. There are no standalone
`railgrid/provider-*` repositories: the splitsh-lite source mirrors that used
to exist have been removed, together with their workflows, deploy keys and the
pull request dry run.

## What is published

| Artifact | Published by | Trigger |
| -------- | ------------ | ------- |
| Provider image and Helm chart | [`provider-release.yaml`](../.github/workflows/provider-release.yaml) | a `providers/<name>/vX.Y.Z` tag |
| SDK version | the tag itself | a `provider-sdk/vX.Y.Z` tag |

Both kinds of tag are cut with `go run ./cmd/release`. A provider tag builds
the multi-arch image and packages the chart, each stamped with the provider's
own version. Every pull request builds each provider image (single-arch,
build-only) and packages each chart through the Prow `pull-railgrid-images`
job, so a broken Dockerfile or chart is caught in the pull request.

## Publishing inline assistant skill packages

A provider may publish App Studio guidance in
`CatalogEntry.spec.hub.assistantSkills`. This is package distribution, not provider
enablement or authority: the authenticated hub catalog distributes the
validated inline bytes into App Studio's read-only system-skill source. Like
other system skills, a provider package is enabled by default and each project
may disable or re-enable it. The package cannot grant tools, credentials,
permissions, models, approvals, or Provider Actions access.

For each package:

1. Create a package with a `SKILL.md` (YAML `name` and `description` plus the
   guidance body). When useful, start with the bundled
   `skill-creator/scripts/init_skill.py` utility, then add only bounded,
   package-relative supporting resources.
2. Validate the package against the provider skill contract: UTF-8 content,
   no traversal or absolute resource paths, no authority-bearing frontmatter,
   and the 32 KiB document / 64 KiB resource / 512 KiB per-provider bounds.
3. Compute the canonical `sha256:` digest over `packageName`, `version`, raw
   `SKILL.md`, and resources sorted by path. Update the digest and version in
   the provider's checked-in `manifest.yaml` (under `spec.hub.assistantSkills`)
   and mirror the complete entry in `deploy/chart/templates/catalogentry.yaml`.
4. If the API contract changes, run the provider code-generation target and
   commit generated schemas. Test that the manifest and Helm CatalogEntry
   render equivalent package name, version, digest, document, and resources;
   do not rely on one copy drifting silently.
5. Link the shipped package's `SKILL.md` from the provider README so its
   authoring contract and operational evidence remain reviewable.

The Databricks provider's shipped example is
[`databricks-app-integration`](https://github.com/railgrid/providers/blob/main/providers/databricks/skills/databricks-app-integration/SKILL.md).

## Go module paths

`provider-sdk` and each provider are separate Go modules with their own
`go.mod`. Inside this repository they resolve to the in-tree source: every
provider `go.mod` carries `replace github.com/railgrid/provider-sdk =>
../../provider-sdk`, and `go.work` (seeded from `go.work.template`) lists the
modules by directory.

The module declarations still carry the names of the removed mirrors
(`github.com/railgrid/provider-sdk`, `github.com/railgrid/provider-<name>`).
Nothing is published at those paths any more, so a consumer outside this
repository cannot `go get` a new version under them. Go resolves a module in a
subdirectory at `github.com/railgrid/railgrid/<dir>`, versioned by a
`<dir>/vX.Y.Z` tag, which is the tag scheme the release tool already uses;
publishing the modules that way needs the `module` lines and their imports
renamed to match.

## Private Planner and Databricks providers

Source and release automation moved to
[railgrid/providers](https://github.com/railgrid/providers). The old Databricks source
mirror is retired after cutover; no source synchronization remains in Railgrid.
Existing public history and previous releases remain available. The Linear
provider that also moved there has since been removed; Planner (one board over
Linear, Jira and GitHub Projects) is its replacement, and the previously
published `railgrid-linear-provider` image and chart versions stay available
but receive no further releases.

Both providers remain platform-installable in Railgrid hubs, using administrator
onboarding, chart bootstrap, CatalogEntry registration and workspace Enable.
Self-hosting is optional. Install the versioned OCI charts
`oci://ghcr.io/railgrid/charts/railgrid-planner-provider` and
`oci://ghcr.io/railgrid/charts/railgrid-databricks-provider`; their images are
`ghcr.io/railgrid/railgrid-planner-provider` and
`ghcr.io/railgrid/railgrid-databricks-provider`. Supply the existing chart kubeconfig
and hub settings. Private source access is not required to install artifacts.

Their `providers/<name>/vX.Y.Z` tags are now created in the private repository,
which verifies before publishing images/charts. The public Railgrid release command
no longer offers those providers. Package Actions access must be granted to the
private repository before its first release without changing package visibility.

The App Studio–Databricks integration suite and dedicated fixtures moved into the
private provider's `test/deferred` directory. Cross-repository adaptation and
execution are deferred; public Railgrid CI does not execute or claim that coverage.
