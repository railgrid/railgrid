#!/usr/bin/env bash

# Push Helm charts to OCI registry
# Requires: IMAGE_REPO environment variable (e.g., ghcr.io/faroshq)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if [ -z "${IMAGE_REPO:-}" ]; then
    echo "ERROR: IMAGE_REPO environment variable is required"
    echo "Example: IMAGE_REPO=ghcr.io/faroshq make helm-push-local"
    exit 1
fi

# Get git revision for version
REV=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
CHART_VERSION="${CHART_VERSION:-0.0.0-$REV}"

export HELM_EXPERIMENTAL_OCI=1

echo "Pushing Helm charts with version: $CHART_VERSION to $IMAGE_REPO"

for chart_file in ./bin/*-"$CHART_VERSION".tgz; do
    if [ -f "$chart_file" ]; then
        chart_filename=$(basename "$chart_file")
        chart_name=${chart_filename%-"$CHART_VERSION".tgz}

        helm push "$chart_file" "oci://${IMAGE_REPO}/charts"
        echo "Pushed: oci://${IMAGE_REPO}/charts/$chart_name:$CHART_VERSION"
    fi
done

echo "Helm charts pushed successfully!"
