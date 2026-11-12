#!/bin/sh
# Run a go command that may fetch modules, retrying it when it fails.
#
# Used by the hub and agent Dockerfiles around `go mod download` and
# `go build`: both reach the module proxy and the checksum database, and
# proxy.golang.org intermittently drops HTTP/2 streams in CI. Only the failed
# RUN step is retried, inside the same docker build, so a retry costs one
# network step rather than a rebuild of every image. The module and build
# caches are mounted, so downloads and compiles that already finished are
# reused.
#
# GO_BUILD_GODEBUG, when set, becomes GODEBUG for the go command only (CI sets
# http2client=0). Every retry uses HTTP/1.1 regardless. A failure that is not
# transient (a missing module, a compile error) is retried too and costs only
# the backoff, because nothing new is fetched or compiled; compile errors are
# caught earlier by `make build` in CI anyway.
#
# Usage: go-retry.sh go <args>...

set -u

attempts=${GO_RETRY_ATTEMPTS:-3}
godebug=${GO_BUILD_GODEBUG:-}
attempt=1
while :; do
    if [ -n "$godebug" ]; then
        GODEBUG=$godebug "$@" && exit 0
    else
        "$@" && exit 0
    fi
    status=$?
    if [ "$attempt" -ge "$attempts" ]; then
        echo "go-retry: '$*' failed after $attempt attempts (exit $status)" >&2
        exit "$status"
    fi
    echo "go-retry: '$*' failed (attempt $attempt of $attempts, exit $status); retrying over HTTP/1.1" >&2
    sleep "$((attempt * ${GO_RETRY_BACKOFF_SECONDS:-10}))"
    attempt=$((attempt + 1))
    godebug=http2client=0
done
