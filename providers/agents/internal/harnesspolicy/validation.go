// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package harnesspolicy validates settings shared by harness execution and
// Agent status reconciliation.
package harnesspolicy

import (
	"fmt"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
)

// UnsupportedFields reports the first spec field that cannot mean anything
// for a harness-backed agent.
//
// Each one is REJECTED rather than ignored, and that is the whole point. An
// ignored spec.tools grant reads as a granted one: the author believes they
// scoped what the agent may do, and the harness — which brings its own Bash and
// its own Edit — is bounded by none of it. Same for a per-purpose model: there is
// no second model to route to, so "background: cheap" is a cost saving that is
// not happening. Silence would make each of these a wrong belief rather than a
// wrong file.
func UnsupportedFields(agent *agentsv1alpha1.Agent) (reason, message string) {
	if len(agent.Spec.Tools.Interactive.Families) > 0 || len(agent.Spec.Tools.Interactive.Connections) > 0 ||
		len(agent.Spec.Tools.Interactive.Toolsets) > 0 || len(agent.Spec.Tools.Interactive.RequireApproval) > 0 ||
		len(agent.Spec.Tools.Background.Families) > 0 || len(agent.Spec.Tools.Background.Connections) > 0 ||
		len(agent.Spec.Tools.Background.Toolsets) > 0 || len(agent.Spec.Tools.Background.RequireApproval) > 0 {
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			"spec.tools has no effect on a harness-backed agent: the harness brings its own tools, and a grant here would read as a restriction that is not enforced anywhere. Remove it."
	}
	switch agent.Spec.Autonomy {
	case agentsv1alpha1.AutonomySuggest:
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			`spec.autonomy "suggest" cannot be enforced by a harness-backed agent: the harness can run its own tools without asking the provider to gate each action. Use a model-backed agent for draft-only behavior.`
	case agentsv1alpha1.AutonomyAuto:
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			`spec.autonomy "auto" cannot be enforced by a harness-backed agent: the provider cannot select the runner's permission mode. Use a model-backed agent for automatic actions.`
	}
	if len(agent.Spec.Delegates) > 0 {
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			"spec.delegates has no effect on a harness-backed agent: the provider's delegate tool is not available to the harness. Remove it or use a model-backed agent."
	}
	if agent.Spec.Limits.MaxToolTurns > 0 {
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			"spec.limits.maxToolTurns has no effect on a harness-backed agent: one runner turn can contain multiple tool calls, and the runner API has no matching tool-turn limit. Remove it or use a model-backed agent."
	}
	if agent.Spec.Limits.MaxSpawnsPerRun > 0 {
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			"spec.limits.maxSpawnsPerRun has no effect on a harness-backed agent: the provider's spawn tool is not available to the harness. Remove it or use a model-backed agent."
	}
	if agent.Spec.Limits.MaxConcurrentSpawns > 0 {
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			"spec.limits.maxConcurrentSpawns has no effect on a harness-backed agent: the provider's spawn tool is not available to the harness. Remove it or use a model-backed agent."
	}
	for purpose := range agent.Spec.ModelCredentials() {
		if purpose != llm.PurposeChat {
			return agentsv1alpha1.ReasonMeaninglessForHarness, fmt.Sprintf(
				"spec.backend.model.credentials[%q] has no effect on a harness-backed agent: there is one harness session and no purpose to route. Use spec.backend.harness instead.", purpose)
		}
	}
	if len(agent.Spec.ModelFallbacks()) > 0 {
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			"spec.backend.model.fallbacks has no effect on a harness-backed agent: a turn runs on one machine's harness, and there is no second endpoint to fail over to."
	}
	return "", ""
}
