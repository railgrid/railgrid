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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
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
	return projectEinoAssistantNormalizeOptimizationMode(os.Getenv(projectEinoAssistantOptimizationEnv))
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

func projectEinoAssistantDynamicToolCatalogDigest(discovery projectEinoAssistantToolDiscovery) string {
	type contract struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Parameters  string `json:"parameters,omitempty"`
		Risk        string `json:"risk,omitempty"`
	}
	items := make([]contract, 0, len(discovery.MCPTools)+1)
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
	out := make([]projectAssistantTool, 0, len(discovery.MCPTools)+1)
	if server != nil && discovery.IncludeCommitBridge {
		for _, tool := range projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(server.projectAssistantToolRegistry().Tools(true), policy), req.CollaborationMode) {
			if tool != nil && tool.Spec().Risk == projectAssistantToolRiskCommit {
				out = append(out, tool)
			}
		}
	}
	out = append(out, projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(discovery.MCPTools, policy), req.CollaborationMode)...)
	return out
}

func projectEinoAssistantToolSearchBackend(server *Server, runReq projectAssistantRunRequest) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectEinoAssistantToolSearchTool,
			Description: "Search less-common provider and repository tools by capability or exact name. Matching tools become available on the next model sample.",
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
			matches := projectEinoAssistantSearchDynamicTools(
				projectEinoAssistantDynamicTools(server, searchReq, discovery), query, limit,
			)
			result := projectEinoAssistantToolSearchResult{CatalogDigest: projectEinoAssistantDynamicToolCatalogDigest(discovery), Matches: matches}
			raw, err := json.Marshal(result)
			return string(raw), err
		},
	}
}

func projectEinoAssistantSearchDynamicTools(tools []projectAssistantTool, query string, limit int) []projectEinoAssistantToolSearchMatch {
	type ranked struct {
		match projectEinoAssistantToolSearchMatch
		score int
	}
	query = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(query), "select:"))
	rankedMatches := make([]ranked, 0)
	seen := map[string]struct{}{}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		spec := tool.Spec()
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
		case name == query:
			score = 3
		case strings.Contains(name, query):
			score = 2
		case strings.Contains(strings.ToLower(description), query), strings.Contains(aliases, query):
			score = 1
		case projectEinoAssistantAllSearchTermsMatch(query, haystack):
			score = 1
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
