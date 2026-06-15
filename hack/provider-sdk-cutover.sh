#!/usr/bin/env bash
# Copyright 2026 The Faros Authors. Licensed under the Apache License, Version 2.0.
#
# provider-sdk-cutover.sh — run ONCE, after github.com/faroshq/provider-sdk is
# published, to switch the provider modules off the local
# `replace => ../../provider-sdk` and onto the published, go-gettable version.
#
# Why this exists: while the SDK is unpublished, every provider go.mod carries a
# `replace github.com/faroshq/provider-sdk => ../../provider-sdk` so the monorepo
# (and the repo-root-context Docker builds) can resolve it. That replace also
# rides the split out to the read-only provider mirrors, where ../../provider-sdk
# does not exist — so the mirrors can't `go get`/build standalone. Once the SDK
# is published this replace is dropped in favour of a real version.
#
# Prerequisites:
#   1. provider-sdk/vX.Y.Z tagged in the monorepo (kedge-release provider-sdk)
#      and split to faroshq/provider-sdk (the split-provider-sdk workflow).
#   2. That version is resolvable from the module proxy:
#        GOWORK=off GOFLAGS=-mod=mod go list -m github.com/faroshq/provider-sdk@vX.Y.Z
#
# Usage:
#   hack/provider-sdk-cutover.sh vX.Y.Z
#
# Then review the diff, `go build ./...` per module, commit, and push.

set -euo pipefail

VERSION="${1:-}"
if [ -z "${VERSION}" ]; then
  echo "usage: $0 vX.Y.Z   (the published github.com/faroshq/provider-sdk version)" >&2
  exit 2
fi

MOD="github.com/faroshq/provider-sdk"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"

PROVIDERS="quickstart kuery code infrastructure app-studio"

# go.work would transparently override the published version with the local
# ./provider-sdk copy, hiding whether the published module actually resolves.
# Disable workspace mode for the cutover so `go mod tidy` exercises the proxy.
export GOWORK=off

echo ">>> Pinning ${MOD}@${VERSION} and dropping the local replace in each provider"
for p in ${PROVIDERS}; do
  echo "    - providers/${p}"
  (
    cd "providers/${p}"
    go mod edit -dropreplace="${MOD}"
    go get "${MOD}@${VERSION}"
    go mod tidy
    go build ./...
  )
done
echo ">>> All providers build against ${MOD}@${VERSION} (no replace)."

echo ">>> Reverting the two CI-built provider images to per-provider Docker context"
# Post-publish, `go mod download` fetches the SDK from the proxy, so the build no
# longer needs the repo-root context + SDK copy. Restore the simpler, faster
# per-provider context.
cat > providers/infrastructure/Dockerfile <<'DOCKERFILE'
# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist).
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. The binary serves `init` + `serve`, so the whole
#    module source has to be present. The kedge-provider-sdk is now a published
#    dependency (no replace), so `go mod download` fetches it from the proxy.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=portal /portal/dist ./portal/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/infrastructure-provider .

# 3. Minimal runtime image.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/infrastructure-provider /infrastructure-provider
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/infrastructure-provider"]
DOCKERFILE

cat > providers/quickstart/Dockerfile <<'DOCKERFILE'
# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist).
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. assets.go //go:embeds portal/dist; init_cmd.go uses
#    the published kedge-provider-sdk (no replace), fetched from the proxy.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go assets.go init_cmd.go ./
COPY --from=portal /portal/dist ./portal/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/quickstart-provider .

# 3. Minimal runtime image. APIResourceSchemas the `init` subcommand applies are
#    baked at /etc/kedge/schemas (KEDGE_SCHEMAS_DIR).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/quickstart-provider /quickstart-provider
COPY deploy/chart/files/schemas /etc/kedge/schemas
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/quickstart-provider"]
DOCKERFILE

echo ">>> Restoring per-provider build contexts in .github/workflows/images.yaml"
perl -0pi -e 's{# Repo-root context so the \Q../../\Eprovider-sdk replace resolves \(see Dockerfile\)\.\n(\s*)context: \.\n(\s*)file: \./providers/infrastructure/Dockerfile}{context: ./providers/infrastructure\n$2file: ./providers/infrastructure/Dockerfile}' .github/workflows/images.yaml
perl -0pi -e 's{# Repo-root context so the \Q../../\Eprovider-sdk replace resolves \(see Dockerfile\)\.\n(\s*)context: \.\n(\s*)file: \./providers/quickstart/Dockerfile}{context: ./providers/quickstart\n$2file: ./providers/quickstart/Dockerfile}' .github/workflows/images.yaml

cat <<EONOTE

>>> Cutover complete (local).
    - providers/*/go.mod now require ${MOD}@${VERSION} (no replace).
    - infrastructure + quickstart Dockerfiles + image-build contexts reverted to
      per-provider.
    - Local SDK development still works: copy go.work.template to go.work
      (workspace mode overrides the published version with ./provider-sdk).

    Next: review 'git diff', run the image builds if you want, then commit + push.
EONOTE
