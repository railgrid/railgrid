// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"testing"

	"github.com/railgrid/provider-agents/api"
	"github.com/railgrid/provider-agents/executor"
)

// submitterWithPurge is an executor that also knows how to tear an agent's
// store data down — the shape api's *background has.
type submitterWithPurge struct{ called bool }

func (s *submitterWithPurge) Submit(context.Context, executor.Job) error { return nil }

func (s *submitterWithPurge) PurgeAgentData(_ context.Context, _, _ string) error {
	s.called = true
	return nil
}

// submitterOnly is an executor with no teardown.
type submitterOnly struct{}

func (submitterOnly) Submit(context.Context, executor.Job) error { return nil }

// The Agent finalizer is wired from the executor rather than from a field of
// ControllerDeps, because the store and the cluster→tenant mapping the purge
// needs both live behind the HTTP half of the provider.
func TestAgentDataPurgerFindsTheTeardown(t *testing.T) {
	exec := &submitterWithPurge{}
	purge := agentDataPurger(api.ControllerDeps{Submit: exec})
	if purge == nil {
		t.Fatal("an executor that exposes PurgeAgentData must be discovered")
	}
	if err := purge(context.Background(), "tenant-a", "helper"); err != nil {
		t.Fatal(err)
	}
	if !exec.called {
		t.Fatal("the discovered function must be the executor's own")
	}
}

// Without a teardown the purge is nil, which disables the finalizer outright.
// A finalizer nothing can clear would make every agent undeletable, so "no
// store" must mean "no finalizer", not "a finalizer that never completes".
func TestAgentDataPurgerIsNilWithoutATeardown(t *testing.T) {
	if purge := agentDataPurger(api.ControllerDeps{Submit: submitterOnly{}}); purge != nil {
		t.Fatal("an executor with no teardown must yield no purge function")
	}
	if purge := agentDataPurger(api.ControllerDeps{}); purge != nil {
		t.Fatal("a missing executor must yield no purge function")
	}
}
