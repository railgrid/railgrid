/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import "strings"

// projectAssistantTurnProfile is the internal tool policy derived from the
// user-selected collaboration mode. It is not a semantic classification.
type projectAssistantTurnProfile string

const (
	projectAssistantTurnProfileDebugging      projectAssistantTurnProfile = "debugging"
	projectAssistantTurnProfileImplementation projectAssistantTurnProfile = "implementation"
)

type projectAssistantTurnPolicy struct {
	profile projectAssistantTurnProfile
}

type projectAssistantModelCapabilities struct {
	VisionToolResults bool
}

// projectAssistantModelCapabilityCatalog is the typed App Studio equivalent
// of Codex's model-catalog capability flags. Unknown models fail closed.
var projectAssistantModelCapabilityCatalog = map[string]projectAssistantModelCapabilities{
	"openai-compatible/gpt-5.4":                {VisionToolResults: true},
	"openai-compatible/gpt-5.6":                {VisionToolResults: true},
	"openai-compatible/gpt-5.6-luna":           {VisionToolResults: true},
	"openai-compatible/gpt-5.6-sol":            {VisionToolResults: true},
	"openai-compatible/gpt-5.6-terra":          {VisionToolResults: true},
	"google-ai-studio/gemini-2.5-pro":          {VisionToolResults: true},
	"google-ai-studio/gemini-3-pro-preview":    {VisionToolResults: true},
	"google-ai-studio/gemini-3.5-flash":        {VisionToolResults: true},
	"google-ai-studio/google/gemini-3.5-flash": {VisionToolResults: true},
}

func projectAssistantCapabilitiesForModel(settings projectLLMSettings) projectAssistantModelCapabilities {
	provider := strings.ToLower(strings.TrimSpace(settings.Provider))
	if provider == "" {
		provider = defaultProjectLLMProvider
	}
	model := strings.ToLower(strings.TrimSpace(settings.Model))
	if model == "" && provider == defaultProjectLLMProvider {
		model = defaultProjectLLMModel
	}
	return projectAssistantModelCapabilityCatalog[provider+"/"+model]
}

func projectAssistantTurnProfileAllowsMutation(profile projectAssistantTurnProfile) bool {
	return normalizeProjectAssistantTurnProfile(profile) == projectAssistantTurnProfileImplementation
}

func normalizeProjectAssistantTurnProfile(profile projectAssistantTurnProfile) projectAssistantTurnProfile {
	if profile == projectAssistantTurnProfileImplementation {
		return profile
	}
	return projectAssistantTurnProfileDebugging
}

func projectAssistantTurnPolicyForProfile(profile projectAssistantTurnProfile) projectAssistantTurnPolicy {
	return projectAssistantTurnPolicy{profile: normalizeProjectAssistantTurnProfile(profile)}
}

func projectAssistantCheckpointTurnPolicyForPolicy(policy projectAssistantTurnPolicy) projectAssistantCheckpointTurnPolicy {
	policy = normalizeProjectAssistantTurnPolicy(policy, projectAssistantTurnProfileDebugging)
	return projectAssistantCheckpointTurnPolicy{Profile: policy.profile}
}

func projectAssistantTurnPolicyForCheckpoint(state projectAssistantCheckpointState) projectAssistantTurnPolicy {
	return normalizeProjectAssistantTurnPolicy(projectAssistantTurnPolicy{profile: state.TurnPolicy.Profile}, projectAssistantTurnProfileDebugging)
}

func normalizeProjectAssistantTurnPolicy(policy projectAssistantTurnPolicy, fallbackProfile projectAssistantTurnProfile) projectAssistantTurnPolicy {
	if strings.TrimSpace(string(policy.profile)) == "" {
		policy.profile = fallbackProfile
	}
	policy.profile = normalizeProjectAssistantTurnProfile(policy.profile)
	return policy
}

func (p projectAssistantTurnPolicy) AllowsTool(spec projectAssistantToolSpec) bool {
	if normalizeProjectAssistantTurnProfile(p.profile) == projectAssistantTurnProfileImplementation {
		return true
	}
	if spec.Risk == projectAssistantToolRiskInput || spec.Risk == projectAssistantToolRiskPlan {
		return true
	}
	switch projectAssistantToolBundleForSpec(spec) {
	case projectAssistantToolBundleWorkflow, projectAssistantToolBundleWorkspaceRead:
		return spec.Risk == projectAssistantToolRiskRead
	case projectAssistantToolBundleInfrastructure, projectAssistantToolBundleRuntime:
		return spec.Risk == projectAssistantToolRiskRead
	default:
		return false
	}
}

func projectAssistantToolCatalogPolicy(req projectAssistantRunRequest) projectAssistantTurnPolicy {
	return normalizeProjectAssistantTurnPolicy(req.TurnPolicy, req.TurnProfile)
}

func projectAssistantToolsForTurnPolicy(tools []projectAssistantTool, policy projectAssistantTurnPolicy) []projectAssistantTool {
	out := make([]projectAssistantTool, 0, len(tools))
	for _, tool := range tools {
		if tool != nil && policy.AllowsTool(tool.Spec()) {
			out = append(out, tool)
		}
	}
	return out
}

func projectAssistantToolSpecsForTurnPolicy(specs []projectAssistantToolSpec, policy projectAssistantTurnPolicy) []projectAssistantToolSpec {
	out := make([]projectAssistantToolSpec, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) != "" && policy.AllowsTool(spec) {
			out = append(out, spec)
		}
	}
	return out
}
