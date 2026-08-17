#!/usr/bin/env bash

# Copyright 2026 The Faros Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"

export FAROS_CONTRIB_NAME="Config Connector"
export FAROS_CONTRIB_TEMPLATE_FILE="${FAROS_KCC_TEMPLATE_FILE:-providers/infrastructure/contrib/config-connector/pubsub-template.yaml}"
export FAROS_CONTRIB_PROVIDER_WORKSPACE="${FAROS_KCC_PROVIDER_WORKSPACE:-root:faros:providers:infrastructure}"
export FAROS_CONTRIB_APIEXPORT_NAME="${FAROS_KCC_APIEXPORT_NAME:-infrastructure.providers.faros.sh}"
export FAROS_CONTRIB_INSTANCE_RESOURCE="${FAROS_KCC_INSTANCE_RESOURCE:-gcppubsubtopics}"
export FAROS_CONTRIB_INSTANCE_GROUP="${FAROS_KCC_INSTANCE_GROUP:-infrastructure.faros.sh}"
export FAROS_CONTRIB_CACHED_RESOURCE_NAME="${FAROS_KCC_CACHED_RESOURCE_NAME:-publish-templates}"
export FAROS_CONTRIB_KCP_SERVER="${FAROS_KCC_KCP_SERVER:-}"
export FAROS_CONTRIB_WAIT="${FAROS_KCC_WAIT:-15m}"
export FAROS_CONTRIB_WAIT_SECONDS="${FAROS_KCC_WAIT_SECONDS:-900}"
export FAROS_CONTRIB_POLL_SECONDS="${FAROS_KCC_POLL_SECONDS:-5}"

exec "${ROOT_DIR}/providers/infrastructure/contrib/lib/enable-template.sh"
