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

import (
	"context"
	"encoding/json"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	einoschema "github.com/cloudwego/eino/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

type projectAssistantTurnProfile string
type projectAssistantTurnConfidence string

const (
	projectAssistantTurnProfileDiscussion     projectAssistantTurnProfile = "discussion"
	projectAssistantTurnProfileGuidance       projectAssistantTurnProfile = "guidance"
	projectAssistantTurnProfileExploration    projectAssistantTurnProfile = "exploration"
	projectAssistantTurnProfileDebugging      projectAssistantTurnProfile = "debugging"
	projectAssistantTurnProfileDebugFix       projectAssistantTurnProfile = "debug_fix"
	projectAssistantTurnProfileImplementation projectAssistantTurnProfile = "implementation"
)

const (
	projectAssistantTurnConfidenceHigh   projectAssistantTurnConfidence = "high"
	projectAssistantTurnConfidenceMedium projectAssistantTurnConfidence = "medium"
	projectAssistantTurnConfidenceLow    projectAssistantTurnConfidence = "low"
)

const projectAssistantTurnClassifierSystemPrompt = `Classify the latest user turn for App Studio's project assistant.
Return only one JSON object with these fields:
- profile: one of "discussion", "guidance", "exploration", "debugging", "debug_fix", "implementation"
- requires_current_state: boolean
- requires_runtime_state: boolean
- requests_mutation: boolean
- confidence: one of "high", "medium", "low"

Definitions:
- discussion: conceptual, exploratory, or conversational; no current project state required.
- guidance: recommendation, how-to, architecture, or design direction; no current project state required.
- exploration: asks about current project, files, workspace, repository, runtime, or preview state without asking for a change.
- debugging: asks to diagnose, explain, or inspect a problem without changing the app.
- debug_fix: reports broken/problematic app behavior or asks to fix, solve, repair, resolve, or make it work, unless the user explicitly asks for diagnosis only.
- implementation: asks to build, add, change, remove, update, deploy, provision, write code, or otherwise evolve the app.

Do not call tools. Do not answer the user. Classify intent only.`

type projectAssistantTurnDecision struct {
	Profile              projectAssistantTurnProfile    `json:"profile"`
	RequiresCurrentState bool                           `json:"requires_current_state"`
	RequiresRuntimeState bool                           `json:"requires_runtime_state"`
	RequestsMutation     bool                           `json:"requests_mutation"`
	Confidence           projectAssistantTurnConfidence `json:"confidence"`
}

type projectAssistantTurnPolicy struct {
	profile              projectAssistantTurnProfile
	requiresRuntimeState bool
}

type projectAssistantTurnRouteRequest struct {
	LLM     projectLLMSettings
	History []store.Message
}

type projectAssistantTurnRouter func(context.Context, projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error)

func classifyProjectAssistantTurnProfile(history []store.Message) projectAssistantTurnProfile {
	return fallbackProjectAssistantTurnDecision(history).Profile
}

func classifyProjectAssistantTurnWithModel(ctx context.Context, model einomodel.BaseChatModel, history []store.Message, extraOpts ...einomodel.Option) (projectAssistantTurnDecision, error) {
	fallback := fallbackProjectAssistantTurnDecision(history)
	if model == nil {
		return fallback, nil
	}
	opts := append([]einomodel.Option{einomodel.WithToolChoice(einoschema.ToolChoiceForbidden)}, extraOpts...)
	msg, err := model.Generate(ctx, []*einoschema.Message{
		einoschema.SystemMessage(projectAssistantTurnClassifierSystemPrompt),
		einoschema.UserMessage(projectAssistantTurnClassifierUserPrompt(history)),
	}, opts...)
	if err != nil || msg == nil || len(msg.ToolCalls) > 0 {
		return fallback, nil
	}
	decision, ok := parseProjectAssistantTurnDecision(msg.Content)
	if !ok {
		return fallback, nil
	}
	decision, ok = normalizeProjectAssistantTurnDecision(decision)
	if !ok || decision.Confidence == projectAssistantTurnConfidenceLow {
		return fallback, nil
	}
	decision = reconcileProjectAssistantTurnDecision(decision, fallback)
	return decision, nil
}

func projectAssistantSemanticTurnRouter(ctx context.Context, req projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
	model, err := newProjectEinoChatModel(ctx, req.LLM)
	if err != nil {
		return fallbackProjectAssistantTurnDecision(req.History), nil
	}
	return classifyProjectAssistantTurnWithModel(ctx, model, req.History, projectTemperatureOptions(req.LLM.Model, 0)...)
}

func projectAssistantFallbackTurnRouter(_ context.Context, req projectAssistantTurnRouteRequest) (projectAssistantTurnDecision, error) {
	return fallbackProjectAssistantTurnDecision(req.History), nil
}

func fallbackProjectAssistantTurnDecision(history []store.Message) projectAssistantTurnDecision {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != aiv1alpha1.ProjectMessageRoleUser {
			continue
		}
		if content := strings.TrimSpace(history[i].Content); content != "" {
			return fallbackProjectAssistantTurnDecisionForMessage(content)
		}
	}
	return fallbackProjectAssistantTurnDecisionWithProfile(projectAssistantTurnProfileDiscussion)
}

func fallbackProjectAssistantTurnDecisionForMessage(content string) projectAssistantTurnDecision {
	normalized := strings.ToLower(strings.TrimSpace(content))
	if normalized == "" {
		return fallbackProjectAssistantTurnDecisionWithProfile(projectAssistantTurnProfileDiscussion)
	}
	hasDebug := containsProjectAssistantTurnKeyword(normalized, []string{
		"error", "failed", "failure", "failing", "bug", "broken", "not working", "doesn't work", "does not work",
		"stack trace", "exception", "crash", "crashing", "issue", "problem", "wrong", "failed to fetch",
		"didn't do anything", "didnt do anything", "doesn't do anything", "does not do anything", "does nothing",
		"nothing happens", "unresponsive",
	})
	hasFix := containsProjectAssistantTurnKeyword(normalized, []string{
		"fix", "solve", "repair", "resolve", "make it work", "make this work", "get it working",
	})
	diagnosisOnly := containsProjectAssistantTurnKeyword(normalized, []string{
		"why", "diagnose", "diagnosis", "explain", "what's wrong", "what is wrong", "root cause",
		"debug only", "do not change", "don't change", "without changing", "no changes", "read only", "read-only",
	})
	if hasFix {
		return fallbackProjectAssistantTurnDecisionWithProfile(projectAssistantTurnProfileDebugFix)
	}
	if hasDebug {
		if !diagnosisOnly {
			return fallbackProjectAssistantTurnDecisionWithProfile(projectAssistantTurnProfileDebugFix)
		}
		return fallbackProjectAssistantTurnDecisionWithProfile(projectAssistantTurnProfileDebugging)
	}
	if containsProjectAssistantTurnKeyword(normalized, []string{
		"build", "add", "change", "update", "implement", "write", "make the app", "create", "remove", "delete", "ship", "commit", "deploy", "provision",
		"git", "push", "pull request", "branch", "merge",
	}) {
		return fallbackProjectAssistantTurnDecisionWithProfile(projectAssistantTurnProfileImplementation)
	}
	if containsProjectAssistantTurnKeyword(normalized, []string{
		"what files", "which files", "show me", "current app", "current project", "in my app", "in this app",
		"in my project", "workspace", "runtime", "preview", "repository", "codebase", "how is", "where is",
	}) {
		decision := fallbackProjectAssistantTurnDecisionWithProfile(projectAssistantTurnProfileExploration)
		decision.RequiresRuntimeState = containsProjectAssistantTurnKeyword(normalized, []string{"runtime", "preview"})
		return decision
	}
	if containsProjectAssistantTurnKeyword(normalized, []string{
		"how should", "what should", "recommend", "recommendation", "guidance", "advise", "advice", "best way",
		"design", "architecture", "approach", "tradeoff", "trade-off", "strategy",
	}) {
		return fallbackProjectAssistantTurnDecisionWithProfile(projectAssistantTurnProfileGuidance)
	}
	return fallbackProjectAssistantTurnDecisionWithProfile(projectAssistantTurnProfileDiscussion)
}

func fallbackProjectAssistantTurnDecisionWithProfile(profile projectAssistantTurnProfile) projectAssistantTurnDecision {
	profile = normalizeProjectAssistantTurnProfile(profile)
	return projectAssistantTurnDecision{
		Profile:              profile,
		RequiresCurrentState: profile == projectAssistantTurnProfileExploration || profile == projectAssistantTurnProfileDebugging || profile == projectAssistantTurnProfileDebugFix || profile == projectAssistantTurnProfileImplementation,
		RequiresRuntimeState: profile == projectAssistantTurnProfileDebugging || profile == projectAssistantTurnProfileDebugFix,
		RequestsMutation:     profile == projectAssistantTurnProfileDebugFix || profile == projectAssistantTurnProfileImplementation,
		Confidence:           projectAssistantTurnConfidenceMedium,
	}
}

func projectAssistantTurnClassifierUserPrompt(history []store.Message) string {
	var b strings.Builder
	b.WriteString("Recent conversation, oldest to newest:\n")
	start := len(history) - 8
	if start < 0 {
		start = 0
	}
	for _, msg := range history[start:] {
		if msg.Role != aiv1alpha1.ProjectMessageRoleUser && msg.Role != aiv1alpha1.ProjectMessageRoleAssistant {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		b.WriteString(string(msg.Role))
		b.WriteString(": ")
		b.WriteString(content)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func parseProjectAssistantTurnDecision(content string) (projectAssistantTurnDecision, bool) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}
	if start := strings.Index(content, "{"); start >= 0 {
		if end := strings.LastIndex(content, "}"); end > start {
			content = content[start : end+1]
		}
	}
	var decision projectAssistantTurnDecision
	if err := json.Unmarshal([]byte(content), &decision); err != nil {
		return projectAssistantTurnDecision{}, false
	}
	return decision, true
}

func normalizeProjectAssistantTurnDecision(decision projectAssistantTurnDecision) (projectAssistantTurnDecision, bool) {
	profile := normalizeProjectAssistantTurnProfile(decision.Profile)
	if profile == projectAssistantTurnProfileDiscussion && decision.Profile != projectAssistantTurnProfileDiscussion {
		return projectAssistantTurnDecision{}, false
	}
	decision.Profile = profile
	switch decision.Confidence {
	case projectAssistantTurnConfidenceHigh, projectAssistantTurnConfidenceMedium, projectAssistantTurnConfidenceLow:
	default:
		decision.Confidence = projectAssistantTurnConfidenceMedium
	}
	return decision, true
}

func reconcileProjectAssistantTurnDecision(decision projectAssistantTurnDecision, fallback projectAssistantTurnDecision) projectAssistantTurnDecision {
	if fallback.RequestsMutation && !decision.RequestsMutation && projectAssistantTurnProfileAllowsMutation(fallback.Profile) {
		fallback.Confidence = decision.Confidence
		fallback.RequiresRuntimeState = fallback.RequiresRuntimeState || decision.RequiresRuntimeState
		return fallback
	}
	if decision.RequestsMutation && !projectAssistantTurnProfileAllowsMutation(decision.Profile) {
		if fallback.RequestsMutation && projectAssistantTurnProfileAllowsMutation(fallback.Profile) {
			fallback.Confidence = decision.Confidence
			return fallback
		}
		decision.Profile = projectAssistantTurnProfileImplementation
		decision.RequiresCurrentState = true
		return decision
	}
	if (decision.RequiresCurrentState || decision.RequiresRuntimeState) && !projectAssistantTurnProfileAllowsCurrentState(decision.Profile) {
		if fallback.RequiresCurrentState && projectAssistantTurnProfileAllowsCurrentState(fallback.Profile) {
			fallback.Confidence = decision.Confidence
			fallback.RequiresRuntimeState = fallback.RequiresRuntimeState || decision.RequiresRuntimeState
			return fallback
		}
		decision.Profile = projectAssistantTurnProfileExploration
		decision.RequiresCurrentState = true
		return decision
	}
	return decision
}

func projectAssistantTurnProfileAllowsMutation(profile projectAssistantTurnProfile) bool {
	switch normalizeProjectAssistantTurnProfile(profile) {
	case projectAssistantTurnProfileDebugFix, projectAssistantTurnProfileImplementation:
		return true
	default:
		return false
	}
}

func projectAssistantTurnProfileAllowsCurrentState(profile projectAssistantTurnProfile) bool {
	switch normalizeProjectAssistantTurnProfile(profile) {
	case projectAssistantTurnProfileExploration, projectAssistantTurnProfileDebugging, projectAssistantTurnProfileDebugFix, projectAssistantTurnProfileImplementation:
		return true
	default:
		return false
	}
}

func normalizeProjectAssistantTurnProfile(profile projectAssistantTurnProfile) projectAssistantTurnProfile {
	switch profile {
	case projectAssistantTurnProfileDiscussion,
		projectAssistantTurnProfileGuidance,
		projectAssistantTurnProfileExploration,
		projectAssistantTurnProfileDebugging,
		projectAssistantTurnProfileDebugFix,
		projectAssistantTurnProfileImplementation:
		return profile
	default:
		return projectAssistantTurnProfileDiscussion
	}
}

func projectAssistantTurnPolicyForProfile(profile projectAssistantTurnProfile) projectAssistantTurnPolicy {
	return projectAssistantTurnPolicy{profile: normalizeProjectAssistantTurnProfile(profile)}
}

func projectAssistantCheckpointTurnPolicyForPolicy(policy projectAssistantTurnPolicy) projectAssistantCheckpointTurnPolicy {
	policy = normalizeProjectAssistantTurnPolicy(policy, projectAssistantTurnProfileDiscussion)
	return projectAssistantCheckpointTurnPolicy{
		Profile:              policy.profile,
		RequiresRuntimeState: policy.requiresRuntimeState,
	}
}

func projectAssistantTurnPolicyForCheckpoint(state projectAssistantCheckpointState) projectAssistantTurnPolicy {
	return normalizeProjectAssistantTurnPolicy(projectAssistantTurnPolicy{
		profile:              state.TurnPolicy.Profile,
		requiresRuntimeState: state.TurnPolicy.RequiresRuntimeState,
	}, projectAssistantTurnProfileDiscussion)
}

func projectAssistantTurnPolicyForDecision(decision projectAssistantTurnDecision) projectAssistantTurnPolicy {
	decision, ok := normalizeProjectAssistantTurnDecision(decision)
	if !ok {
		return projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDiscussion)
	}
	return projectAssistantTurnPolicy{
		profile:              decision.Profile,
		requiresRuntimeState: decision.RequiresRuntimeState,
	}
}

func normalizeProjectAssistantTurnPolicy(policy projectAssistantTurnPolicy, fallbackProfile projectAssistantTurnProfile) projectAssistantTurnPolicy {
	if strings.TrimSpace(string(policy.profile)) == "" {
		return projectAssistantTurnPolicyForProfile(fallbackProfile)
	}
	return projectAssistantTurnPolicy{
		profile:              normalizeProjectAssistantTurnProfile(policy.profile),
		requiresRuntimeState: policy.requiresRuntimeState,
	}
}

func (p projectAssistantTurnPolicy) AllowsTool(spec projectAssistantToolSpec) bool {
	switch normalizeProjectAssistantTurnProfile(p.profile) {
	case projectAssistantTurnProfileDiscussion, projectAssistantTurnProfileGuidance:
		return false
	case projectAssistantTurnProfileExploration:
		switch projectAssistantToolBundleForSpec(spec) {
		case projectAssistantToolBundleWorkflow, projectAssistantToolBundleWorkspaceRead:
			return true
		case projectAssistantToolBundleInfrastructure:
			return spec.Risk == projectAssistantToolRiskRead
		case projectAssistantToolBundleRuntime:
			return p.requiresRuntimeState && spec.Risk == projectAssistantToolRiskRead
		default:
			return false
		}
	case projectAssistantTurnProfileDebugging:
		switch projectAssistantToolBundleForSpec(spec) {
		case projectAssistantToolBundleWorkflow, projectAssistantToolBundleWorkspaceRead:
			return true
		case projectAssistantToolBundleInfrastructure:
			return spec.Risk == projectAssistantToolRiskRead
		case projectAssistantToolBundleRuntime:
			return spec.Risk == projectAssistantToolRiskRead
		default:
			return false
		}
	case projectAssistantTurnProfileDebugFix, projectAssistantTurnProfileImplementation:
		return true
	default:
		return false
	}
}

func projectAssistantToolsForTurnProfile(tools []projectAssistantTool, profile projectAssistantTurnProfile) []projectAssistantTool {
	return projectAssistantToolsForTurnPolicy(tools, projectAssistantTurnPolicyForProfile(profile))
}

func projectAssistantToolsForTurnPolicy(tools []projectAssistantTool, policy projectAssistantTurnPolicy) []projectAssistantTool {
	out := make([]projectAssistantTool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if policy.AllowsTool(tool.Spec()) {
			out = append(out, tool)
		}
	}
	return out
}

func projectAssistantToolSpecsForTurnPolicy(specs []projectAssistantToolSpec, policy projectAssistantTurnPolicy) []projectAssistantToolSpec {
	out := make([]projectAssistantToolSpec, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) == "" {
			continue
		}
		if policy.AllowsTool(spec) {
			out = append(out, spec)
		}
	}
	return out
}

func containsProjectAssistantTurnKeyword(value string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(value, keyword) {
			return true
		}
	}
	return false
}
