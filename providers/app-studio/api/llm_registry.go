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
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/railgrid/provider-sdk/modelcatalog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
)

const (
	projectLLMLegacyDefaultModelID = "default"
	projectLLMMaxModels            = 20
	projectLLMMaxStoredRevisions   = 200
)

var projectLLMModelIDInvalid = regexp.MustCompile(`[^a-z0-9]+`)

type ProjectLLMModelView struct {
	Catalog    *modelcatalog.ModelInfo `json:"catalog,omitempty"`
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	Provider   string                  `json:"provider"`
	BaseURL    string                  `json:"baseURL"`
	Model      string                  `json:"model"`
	Configured bool                    `json:"configured"`
	Default    bool                    `json:"default,omitempty"`
}

type CreateProjectLLMModelRequest struct {
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"baseURL,omitempty"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
}

type TestProjectLLMConnectionRequest struct {
	ExistingModelID string `json:"existingModelID,omitempty"`
	Provider        string `json:"provider,omitempty"`
	BaseURL         string `json:"baseURL,omitempty"`
	Model           string `json:"model"`
	APIKey          string `json:"apiKey"`
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
		var catalog *modelcatalog.ModelInfo
		if entry, ok := modelcatalog.LookupModel(model.Settings.Model); ok {
			catalog = &entry
		}
		views = append(views, ProjectLLMModelView{
			Catalog:    catalog,
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

// readProjectLLMRegistry assembles the workspace's model registry from the
// Studio's spec.llm plus, for each model that names one, its own credential
// Secret.
//
// The registry used to be a single Opaque Secret holding every model AND every
// key, which meant any read of the model list was a read of all credentials.
// Now the list is typed spec on an object anyone in the workspace may read,
// and a key is fetched only for the model about to be used. Callers get the
// same projectLLMRegistry they always did — the split is in where the parts
// come from, not in what the assistant runtime sees.
func readProjectLLMRegistry(ctx context.Context, c *asclient.Client) (projectLLMRegistry, error) {
	registry := defaultProjectLLMRegistry()
	st, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return registry, nil
	}
	if err != nil {
		return registry, err
	}

	var studio aiv1alpha1.Studio
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(st.Object, &studio); err != nil {
		return registry, fmt.Errorf("decode studio: %w", err)
	}
	spec := studio.Spec.LLM
	if spec == nil {
		return registry, nil
	}

	applyProjectLLMRuntimeSpec(&registry.Runtime, spec.Runtime)
	// One Secret read per distinct model, not per revision: revisions of a
	// model share its credential, and a registry with deep history would
	// otherwise re-read the same Secret dozens of times per request.
	keys := map[string]string{}
	for _, item := range spec.Models {
		name := ""
		if item.SecretRef != nil {
			name = strings.TrimSpace(item.SecretRef.Name)
		}
		if name == "" {
			continue
		}
		if _, done := keys[name]; done {
			continue
		}
		key, err := readProjectLLMCredential(ctx, c, name)
		if err != nil {
			return registry, err
		}
		keys[name] = key
	}

	for _, item := range spec.Models {
		model := projectLLMModelSettings{
			ID: item.ID, RevisionID: item.RevisionID, Archived: item.Archived, Name: item.Name,
			Settings: projectLLMSettings{Provider: item.Provider, BaseURL: item.BaseURL, Model: item.Model},
		}
		if item.SecretRef != nil {
			model.Settings.APIKey = keys[strings.TrimSpace(item.SecretRef.Name)]
		}
		if err := normalizeProjectLLMModel(&model, registry.Runtime); err != nil {
			return registry, err
		}
		registry.Models = append(registry.Models, model)
	}

	// Structural faults (duplicate revisions, a default naming nothing) are
	// the Studio reconciler's to report, on status.conditions, where they
	// stay visible instead of failing whichever request happened to read
	// next. Here we only need a default that resolves.
	registry.DefaultModelID = strings.TrimSpace(spec.DefaultModel)
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

// readProjectLLMCredential reads one model's key. A Secret that is absent or
// has no apiKey yields "", which reads downstream as "not configured" — the
// same state a model has before anyone enters a key, and the state the
// reconciler reports as SecretMissing/SecretIncomplete.
func readProjectLLMCredential(ctx context.Context, c *asclient.Client, name string) (string, error) {
	secret, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return secretDataValue(secret, projectLLMCredentialKey), nil
}

func applyProjectLLMRuntimeSpec(settings *projectLLMSettings, runtime aiv1alpha1.StudioLLMRuntime) {
	if runtime.MaxRetries != nil && *runtime.MaxRetries >= 0 && *runtime.MaxRetries <= 10 {
		settings.MaxRetries = int(*runtime.MaxRetries)
		settings.MaxRetriesConfigured = true
	}
	if runtime.RetryBackoffMS != nil && *runtime.RetryBackoffMS > 0 {
		settings.RetryBackoff = time.Duration(*runtime.RetryBackoffMS) * time.Millisecond
	}
	if runtime.StreamIdleTimeoutMS != nil && *runtime.StreamIdleTimeoutMS > 0 {
		settings.StreamIdleTimeout = time.Duration(*runtime.StreamIdleTimeoutMS) * time.Millisecond
	}
}

func (s *Server) testProjectLLMConnection(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var request TestProjectLLMConnectionRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.APIKey) == "" && request.ExistingModelID != "" {
		registry, err := readProjectLLMRegistry(r.Context(), c)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		stored, found := registry.model(request.ExistingModelID)
		if !found {
			writeStatus(w, http.StatusNotFound, "NotFound", "model configuration not found")
			return
		}
		base, err := normalizeLLMBaseURL(request.BaseURL)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		storedBase, err := normalizeLLMBaseURL(stored.Settings.BaseURL)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		if !strings.EqualFold(strings.TrimSpace(request.Provider), strings.TrimSpace(stored.Settings.Provider)) || base != storedBase {
			writeProjectError(w, newValidationError("enter a credential before testing a changed provider or endpoint"))
			return
		}
		request.APIKey = stored.Settings.APIKey
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := verifyProjectLLMConnection(ctx, projectLLMSettings{
		Provider: request.Provider,
		BaseURL:  request.BaseURL,
		Model:    request.Model,
		APIKey:   request.APIKey,
	}); err != nil {
		writeProjectLLMConnectionTestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeProjectLLMConnectionTestError(w http.ResponseWriter, err error) {
	var connectionErr *projectLLMConnectionTestError
	if !errors.As(err, &connectionErr) {
		writeProjectError(w, err)
		return
	}
	switch connectionErr.Kind {
	case projectLLMConnectionTestRejected:
		writeStatus(w, http.StatusUnprocessableEntity, "InvalidConnection", connectionErr.Error())
	case projectLLMConnectionTestTimeout:
		writeStatus(w, http.StatusGatewayTimeout, "GatewayTimeout", "Model connection test timed out before the provider responded.")
	default:
		writeStatus(w, http.StatusBadGateway, "BadGateway", connectionErr.Error())
	}
}
