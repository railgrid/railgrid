#!/usr/bin/env bash

# Copyright 2026 The Faros Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

# Compatibility entrypoint retained for callers of the original POC path.
# The maintained installer lives under contrib so the opt-in runtime is kept
# next to its checked-in Template fixture and Tilt workflow.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
exec "${ROOT_DIR}/providers/infrastructure/contrib/config-connector/install.sh" "$@"
