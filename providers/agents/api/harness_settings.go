// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"fmt"
	"strings"

	runnerharness "github.com/railgrid/railgrid/pkg/runner/harness"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

func resolveHarnessSettings(agent *agentsv1alpha1.Agent, snapshot *store.HarnessSettings) *store.HarnessSettings {
	if snapshot != nil {
		dup := *snapshot
		return &dup
	}
	if cfg := agent.Spec.Harness(); cfg != nil {
		return &store.HarnessSettings{Model: strings.TrimSpace(cfg.Model), ReasoningEffort: cfg.ReasoningEffort}
	}
	return nil
}

// invokeHarnessSettings validates transport shape before admission or persistence.
// invokeAgentRun separately checks the credential's actual harness provider.
func invokeHarnessSettings(agent *agentsv1alpha1.Agent, model, effort *string) (*store.HarnessSettings, error) {
	settings := resolveHarnessSettings(agent, nil)
	if settings == nil {
		if model != nil || effort != nil {
			return nil, fmt.Errorf("model and reasoningEffort overrides require a harness-backed agent")
		}
		return nil, nil
	}
	if model != nil {
		settings.Model = strings.TrimSpace(*model)
	}
	if effort != nil {
		settings.ReasoningEffort = strings.TrimSpace(*effort)
	}
	if len(settings.Model) > 253 {
		return nil, fmt.Errorf("model exceeds its length limit")
	}
	if err := runnerharness.ValidateReasoningEffort("codex", settings.ReasoningEffort); err != nil {
		return nil, err
	}
	return settings, nil
}
