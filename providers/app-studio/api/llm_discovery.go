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

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/anthropicmodel"
	"github.com/railgrid/provider-sdk/modelcatalog"
)

const (
	projectLLMDiscoveryRequestMaxBytes  = 128 << 10
	projectLLMDiscoveryResponseMaxBytes = 2 << 20
	projectLLMDiscoveryTimeout          = 10 * time.Second
	projectLLMDiscoveryMaxModels        = 1000
)

type DiscoverProjectLLMModelsRequest struct {
	Provider        string `json:"provider,omitempty"`
	BaseURL         string `json:"baseURL,omitempty"`
	APIKey          string `json:"apiKey,omitempty"`
	ExistingModelID string `json:"existingModelID,omitempty"`
}

type ProjectLLMDiscoveredModelView struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Compatibility string   `json:"compatibility"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

type ProjectLLMModelDiscoveryResponse struct {
	Models []ProjectLLMDiscoveredModelView `json:"models"`
	Source string                          `json:"source"`
}

type projectLLMDiscoveryUpstreamError struct {
	StatusCode int
	Message    string
}

func (e *projectLLMDiscoveryUpstreamError) Error() string { return e.Message }

func newProjectLLMDiscoveryHTTPClient() *http.Client {
	return &http.Client{
		Timeout: projectLLMDiscoveryTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Never forward a workspace credential to a different location. Known
			// provider endpoints do not require redirects, and custom gateways can
			// expose their final API base URL explicitly.
			return http.ErrUseLastResponse
		},
	}
}

func (s *Server) discoverProjectLLMModels(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var request DiscoverProjectLLMModelsRequest
	if !decodeStrictJSONWithBodyLimit(w, r, &request, projectLLMDiscoveryRequestMaxBytes) {
		return
	}

	settings := projectLLMSettings{
		Provider: strings.TrimSpace(request.Provider),
		BaseURL:  strings.TrimSpace(request.BaseURL),
		APIKey:   strings.TrimSpace(request.APIKey),
	}
	if settings.Provider == "" {
		settings.Provider = defaultProjectLLMProvider
	}
	if settings.BaseURL == "" {
		if strings.EqualFold(settings.Provider, projectLLMProviderGoogle) {
			settings.BaseURL = defaultProjectLLMGoogleBaseURL
		} else {
			settings.BaseURL = defaultProjectLLMBaseURL
		}
	}

	baseURL, err := normalizeLLMBaseURL(settings.BaseURL)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	settings.BaseURL = baseURL
	if err := validateProjectLLMBaseURL(settings.Provider, settings.BaseURL); err != nil {
		writeProjectError(w, err)
		return
	}
	if settings.APIKey == "" && strings.TrimSpace(request.ExistingModelID) != "" {
		stored, err := readProjectLLMModelForCredentialReuse(r.Context(), c, request.ExistingModelID)
		if err != nil {
			if errors.Is(err, errProjectLLMModelNotFound) {
				writeStatus(w, http.StatusNotFound, "NotFound", "model configuration not found")
				return
			}
			writeProjectError(w, err)
			return
		}
		storedBaseURL, err := normalizeLLMBaseURL(stored.Settings.BaseURL)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		if !strings.EqualFold(strings.TrimSpace(settings.Provider), strings.TrimSpace(stored.Settings.Provider)) || settings.BaseURL != storedBaseURL {
			writeProjectError(w, newValidationError("enter a credential before finding models from a changed provider or endpoint"))
			return
		}
		settings.APIKey = strings.TrimSpace(stored.Settings.APIKey)
	}
	if settings.APIKey == "" {
		writeProjectError(w, newValidationError("enter a credential before finding models"))
		return
	}
	if err := validateProjectLLMAPIKey(settings.Provider, settings.APIKey); err != nil {
		writeProjectError(w, err)
		return
	}
	if strings.EqualFold(settings.Provider, projectLLMProviderGoogle) {
		if _, serviceAccount, err := googleServiceAccountCredentialFromJSON(settings.APIKey); err != nil {
			writeProjectError(w, err)
			return
		} else if serviceAccount {
			writeProjectError(w, newValidationError("automatic model discovery is not available for Vertex AI service accounts yet; enter the model ID manually"))
			return
		}
	}

	client := s.llmDiscoveryHTTPClient
	if client == nil {
		client = newProjectLLMDiscoveryHTTPClient()
	}
	response, err := discoverProjectLLMModels(r.Context(), client, settings)
	if err != nil {
		var upstream *projectLLMDiscoveryUpstreamError
		if errors.As(err, &upstream) {
			writeStatus(w, http.StatusBadGateway, "BadGateway", upstream.Message)
			return
		}
		writeStatus(w, http.StatusBadGateway, "BadGateway", "model discovery failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func discoverProjectLLMModels(ctx context.Context, client *http.Client, settings projectLLMSettings) (ProjectLLMModelDiscoveryResponse, error) {
	if strings.EqualFold(settings.Provider, projectLLMProviderGoogle) {
		models, err := discoverGoogleAIStudioModels(ctx, client, settings)
		return ProjectLLMModelDiscoveryResponse{Models: models, Source: projectLLMProviderGoogle}, err
	}
	models, err := discoverOpenAICompatibleModels(ctx, client, settings)
	return ProjectLLMModelDiscoveryResponse{Models: models, Source: defaultProjectLLMProvider}, err
}

// Anthropic's official endpoint uses its native, paginated Models API even
// when the saved connection provider is openai-compatible.
func discoverProjectAnthropicModels(ctx context.Context, client *http.Client, settings projectLLMSettings) ([]ProjectLLMDiscoveredModelView, error) {
	models := make([]ProjectLLMDiscoveredModelView, 0)
	seen := make(map[string]bool)
	cursors := make(map[string]bool)
	cursor := ""
	for page := 0; page < 10; page++ {
		query := url.Values{"limit": {"1000"}}
		if cursor != "" {
			query.Set("after_id", cursor)
		}
		endpoint, err := projectLLMDiscoveryEndpoint(settings.BaseURL, "models", query)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(settings.APIKey))
		request.Header.Set("anthropic-version", "2023-06-01")
		var payload struct {
			Data *[]struct {
				ID   string `json:"id"`
				Name string `json:"display_name"`
			} `json:"data"`
			HasMore *bool  `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := projectLLMDiscoveryJSON(client, request, &payload); err != nil {
			return nil, err
		}
		if payload.Data == nil || payload.HasMore == nil {
			return nil, fmt.Errorf("anthropic model list is missing data or has_more")
		}
		for _, item := range *payload.Data {
			id := strings.TrimSpace(item.ID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = id
			}
			models = append(models, projectLLMDiscoveredModel(id, name, defaultProjectLLMProvider, nil))
			if len(models) > projectLLMDiscoveryMaxModels {
				return nil, fmt.Errorf("anthropic model list exceeds %d models", projectLLMDiscoveryMaxModels)
			}
		}
		if !*payload.HasMore {
			return sortProjectLLMDiscoveredModels(models), nil
		}
		cursor = strings.TrimSpace(payload.LastID)
		if len(*payload.Data) == 0 || cursor == "" || cursors[cursor] {
			return nil, fmt.Errorf("anthropic model list returned an invalid pagination cursor")
		}
		cursors[cursor] = true
	}
	return nil, fmt.Errorf("anthropic model list exceeded pagination limit")
}

func discoverOpenAICompatibleModels(ctx context.Context, client *http.Client, settings projectLLMSettings) ([]ProjectLLMDiscoveredModelView, error) {
	if anthropicmodel.UsesEndpoint(settings.BaseURL) {
		return discoverProjectAnthropicModels(ctx, client, settings)
	}
	endpoint, err := projectLLMDiscoveryEndpoint(settings.BaseURL, "models", nil)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(settings.APIKey))

	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := projectLLMDiscoveryJSON(client, request, &payload); err != nil {
		return nil, err
	}
	models := make([]ProjectLLMDiscoveredModelView, 0, min(len(payload.Data), projectLLMDiscoveryMaxModels))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		models = append(models, projectLLMDiscoveredModel(id, id, defaultProjectLLMProvider, nil))
		if len(models) >= projectLLMDiscoveryMaxModels {
			break
		}
	}
	return sortProjectLLMDiscoveredModels(models), nil
}

func discoverGoogleAIStudioModels(ctx context.Context, client *http.Client, settings projectLLMSettings) ([]ProjectLLMDiscoveredModelView, error) {
	endpoint, err := projectLLMDiscoveryEndpoint(settings.BaseURL, "v1beta/models", url.Values{"pageSize": []string{"1000"}})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-goog-api-key", strings.TrimSpace(settings.APIKey))

	var payload struct {
		Models []struct {
			Name                       string   `json:"name"`
			BaseModelID                string   `json:"baseModelId"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			SupportedActions           []string `json:"supportedActions"`
		} `json:"models"`
	}
	if err := projectLLMDiscoveryJSON(client, request, &payload); err != nil {
		return nil, err
	}
	models := make([]ProjectLLMDiscoveredModelView, 0, min(len(payload.Models), projectLLMDiscoveryMaxModels))
	for _, item := range payload.Models {
		id := strings.TrimPrefix(strings.TrimSpace(item.Name), "models/")
		if strings.TrimSpace(item.BaseModelID) != "" {
			id = strings.TrimSpace(item.BaseModelID)
		}
		if id == "" {
			continue
		}
		capabilities := append([]string{}, item.SupportedGenerationMethods...)
		capabilities = append(capabilities, item.SupportedActions...)
		models = append(models, projectLLMDiscoveredModel(id, item.DisplayName, projectLLMProviderGoogle, capabilities))
		if len(models) >= projectLLMDiscoveryMaxModels {
			break
		}
	}
	return sortProjectLLMDiscoveredModels(models), nil
}

func projectLLMDiscoveryJSON(client *http.Client, request *http.Request, target any) error {
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request provider models: %w", err)
	}
	// Close errors on a read-only response body are not actionable.
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return &projectLLMDiscoveryUpstreamError{
			StatusCode: response.StatusCode,
			Message:    fmt.Sprintf("provider model discovery returned %s; check the credential and endpoint", response.Status),
		}
	}
	reader := io.LimitReader(response.Body, projectLLMDiscoveryResponseMaxBytes+1)
	raw, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read provider model response: %w", err)
	}
	if len(raw) > projectLLMDiscoveryResponseMaxBytes {
		return errors.New("provider model response exceeded the 2 MiB limit")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return errors.New("provider model discovery returned invalid JSON")
	}
	return nil
}

func projectLLMDiscoveryEndpoint(baseURL, suffix string, query url.Values) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", newValidationError("baseURL must be an absolute HTTP(S) URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
	u.RawPath = ""
	u.RawQuery = query.Encode()
	u.Fragment = ""
	return u.String(), nil
}

func projectLLMDiscoveredModel(id, name, provider string, capabilities []string) ProjectLLMDiscoveredModelView {
	name = strings.TrimSpace(name)
	if name == "" {
		name = id
	}
	compatibility := "available"
	if projectLLMModelKnownUnsuitable(id, capabilities) {
		compatibility = "unsuitable"
	} else if modelcatalog.IsRecommended(id) || (provider == projectLLMProviderGoogle && projectAssistantCapabilitiesForModel(projectLLMSettings{Provider: provider, Model: id}).VisionToolResults) {
		compatibility = "recommended"
	}
	return ProjectLLMDiscoveredModelView{
		ID:            id,
		Name:          name,
		Compatibility: compatibility,
		Capabilities:  projectLLMDiscoveryCapabilities(capabilities),
	}
}

func projectLLMModelKnownUnsuitable(id string, capabilities []string) bool {
	for _, capability := range capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), "generateContent") {
			return false
		}
	}
	if len(capabilities) > 0 {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(id))
	for _, prefix := range []string{"text-embedding", "embedding", "whisper", "tts", "dall-e", "gpt-image", "omni-moderation", "sora"} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func projectLLMDiscoveryCapabilities(values []string) []string {
	seen := map[string]struct{}{}
	capabilities := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		capabilities = append(capabilities, value)
	}
	sort.Strings(capabilities)
	return capabilities
}

func sortProjectLLMDiscoveredModels(models []ProjectLLMDiscoveredModelView) []ProjectLLMDiscoveredModelView {
	rank := map[string]int{"recommended": 0, "available": 1, "unsuitable": 2}
	sort.SliceStable(models, func(i, j int) bool {
		if rank[models[i].Compatibility] != rank[models[j].Compatibility] {
			return rank[models[i].Compatibility] < rank[models[j].Compatibility]
		}
		return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name)
	})
	return models
}
