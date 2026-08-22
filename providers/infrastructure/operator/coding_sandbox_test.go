/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import (
	"strings"
	"testing"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

func TestValidateCodingSandboxConfig(t *testing.T) {
	spec := infrav1alpha1.InfrastructureProviderSpec{}
	if err := validateCodingSandboxConfig(spec); err != nil {
		t.Fatalf("disabled coding sandbox rejected: %v", err)
	}
	spec.CodingSandbox.Enabled = true
	if err := validateCodingSandboxConfig(spec); err == nil {
		t.Fatal("enabled coding sandbox without universal image was accepted")
	}
	spec.Development.Images = map[string]string{
		"universal": "ghcr.io/faroshq/faros-universal-dev@sha256:" + strings.Repeat("a", 64),
	}
	if err := validateCodingSandboxConfig(spec); err == nil || !strings.Contains(err.Error(), "development.agentImage") {
		t.Fatalf("missing agent image error = %v, want agent image validation", err)
	}
	spec.Development.AgentImage = "ghcr.io/faroshq/faros-dev-agent@sha256:" + strings.Repeat("b", 64)
	if err := validateCodingSandboxConfig(spec); err != nil {
		t.Fatalf("immutable universal and agent images rejected: %v", err)
	}
}

func TestValidateCodingSandboxConfigRejectsMutableAgentImage(t *testing.T) {
	spec := infrav1alpha1.InfrastructureProviderSpec{
		CodingSandbox: infrav1alpha1.CodingSandboxSpec{Enabled: true},
		Development: infrav1alpha1.DevelopmentSpec{
			AgentImage: "ghcr.io/faroshq/faros-dev-agent:latest",
			Images: map[string]string{
				"universal": "ghcr.io/faroshq/faros-universal-dev@sha256:" + strings.Repeat("a", 64),
			},
		},
	}
	if err := validateCodingSandboxConfig(spec); err == nil || !strings.Contains(err.Error(), "development.agentImage") {
		t.Fatalf("mutable agent image error = %v, want agent image validation", err)
	}
}
