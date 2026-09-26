// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-sdk/dataplane"
)

func TestSnapshotHandlesAreScopedExpireAndDetectTampering(t *testing.T) {
	server := New(nil, nil)
	server.SnapshotDir = t.TempDir()
	// The caller is the identity kcp stamped; a ServiceAccount also carries
	// the logical cluster it lives in.
	caller := dataplane.ProxiedIdentity{User: "system:serviceaccount:default:actor", Extra: map[string][]string{dataplane.ClusterNameExtra: {"clustera"}}}
	input := Input{RepositoryUID: "repo-uid", ConnectionUID: "conn-uid", Snapshot: &backend.Snapshot{BaseCommit: "base", Commit: "commit", Tree: "tree", Bundle: []byte("private bundle")}}
	result, err := server.stage(caller, "tenant-a", input)
	if err != nil {
		t.Fatal(err)
	}
	input.BundleRef = result.(map[string]any)["bundleRef"].(string)
	expected := *input.Snapshot
	input.Snapshot = nil
	got, err := server.loadSnapshot(caller, "tenant-a", input)
	if err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("round trip: %#v %v", got, err)
	}
	for _, kind := range []string{"tenant", "repository", "connection", "caller", "caller cluster"} {
		changed := input
		cluster := "tenant-a"
		other := caller
		switch kind {
		case "tenant":
			cluster = "tenant-b"
		case "repository":
			changed.RepositoryUID = "other"
		case "connection":
			changed.ConnectionUID = "other"
		case "caller":
			other = dataplane.ProxiedIdentity{User: "system:serviceaccount:default:someone-else", Extra: caller.Extra}
		case "caller cluster":
			// Two providers' ServiceAccounts spell their names identically;
			// the cluster they live in is what tells them apart.
			other = dataplane.ProxiedIdentity{User: caller.User, Extra: map[string][]string{dataplane.ClusterNameExtra: {"clusterb"}}}
		}
		if _, err := server.loadSnapshot(other, cluster, changed); err == nil {
			t.Fatalf("snapshot crossed %s boundary", kind)
		}
	}
	path := filepath.Join(server.snapshotScope(caller, "tenant-a", input), input.BundleRef+".json")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := server.loadSnapshot(caller, "tenant-a", input); err == nil {
		t.Fatal("expired snapshot accepted")
	}
	if err := os.WriteFile(path, []byte(`{"bundle":"Y2hhbmdlZA=="}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.loadSnapshot(caller, "tenant-a", input); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
}
