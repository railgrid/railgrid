/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

const codingSandboxEnabledEnv = "FAROS_CODING_SANDBOX_ENABLED"

func codingSandboxEnabled() bool {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(codingSandboxEnabledEnv)))
	return enabled
}

// validateLegacyCodingSandboxImage is the env-driven operator's fail-closed
// configuration check. The CR-driven operator validates the image from its
// InfrastructureProvider spec; the legacy operator has no CR, so it must
// validate FAROS_DEV_IMAGE_UNIVERSAL before the shared bootstrap can attempt
// to seed the platform-owned Template.
func validateLegacyCodingSandboxImage() error {
	if !codingSandboxEnabled() {
		return nil
	}
	if err := infrav1alpha1.ValidateImmutableImageRef(os.Getenv("FAROS_DEV_IMAGE_UNIVERSAL")); err != nil {
		return fmt.Errorf("coding sandbox universal image is not immutable: %w", err)
	}
	if err := infrav1alpha1.ValidateImmutableImageRef(os.Getenv("FAROS_DEV_AGENT_IMAGE")); err != nil {
		return fmt.Errorf("coding sandbox dev-agent image is not immutable: %w", err)
	}
	return nil
}
