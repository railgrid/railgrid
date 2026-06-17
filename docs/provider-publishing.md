# Publishing providers to standalone mirrors

**Last updated:** 2026-06-11

kedge is a monorepo, but each provider under `providers/` is also published to
its own standalone, **read-only** GitHub repository. This lets external
consumers depend on (and browse) a single provider without cloning the whole
monorepo — the same pattern Kubernetes uses with its
[`publishing-bot`](https://github.com/kubernetes/publishing-bot) and Symfony/Laravel
use for their components.

> **Mirrors are source-only.** Container images and Helm charts are built and
> published **from the monorepo** ([`images.yaml`](../.github/workflows/images.yaml)
> and [`helm-images.yaml`](../.github/workflows/helm-images.yaml)), not from the
> mirrors. Every monorepo PR builds each provider image (single-arch,
> build-only) and packages each provider chart, so a broken Dockerfile or chart
> is caught **in the PR** rather than surfacing only after the split sync
> reaches a mirror. The mirrors exist purely so the provider modules are
> `go get`-able at their own paths and browsable in isolation.

Publishing is done with [splitsh-lite](https://github.com/splitsh/lite), which
produces a real, **history-preserving** subtree split (not a squashed
snapshot). splitsh-lite is deterministic: the same source always splits to the
same commit sha1s, so the mirror is append-only and pushes normally
fast-forward.

## What is published

| Provider directory          | Mirror repository                  | Secret                  | Workflow                                                  |
| --------------------------- | ---------------------------------- | ----------------------- | -------------------------------------------------------- |
| `providers/quickstart`      | `faroshq/provider-quickstart`      | `QUICKSTART_DEPLOY_KEY` | [`split-quickstart.yaml`](../.github/workflows/split-quickstart.yaml) |
| `providers/code`            | `faroshq/provider-code`            | `CODE_DEPLOY_KEY`       | [`split-code.yaml`](../.github/workflows/split-code.yaml) |
| `providers/infrastructure`  | `faroshq/provider-infrastructure`  | `INFRA_DEPLOY_KEY`      | [`split-infrastructure.yaml`](../.github/workflows/split-infrastructure.yaml) |
| `providers/app-studio`      | `faroshq/provider-app-studio`      | `APP_STUDIO_DEPLOY_KEY` | [`split-app-studio.yaml`](../.github/workflows/split-app-studio.yaml) |
| `providers/kuery`           | `faroshq/provider-kuery`           | `KUERY_DEPLOY_KEY`      | [`split-kuery.yaml`](../.github/workflows/split-kuery.yaml) |

Each provider has its own workflow (identical except for the four `env:` values
and the `secrets.*` reference) and its own deploy key — a GitHub deploy key is
scoped to a single repo, so the keys **cannot** be shared across mirrors. See
[Adding another provider](#adding-another-provider) for the generic pattern.

## When it runs

The split workflow triggers on:

- **push to `main`** — mirrors the branch (force-pushed, so the mirror always
  reflects the monorepo even if history is rewritten).
- **per-provider release tags** — `providers/<name>/vX.Y.Z`. The path prefix is
  stripped on the way out, so the mirror receives a plain `vX.Y.Z` tag (which
  records the released source on the mirror). Pushed non-force, so re-tagging
  fails loudly since tags are immutable. Note the mirror tag no longer builds
  anything — provider images and charts are published from the monorepo (a
  repo-wide `vX.Y.Z` tag drives those builds via `images.yaml` /
  `helm-images.yaml`).
- **pull requests** touching the provider's subtree (or its workflow file) —
  these only *validate* (install splitsh-lite + compute the split); the
  deploy-key and push steps are gated to non-PR events, so a PR never writes to
  a mirror.
- **`workflow_dispatch`** — manual run, used for the initial seed or a manual
  re-sync.

Runs are serialized per ref (`concurrency` group) and never cancelled
mid-push.

## One-time setup per mirror

These steps are **manual** and must be done once per provider mirror. They
require admin on both the monorepo and the target repo. Substitute the
provider's values from the [What is published](#what-is-published) table for
`<name>` (e.g. `quickstart`), the mirror repo, and the secret name.

> **Each mirror needs its own deploy key.** A GitHub deploy key (the public
> half) can be registered on only one repository — adding a key that is already
> a deploy key on another repo fails with *"Key is already in use."* So you
> cannot reuse one key across `provider-quickstart`, `provider-code`, and
> `provider-infrastructure`; generate a fresh key per mirror.

### 1. Create the target repository

Create the mirror repo (e.g. `faroshq/provider-code`). An empty repo is
fine — the first run creates the `main` branch.

### 2. Generate an SSH deploy key

```bash
ssh-keygen -t ed25519 -C "kedge-split-<name>" -f /tmp/<name>_split -N ""
```

This produces a private key (`/tmp/<name>_split`) and a public key
(`/tmp/<name>_split.pub`).

### 3. Add the public key as a write deploy key on the mirror

In **the mirror repo** → **Settings → Deploy keys → Add deploy key**:

- Paste the contents of `/tmp/<name>_split.pub`.
- **Check "Allow write access".**

A deploy key is scoped to that single repo, which is why we use it instead of a
broad personal access token.

### 4. Add the private key as a secret on the monorepo

In **`faroshq/kedge`** → **Settings → Secrets and variables → Actions → New repository secret**:

- Name: the provider's secret from the table (e.g. `CODE_DEPLOY_KEY`).
- Value: the full contents of `/tmp/<name>_split` (the private key,
  including the `-----BEGIN/END-----` lines).

### 5. Seed the mirror

Run the workflow once manually: **Actions → Split \<name> provider → Run
workflow**. After the first successful run the mirror tracks the monorepo
automatically.

### 6. Clean up

Delete the local key copies once they are stored in GitHub:

```bash
rm -f /tmp/<name>_split /tmp/<name>_split.pub
```

## How the split works (internals)

Each split workflow (e.g. [`split-code.yaml`](../.github/workflows/split-code.yaml)):

1. Checks out the monorepo with **full history** (`fetch-depth: 0`) — required
   for splitsh-lite to compute the subtree.
2. Downloads the pinned **splitsh-lite `v1.0.1`** prebuilt Linux binary
   (statically bundles libgit2; `v2.0.0` ships no Linux binary and needs cgo,
   so we pin `v1.0.1`). The `v1.0.1` tarball stores the binary as
   `./splitsh-lite` (leading `./`), so the extraction names it explicitly.
3. On non-PR events, writes the provider's deploy-key secret to `~/.ssh` and
   configures `github.com` to use it. (On PRs this step is skipped.)
4. Runs `splitsh-lite --prefix=providers/<name> --origin=HEAD`, which writes the
   split commits into the local object store and prints the tip sha. This runs
   on **every** event (PRs included) so it validates the install + split
   end-to-end without publishing. `--origin` takes a git ref, and `HEAD`
   resolves cleanly for both branch pushes and PR merge commits.
5. On non-PR events, pushes that sha to the mirror — to `refs/heads/main` for
   branch pushes (force), or `refs/tags/vX.Y.Z` for tag pushes (non-force,
   prefix stripped from `providers/<name>/vX.Y.Z`).

## Adding another provider

All three current providers (`quickstart`, `code`, `infrastructure`) are wired
up. To publish a new one, copy any existing split workflow (they are identical
apart from four `env:` values, the trigger paths/tags, the concurrency group,
and the `secrets.*` reference) and change:

```yaml
name: Split <name> provider
on:
  push:
    tags: ['providers/<name>/v*']
  pull_request:
    paths:
      - 'providers/<name>/**'
      - '.github/workflows/split-<name>.yaml'
concurrency:
  group: split-<name>-${{ github.ref }}
env:
  PREFIX: providers/<name>                         # the subtree to split
  TARGET_REPO: git@github.com:faroshq/provider-<name>.git
  TARGET_BRANCH: main
  SPLITSH_VERSION: v1.0.1
# ...and reference a per-mirror secret, e.g. secrets.<NAME>_DEPLOY_KEY
```

Then repeat the [one-time setup](#one-time-setup-per-mirror) with a fresh key
and secret name. Use a distinct deploy key + secret per mirror so a leaked key
only affects one repo. (These can later be collapsed into a single matrix
workflow if the list grows.)

## Go module paths

A split mirror keeps the monorepo's `go.mod` verbatim, so a provider's module
path must match its mirror URL for the mirror to be `go get`-able at its own
path. Each published provider's `go.mod` is therefore declared with the mirror
path rather than a monorepo-nested path:

| Provider                   | `module` declaration in `go.mod`              |
| -------------------------- | --------------------------------------------- |
| `providers/quickstart`     | `github.com/faroshq/provider-quickstart`      |
| `providers/code`           | `github.com/faroshq/provider-code`            |
| `providers/infrastructure` | `github.com/faroshq/provider-infrastructure`  |

This is transparent to the monorepo because `go.work` references providers by
directory, not by module path, and no other monorepo module imports these
provider modules. When wiring up a new provider mirror, set its `go.mod` module
to the mirror URL (e.g. `github.com/faroshq/provider-code`) before the first
split, and fix up the provider's own in-repo imports of that module path
accordingly (`code` and `infrastructure` each had ~17 self-imports to rewrite).
