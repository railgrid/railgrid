/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcppaths

import "testing"

func TestOrgProviderPaths(t *testing.T) {
	if got, want := OrgProvidersParent("org1"), "root:faros:tenants:org1:providers"; got != want {
		t.Errorf("OrgProvidersParent = %q, want %q", got, want)
	}
	if got, want := OrgProviderPath("org1", "vault"), "root:faros:tenants:org1:providers:vault"; got != want {
		t.Errorf("OrgProviderPath = %q, want %q", got, want)
	}
}

func TestSplitOrgProviderPath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		wantOrg string
		wantSvc string
		wantOK  bool
	}{
		{
			name:    "org provider round-trips",
			path:    OrgProviderPath("org1", "vault"),
			wantOrg: "org1",
			wantSvc: "vault",
			wantOK:  true,
		},
		{
			// The whole point of the split: a platform provider must not be
			// attributed to any Org.
			name: "platform provider is not org-scoped",
			path: ProviderPath("edges"),
		},
		{
			name: "team workspace is not a provider",
			path: WorkspacePath("org1", "ws1"),
		},
		{
			name: "org workspace itself is not a provider",
			path: OrgPath("org1"),
		},
		{
			name: "the providers parent alone names no provider",
			path: OrgProvidersParent("org1"),
		},
		{
			// A team workspace could otherwise be mistaken for a provider if the
			// suffix were matched loosely.
			name: "nested path below a provider is rejected",
			path: OrgProviderPath("org1", "vault") + ":extra",
		},
		{
			name: "unrelated root path",
			path: "root:something:else",
		},
		{
			name: "empty",
			path: "",
		},
		{
			// A team workspace that happened to be named "providers" would sit at
			// tenants:<org>:providers with nothing after it — still not a provider.
			name: "org whose child is literally named providers",
			path: "root:faros:tenants:org1:providers",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org, svc, ok := SplitOrgProviderPath(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("SplitOrgProviderPath(%q) ok = %v, want %v", tc.path, ok, tc.wantOK)
			}
			if org != tc.wantOrg || svc != tc.wantSvc {
				t.Errorf("SplitOrgProviderPath(%q) = (%q, %q), want (%q, %q)", tc.path, org, svc, tc.wantOrg, tc.wantSvc)
			}
		})
	}
}
