#!/usr/bin/env bash

# Copyright 2026 The Railgrid Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"

export RAILGRID_CONTRIB_NAME="Terraform through Infrakube"
export RAILGRID_CONTRIB_TEMPLATE_FILE="${RAILGRID_TERRAFORM_TEMPLATE_FILE:-providers/infrastructure/contrib/terraform/terraform-stack-template.yaml}"
export RAILGRID_CONTRIB_PROVIDER_WORKSPACE="${RAILGRID_TERRAFORM_PROVIDER_WORKSPACE:-root:railgrid:providers:infrastructure}"
export RAILGRID_CONTRIB_APIEXPORT_NAME="${RAILGRID_TERRAFORM_APIEXPORT_NAME:-infrastructure.providers.railgrid.ai}"
export RAILGRID_CONTRIB_INSTANCE_RESOURCE="${RAILGRID_TERRAFORM_INSTANCE_RESOURCE:-terraformstacks}"
export RAILGRID_CONTRIB_INSTANCE_GROUP="${RAILGRID_TERRAFORM_INSTANCE_GROUP:-infrastructure.railgrid.ai}"
export RAILGRID_CONTRIB_CACHED_RESOURCE_NAME="${RAILGRID_TERRAFORM_CACHED_RESOURCE_NAME:-publish-templates}"
export RAILGRID_CONTRIB_KCP_SERVER="${RAILGRID_TERRAFORM_KCP_SERVER:-}"
export RAILGRID_CONTRIB_WAIT="${RAILGRID_TERRAFORM_WAIT:-15m}"
export RAILGRID_CONTRIB_WAIT_SECONDS="${RAILGRID_TERRAFORM_WAIT_SECONDS:-900}"
export RAILGRID_CONTRIB_POLL_SECONDS="${RAILGRID_TERRAFORM_POLL_SECONDS:-5}"

exec "${ROOT_DIR}/providers/infrastructure/contrib/lib/enable-template.sh"
