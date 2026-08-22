// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"strings"
	"testing"
)

func TestBootstrapOnceRejectsMutableUniversalImageBeforeSeed(t *testing.T) {
	t.Setenv("FAROS_CODING_SANDBOX_ENABLED", "true")
	t.Setenv("FAROS_DEV_IMAGE_UNIVERSAL", "ghcr.io/faroshq/faros-universal-dev:latest")

	err := bootstrapOnce(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected legacy bootstrap to reject a mutable universal image before seeding")
	}
	if !strings.Contains(err.Error(), "immutable sha256 digest") {
		t.Fatalf("error = %q, want immutable digest validation", err)
	}
}

func TestValidateLegacyCodingSandboxImagePreservesDisabledPath(t *testing.T) {
	t.Setenv("FAROS_CODING_SANDBOX_ENABLED", "false")
	t.Setenv("FAROS_DEV_IMAGE_UNIVERSAL", "ghcr.io/faroshq/faros-universal-dev:latest")
	t.Setenv("FAROS_DEV_AGENT_IMAGE", "ghcr.io/faroshq/faros-dev-agent:latest")
	if err := validateLegacyCodingSandboxImage(); err != nil {
		t.Fatalf("disabled coding sandbox rejected mutable image: %v", err)
	}
}

func TestValidateLegacyCodingSandboxImageRejectsMutableDevAgent(t *testing.T) {
	t.Setenv("FAROS_CODING_SANDBOX_ENABLED", "true")
	t.Setenv("FAROS_DEV_IMAGE_UNIVERSAL", "ghcr.io/faroshq/faros-universal-dev@sha256:"+strings.Repeat("a", 64))
	t.Setenv("FAROS_DEV_AGENT_IMAGE", "ghcr.io/faroshq/faros-dev-agent:latest")
	if err := validateLegacyCodingSandboxImage(); err == nil || !strings.Contains(err.Error(), "dev-agent image") {
		t.Fatalf("error = %v, want mutable dev-agent validation", err)
	}
}
