/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import (
	"testing"

	"github.com/faroshq/faros/pkg/kcppaths"
)

// providerNameFromExportPath decides which APIBindings count as "this provider
// is enabled here". Getting it wrong either hides enabled providers or reports
// another Org's provider as enabled.
func TestProviderNameFromExportPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		orgUUID  string
		wantName string
		wantOK   bool
	}{
		{
			name:     "platform provider",
			path:     kcppaths.ProviderPath("edges"),
			orgUUID:  "org1",
			wantName: "edges",
			wantOK:   true,
		},
		{
			name:     "own org provider",
			path:     kcppaths.OrgProviderPath("org1", "vault"),
			orgUUID:  "org1",
			wantName: "vault",
			wantOK:   true,
		},
		{
			// The isolation case: a binding pointing at another Org's export
			// must never be reported as an enabled provider here.
			name:    "another org's provider",
			path:    kcppaths.OrgProviderPath("org2", "vault"),
			orgUUID: "org1",
		},
		{
			name:    "core platform export is not a provider",
			path:    kcppaths.SystemControllers,
			orgUUID: "org1",
		},
		{
			name:    "team workspace path is not a provider",
			path:    kcppaths.WorkspacePath("org1", "ws1"),
			orgUUID: "org1",
		},
		{
			name:    "providers parent alone names no provider",
			path:    kcppaths.ProvidersParent,
			orgUUID: "org1",
		},
		{
			name:    "nested path below a platform provider",
			path:    kcppaths.ProviderPath("edges") + ":nested",
			orgUUID: "org1",
		},
		{
			name:    "empty path",
			path:    "",
			orgUUID: "org1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := providerNameFromExportPath(tc.path, tc.orgUUID)
			if ok != tc.wantOK {
				t.Fatalf("providerNameFromExportPath(%q, %q) ok = %v, want %v", tc.path, tc.orgUUID, ok, tc.wantOK)
			}
			if got != tc.wantName {
				t.Errorf("providerNameFromExportPath(%q, %q) = %q, want %q", tc.path, tc.orgUUID, got, tc.wantName)
			}
		})
	}
}
