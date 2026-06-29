/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import "testing"

// App Studio supplies only projectRef on a SandboxRunner binding. The runner
// image is a schema field with a sane default on the sandbox-runner template
// (the web-app convention), so App Studio never sets it — not even from the
// legacy env.
func TestSandboxRunnerValuesSuppliesOnlyProjectRef(t *testing.T) {
	t.Setenv("APP_STUDIO_SANDBOX_RUNNER_IMAGE", "ghcr.io/faroshq/kedge-sandbox-runner@sha256:runner")
	t.Setenv("APP_STUDIO_SANDBOX_TOKEN_GENERATOR_IMAGE", "registry.example.com/kubectl@sha256:token")

	values := sandboxRunnerValues("demo")
	if got := values["projectRef"]; got != "demo" {
		t.Fatalf("projectRef = %q, want demo", got)
	}
	if len(values) != 1 {
		t.Fatalf("sandboxRunnerValues = %#v, want only projectRef (image is a template schema default)", values)
	}
}
