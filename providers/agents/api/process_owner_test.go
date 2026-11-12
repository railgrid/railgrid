// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"

	"k8s.io/client-go/rest"
)

func TestNewAssignsOneProcessIncarnationForAPIAndController(t *testing.T) {
	first, err := New(t.Context(), Config{InMemoryStore: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	second, err := New(t.Context(), Config{InMemoryStore: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	if first.cfg.ProcessID == "" || first.cfg.ProcessID == second.cfg.ProcessID {
		t.Fatalf("process IDs = %q and %q; each New server needs a unique incarnation", first.cfg.ProcessID, second.cfg.ProcessID)
	}

	first.bg = &background{base: &rest.Config{}}
	deps, ok := first.ControllerDeps()
	if !ok || deps.ProcessID != first.cfg.ProcessID {
		t.Fatalf("controller process ID = %q (enabled=%t), want API process ID %q", deps.ProcessID, ok, first.cfg.ProcessID)
	}
}
