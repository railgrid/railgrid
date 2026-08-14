// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package hostpolicy

import "testing"

func TestWorkspaceHostSuffixesAreAdditiveAndRejectIPs(t *testing.T) {
	t.Setenv(AllowedHostSuffixesEnv, "example.test, .cloud.databricks.com, 127.0.0.1, [::1]")
	suffixes := WorkspaceHostSuffixes(nil)
	for _, want := range []string{"cloud.databricks.com", "gcp.databricks.com", "azuredatabricks.net", "example.test"} {
		if !contains(suffixes, want) {
			t.Fatalf("suffixes = %#v, missing %q", suffixes, want)
		}
	}
	for _, rejected := range []string{"127.0.0.1", "::1"} {
		if contains(suffixes, rejected) {
			t.Fatalf("suffixes = %#v, unexpectedly contains IP %q", suffixes, rejected)
		}
	}
	if !AllowedWorkspaceHost("dbc.example.test", nil) {
		t.Fatal("configured suffix did not allow subdomain")
	}
	if AllowedWorkspaceHost("127.0.0.1", nil) || AllowedWorkspaceHost("::1", nil) {
		t.Fatal("IP workspace host was allowed")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
