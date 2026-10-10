/*
Copyright 2026 The Railgrid Authors.

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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"unicode"
)

const (
	projectEinoAssistantOptimizationEnv           = "APP_STUDIO_AGENT_OPTIMIZATIONS"
	projectEinoAssistantOptimizationCodexPOC      = "codex_poc"
	projectEinoAssistantMaxToolSearchResults      = 5
	projectEinoAssistantMaxSelectedDynamicTools   = 16
	projectEinoAssistantToolSearchSummaryMaxRunes = 180
)

type projectEinoAssistantToolSearchMatch struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Risk    string `json:"risk"`
	Bundle  string `json:"bundle"`
}

type projectEinoAssistantToolSearchResult struct {
	CatalogDigest string                                `json:"catalogDigest"`
	Matches       []projectEinoAssistantToolSearchMatch `json:"matches"`
}

func projectEinoAssistantOptimizationModeFromEnvironment() string {
	mode := strings.TrimSpace(os.Getenv(projectEinoAssistantOptimizationEnv))
	if mode == "" {
		// Provider catalogs can exceed the model's tool limit. Keep them
		// searchable and expose only selected tools by default.
		return projectEinoAssistantOptimizationCodexPOC
	}
	return projectEinoAssistantNormalizeOptimizationMode(mode)
}

func projectEinoAssistantNormalizeOptimizationMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), projectEinoAssistantOptimizationCodexPOC) {
		return projectEinoAssistantOptimizationCodexPOC
	}
	return ""
}

func projectEinoAssistantDynamicToolNameSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, min(len(names), projectEinoAssistantMaxSelectedDynamicTools))
	for _, name := range names {
		name = projectAssistantToolKey(name)
		if name == "" || len(out) >= projectEinoAssistantMaxSelectedDynamicTools {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}

func projectEinoAssistantSortedDynamicToolNames(names map[string]struct{}) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		if name = projectAssistantToolKey(name); name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if len(out) > projectEinoAssistantMaxSelectedDynamicTools {
		out = out[:projectEinoAssistantMaxSelectedDynamicTools]
	}
	return out
}

func projectEinoAssistantDynamicToolSelectionOrdinals(
	selected map[string]struct{},
	ordinals map[string]int,
	fallback int,
) map[string]int {
	fallback = max(fallback, 0)
	out := make(map[string]int, min(len(selected), projectEinoAssistantMaxSelectedDynamicTools))
	for name := range selected {
		name = projectAssistantToolKey(name)
		if name == "" || len(out) >= projectEinoAssistantMaxSelectedDynamicTools {
			continue
		}
		ordinal, ok := ordinals[name]
		if !ok || ordinal < 0 {
			// Older checkpoints persist selection names but not their sample
			// boundary. Treat those selections as made at the checkpoint's latest
			// model call so they cannot authorize an in-flight same-batch call.
			ordinal = fallback
		}
		out[name] = ordinal
	}
	return out
}

func projectEinoAssistantDynamicToolCatalogDigest(discovery projectEinoAssistantToolDiscovery) string {
	type contract struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Parameters  string `json:"parameters,omitempty"`
		Risk        string `json:"risk,omitempty"`
	}
	items := make([]contract, 0, len(discovery.MCPTools)+len(discovery.BrowserTools)+len(discovery.DeferredWorkflowTools)+1)
	for _, spec := range discovery.DeferredWorkflowTools {
		if strings.TrimSpace(spec.Name) == "" {
			continue
		}
		items = append(items, contract{
			Name: projectAssistantToolKey(spec.Name), Description: strings.TrimSpace(spec.Description),
			Parameters: string(spec.Parameters), Risk: string(spec.Risk),
		})
	}
	for _, tool := range discovery.DeferredLocalTools {
		if tool == nil {
			continue
		}
		spec := tool.Spec()
		items = append(items, contract{
			Name: projectAssistantToolKey(spec.Name), Description: strings.TrimSpace(spec.Description),
			Parameters: string(spec.Parameters), Risk: string(spec.Risk),
		})
	}
	if discovery.IncludeCommitBridge {
		commitSpec := projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit}
		for _, tool := range projectAssistantLocalToolRegistry(nil).Tools(true) {
			if tool != nil && projectAssistantToolKey(tool.Spec().Name) == projectToolCommitProjectFiles {
				commitSpec = tool.Spec()
				break
			}
		}
		items = append(items, contract{
			Name: projectAssistantToolKey(commitSpec.Name), Description: strings.TrimSpace(commitSpec.Description),
			Parameters: string(commitSpec.Parameters), Risk: string(commitSpec.Risk),
		})
	}
	for _, tool := range discovery.MCPTools {
		if tool == nil {
			continue
		}
		spec := tool.Spec()
		items = append(items, contract{
			Name: projectAssistantToolKey(spec.Name), Description: strings.TrimSpace(spec.Description),
			Parameters: string(spec.Parameters), Risk: string(spec.Risk),
		})
	}
	for _, tool := range discovery.BrowserTools {
		if tool == nil {
			continue
		}
		spec := tool.Spec()
		items = append(items, contract{
			Name: projectAssistantToolKey(spec.Name), Description: strings.TrimSpace(spec.Description),
			Parameters: string(spec.Parameters), Risk: string(spec.Risk),
		})
	}
	if len(items) == 0 {
		return ""
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		if items[i].Description != items[j].Description {
			return items[i].Description < items[j].Description
		}
		if items[i].Parameters != items[j].Parameters {
			return items[i].Parameters < items[j].Parameters
		}
		return items[i].Risk < items[j].Risk
	})
	raw, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func projectEinoAssistantDynamicTools(
	server *Server,
	req projectAssistantRunRequest,
	discovery projectEinoAssistantToolDiscovery,
) []projectAssistantTool {
	policy := projectAssistantToolCatalogPolicy(req)
	out := make([]projectAssistantTool, 0, len(discovery.MCPTools)+len(discovery.BrowserTools)+1)
	out = append(out, projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(discovery.DeferredLocalTools, policy), req.CollaborationMode)...)
	if server != nil && discovery.IncludeCommitBridge {
		for _, tool := range projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(server.projectAssistantToolRegistry().Tools(true), policy), req.CollaborationMode) {
			if tool != nil && tool.Spec().Risk == projectAssistantToolRiskCommit {
				out = append(out, tool)
			}
		}
	}
	out = append(out, projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(discovery.MCPTools, policy), req.CollaborationMode)...)
	out = append(out, projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(discovery.BrowserTools, policy), req.CollaborationMode)...)
	return out
}

// projectEinoAssistantDynamicToolSpecs combines deferred registry tools with
// workflow metadata without constructing local stand-ins for graph tools.
// Graph tools remain in the graph factory and retain their permission and
// durable execution wrappers; this list is used only for search and selection.
func projectEinoAssistantDynamicToolSpecs(
	server *Server,
	req projectAssistantRunRequest,
	discovery projectEinoAssistantToolDiscovery,
) []projectAssistantToolSpec {
	policy := projectAssistantToolCatalogPolicy(req)
	out := make([]projectAssistantToolSpec, 0, len(discovery.DeferredWorkflowTools)+len(discovery.DeferredLocalTools)+len(discovery.MCPTools)+len(discovery.BrowserTools)+1)
	for _, spec := range projectAssistantToolSpecsForTurnPolicy(discovery.DeferredWorkflowTools, policy) {
		if projectEinoAssistantLocalToolDeferred(spec.Name) && projectEinoAssistantToolSpecAllowedForCollaborationMode(spec, req.CollaborationMode) {
			out = append(out, spec)
		}
	}
	for _, tool := range projectEinoAssistantDynamicTools(server, req, discovery) {
		if tool != nil {
			out = append(out, tool.Spec())
		}
	}
	return out
}

func projectEinoAssistantToolSearchBackend(server *Server, runReq projectAssistantRunRequest) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectEinoAssistantToolSearchTool,
			Description: "Search less-common workspace, runtime, build, web, provider, and repository tools by capability or exact name. Matching tools become available on the next model sample.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":160},"maxResults":{"type":"integer","minimum":1,"maximum":5}},"required":["query"],"additionalProperties":false}`),
			Risk:        projectAssistantToolRiskRead, ParallelSafe: true,
		},
		call: func(_ context.Context, req projectAssistantToolCallRequest) (string, error) {
			if req.RunState == nil || !req.RunState.CodexPOCEnabled() {
				return "", errors.New("tool search is not enabled for this run")
			}
			discovery, ok := req.RunState.ToolDiscovery()
			if !ok {
				return "", errors.New("tool catalog is unavailable")
			}
			query := strings.ToLower(strings.TrimSpace(projectToolString(req.Arguments["query"])))
			if query == "" {
				return "", errors.New("tool_search requires query")
			}
			limit := projectEinoAssistantPositiveJSONInt(req.Arguments["maxResults"], projectEinoAssistantMaxToolSearchResults)
			limit = min(limit, projectEinoAssistantMaxToolSearchResults)
			searchReq := runReq
			searchReq.Identity = req.Identity
			searchReq.Project = req.Project
			searchReq.TurnPolicy = req.RunState.TurnPolicy()
			searchReq.TurnProfile = searchReq.TurnPolicy.profile
			matches := projectEinoAssistantSearchDynamicToolSpecs(
				projectEinoAssistantDynamicToolSpecs(server, searchReq, discovery), query, limit,
			)
			result := projectEinoAssistantToolSearchResult{CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery), Matches: matches}
			raw, err := json.Marshal(result)
			return string(raw), err
		},
	}
}

// Keep the frequent source-editing loop immediately callable. Uncommon local
// helpers use the same validated, digest-bound selection as provider tools,
// avoiding their full schemas on every model call without removing capability.
func projectEinoAssistantLocalToolDeferred(name string) bool {
	switch projectToolBaseName(name) {
	case projectToolCheckProjectReadiness, projectToolPrepareProjectDeployment,
		projectToolGetRuntimeStatus, projectToolGetRuntimeLogs, projectToolRestartRuntime,
		projectToolSetRuntimeEnv, projectToolDownloadFile, projectToolSelectTemplate,
		projectToolHydrateWorkspace, projectToolWebSearch, projectToolWebFetch,
		"check_project_build", "get_build_logs", "get_project_checkpoints",
		"inspect_development_templates", "rebuild_project", "promote_project":
		return true
	default:
		return false
	}
}

func projectEinoAssistantSearchDynamicTools(tools []projectAssistantTool, query string, limit int) []projectEinoAssistantToolSearchMatch {
	specs := make([]projectAssistantToolSpec, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			specs = append(specs, tool.Spec())
		}
	}
	return projectEinoAssistantSearchDynamicToolSpecs(specs, query, limit)
}

func projectEinoAssistantSearchDynamicToolSpecs(specs []projectAssistantToolSpec, query string, limit int) []projectEinoAssistantToolSearchMatch {
	type ranked struct {
		match projectEinoAssistantToolSearchMatch
		score int
	}
	query = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.ToLower(query)), "select:"))
	if query == "" {
		return nil
	}
	rankedMatches := make([]ranked, 0)
	seen := map[string]struct{}{}
	for _, spec := range specs {
		name := projectAssistantToolKey(spec.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		description := strings.TrimSpace(spec.Description)
		aliases := projectEinoAssistantToolSearchAliases(spec)
		haystack := strings.ToLower(name + " " + description + " " + aliases)
		score := 0
		switch {
		case name == query || strings.Contains(query, name):
			score = 1000
		case strings.Contains(name, query):
			score = 500
		case strings.Contains(strings.ToLower(description), query), strings.Contains(aliases, query):
			score = 100
		case projectEinoAssistantAllSearchTermsMatch(query, haystack):
			score = 100
		default:
			score = projectEinoAssistantToolSearchTermScore(query, name, description, aliases)
		}
		if score == 0 {
			continue
		}
		summaryRunes := []rune(description)
		if len(summaryRunes) > projectEinoAssistantToolSearchSummaryMaxRunes {
			description = strings.TrimSpace(string(summaryRunes[:projectEinoAssistantToolSearchSummaryMaxRunes-3])) + "..."
		}
		rankedMatches = append(rankedMatches, ranked{score: score, match: projectEinoAssistantToolSearchMatch{
			Name: name, Summary: description, Risk: string(spec.Risk), Bundle: string(projectAssistantToolBundleForSpec(spec)),
		}})
	}
	sort.Slice(rankedMatches, func(i, j int) bool {
		if rankedMatches[i].score != rankedMatches[j].score {
			return rankedMatches[i].score > rankedMatches[j].score
		}
		return rankedMatches[i].match.Name < rankedMatches[j].match.Name
	})
	if limit <= 0 || limit > projectEinoAssistantMaxToolSearchResults {
		limit = projectEinoAssistantMaxToolSearchResults
	}
	if len(rankedMatches) > limit {
		rankedMatches = rankedMatches[:limit]
	}
	out := make([]projectEinoAssistantToolSearchMatch, len(rankedMatches))
	for i := range rankedMatches {
		out[i] = rankedMatches[i].match
	}
	return out
}

// A capability query can request several tools, such as snapshot, console and
// network observation. Rank those words independently instead of requiring
// each tool to contain the entire query. The caller has already filtered the
// catalog by collaboration mode and turn policy; ranking never grants access.
func projectEinoAssistantToolSearchTermScore(query, name, description, aliases string) int {
	nameWords := projectEinoAssistantToolSearchWords(name)
	descriptionWords := projectEinoAssistantToolSearchWords(description)
	aliasWords := projectEinoAssistantToolSearchWords(aliases)
	score := 0
	meaningfulTerms := 0
	categoryScore := 0
	for term := range projectEinoAssistantToolSearchWords(query) {
		_, inName := nameWords[term]
		_, inDescription := descriptionWords[term]
		_, inAlias := aliasWords[term]
		switch term {
		case "a", "an", "and", "at", "capability", "capabilities", "current", "find", "for", "from", "get", "in", "inspect", "is", "of", "on", "or", "project", "the", "this", "to", "tool", "tools", "use", "with":
			continue
		case "browser", "native", "playwright", "preview", "provider", "runtime", "workspace":
			if inName || inDescription || inAlias {
				categoryScore++
			}
			continue
		}
		meaningfulTerms++
		switch {
		case inName:
			score += 4
		case inAlias:
			score += 2
		case inDescription:
			score++
		}
	}
	if meaningfulTerms == 0 {
		return categoryScore
	}
	// Keep exact names and phrases ahead of individual-word matches even when
	// the query is unusually long. Generic category words alone cannot turn an
	// unknown specific capability into a match.
	return min(score, 99)
}

func projectEinoAssistantToolSearchWords(value string) map[string]struct{} {
	words := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make(map[string]struct{}, len(words))
	for _, word := range words {
		out[word] = struct{}{}
	}
	return out
}

func projectEinoAssistantAllSearchTermsMatch(query, haystack string) bool {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return false
	}
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func projectEinoAssistantToolSearchAliases(spec projectAssistantToolSpec) string {
	switch projectToolBaseName(spec.Name) {
	case projectToolDatabricksListTables, projectToolDatabricksDescribeTable:
		return "databricks table provider"
	case projectToolInfrastructureListTemplates, projectToolInfrastructureDescribeTemplate,
		projectToolInfrastructureListInstances, projectToolInfrastructureGetInstance,
		projectToolInfrastructureProvision:
		return "infrastructure template instance provider"
	case projectToolCommitProjectFiles, projectToolCommitFiles:
		return "git repository commit code provider"
	default:
		return ""
	}
}
