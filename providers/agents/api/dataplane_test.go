// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

// Instance-backed tools (self-hosted search, a browser instance) need BOTH the
// caller's hub token and the tenant cluster ID. Every execution path builds its
// own taskRun, and a path that forgets either one degrades silently: the tool
// still loads, the model still calls it, and the user gets an error that reads
// like the instance is broken. This asserts the wiring per path instead.
func TestDataPlaneForRunPaths(t *testing.T) {
	s := &Server{}
	s.cfg.HubURL = "https://hub.example.com"

	t.Run("a run carrying both is usable", func(t *testing.T) {
		dp := s.dataPlaneFor(taskRun{ClusterID: "23qp2e0jwjeqwp2i", HubToken: "user-token"})
		if !dp.Available() {
			t.Fatalf("data plane unusable: %+v", dp)
		}
	})

	t.Run("a background run is unusable, by design", func(t *testing.T) {
		// No user to act as. The tool reports this precisely; it must not
		// silently compose a URL that 401s at the hub.
		if s.dataPlaneFor(taskRun{ClusterID: "23qp2e0jwjeqwp2i", Trigger: agentsv1alpha1.RunTriggerSchedule}).Available() {
			t.Fatal("a run with no hub token must not be usable")
		}
	})

	t.Run("a token without a cluster is unusable", func(t *testing.T) {
		// The regression that motivated this test: the interactive chat path
		// set HubToken but not ClusterID, so search failed in chat — the one
		// place it was supposed to work.
		if s.dataPlaneFor(taskRun{HubToken: "user-token"}).Available() {
			t.Fatal("a run with no cluster ID must not be usable")
		}
	})
}
