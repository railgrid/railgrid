// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package modelcatalog

import (
	"fmt"
	"net/url"
	"strings"
)

const officialOpenAIBaseURL = "https://api.openai.com/v1"

// ReasoningProfile describes the reasoning controls Railgrid knows are
// supported by an exact OpenAI model alias or snapshot. Pricing and broad
// model-family lookups are deliberately not used to infer reasoning support.
type ReasoningProfile struct {
	ID                    string   `json:"id"`
	Aliases               []string `json:"aliases,omitempty"`
	Efforts               []string `json:"efforts"`
	ResponsesForReasoning bool     `json:"responsesForReasoning,omitempty"`
}

// Official model documentation is the source for the supported effort values
// and snapshots. Custom OpenAI-compatible endpoints, proxies, and Azure-style
// deployments are excluded because their model IDs and feature support vary.
//
// Sources:
//   - https://developers.openai.com/api/docs/guides/deployment-checklist
//   - https://developers.openai.com/api/docs/guides/reasoning
//   - https://developers.openai.com/api/docs/models/gpt-5.6-luna
//   - https://developers.openai.com/api/docs/models/gpt-6-astra
//   - https://developers.openai.com/api/docs/models/gpt-6.1-sol
//   - https://developers.openai.com/api/docs/models/gpt-6-luna
//   - https://developers.openai.com/api/docs/models/gpt-5
//   - https://developers.openai.com/api/docs/models/gpt-5-mini
//   - https://developers.openai.com/api/docs/models/gpt-5.1
//   - https://developers.openai.com/api/docs/models/o1
//   - https://developers.openai.com/api/docs/models/o3
//   - https://developers.openai.com/api/docs/models/o3-mini
//   - https://developers.openai.com/api/docs/models/o4-mini
var reasoningProfiles = []ReasoningProfile{
	{ID: "gpt-6-astra", Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-6.1-sol", Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-6-sol", Efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-6-luna", Efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-5.6-luna", Efforts: []string{"none", "low", "medium", "high", "xhigh", "max"}, ResponsesForReasoning: true},
	{ID: "gpt-5", Aliases: []string{"gpt-5-2025-08-07"}, Efforts: []string{"minimal", "low", "medium", "high"}},
	{ID: "gpt-5-mini", Aliases: []string{"gpt-5-mini-2025-08-07"}, Efforts: []string{"minimal", "low", "medium", "high"}},
	{ID: "gpt-5.1", Aliases: []string{"gpt-5.1-2025-11-13"}, Efforts: []string{"none", "low", "medium", "high"}},
	{ID: "o1", Aliases: []string{"o1-2024-12-17"}, Efforts: []string{"low", "medium", "high"}},
	{ID: "o3", Aliases: []string{"o3-2025-04-16"}, Efforts: []string{"low", "medium", "high"}},
	{ID: "o3-mini", Aliases: []string{"o3-mini-2025-01-31"}, Efforts: []string{"low", "medium", "high"}},
	{ID: "o4-mini", Aliases: []string{"o4-mini-2025-04-16"}, Efforts: []string{"low", "medium", "high"}},
}

var reasoningProfileByModel = func() map[string]ReasoningProfile {
	profiles := make(map[string]ReasoningProfile, len(reasoningProfiles)*2)
	for _, profile := range reasoningProfiles {
		profiles[profile.ID] = profile
		for _, alias := range profile.Aliases {
			profiles[alias] = profile
		}
	}
	return profiles
}()

// ReasoningProfiles returns the supported profile catalog. The slices are
// copied so callers cannot mutate the shared source table.
func ReasoningProfiles() []ReasoningProfile {
	profiles := make([]ReasoningProfile, len(reasoningProfiles))
	for i, profile := range reasoningProfiles {
		profiles[i] = copyReasoningProfile(profile)
	}
	return profiles
}

// ReasoningProfileForConnection returns the profile for a known model only
// when the configured connection targets the official OpenAI API endpoint.
// Model names are case-insensitive, but the ID must exactly match a documented
// alias; gateway namespaces and inferred date suffixes are not accepted.
func ReasoningProfileForConnection(provider, baseURL, model string) (ReasoningProfile, bool) {
	if !IsOfficialOpenAIEndpoint(provider, baseURL) {
		return ReasoningProfile{}, false
	}
	id := strings.ToLower(strings.TrimSpace(model))
	profile, ok := reasoningProfileByModel[id]
	if !ok {
		return ReasoningProfile{}, false
	}
	return copyReasoningProfile(profile), true
}

// ValidateReasoningEffort validates a user-selected effort for the exact
// provider, endpoint, and model tuple. An empty value leaves provider defaults
// in effect; nonempty values must match a supported identifier exactly.
func ValidateReasoningEffort(provider, baseURL, model, effort string) error {
	if effort == "" {
		return nil
	}
	profile, ok := ReasoningProfileForConnection(provider, baseURL, model)
	if !ok {
		return fmt.Errorf("reasoning effort %q is not supported for this provider, endpoint, or model", effort)
	}
	for _, supported := range profile.Efforts {
		if effort == supported {
			return nil
		}
	}
	return fmt.Errorf("reasoning effort %q is not supported for model %q", effort, profile.ID)
}

// IsOfficialOpenAIEndpoint reports whether provider and baseURL identify the
// official OpenAI API endpoint. Keep this predicate shared by routing and
// capability checks so a gateway cannot inherit official model behavior just
// by serving an OpenAI model ID.
func IsOfficialOpenAIEndpoint(provider, baseURL string) bool {
	provider = strings.TrimSpace(provider)
	if provider != "" && !strings.EqualFold(provider, "openai-compatible") {
		return false
	}
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		raw = officialOpenAIBaseURL
	}
	if strings.ContainsAny(raw, "?#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if !strings.EqualFold(u.Host, "api.openai.com") && !strings.EqualFold(u.Host, "api.openai.com:443") {
		return false
	}
	return u.EscapedPath() == "/v1" || u.EscapedPath() == "/v1/"
}

func copyReasoningProfile(profile ReasoningProfile) ReasoningProfile {
	profile.Aliases = append([]string(nil), profile.Aliases...)
	profile.Efforts = append([]string(nil), profile.Efforts...)
	return profile
}
