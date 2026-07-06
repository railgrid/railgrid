/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import "testing"

// New projects carry no development binding and no template: the environment
// stays empty until select_project_template / PUT /template binds one. The
// legacy always-on SandboxRunner default is gone (no-compat decision,
// docs/app-studio-template-sandboxes.md §7 Phase 4).
func TestDefaultProjectSpecStartsWithoutDevelopmentBinding(t *testing.T) {
	spec := defaultProjectSpec("demo", "Demo", "", nil)
	if spec.Template != nil {
		t.Fatalf("spec.template = %+v, want nil until selection", spec.Template)
	}
	if got := len(spec.Environments); got != 1 {
		t.Fatalf("environments = %d, want 1", got)
	}
	if got := len(spec.Environments[0].Bindings); got != 0 {
		t.Fatalf("development bindings = %d, want none until a template is selected", got)
	}
}
