// Copyright 2026 The Faros Authors.
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	asclient "github.com/faroshq/provider-app-studio/client"
)

const (
	projectLLMLegacyDefaultModelID = "default"
	projectLLMMaxModels            = 20
	projectLLMMaxStoredRevisions   = 200
)

var projectLLMModelIDInvalid = regexp.MustCompile(`[^a-z0-9]+`)

type ProjectLLMModelView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	BaseURL    string `json:"baseURL"`
	Model      string `json:"model"`
	Configured bool   `json:"configured"`
	Default    bool   `json:"default,omitempty"`
}

type CreateProjectLLMModelRequest struct {
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"baseURL,omitempty"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
}

type PatchProjectLLMModelRequest struct {
	Name     *string `json:"name,omitempty"`
	Provider *string `json:"provider,omitempty"`
	BaseURL  *string `json:"baseURL,omitempty"`
	Model    *string `json:"model,omitempty"`
	APIKey   *string `json:"apiKey,omitempty"`
}

type SetDefaultProjectLLMModelRequest struct {
	ModelID string `json:"modelID"`
}

type projectLLMStoredModel struct {
	ID         string `json:"id"`
	RevisionID string `json:"revisionID,omitempty"`
	Archived   bool   `json:"archived,omitempty"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	BaseURL    string `json:"baseURL"`
	Model      string `json:"model"`
	APIKey     string `json:"apiKey,omitempty"`
}

type projectLLMModelSettings struct {
	ID         string
	RevisionID string
	Archived   bool
	Name       string
	Settings   projectLLMSettings
}

type projectLLMRegistry struct {
	DefaultModelID string
	Models         []projectLLMModelSettings
	Runtime        projectLLMSettings
}

func defaultProjectLLMRegistry() projectLLMRegistry {
	return projectLLMRegistry{Runtime: defaultProjectLLMSettings()}
}

func (r projectLLMRegistry) model(id string) (projectLLMModelSettings, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = strings.TrimSpace(r.DefaultModelID)
	}
	for _, model := range r.Models {
		if !model.Archived && model.ID == id {
			return model, true
		}
	}
	return projectLLMModelSettings{}, false
}

func (r projectLLMRegistry) modelRevision(id, revisionID string) (projectLLMModelSettings, bool) {
	id = strings.TrimSpace(id)
	revisionID = strings.TrimSpace(revisionID)
	if id == "" && revisionID == "" {
		id = strings.TrimSpace(r.DefaultModelID)
	}
	if revisionID != "" {
		for _, model := range r.Models {
			if model.RevisionID == revisionID && (id == "" || model.ID == id) {
				return model, true
			}
		}
		return projectLLMModelSettings{}, false
	}
	// Runs created before revision pinning carry only the logical ID. Resolve
	// those to the oldest retained revision so a later patch cannot silently
	// change the model used by a resumable legacy run.
	for _, model := range r.Models {
		if model.ID == id {
			return model, true
		}
	}
	return projectLLMModelSettings{}, false
}

func (r projectLLMRegistry) selectedSettings(id, revisionID string) (projectLLMSettings, error) {
	model, ok := r.modelRevision(id, revisionID)
	if !ok {
		if strings.TrimSpace(id) == "" && strings.TrimSpace(revisionID) == "" && len(r.Models) == 0 {
			return r.Runtime, nil
		}
		return projectLLMSettings{}, newValidationError("selected model configuration was not found")
	}
	settings := model.Settings
	settings.MaxRetries = r.Runtime.MaxRetries
	settings.MaxRetriesConfigured = r.Runtime.MaxRetriesConfigured
	settings.RetryBackoff = r.Runtime.RetryBackoff
	settings.StreamIdleTimeout = r.Runtime.StreamIdleTimeout
	return settings, nil
}

func (r projectLLMRegistry) selectedModelID(requested string) (string, error) {
	model, ok := r.model(requested)
	if !ok {
		return "", newValidationError("selected model configuration was not found")
	}
	if strings.TrimSpace(model.Settings.APIKey) == "" {
		return "", newValidationError("selected model configuration does not have a credential")
	}
	return model.ID, nil
}

func (r projectLLMRegistry) selectedModel(requested string) (projectLLMModelSettings, error) {
	model, ok := r.model(requested)
	if !ok {
		return projectLLMModelSettings{}, newValidationError("selected model configuration was not found")
	}
	if strings.TrimSpace(model.Settings.APIKey) == "" {
		return projectLLMModelSettings{}, newValidationError("selected model configuration does not have a credential")
	}
	return model, nil
}

func (r projectLLMRegistry) view() ProjectLLMSettingsView {
	views := make([]ProjectLLMModelView, 0, len(r.Models))
	for _, model := range r.Models {
		if model.Archived {
			continue
		}
		views = append(views, ProjectLLMModelView{
			ID:         model.ID,
			Name:       model.Name,
			Provider:   model.Settings.Provider,
			BaseURL:    model.Settings.BaseURL,
			Model:      model.Settings.Model,
			Configured: strings.TrimSpace(model.Settings.APIKey) != "",
			Default:    model.ID == r.DefaultModelID,
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Default != views[j].Default {
			return views[i].Default
		}
		return strings.ToLower(views[i].Name) < strings.ToLower(views[j].Name)
	})
	view := ProjectLLMSettingsView{DefaultModelID: r.DefaultModelID, Models: views}
	if selected, ok := r.model(""); ok {
		view.Provider = selected.Settings.Provider
		view.BaseURL = selected.Settings.BaseURL
		view.Model = selected.Settings.Model
		view.Configured = strings.TrimSpace(selected.Settings.APIKey) != ""
	} else {
		view.Provider = r.Runtime.Provider
		view.BaseURL = r.Runtime.BaseURL
		view.Model = r.Runtime.Model
	}
	return view
}

func normalizeProjectLLMModelName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", newValidationError("model configuration name is required")
	}
	if len(value) > 80 {
		return "", newValidationError("model configuration name must be 80 characters or fewer")
	}
	return value, nil
}

func projectLLMModelID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(projectLLMModelIDInvalid.ReplaceAllString(value, "-"), "-")
	if len(value) > 63 {
		value = strings.Trim(value[:63], "-")
	}
	if value == "" {
		return projectLLMLegacyDefaultModelID
	}
	return value
}

func projectLLMLegacyRevision(item projectLLMStoredModel) string {
	item.RevisionID = ""
	raw, _ := json.Marshal(item)
	sum := sha256.Sum256(raw)
	return "legacy-" + hex.EncodeToString(sum[:16])
}

func normalizeProjectLLMModel(model *projectLLMModelSettings, runtime projectLLMSettings) error {
	name, err := normalizeProjectLLMModelName(model.Name)
	if err != nil {
		return err
	}
	model.Name = name
	model.ID = projectLLMModelID(model.ID)
	model.RevisionID = strings.TrimSpace(model.RevisionID)
	if model.RevisionID == "" {
		model.RevisionID = uuid.NewString()
	}
	model.Settings.MaxRetries = runtime.MaxRetries
	model.Settings.MaxRetriesConfigured = runtime.MaxRetriesConfigured
	model.Settings.RetryBackoff = runtime.RetryBackoff
	model.Settings.StreamIdleTimeout = runtime.StreamIdleTimeout
	return normalizeProjectLLMSettings(&model.Settings)
}

func validateProjectLLMModelCredential(model projectLLMModelSettings) error {
	if strings.TrimSpace(model.Settings.APIKey) == "" {
		return newValidationError("a credential is required to connect this model")
	}
	return nil
}

func firstConfiguredProjectLLMModelID(models []projectLLMModelSettings) string {
	var selected *projectLLMModelSettings
	for _, model := range models {
		if model.Archived || strings.TrimSpace(model.Settings.APIKey) == "" {
			continue
		}
		if selected == nil || strings.ToLower(model.Name) < strings.ToLower(selected.Name) {
			candidate := model
			selected = &candidate
		}
	}
	if selected == nil {
		return ""
	}
	return selected.ID
}

func configuredProjectLLMDefaultModelID(registry projectLLMRegistry) string {
	if model, found := registry.model(registry.DefaultModelID); found && strings.TrimSpace(model.Settings.APIKey) != "" {
		return model.ID
	}
	return firstConfiguredProjectLLMModelID(registry.Models)
}

func readProjectLLMRegistry(ctx context.Context, c *asclient.Client) (projectLLMRegistry, error) {
	registry := defaultProjectLLMRegistry()
	secret, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, projectLLMSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return registry, nil
	}
	if err != nil {
		return registry, err
	}
	applyProjectLLMRuntimeSecretValues(&registry.Runtime, secret)
	if raw := secretDataValue(secret, "models"); raw != "" {
		var stored []projectLLMStoredModel
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return registry, fmt.Errorf("decode model registry: %w", err)
		}
		seenRevisions := map[string]struct{}{}
		activeIDs := map[string]struct{}{}
		for _, item := range stored {
			if strings.TrimSpace(item.RevisionID) == "" {
				item.RevisionID = projectLLMLegacyRevision(item)
			}
			model := projectLLMModelSettings{ID: item.ID, RevisionID: item.RevisionID, Archived: item.Archived, Name: item.Name, Settings: projectLLMSettings{
				Provider: item.Provider, BaseURL: item.BaseURL, Model: item.Model, APIKey: item.APIKey,
			}}
			if err := normalizeProjectLLMModel(&model, registry.Runtime); err != nil {
				return registry, err
			}
			if _, exists := seenRevisions[model.RevisionID]; exists {
				return registry, fmt.Errorf("model registry contains duplicate revision %q", model.RevisionID)
			}
			seenRevisions[model.RevisionID] = struct{}{}
			if !model.Archived {
				if _, exists := activeIDs[model.ID]; exists {
					return registry, fmt.Errorf("model registry contains duplicate active id %q", model.ID)
				}
				activeIDs[model.ID] = struct{}{}
			}
			registry.Models = append(registry.Models, model)
		}
		registry.DefaultModelID = strings.TrimSpace(secretDataValue(secret, "defaultModelID"))
		if _, ok := registry.model(registry.DefaultModelID); !ok {
			registry.DefaultModelID = ""
			for _, model := range registry.Models {
				if !model.Archived {
					registry.DefaultModelID = model.ID
					break
				}
			}
		}
		return registry, nil
	}

	legacy := registry.Runtime
	if v := secretDataValue(secret, "provider"); v != "" {
		legacy.Provider = v
	}
	if v := secretDataValue(secret, "baseURL"); v != "" {
		legacy.BaseURL = v
	}
	if v := secretDataValue(secret, "model"); v != "" {
		legacy.Model = v
	}
	legacy.APIKey = secretDataValue(secret, "apiKey")
	if err := normalizeProjectLLMSettings(&legacy); err != nil {
		return registry, err
	}
	registry.DefaultModelID = projectLLMLegacyDefaultModelID
	legacyStored := projectLLMStoredModel{ID: projectLLMLegacyDefaultModelID, Name: legacy.Model, Provider: legacy.Provider, BaseURL: legacy.BaseURL, Model: legacy.Model, APIKey: legacy.APIKey}
	registry.Models = []projectLLMModelSettings{{ID: projectLLMLegacyDefaultModelID, RevisionID: projectLLMLegacyRevision(legacyStored), Name: legacy.Model, Settings: legacy}}
	return registry, nil
}

func applyProjectLLMRuntimeSecretValues(settings *projectLLMSettings, secret *unstructured.Unstructured) {
	if v := secretDataValue(secret, "maxRetries"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 && parsed <= 10 {
			settings.MaxRetries = parsed
			settings.MaxRetriesConfigured = true
		}
	}
	if v := secretDataValue(secret, "retryBackoffMS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			settings.RetryBackoff = time.Duration(parsed) * time.Millisecond
		}
	}
	if v := secretDataValue(secret, "streamIdleTimeoutMS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			settings.StreamIdleTimeout = time.Duration(parsed) * time.Millisecond
		}
	}
}

func writeProjectLLMRegistry(ctx context.Context, c *asclient.Client, registry projectLLMRegistry) error {
	secret, err := projectLLMRegistrySecret(registry)
	if err != nil {
		return err
	}
	existing, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, projectLLMSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.Resource(secretResource, projectLLMSecretNamespace).Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	secret.SetResourceVersion(existing.GetResourceVersion())
	_, err = c.Resource(secretResource, projectLLMSecretNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

func projectLLMRegistrySecret(registry projectLLMRegistry) (*unstructured.Unstructured, error) {
	if len(registry.Models) > projectLLMMaxStoredRevisions {
		return nil, newValidationError("model configuration revision history is full")
	}
	stored := make([]projectLLMStoredModel, 0, len(registry.Models))
	seenRevisions := map[string]struct{}{}
	activeIDs := map[string]struct{}{}
	activeCount := 0
	for index := range registry.Models {
		model := registry.Models[index]
		if err := normalizeProjectLLMModel(&model, registry.Runtime); err != nil {
			return nil, err
		}
		if _, exists := seenRevisions[model.RevisionID]; exists {
			return nil, newValidationError("model configuration revisions must be unique")
		}
		seenRevisions[model.RevisionID] = struct{}{}
		if !model.Archived {
			activeCount++
			if _, exists := activeIDs[model.ID]; exists {
				return nil, newValidationError("active model configuration IDs must be unique")
			}
			activeIDs[model.ID] = struct{}{}
		}
		registry.Models[index] = model
		stored = append(stored, projectLLMStoredModel{ID: model.ID, RevisionID: model.RevisionID, Archived: model.Archived, Name: model.Name, Provider: model.Settings.Provider, BaseURL: model.Settings.BaseURL, Model: model.Settings.Model, APIKey: model.Settings.APIKey})
	}
	if activeCount > projectLLMMaxModels {
		return nil, newValidationError("at most 20 model configurations are supported")
	}
	if _, ok := registry.model(registry.DefaultModelID); !ok {
		registry.DefaultModelID = ""
		for _, model := range registry.Models {
			if !model.Archived {
				registry.DefaultModelID = model.ID
				break
			}
		}
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	data := map[string]interface{}{
		"models":              encodeSecretValue(string(raw)),
		"defaultModelID":      encodeSecretValue(registry.DefaultModelID),
		"maxRetries":          encodeSecretValue(strconv.Itoa(registry.Runtime.MaxRetries)),
		"retryBackoffMS":      encodeSecretValue(strconv.FormatInt(registry.Runtime.RetryBackoff.Milliseconds(), 10)),
		"streamIdleTimeoutMS": encodeSecretValue(strconv.FormatInt(registry.Runtime.StreamIdleTimeout.Milliseconds(), 10)),
	}
	if selected, ok := registry.model(""); ok {
		data["provider"] = encodeSecretValue(selected.Settings.Provider)
		data["baseURL"] = encodeSecretValue(selected.Settings.BaseURL)
		data["model"] = encodeSecretValue(selected.Settings.Model)
		if strings.TrimSpace(selected.Settings.APIKey) != "" {
			data["apiKey"] = encodeSecretValue(selected.Settings.APIKey)
		}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      projectLLMSecretName,
			"namespace": projectLLMSecretNamespace,
		},
		"type": "Opaque",
		"data": data,
	}}, nil
}

func (s *Server) createProjectLLMModel(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var request CreateProjectLLMModelRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	registry, err := readProjectLLMRegistry(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if len(registry.view().Models) >= projectLLMMaxModels {
		writeProjectError(w, newValidationError("at most 20 model configurations are supported"))
		return
	}
	name, err := normalizeProjectLLMModelName(request.Name)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	id := projectLLMModelID(name)
	if _, exists := registry.model(id); exists {
		writeProjectError(w, newValidationError("a model configuration with this name already exists"))
		return
	}
	model := projectLLMModelSettings{ID: id, RevisionID: uuid.NewString(), Name: name, Settings: projectLLMSettings{
		Provider: request.Provider, BaseURL: request.BaseURL, Model: request.Model, APIKey: strings.TrimSpace(request.APIKey),
	}}
	if err := normalizeProjectLLMModel(&model, registry.Runtime); err != nil {
		writeProjectError(w, err)
		return
	}
	if err := validateProjectLLMModelCredential(model); err != nil {
		writeProjectError(w, err)
		return
	}
	registry.Models = append(registry.Models, model)
	registry.DefaultModelID = configuredProjectLLMDefaultModelID(registry)
	if err := writeProjectLLMRegistry(r.Context(), c, registry); err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, registry.view())
}

func (s *Server) patchProjectLLMModel(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var request PatchProjectLLMModelRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	registry, err := readProjectLLMRegistry(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	id := strings.TrimSpace(muxVar(r, "model"))
	index := -1
	for i := range registry.Models {
		if !registry.Models[i].Archived && registry.Models[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		writeStatus(w, http.StatusNotFound, "NotFound", "model configuration not found")
		return
	}
	model := registry.Models[index]
	if request.Name != nil {
		model.Name, err = normalizeProjectLLMModelName(*request.Name)
		if err != nil {
			writeProjectError(w, err)
			return
		}
	}
	if request.Provider != nil {
		model.Settings.Provider = strings.TrimSpace(*request.Provider)
	}
	if request.BaseURL != nil {
		model.Settings.BaseURL = strings.TrimSpace(*request.BaseURL)
	}
	if request.Model != nil {
		model.Settings.Model = strings.TrimSpace(*request.Model)
	}
	if request.APIKey != nil {
		model.Settings.APIKey = strings.TrimSpace(*request.APIKey)
	}
	if err := normalizeProjectLLMModel(&model, registry.Runtime); err != nil {
		writeProjectError(w, err)
		return
	}
	if err := validateProjectLLMModelCredential(model); err != nil {
		writeProjectError(w, err)
		return
	}
	registry.Models[index].Archived = true
	model.RevisionID = uuid.NewString()
	model.Archived = false
	registry.Models = append(registry.Models, model)
	registry.DefaultModelID = configuredProjectLLMDefaultModelID(registry)
	if err := writeProjectLLMRegistry(r.Context(), c, registry); err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, registry.view())
}

func (s *Server) deleteProjectLLMModel(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	registry, err := readProjectLLMRegistry(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	id := strings.TrimSpace(muxVar(r, "model"))
	found := false
	for index := range registry.Models {
		if !registry.Models[index].Archived && registry.Models[index].ID == id {
			found = true
			registry.Models[index].Archived = true
			break
		}
	}
	if !found {
		writeStatus(w, http.StatusNotFound, "NotFound", "model configuration not found")
		return
	}
	registry.DefaultModelID = configuredProjectLLMDefaultModelID(registry)
	if err := writeProjectLLMRegistry(r.Context(), c, registry); err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, registry.view())
}

func (s *Server) setDefaultProjectLLMModel(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var request SetDefaultProjectLLMModelRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	registry, err := readProjectLLMRegistry(r.Context(), c)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	model, found := registry.model(request.ModelID)
	if !found {
		writeStatus(w, http.StatusNotFound, "NotFound", "model configuration not found")
		return
	}
	if strings.TrimSpace(model.Settings.APIKey) == "" {
		writeProjectError(w, newValidationError("the default model must have a credential"))
		return
	}
	registry.DefaultModelID = model.ID
	if err := writeProjectLLMRegistry(r.Context(), c, registry); err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, registry.view())
}

// muxVar is kept tiny so the registry module does not leak URL routing into
// its persistence helpers.
func muxVar(r *http.Request, key string) string {
	return mux.Vars(r)[key]
}
