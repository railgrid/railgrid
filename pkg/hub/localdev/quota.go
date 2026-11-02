// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package localdev contains explicit overrides for local development only.
package localdev

import "os"

// BYOQuotaBypass is an explicit, temporary local E2E switch. The hub refuses
// to start with this switch outside dev mode with an embedded KCP.
func BYOQuotaBypass() bool {
	return os.Getenv("RAILGRID_LOCAL_E2E_BYO_QUOTA_BYPASS") == "1"
}
