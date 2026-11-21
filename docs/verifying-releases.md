---
layout: default
title: Verifying images and charts
description: "Release images and Helm charts are signed with cosign; how to check a signature before you deploy"
---

# Verifying images and charts

Every container image and Helm chart Railgrid publishes to `ghcr.io/railgrid`
is signed with [cosign](https://docs.sigstore.dev/cosign/) by the GitHub
Actions workflow that built it. Signing is keyless: there is no Railgrid
signing key to distribute or rotate. The signature is bound to the identity of
the workflow run and recorded in the public Rekor transparency log.

This applies to artifacts published after signing was introduced. Earlier
tags are not signed.

## What is signed

| Artifact | Published by |
| -------- | ------------ |
| `railgrid-hub`, `railgrid-agent` images (`v*` tags) | `images.yaml` |
| `charts/railgrid-hub`, `charts/railgrid-agent` (`v*` tags) | `helm-images.yaml` |
| Provider images and charts, and the infrastructure provider's `railgrid-dev-agent`, `railgrid-universal-dev` and `railgrid-access-proxy` images (`providers/<name>/v*` tags) | `provider-release.yaml` |
| `sha-<commit>` images and `0.0.0-main` charts built from `main` | `main-builds.yaml` |

Signatures are made on the digest, so they cover every tag that points at it.
For a multi-arch image the signed digest is the image index.

## Verifying

```sh
cosign verify ghcr.io/railgrid/railgrid-hub:v0.3.1 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/railgrid/railgrid/\.github/workflows/'
```

A chart is an OCI artifact and verifies the same way:

```sh
cosign verify ghcr.io/railgrid/charts/railgrid-hub:0.3.1 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/railgrid/railgrid/\.github/workflows/'
```

The identity is the workflow file and the ref it ran on, for example
`https://github.com/railgrid/railgrid/.github/workflows/images.yaml@refs/tags/v0.3.1`.
To accept release builds only, and not builds from `main`, end the pattern
with `@refs/tags/` and the tag shape you expect.

Always pass both flags. A signature checked without an expected identity only
proves that someone signed the artifact.

## Enforcing it in a cluster

Admission controllers that understand cosign keyless signatures (Kyverno,
the Sigstore policy-controller) and Flux (`spec.verify` on an `OCIRepository`
or `HelmChart`, provider `cosign`, with `matchOIDCIdentity`) take the same
issuer and identity pattern.
