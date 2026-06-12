// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package core

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestParseGVRList(t *testing.T) {
	gvrs, err := parseGVRList("pods, deployments.apps ,ingresses.networking.k8s.io")
	if err != nil {
		t.Fatal(err)
	}
	want := []schema.GroupVersionResource{
		{Resource: "pods"},
		{Group: "apps", Resource: "deployments"},
		{Group: "networking.k8s.io", Resource: "ingresses"},
	}
	if len(gvrs) != len(want) {
		t.Fatalf("got %d entries, want %d", len(gvrs), len(want))
	}
	for i := range want {
		if gvrs[i] != want[i] {
			t.Errorf("entry %d = %v, want %v", i, gvrs[i], want[i])
		}
	}
}

func TestParseWhitelist_EmptyMeansNil(t *testing.T) {
	wl, err := parseWhitelist("  ")
	if err != nil {
		t.Fatal(err)
	}
	if wl != nil {
		t.Fatal("empty whitelist must be nil (sync everything watchable)")
	}
}

func TestParseGVRList_InvalidEntry(t *testing.T) {
	if _, err := parseGVRList(".apps"); err == nil {
		t.Fatal("expected error for entry with empty resource")
	}
}
