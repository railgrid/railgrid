// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"strings"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

func resolveHarnessSettings(agent *agentsv1alpha1.Agent, snapshot *store.HarnessSettings) *store.HarnessSettings {
	if snapshot != nil {
		copy := *snapshot
		return &copy
	}
	if cfg := agent.Spec.Harness(); cfg != nil {
		return &store.HarnessSettings{Model: strings.TrimSpace(cfg.Model), ReasoningEffort: cfg.ReasoningEffort}
	}
	return nil
}
