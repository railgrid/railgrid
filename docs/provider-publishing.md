# Publishing providers to standalone mirrors

**Last updated:** 2026-06-11

kedge is a monorepo, but each provider under `providers/` is also published to
its own standalone, **read-only** GitHub repository. This lets external
consumers depend on (and browse) a single provider without cloning the whole
monorepo — the same pattern Kubernetes uses with its
[`publishing-bot`](https://github.com/kubernetes/publishing-bot) and Symfony/Laravel
use for their components.

Publishing is done with [splitsh-lite](https://github.com/splitsh/lite), which
produces a real, **history-preserving** subtree split (not a squashed
snapshot). splitsh-lite is deterministic: the same source always splits to the
same commit sha1s, so the mirror is append-only and pushes normally
fast-forward.

## What is published

| Provider directory          | Mirror repository                | Workflow                                                  |
| --------------------------- | -------------------------------- | --------------------------------------------------------- |
| `providers/quickstart`      | `faroshq/provider-quickstart`    | [`.github/workflows/split-quickstart.yaml`](../.github/workflows/split-quickstart.yaml) |

> Only `quickstart` is wired up so far. `code` and `infrastructure` follow the
> same pattern — see [Adding another provider](#adding-another-provider).

## When it runs

The split workflow triggers on:

- **push to `main`** — mirrors the branch (force-pushed, so the mirror always
  reflects the monorepo even if history is rewritten).
- **`v*` tags** — mirrors the tag (non-force; re-tagging fails loudly since
  tags are immutable).
- **`workflow_dispatch`** — manual run, used for the initial seed or a manual
  re-sync.

Runs are serialized per ref (`concurrency` group) and never cancelled
mid-push.

## One-time setup per mirror

These steps are **manual** and must be done once per provider mirror. They
require admin on both the monorepo and the target repo.

### 1. Create the target repository

Create the mirror repo (e.g. `faroshq/provider-quickstart`). An empty repo is
fine — the first run creates the `main` branch.

### 2. Generate an SSH deploy key

```bash
ssh-keygen -t ed25519 -C "kedge-split-quickstart" -f /tmp/quickstart_split -N ""
```

This produces a private key (`/tmp/quickstart_split`) and a public key
(`/tmp/quickstart_split.pub`).

### 3. Add the public key as a write deploy key on the mirror

In **`faroshq/provider-quickstart`** → **Settings → Deploy keys → Add deploy key**:

- Paste the contents of `/tmp/quickstart_split.pub`.
- **Check "Allow write access".**

A deploy key is scoped to that single repo, which is why we use it instead of a
broad personal access token.

### 4. Add the private key as a secret on the monorepo

In **`faroshq/kedge`** → **Settings → Secrets and variables → Actions → New repository secret**:

- Name: `QUICKSTART_DEPLOY_KEY`
- Value: the full contents of `/tmp/quickstart_split` (the private key,
  including the `-----BEGIN/END-----` lines).

### 5. Seed the mirror

Run the workflow once manually: **Actions → Split quickstart provider → Run
workflow**. After the first successful run the mirror tracks the monorepo
automatically.

### 6. Clean up

Delete the local key copies once they are stored in GitHub:

```bash
rm -f /tmp/quickstart_split /tmp/quickstart_split.pub
```

## How the split works (internals)

The workflow ([`split-quickstart.yaml`](../.github/workflows/split-quickstart.yaml)):

1. Checks out the monorepo with **full history** (`fetch-depth: 0`) — required
   for splitsh-lite to compute the subtree.
2. Downloads the pinned **splitsh-lite `v1.0.1`** prebuilt Linux binary
   (statically bundles libgit2; `v2.0.0` ships no Linux binary and needs cgo,
   so we pin `v1.0.1`).
3. Writes `QUICKSTART_DEPLOY_KEY` to `~/.ssh` and configures `github.com` to
   use it.
4. Runs `splitsh-lite --prefix=providers/quickstart --origin=$GITHUB_SHA`,
   which writes the split commits into the local object store and prints the
   tip sha.
5. Pushes that sha to the mirror — to `refs/heads/main` for branch pushes, or
   `refs/tags/<tag>` for tag pushes.

## Adding another provider

To publish `code` or `infrastructure`, copy the quickstart workflow and change
three things, then repeat the [one-time setup](#one-time-setup-per-mirror) with
a new key and secret name:

```yaml
env:
  PREFIX: providers/code                          # the subtree to split
  TARGET_REPO: git@github.com:faroshq/provider-code.git
  TARGET_BRANCH: main
  SPLITSH_VERSION: v1.0.1
# ...and reference a per-mirror secret, e.g. secrets.CODE_DEPLOY_KEY
```

Use a distinct deploy key + secret per mirror so a leaked key only affects one
repo. (These can later be collapsed into a single matrix workflow if the list
grows.)

## Go module paths

A split mirror keeps the monorepo's `go.mod` verbatim, so a provider's module
path must match its mirror URL for the mirror to be `go get`-able at its own
path. Each published provider's `go.mod` is therefore declared with the mirror
path rather than a monorepo-nested path:

| Provider               | `module` declaration in `go.mod`              |
| ---------------------- | --------------------------------------------- |
| `providers/quickstart` | `github.com/faroshq/provider-quickstart`      |

This is transparent to the monorepo because `go.work` references providers by
directory, not by module path. When wiring up a new provider mirror, set its
`go.mod` module to the mirror URL (e.g. `github.com/faroshq/provider-code`)
before the first split, and fix up any in-repo imports of that module
accordingly.
