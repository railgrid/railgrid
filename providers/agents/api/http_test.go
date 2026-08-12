// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import "testing"

func TestParseTenantPath(t *testing.T) {
	cases := []struct {
		path    string
		wantOrg string
		wantWS  string
	}{
		// Workspace scope: root:faros:tenants:<org>:<ws>.
		{"root:faros:tenants:acme:ws1", "acme", "ws1"},
		// Org scope: root:faros:tenants:<org> (no workspace).
		{"root:faros:tenants:acme", "acme", ""},
		// UUID-shaped ids (the real form).
		{"root:faros:tenants:9f2c-1a:proj-7", "9f2c-1a", "proj-7"},
		// Wrong / missing prefix → empty.
		{"root:faros:orgs:acme:ws1", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		org, ws := parseTenantPath(c.path)
		if org != c.wantOrg || ws != c.wantWS {
			t.Errorf("parseTenantPath(%q) = (%q,%q), want (%q,%q)", c.path, org, ws, c.wantOrg, c.wantWS)
		}
	}
}
