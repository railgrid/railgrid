// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package identityclient

import "testing"

// The hub names minted accounts with this function and Code recognizes an
// Agents Run's identity by it. A change renames every live identity, so it is
// pinned.
func TestServiceAccountNameIsStable(t *testing.T) {
	owner := Owner{Provider: "agents", Group: "agents.railgrid.ai", Kind: "Run", Name: "run-1", UID: "uid-1",
		Version: "v1alpha1", Resource: "runs", ClusterID: "ignored"}
	got := ServiceAccountName(owner)
	const want = "railgrid-si-8f92455d4902b40e007f7237da4f95c85cc6d9fb"
	if got != want {
		t.Fatalf("ServiceAccountName = %q, want %q", got, want)
	}
	other := owner
	other.Version, other.Resource, other.ClusterID = "v2", "other", "elsewhere"
	if ServiceAccountName(other) != got {
		t.Fatal("version, resource and cluster must not change the account name")
	}
	other = owner
	other.UID = "uid-2"
	if ServiceAccountName(other) == got {
		t.Fatal("a recreated owner must not inherit the account")
	}
}
