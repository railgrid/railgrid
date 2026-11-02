// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
)

// ConsentDigest binds an Enable decision to the provider identity and exact
// permission declaration displayed by the catalog. encoding/json gives this
// typed projection stable field and map-key ordering; display/routing/liveness
// changes do not invalidate consent. Slice reordering conservatively does.
func ConsentDigest(p Provider) string {
	declaration := struct {
		Owner, Name, ExportPath, ExportName string
		Requires                            []providersv1alpha1.ProviderRequirement
		HubAccess                           []providersv1alpha1.ProviderHubAccess
	}{p.OrgUUID, p.Name, p.APIExportPath, p.APIExportName, p.Requires, p.HubAccess}
	data, err := json.Marshal(declaration)
	if err != nil {
		// These API types contain only JSON values. If that ever changes,
		// refuse consent rather than returning a digest of partial data.
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
