/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import (
	"fmt"
	"strings"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

func validateCodingSandboxConfig(spec infrav1alpha1.InfrastructureProviderSpec) error {
	if !spec.CodingSandbox.Enabled {
		return nil
	}
	image := strings.TrimSpace(spec.Development.Images["universal"])
	if err := infrav1alpha1.ValidateImmutableImageRef(image); err != nil {
		return fmt.Errorf("codingSandbox.enabled requires development.images.universal to be immutable: %w", err)
	}
	if err := infrav1alpha1.ValidateImmutableImageRef(spec.Development.AgentImage); err != nil {
		return fmt.Errorf("codingSandbox.enabled requires development.agentImage to be immutable: %w", err)
	}
	return nil
}
