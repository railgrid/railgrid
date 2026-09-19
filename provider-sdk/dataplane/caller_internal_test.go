// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"errors"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

// These stay in-package: they assert on the factory's stripped base config
// and on the cache internals, which is the point of the bound.

func TestCallerFactoryTargetsTheClusterAndRefusesJunk(t *testing.T) {
	base := &rest.Config{Host: "https://hub.example:9443/clusters/root:railgrid", BearerToken: "provider-token"}
	callers, err := NewCallerFactory(base, WithCallerCacheSize(2), WithCallerCacheTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	if callers.base.Host != "https://hub.example:9443" {
		t.Fatalf("base host = %q, want the /clusters suffix stripped", callers.base.Host)
	}
	if callers.base.BearerToken != "" {
		t.Fatalf("base config kept the provider's token")
	}

	first, err := callers.For(testCluster, "t1")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	again, err := callers.For(testCluster, "t1")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if first != again {
		t.Fatalf("For did not reuse the cached client")
	}
	if other, err := callers.For(testCluster, "t2"); err != nil || other == first {
		t.Fatalf("For keyed the cache on the cluster alone (err %v)", err)
	}
	if _, err := callers.For("root:railgrid:tenants:acme", "t1"); err == nil {
		t.Fatal("For accepted a workspace path as a cluster ID")
	}
	if _, err := callers.For(testCluster, ""); !errors.Is(err, ErrNoBearer) {
		t.Fatalf("For with no token = %v, want ErrNoBearer", err)
	}
}

func TestCallerCacheIsBounded(t *testing.T) {
	callers, err := NewCallerFactory(&rest.Config{Host: "https://hub.example:9443"}, WithCallerCacheSize(4))
	if err != nil {
		t.Fatalf("NewCallerFactory: %v", err)
	}
	for i := range 64 {
		if _, err := callers.For(testCluster, string(rune('a'+i%26))+string(rune('a'+i/26))); err != nil {
			t.Fatalf("For: %v", err)
		}
	}
	callers.mu.Lock()
	size := len(callers.cache)
	callers.mu.Unlock()
	if size > 4 {
		t.Fatalf("cache grew to %d entries, want at most 4", size)
	}
}
