// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-code/snapshotstore"
	"github.com/railgrid/provider-sdk/dataplane"
)

func TestSnapshotHandlesAreScopedExpireAndDetectTampering(t *testing.T) {
	server := New(nil, nil)
	dir := t.TempDir()
	store, err := snapshotstore.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	server.Snapshots = store
	ctx := context.Background()
	// The caller is the identity kcp stamped; a ServiceAccount also carries
	// the logical cluster it lives in.
	caller := dataplane.ProxiedIdentity{User: "system:serviceaccount:default:actor", Extra: map[string][]string{dataplane.ClusterNameExtra: {"clustera"}}}
	input := Input{RepositoryUID: "repo-uid", ConnectionUID: "conn-uid", Snapshot: &backend.Snapshot{BaseCommit: "base", Commit: "commit", Tree: "tree", Bundle: []byte("private bundle")}}
	result, err := server.stage(ctx, caller, "tenant-a", input)
	if err != nil {
		t.Fatal(err)
	}
	input.BundleRef = result.(map[string]any)["bundleRef"].(string)
	expected := *input.Snapshot
	input.Snapshot = nil
	got, err := server.loadSnapshot(ctx, caller, "tenant-a", input)
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
		if _, err := server.loadSnapshot(ctx, other, cluster, changed); err == nil {
			t.Fatalf("snapshot crossed %s boundary", kind)
		}
	}
	hashed := func(v string) string {
		sum := sha256.Sum256([]byte(v))
		return hex.EncodeToString(sum[:])
	}
	path := filepath.Join(dir, hashed("tenant-a"), hashed(server.snapshotScope(caller, input)), input.BundleRef+".json")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := server.loadSnapshot(ctx, caller, "tenant-a", input); err == nil {
		t.Fatal("expired snapshot accepted")
	}
	if err := os.WriteFile(path, []byte(`{"bundle":"Y2hhbmdlZA=="}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.loadSnapshot(ctx, caller, "tenant-a", input); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
}

func TestSnapshotCanStageScreenshotOnlyFollowup(t *testing.T) {
	store, err := snapshotstore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := New(nil, nil)
	server.Snapshots = store
	image := backend.Attachment{Path: "artifacts/screenshots/mobile.png", MediaType: "image/png", Data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)}
	input := Input{RepositoryUID: "repo", ConnectionUID: "conn", Snapshot: &backend.Snapshot{Attachments: []backend.Attachment{image}}}
	result, err := server.stage(t.Context(), dataplane.ProxiedIdentity{User: "coordinator"}, "tenant", input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := FindStaged(t.Context(), store, "tenant", result.(map[string]any)["bundleRef"].(string))
	if err != nil || len(snapshot.Attachments) != 1 || len(snapshot.Bundle) != 0 {
		t.Fatalf("screenshot-only stage: %+v %v", snapshot, err)
	}
	input.Snapshot.Attachments[0].Path = "../secret.png"
	if _, err := server.stage(t.Context(), dataplane.ProxiedIdentity{User: "coordinator"}, "tenant", input); err == nil {
		t.Fatal("unsafe screenshot path accepted")
	}
}
