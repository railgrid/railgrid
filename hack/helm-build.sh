#!/usr/bin/env bash

# Build and package Helm charts locally
# Version format: 0.0.0-<git-sha> for local builds

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Get git revision for version
REV=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
CHART_VERSION="${CHART_VERSION:-0.0.0-$REV}"

# Ensure bin directory exists
mkdir -p ./bin

echo "Building Helm charts with version: $CHART_VERSION"

for chart_dir in deploy/charts/*/; do
    if [ -f "${chart_dir}Chart.yaml" ]; then
        chart_name=$(basename "$chart_dir")

        # Backup Chart.yaml
        cp "${chart_dir}Chart.yaml" "${chart_dir}Chart.yaml.bak"

        # Patch version and appVersion
        sed -i.tmp "s/^version:.*/version: $CHART_VERSION/" "${chart_dir}Chart.yaml"
        sed -i.tmp "s/^appVersion:.*/appVersion: $CHART_VERSION/" "${chart_dir}Chart.yaml"
        rm -f "${chart_dir}Chart.yaml.tmp"

        # Package chart
        helm package "$chart_dir" --version "$CHART_VERSION" --destination ./bin/
        echo "Packaged: ./bin/$chart_name-$CHART_VERSION.tgz"

        # Restore original Chart.yaml
        mv "${chart_dir}Chart.yaml.bak" "${chart_dir}Chart.yaml"
    fi
done

echo "Helm charts built successfully!"
