/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import "testing"

func TestSandboxRunnerValuesOmitImagesWithoutConfiguration(t *testing.T) {
	t.Setenv("APP_STUDIO_SANDBOX_RUNNER_IMAGE", "")
	t.Setenv("APP_STUDIO_SANDBOX_TOKEN_GENERATOR_IMAGE", "")

	values := sandboxRunnerValues("demo")
	if got := values["projectRef"]; got != "demo" {
		t.Fatalf("projectRef = %q, want demo", got)
	}
	if _, ok := values["runnerImage"]; ok {
		t.Fatal("runnerImage must not default to a mutable development image")
	}
	if _, ok := values["tokenGeneratorImage"]; ok {
		t.Fatal("tokenGeneratorImage must not default to a mutable development image")
	}
}

func TestSandboxRunnerValuesUseConfiguredImages(t *testing.T) {
	t.Setenv("APP_STUDIO_SANDBOX_RUNNER_IMAGE", " ghcr.io/faroshq/kedge-sandbox-runner@sha256:runner ")
	t.Setenv("APP_STUDIO_SANDBOX_TOKEN_GENERATOR_IMAGE", " registry.example.com/kubectl@sha256:token ")

	values := sandboxRunnerValues("demo")
	if got := values["runnerImage"]; got != "ghcr.io/faroshq/kedge-sandbox-runner@sha256:runner" {
		t.Fatalf("runnerImage = %q, want configured digest", got)
	}
	if got := values["tokenGeneratorImage"]; got != "registry.example.com/kubectl@sha256:token" {
		t.Fatalf("tokenGeneratorImage = %q, want configured digest", got)
	}
}
