// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/railgrid/provider-code/backend"
)

func TestSnapshotHandlesAreScopedExpireAndDetectTampering(t *testing.T) {
	server := New(nil, nil, nil)
	server.SnapshotDir = t.TempDir()
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("Authorization", "Bearer actor-a")
	input := Input{RepositoryUID: "repo-uid", ConnectionUID: "conn-uid", Snapshot: &backend.Snapshot{BaseCommit: "base", Commit: "commit", Tree: "tree", Bundle: []byte("private bundle")}}
	result, err := server.stage(request, "tenant-a", input)
	if err != nil {
		t.Fatal(err)
	}
	input.BundleRef = result.(map[string]any)["bundleRef"].(string)
	expected := *input.Snapshot
	input.Snapshot = nil
	got, err := server.loadSnapshot(request, "tenant-a", input)
	if err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("round trip: %#v %v", got, err)
	}
	for _, kind := range []string{"tenant", "repository", "connection", "caller"} {
		changed := input
		cluster := "tenant-a"
		r := request.Clone(request.Context())
		switch kind {
		case "tenant":
			cluster = "tenant-b"
		case "repository":
			changed.RepositoryUID = "other"
		case "connection":
			changed.ConnectionUID = "other"
		case "caller":
			r.Header.Set("Authorization", "Bearer actor-b")
		}
		if _, err := server.loadSnapshot(r, cluster, changed); err == nil {
			t.Fatalf("snapshot crossed %s boundary", kind)
		}
	}
	path := filepath.Join(server.snapshotScope(request, "tenant-a", input), input.BundleRef+".json")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := server.loadSnapshot(request, "tenant-a", input); err == nil {
		t.Fatal("expired snapshot accepted")
	}
	if err := os.WriteFile(path, []byte(`{"bundle":"Y2hhbmdlZA=="}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.loadSnapshot(request, "tenant-a", input); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
}
