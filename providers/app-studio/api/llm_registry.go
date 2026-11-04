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
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/railgrid/provider-sdk/anthropicmodel"
	"github.com/railgrid/provider-sdk/modelcatalog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/store"
)

const (
	projectLLMLegacyDefaultModelID = "default"
	projectLLMMaxModels            = 20
	projectLLMMaxStoredRevisions   = 200
)

var errProjectLLMModelNotFound = newValidationError("selected model configuration was not found")

var projectLLMModelIDInvalid = regexp.MustCompile(`[^a-z0-9]+`)

type ProjectLLMModelView struct {
	Catalog              *modelcatalog.ModelInfo `json:"catalog,omitempty"`
	ID                   string                  `json:"id"`
	Name                 string                  `json:"name"`
	ConnectionRef        string                  `json:"connectionRef,omitempty"`
	Provider             string                  `json:"provider"`
	BaseURL              string                  `json:"baseURL"`
	AnthropicWorkspaceID string                  `json:"anthropicWorkspaceID,omitempty"`
	Model                string                  `json:"model"`
	ReasoningEffort      string                  `json:"reasoningEffort,omitempty"`
	Configured           bool                    `json:"configured"`
	Default              bool                    `json:"default,omitempty"`
}

type CreateProjectLLMModelRequest struct {
	Name            string `json:"name"`
	ConnectionRef   string `json:"connectionRef"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

type TestProjectLLMConnectionRequest struct {
	ExistingModelID       string  `json:"existingModelID,omitempty"`
	ExistingConnectionID  string  `json:"existingConnectionID,omitempty"`
	ConnectionFingerprint string  `json:"connectionFingerprint,omitempty"`
	CredentialMode        string  `json:"credentialMode,omitempty"`
	ReasoningEffort       *string `json:"reasoningEffort,omitempty"`
	Provider              string  `json:"provider,omitempty"`
	BaseURL               string  `json:"baseURL,omitempty"`
	AnthropicWorkspaceID  string  `json:"anthropicWorkspaceID,omitempty"`
	Model                 string  `json:"model"`
	APIKey                string  `json:"apiKey,omitempty"`
}

type PatchProjectLLMModelRequest struct {
	Name            *string `json:"name,omitempty"`
	ConnectionRef   *string `json:"connectionRef,omitempty"`
	Model           *string `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoningEffort,omitempty"`
}

type SetDefaultProjectLLMModelRequest struct {
	ModelID string `json:"modelID"`
}

type projectLLMModelSettings struct {
	ID                    string
	RevisionID            string
	Archived              bool
	Name                  string
	Configured            bool
	ConnectionRef         string
	ConnectionSnapshot    *aiv1alpha1.StudioLLMConnectionSnapshot
	ConnectionFingerprint string
	SecretName            string
	SecretUID             string
	SecretResourceVersion string
	GeneratedSecretID     string
	Settings              projectLLMSettings
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
	if !model.Configured && strings.TrimSpace(model.Settings.APIKey) == "" {
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
			Catalog:              catalog,
			ID:                   model.ID,
			Name:                 model.Name,
			ConnectionRef:        model.ConnectionRef,
			Provider:             model.Settings.Provider,
			BaseURL:              model.Settings.BaseURL,
			AnthropicWorkspaceID: model.Settings.AnthropicWorkspaceID,
			Model:                model.Settings.Model,
			ReasoningEffort:      model.Settings.ReasoningEffort,
			Configured:           model.Configured || strings.TrimSpace(model.Settings.APIKey) != "",
			Default:              model.ID == r.DefaultModelID,
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
		view.Configured = selected.Configured || strings.TrimSpace(selected.Settings.APIKey) != ""
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
	// Archived revisions remain available to in-flight runs and audit history,
	// even if a later catalog no longer recognizes their reasoning default.
	// Active revisions are validated at admission and by the Studio controller.
	archivedReasoningEffort := model.Settings.ReasoningEffort
	if model.Archived {
		model.Settings.ReasoningEffort = ""
	}
	err = normalizeProjectLLMSettings(&model.Settings)
	if model.Archived {
		model.Settings.ReasoningEffort = archivedReasoningEffort
	}
	return err
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
		if model.Archived || (!model.Configured && strings.TrimSpace(model.Settings.APIKey) == "") {
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
	if model, found := registry.model(registry.DefaultModelID); found && (model.Configured || strings.TrimSpace(model.Settings.APIKey) != "") {
		return model.ID
	}
	return firstConfiguredProjectLLMModelID(registry.Models)
}

// readProjectLLMRegistry reads the public model bindings and status projection.
// Listing bindings never reads a key Secret; a key is loaded only for the
// model selected for an authorized inference or probe.
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
	configured := map[string]bool{}
	for _, item := range studio.Status.Models {
		configured[item.ID] = item.Configured
	}

	for _, item := range spec.Models {
		model := projectLLMModelSettings{
			ID: item.ID, RevisionID: item.RevisionID, Archived: item.Archived, Name: item.Name,
			Configured: configured[item.ID], ConnectionRef: strings.TrimSpace(item.ConnectionRef),
			ConnectionSnapshot: item.ConnectionSnapshot,
			Settings:           projectLLMSettings{Provider: item.Provider, BaseURL: item.BaseURL, AnthropicWorkspaceID: item.AnthropicWorkspaceID, Model: item.Model, ReasoningEffort: item.ReasoningEffort},
		}
		if item.ConnectionSnapshot != nil {
			model.ConnectionFingerprint = item.ConnectionSnapshot.ConnectionFingerprint
			model.Settings.Provider = item.ConnectionSnapshot.Provider
			model.Settings.BaseURL = item.ConnectionSnapshot.BaseURL
			model.Settings.AnthropicWorkspaceID = item.ConnectionSnapshot.AnthropicWorkspaceID
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

// readProjectLLMModelForTurn resolves one logical model or pinned revision and
// reads only that model's credential. Registry views still use
// readProjectLLMRegistry; a turn should not fetch unrelated model Secrets just
// to build its runtime configuration.
func readProjectLLMModelForTurn(ctx context.Context, c *asclient.Client, modelID, revisionID string) (projectLLMModelSettings, error) {
	return readProjectLLMModelWithSettings(ctx, c, modelID, revisionID, nil)
}

// readProjectLLMModelForCredentialReuse reads only the active model whose
// credential is being reused by a connection test or model discovery request.
// The saved reasoning default is returned unchanged, but is temporarily
// suppressed while normalizing the connection so an invalid or stale default
// can be replaced without making unrelated registry entries block the read.
func readProjectLLMModelForCredentialReuse(ctx context.Context, c *asclient.Client, modelID string) (projectLLMModelSettings, error) {
	config, err := readProjectLLMModelConfig(ctx, c, modelID)
	if err != nil {
		return projectLLMModelSettings{}, err
	}
	if config.ConnectionRef != "" || config.ConnectionSnapshot != nil {
		return projectLLMModelSettings{}, newValidationError("reuse this saved key through existingConnectionID and its connection fingerprint")
	}
	if err := validateLegacyCredentialReference(ctx, c, config.SecretName); err != nil {
		return projectLLMModelSettings{}, err
	}
	// Use the exact configuration checked above; re-reading the Studio here
	// would let a concurrent spec change replace the guarded key reference.
	model := config
	savedReasoningEffort := model.Settings.ReasoningEffort
	model.Settings.ReasoningEffort = ""
	model.Settings.APIKey, err = readProjectLLMCredential(ctx, c, model.SecretName)
	if err != nil {
		return projectLLMModelSettings{}, err
	}
	if err := normalizeProjectLLMModel(&model, defaultProjectLLMSettings()); err != nil {
		return projectLLMModelSettings{}, err
	}
	if strings.TrimSpace(model.Settings.APIKey) == "" {
		return projectLLMModelSettings{}, newValidationError("selected model configuration does not have a credential")
	}
	model.Settings.ReasoningEffort = savedReasoningEffort
	return model, nil
}

// readProjectLLMModelForIdentity checks name-scoped use permission before it
// reads the connection's Secret. The returned connection values come from the
// model revision snapshot, not from a mutable connection object.
func (s *Server) readProjectLLMModelForIdentity(ctx context.Context, id identity, c *asclient.Client, modelID, revisionID string, restore func(*projectLLMSettings)) (projectLLMModelSettings, error) {
	config, err := readProjectLLMModelWithOptions(ctx, c, modelID, revisionID, nil, false, false)
	if err != nil {
		return projectLLMModelSettings{}, err
	}
	if revisionID == "" && config.ConnectionRef != "" && config.ConnectionSnapshot != nil {
		// Studio and connection status have separate CAS boundaries. If a
		// rollover was written but its admission status lost a concurrent spec
		// race, keep selecting the last controller-admitted archived revision.
		connection, readErr := readModelProviderConnection(ctx, c, config.ConnectionRef)
		if readErr != nil {
			return projectLLMModelSettings{}, readErr
		}
		if active := connection.Status.ActiveSnapshot; active != nil && !reflect.DeepEqual(active, config.ConnectionSnapshot) {
			obj, readErr := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
			if readErr != nil {
				return projectLLMModelSettings{}, readErr
			}
			var studio aiv1alpha1.Studio
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &studio); err != nil {
				return projectLLMModelSettings{}, err
			}
			if studio.Spec.LLM != nil {
				for _, prior := range studio.Spec.LLM.Models {
					if prior.ID == config.ID && prior.ConnectionRef == config.ConnectionRef && prior.Model == config.Settings.Model && prior.ReasoningEffort == config.Settings.ReasoningEffort && reflect.DeepEqual(prior.ConnectionSnapshot, active) {
						config, err = readProjectLLMModelWithOptions(ctx, c, prior.ID, prior.RevisionID, nil, false, false)
						if err != nil {
							return projectLLMModelSettings{}, err
						}
						break
					}
				}
			}
		}
	}
	if config.ConnectionRef != "" {
		if config.ConnectionSnapshot == nil {
			return projectLLMModelSettings{}, newValidationError("model binding is waiting for its immutable provider connection snapshot")
		}
	}
	selected, err := readProjectLLMModelWithOptions(ctx, c, config.ID, config.RevisionID, nil, false, false)
	if err != nil {
		return projectLLMModelSettings{}, err
	}
	if config.ConnectionRef != selected.ConnectionRef || config.ConnectionFingerprint != selected.ConnectionFingerprint ||
		!reflect.DeepEqual(config.ConnectionSnapshot, selected.ConnectionSnapshot) || config.SecretName != selected.SecretName ||
		config.Settings.Provider != selected.Settings.Provider || config.Settings.BaseURL != selected.Settings.BaseURL ||
		config.Settings.AnthropicWorkspaceID != selected.Settings.AnthropicWorkspaceID || config.Settings.Model != selected.Settings.Model ||
		config.Settings.ReasoningEffort != selected.Settings.ReasoningEffort {
		return projectLLMModelSettings{}, apierrors.NewConflict(studiosGVR.GroupResource(), aiv1alpha1.StudioName, errors.New("model binding changed during selection; retry the request"))
	}
	if selected.ConnectionSnapshot != nil {
		if selected.ConnectionRef == "" {
			return projectLLMModelSettings{}, newValidationError("model connection snapshot has no authorized connection reference")
		}
		selected.Settings.APIKey, err = s.readAuthorizedConnectionSnapshotCredential(ctx, id, c, selected.ConnectionRef, selected.ConnectionSnapshot)
	} else if selected.ConnectionRef != "" {
		return projectLLMModelSettings{}, newValidationError("model binding is waiting for its immutable provider connection snapshot")
	} else if selected.SecretName != "" {
		if err = validateLegacyCredentialReference(ctx, c, selected.SecretName); err == nil {
			selected.Settings.APIKey, err = readProjectLLMCredential(ctx, c, selected.SecretName)
		}
	}
	if err != nil {
		return projectLLMModelSettings{}, err
	}
	if restore != nil {
		restore(&selected.Settings)
	}
	if err := normalizeProjectLLMModel(&selected, selected.Settings); err != nil {
		return projectLLMModelSettings{}, err
	}
	if strings.TrimSpace(selected.Settings.APIKey) == "" {
		return projectLLMModelSettings{}, newValidationError("selected model configuration does not have a credential")
	}
	selected.Configured = true
	return selected, nil
}

func (s *Server) readDefaultProjectLLMModelForIdentity(ctx context.Context, id identity, c *asclient.Client) (projectLLMModelSettings, error) {
	registry, err := readProjectLLMRegistry(ctx, c)
	if err != nil {
		return projectLLMModelSettings{}, err
	}
	modelID := strings.TrimSpace(registry.DefaultModelID)
	if modelID == "" {
		return projectLLMModelSettings{}, errProjectLLMModelNotFound
	}
	return s.readProjectLLMModelForIdentity(ctx, id, c, modelID, "", nil)
}

// readProjectLLMModelConfig reads the active model's saved configuration
// without fetching or requiring its credential. Use it for decisions that
// depend on provider/model settings but do not need to connect to the model.
func readProjectLLMModelConfig(ctx context.Context, c *asclient.Client, modelID string) (projectLLMModelSettings, error) {
	return readProjectLLMModelWithOptions(ctx, c, modelID, "", nil, false, false)
}

// readProjectLLMModelForRun restores the admitted reasoning settings before
// validating the selected revision. A resumed run must not validate a later
// model default, or an unrelated model, before restoring its own snapshot.
func readProjectLLMModelForRun(ctx context.Context, c *asclient.Client, run store.AssistantRun) (projectLLMModelSettings, error) {
	modelID, revisionID := projectAssistantModelReferenceFromRunAudit(run)
	if snapshot := projectAssistantConnectionSnapshotFromRunAudit(run); snapshot != nil {
		selected, err := readProjectLLMModelWithOptions(ctx, c, modelID, revisionID, nil, false, false)
		if err != nil {
			return projectLLMModelSettings{}, err
		}
		selected.ConnectionRef = strings.TrimSpace(snapshot.ConnectionRef)
		selected.ConnectionSnapshot = snapshot
		selected.ConnectionFingerprint = snapshot.ConnectionFingerprint
		selected.Settings.Provider = snapshot.Provider
		selected.Settings.BaseURL = snapshot.BaseURL
		selected.Settings.AnthropicWorkspaceID = snapshot.AnthropicWorkspaceID
		selected.SecretName = ""
		if snapshot.SecretRef != nil {
			selected.SecretName = strings.TrimSpace(snapshot.SecretRef.Name)
		}
		selected.SecretUID = snapshot.SecretUID
		selected.SecretResourceVersion = snapshot.SecretResourceVersion
		selected.GeneratedSecretID = snapshot.GeneratedSecretID
		selected.Settings.APIKey, err = readConnectionSnapshotCredential(ctx, c, snapshot)
		if err != nil {
			return projectLLMModelSettings{}, err
		}
		applyProjectAssistantReasoningSnapshot(&selected.Settings, run)
		if err := normalizeProjectLLMModel(&selected, selected.Settings); err != nil {
			return projectLLMModelSettings{}, err
		}
		selected.Configured = true
		return selected, nil
	}
	if legacy, found, err := readLegacyProjectLLMRunFromPlan(ctx, c, run); found || err != nil {
		return legacy, err
	}
	return readProjectLLMModelWithSettings(ctx, c, modelID, revisionID, func(settings *projectLLMSettings) {
		applyProjectAssistantReasoningSnapshot(settings, run)
	})
}

// The source plan survives capacity-limited in-place migration and prevents a
// caller from rewriting the old revision's transport before a legacy resume.
func legacyProjectLLMRunSource(ctx context.Context, c *asclient.Client, run store.AssistantRun) (*aiv1alpha1.StudioLegacyModelMigration, error) {
	modelID, revisionID := projectAssistantModelReferenceFromRunAudit(run)
	if modelID == "" || revisionID == "" {
		return nil, nil
	}
	if c == nil {
		return nil, errors.New("project client is required for legacy model resolution")
	}
	obj, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var studio aiv1alpha1.Studio
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &studio); err != nil {
		return nil, err
	}
	for _, entry := range studio.Status.LegacyModelMigrations {
		if entry.Model.ID == modelID && entry.Model.RevisionID == revisionID && entry.Model.SecretRef != nil && entry.SecretUID != "" {
			return &entry, nil
		}
	}
	return nil, nil
}

func readLegacyProjectLLMRunFromPlan(ctx context.Context, c *asclient.Client, run store.AssistantRun) (projectLLMModelSettings, bool, error) {
	entry, err := legacyProjectLLMRunSource(ctx, c, run)
	if err != nil {
		return projectLLMModelSettings{}, false, err
	}
	if entry == nil {
		return projectLLMModelSettings{}, false, nil
	}
	model := entry.Model
	key, err := readProjectLLMCredential(ctx, c, model.SecretRef.Name, entry.SecretUID)
	if err != nil {
		return projectLLMModelSettings{}, true, err
	}
	selected := projectLLMModelSettings{ID: model.ID, RevisionID: model.RevisionID, Name: model.Name, Settings: projectLLMSettings{Provider: model.Provider, BaseURL: model.BaseURL, AnthropicWorkspaceID: model.AnthropicWorkspaceID, Model: model.Model, ReasoningEffort: model.ReasoningEffort, APIKey: key}}
	applyProjectAssistantReasoningSnapshot(&selected.Settings, run)
	studio, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return projectLLMModelSettings{}, true, err
	}
	var typed aiv1alpha1.Studio
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(studio.Object, &typed); err != nil {
		return projectLLMModelSettings{}, true, err
	}
	runtimeSettings := defaultProjectLLMSettings()
	if typed.Spec.LLM != nil {
		applyProjectLLMRuntimeSpec(&runtimeSettings, typed.Spec.LLM.Runtime)
	}
	if err := normalizeProjectLLMModel(&selected, runtimeSettings); err != nil {
		return projectLLMModelSettings{}, true, err
	}
	if key == "" {
		return projectLLMModelSettings{}, true, newValidationError("legacy run credential is no longer available")
	}
	selected.Configured = true
	return selected, true, nil
}

// readProjectLLMModelForContinuation starts a new run that continues an
// interrupted predecessor. Unlike an ordinary model selection, this uses the
// predecessor's server-written audit snapshot so a connection rotation cannot
// change the interrupted conversation's model. It still checks the caller's
// current name-scoped `use` permission before reading that pinned Secret.
// authorizeProjectAssistantRunModelUse runs before claiming a paused run and
// again before reading its key. Legacy audits resolve authority through the
// migrated connection, including its original source revision after rotation.
func (s *Server) authorizeProjectAssistantRunModelUse(ctx context.Context, id identity, c *asclient.Client, run store.AssistantRun) error {
	if snapshot := projectAssistantConnectionSnapshotFromRunAudit(run); snapshot != nil {
		if strings.TrimSpace(snapshot.ConnectionRef) == "" {
			return newValidationError("run connection snapshot is incomplete")
		}
		return s.authorizeModelProviderConnectionUse(ctx, id, snapshot.ConnectionRef)
	}
	// Resolve legacy authority from controller-owned provenance before reading
	// the caller-writable current row; its connectionRef may have been edited.
	if source, err := legacyProjectLLMRunSource(ctx, c, run); err != nil {
		return err
	} else if source != nil {
		return s.authorizeModelProviderConnectionUse(ctx, id, source.Model.ID)
	}
	if c == nil {
		return errors.New("project client is required for model authorization")
	}
	modelID, revisionID := projectAssistantModelReferenceFromRunAudit(run)
	model, err := readProjectLLMModelWithOptions(ctx, c, modelID, revisionID, nil, false, false)
	if err != nil {
		return err
	}
	if model.ConnectionRef != "" {
		return s.authorizeModelProviderConnectionUse(ctx, id, model.ConnectionRef)
	}
	connections, err := c.Resource(modelProviderConnectionResource, "").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	// A migration destination has the original logical model name. Prefer
	// that authority over other legitimate bindings sharing the same key.
	for _, connection := range connections.Items {
		if connection.GetName() == model.ID && connection.GetAnnotations()["ai.railgrid.ai/migrated-from-studio-revision"] == model.RevisionID {
			if err := s.authorizeModelProviderConnectionUse(ctx, id, connection.GetName()); err != nil {
				return err
			}
			return validateLegacyResumeTransport(ctx, c, model, &connection)
		}
	}
	found := false
	for _, connection := range connections.Items {
		secretName, _, _ := unstructured.NestedString(connection.Object, "spec", "secretRef", "name")
		activeSecret, _, _ := unstructured.NestedString(connection.Object, "status", "activeSnapshot", "secretRef", "name")
		sourceRevision := connection.GetAnnotations()["ai.railgrid.ai/migrated-from-studio-revision"]
		if (model.SecretName != "" && (strings.TrimSpace(secretName) == model.SecretName || strings.TrimSpace(activeSecret) == model.SecretName)) || (connection.GetName() == model.ID && sourceRevision == model.RevisionID) {
			found = true
			if err := s.authorizeModelProviderConnectionUse(ctx, id, connection.GetName()); err != nil {
				return err
			}
			if err := validateLegacyResumeTransport(ctx, c, model, &connection); err != nil {
				return err
			}
		}
	}
	if model.Archived && !found {
		// A deleted migration destination must not erase its name-scoped gate.
		return s.authorizeModelProviderConnectionUse(ctx, id, model.ID)
	}
	return nil
}

// Legacy audits pinned a revision ID but not its endpoint. Compare that
// caller-writable row with controller-owned provenance before sending a key.
func validateLegacyResumeTransport(ctx context.Context, c *asclient.Client, model projectLLMModelSettings, connection *unstructured.Unstructured) error {
	matches := func(provider, baseURL, workspace, secret string) bool {
		if strings.TrimSpace(secret) != model.SecretName || canonicalModelProvider(provider) != canonicalModelProvider(model.Settings.Provider) || strings.TrimSpace(workspace) != strings.TrimSpace(model.Settings.AnthropicWorkspaceID) {
			return false
		}
		base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
		target := strings.TrimRight(strings.TrimSpace(model.Settings.BaseURL), "/")
		if base == "" {
			if canonicalModelProvider(provider) == projectLLMProviderGoogle {
				base = defaultProjectLLMGoogleBaseURL
			} else {
				base = defaultProjectLLMBaseURL
			}
		}
		if target == "" {
			if canonicalModelProvider(model.Settings.Provider) == projectLLMProviderGoogle {
				target = defaultProjectLLMGoogleBaseURL
			} else {
				target = defaultProjectLLMBaseURL
			}
		}
		return base == target
	}
	var typed aiv1alpha1.ModelProviderConnection
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(connection.Object, &typed); err != nil {
		return err
	}
	if matches(typed.Spec.Provider, typed.Spec.BaseURL, typed.Spec.AnthropicWorkspaceID, typed.Spec.SecretRef.Name) {
		return nil
	}
	if active := typed.Status.ActiveSnapshot; active != nil && active.SecretRef != nil && matches(active.Provider, active.BaseURL, active.AnthropicWorkspaceID, active.SecretRef.Name) {
		return nil
	}
	obj, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	var studio aiv1alpha1.Studio
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &studio); err != nil {
		return err
	}
	for _, entry := range studio.Status.LegacyModelMigrations {
		prior := entry.Model
		if prior.ID == model.ID && prior.RevisionID == model.RevisionID && prior.SecretRef != nil && matches(prior.Provider, prior.BaseURL, prior.AnthropicWorkspaceID, prior.SecretRef.Name) {
			return nil
		}
	}
	return newValidationError("the legacy run configuration no longer matches its admitted provider; start a new turn with the current model")
}

func (s *Server) readProjectLLMModelForRunForIdentity(ctx context.Context, id identity, c *asclient.Client, run store.AssistantRun) (projectLLMModelSettings, error) {
	if err := s.authorizeProjectAssistantRunModelUse(ctx, id, c, run); err != nil {
		return projectLLMModelSettings{}, err
	}
	if projectAssistantConnectionSnapshotFromRunAudit(run) == nil {
		if legacy, found, err := readLegacyProjectLLMRunFromPlan(ctx, c, run); found || err != nil {
			return legacy, err
		}

		modelID, revisionID := projectAssistantModelReferenceFromRunAudit(run)
		model, err := readProjectLLMModelWithOptions(ctx, c, modelID, revisionID, nil, false, false)
		if err != nil {
			return projectLLMModelSettings{}, err
		}
		if model.ConnectionRef != "" || model.ConnectionSnapshot != nil {
			return s.readProjectLLMModelForIdentity(ctx, id, c, modelID, revisionID, func(settings *projectLLMSettings) { applyProjectAssistantReasoningSnapshot(settings, run) })
		}
	}
	return readProjectLLMModelForRun(ctx, c, run)
}

func (s *Server) readProjectLLMModelForContinuation(ctx context.Context, id identity, c *asclient.Client, predecessor store.AssistantRun) (projectLLMModelSettings, error) {
	return s.readProjectLLMModelForRunForIdentity(ctx, id, c, predecessor)
}

func readProjectLLMModelWithSettings(ctx context.Context, c *asclient.Client, modelID, revisionID string, restore func(*projectLLMSettings)) (projectLLMModelSettings, error) {
	return readProjectLLMModelWithOptions(ctx, c, modelID, revisionID, restore, true, true)
}

func readProjectLLMModelWithOptions(ctx context.Context, c *asclient.Client, modelID, revisionID string, restore func(*projectLLMSettings), loadCredential, normalizeModel bool) (projectLLMModelSettings, error) {
	st, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return projectLLMModelSettings{}, errProjectLLMModelNotFound
	}
	if err != nil {
		return projectLLMModelSettings{}, err
	}
	var studio aiv1alpha1.Studio
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(st.Object, &studio); err != nil {
		return projectLLMModelSettings{}, fmt.Errorf("decode studio: %w", err)
	}
	if studio.Spec.LLM == nil {
		return projectLLMModelSettings{}, errProjectLLMModelNotFound
	}
	configured := map[string]bool{}
	for _, status := range studio.Status.Models {
		configured[status.ID] = status.Configured
	}

	runtimeSettings := defaultProjectLLMSettings()
	applyProjectLLMRuntimeSpec(&runtimeSettings, studio.Spec.LLM.Runtime)
	candidates := projectLLMRegistry{Runtime: runtimeSettings}
	secretRefs := make([]string, 0, len(studio.Spec.LLM.Models))
	for _, item := range studio.Spec.LLM.Models {
		revision := strings.TrimSpace(item.RevisionID)
		if revision == "" {
			// Match readProjectLLMRegistry's compatibility behavior for legacy
			// Studio objects that predate explicit revision IDs.
			revision = uuid.NewString()
		}
		candidates.Models = append(candidates.Models, projectLLMModelSettings{
			ID: projectLLMModelID(item.ID), RevisionID: revision, Archived: item.Archived,
		})
		secretName := ""
		if item.SecretRef != nil {
			secretName = strings.TrimSpace(item.SecretRef.Name)
		}
		secretRefs = append(secretRefs, secretName)
	}
	candidates.DefaultModelID = strings.TrimSpace(studio.Spec.LLM.DefaultModel)
	if _, ok := candidates.model(candidates.DefaultModelID); !ok {
		candidates.DefaultModelID = ""
		for _, candidate := range candidates.Models {
			if !candidate.Archived {
				candidates.DefaultModelID = candidate.ID
				break
			}
		}
	}

	var selected projectLLMModelSettings
	var found bool
	if strings.TrimSpace(revisionID) != "" {
		selected, found = candidates.modelRevision(modelID, revisionID)
	} else {
		selected, found = candidates.model(modelID)
	}
	if !found {
		return projectLLMModelSettings{}, errProjectLLMModelNotFound
	}
	selectedIndex := -1
	for index, candidate := range candidates.Models {
		if candidate.ID == selected.ID && candidate.RevisionID == selected.RevisionID {
			selectedIndex = index
			break
		}
	}
	if selectedIndex < 0 || selectedIndex >= len(studio.Spec.LLM.Models) {
		return projectLLMModelSettings{}, errProjectLLMModelNotFound
	}
	item := studio.Spec.LLM.Models[selectedIndex]
	selected.Name = item.Name
	selected.Configured = configured[selected.ID]
	selected.ConnectionRef = strings.TrimSpace(item.ConnectionRef)
	selected.ConnectionSnapshot = item.ConnectionSnapshot
	if selected.ConnectionRef != "" && item.ConnectionSnapshot != nil && strings.TrimSpace(item.ConnectionSnapshot.ConnectionRef) != selected.ConnectionRef {
		return projectLLMModelSettings{}, newValidationError("model connection snapshot does not match its connectionRef")
	}
	selected.Settings = projectLLMSettings{Provider: item.Provider, BaseURL: item.BaseURL, AnthropicWorkspaceID: item.AnthropicWorkspaceID, Model: item.Model, ReasoningEffort: item.ReasoningEffort,
		MaxRetries: runtimeSettings.MaxRetries, MaxRetriesConfigured: runtimeSettings.MaxRetriesConfigured, RetryBackoff: runtimeSettings.RetryBackoff, StreamIdleTimeout: runtimeSettings.StreamIdleTimeout}
	secretName := secretRefs[selectedIndex]
	if item.ConnectionSnapshot != nil {
		selected.Settings.Provider = item.ConnectionSnapshot.Provider
		selected.Settings.BaseURL = item.ConnectionSnapshot.BaseURL
		selected.Settings.AnthropicWorkspaceID = item.ConnectionSnapshot.AnthropicWorkspaceID
		selected.ConnectionFingerprint = item.ConnectionSnapshot.ConnectionFingerprint
		if item.ConnectionSnapshot.SecretRef != nil {
			secretName = strings.TrimSpace(item.ConnectionSnapshot.SecretRef.Name)
		}
		selected.SecretName = secretName
		selected.SecretUID = item.ConnectionSnapshot.SecretUID
		selected.SecretResourceVersion = item.ConnectionSnapshot.SecretResourceVersion
		selected.GeneratedSecretID = item.ConnectionSnapshot.GeneratedSecretID
	} else if item.SecretRef != nil {
		selected.SecretName = strings.TrimSpace(item.SecretRef.Name)
	}
	if loadCredential {
		if selected.ConnectionRef != "" || item.ConnectionSnapshot != nil {
			return projectLLMModelSettings{}, newValidationError("connected model credentials require caller authorization against the live provider connection")
		} else if secretName != "" {
			selected.Settings.APIKey, err = readProjectLLMCredential(ctx, c, secretName)
		}
		if err != nil {
			return projectLLMModelSettings{}, err
		}
		selected.Configured = strings.TrimSpace(selected.Settings.APIKey) != ""
	}
	if restore != nil {
		restore(&selected.Settings)
	}
	if normalizeModel {
		if err := normalizeProjectLLMModel(&selected, runtimeSettings); err != nil {
			return projectLLMModelSettings{}, err
		}
	}
	if loadCredential && strings.TrimSpace(selected.Settings.APIKey) == "" {
		return projectLLMModelSettings{}, newValidationError("selected model configuration does not have a credential")
	}
	return selected, nil
}

// validateLegacyCredentialReference keeps caller-writable direct entries from
// bypassing the use check on a saved connection. Generated keys are never
// legacy credentials, including after their owning connection is deleted.
func validateLegacyCredentialReference(ctx context.Context, c *asclient.Client, name string) error {
	if name == "" {
		return newValidationError("selected model configuration does not have a credential")
	}
	if strings.HasPrefix(name, aiv1alpha1.ModelProviderConnectionSecretPrefix) {
		return newValidationError("saved provider keys require a provider connection binding")
	}
	studio, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	var typed aiv1alpha1.Studio
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(studio.Object, &typed); err != nil {
		return err
	}
	for _, entry := range typed.Status.ProtectedModelCredentials {
		if entry.SecretName == name {
			return newValidationError("retained provider keys require a provider connection binding")
		}
	}
	for _, entry := range typed.Status.LegacyModelMigrations {
		if entry.Model.SecretRef != nil && strings.TrimSpace(entry.Model.SecretRef.Name) == name {
			return newValidationError("migrated provider keys require a provider connection binding")
		}
	}
	connections, err := c.Resource(modelProviderConnectionResource, "").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, connection := range connections.Items {
		secretName, _, _ := unstructured.NestedString(connection.Object, "spec", "secretRef", "name")
		activeSecretName, _, _ := unstructured.NestedString(connection.Object, "status", "activeSnapshot", "secretRef", "name")
		if strings.TrimSpace(secretName) == name || strings.TrimSpace(activeSecretName) == name {
			return newValidationError("this legacy model key belongs to a saved provider connection; refresh and select its connection binding")
		}
	}
	return nil
}

// readProjectLLMCredential preserves genuinely legacy credentials for runs
// admitted before migration, but never resolves a connection-owned key.
func readProjectLLMCredential(ctx context.Context, c *asclient.Client, name string, expectedUID ...string) (string, error) {
	if strings.HasPrefix(name, aiv1alpha1.ModelProviderConnectionSecretPrefix) {
		return "", newValidationError("saved provider keys require a provider connection binding")
	}
	secret, err := c.Resource(secretResource, projectLLMSecretNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(expectedUID) > 0 && string(secret.GetUID()) != expectedUID[0] {
		return "", newValidationError("the legacy run credential Secret was replaced")
	}
	if secret.GetAnnotations()[aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation] != "" {
		return "", newValidationError("saved provider keys cannot be used by a legacy direct model")
	}
	for _, owner := range secret.GetOwnerReferences() {
		if owner.APIVersion == aiv1alpha1.SchemeGroupVersion.String() && owner.Kind == "ModelProviderConnection" {
			return "", newValidationError("saved provider keys cannot be used by a legacy direct model")
		}
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
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	var request TestProjectLLMConnectionRequest
	if !decodeStrictJSON(w, r, &request) {
		return
	}
	var savedConnectionProbe *modelProviderConnectionProbeTarget
	if strings.TrimSpace(request.APIKey) == "" && strings.TrimSpace(request.ExistingConnectionID) != "" {
		provider := strings.TrimSpace(request.Provider)
		baseURL := strings.TrimSpace(request.BaseURL)
		if baseURL == "" {
			if strings.EqualFold(provider, projectLLMProviderGoogle) {
				baseURL = defaultProjectLLMGoogleBaseURL
			} else {
				baseURL = defaultProjectLLMBaseURL
			}
		}
		stored, target, err := s.resolveSavedConnectionCredentialForProbe(r.Context(), id, c, request.ExistingConnectionID, request.ConnectionFingerprint, provider, baseURL, request.AnthropicWorkspaceID)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		savedConnectionProbe = target
		request.Provider = stored.Provider
		request.BaseURL = stored.BaseURL
		request.AnthropicWorkspaceID = stored.AnthropicWorkspaceID
		request.APIKey = stored.APIKey
		if request.ReasoningEffort == nil {
			if model, modelErr := readProjectLLMModelConfig(r.Context(), c, request.ExistingModelID); modelErr == nil {
				effort := model.Settings.ReasoningEffort
				request.ReasoningEffort = &effort
			}
		}
	} else if strings.TrimSpace(request.APIKey) == "" && request.ExistingModelID != "" {
		stored, err := readProjectLLMModelForCredentialReuse(r.Context(), c, request.ExistingModelID)
		if err != nil {
			if errors.Is(err, errProjectLLMModelNotFound) {
				writeStatus(w, http.StatusNotFound, "NotFound", "model configuration not found")
				return
			}
			writeProjectError(w, err)
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
		if request.ReasoningEffort == nil {
			effort := stored.Settings.ReasoningEffort
			request.ReasoningEffort = &effort
		}
	} else if strings.TrimSpace(request.APIKey) != "" && request.ExistingModelID != "" && request.ReasoningEffort == nil {
		stored, err := readProjectLLMModelConfig(r.Context(), c, request.ExistingModelID)
		if err != nil {
			if errors.Is(err, errProjectLLMModelNotFound) {
				writeStatus(w, http.StatusNotFound, "NotFound", "model configuration not found")
				return
			}
			writeProjectError(w, err)
			return
		}
		effort := stored.Settings.ReasoningEffort
		request.ReasoningEffort = &effort
	}
	if err := validateProjectLLMCredentialMode(request.CredentialMode, request.Provider, request.APIKey); err != nil {
		writeProjectError(w, err)
		return
	}
	if strings.TrimSpace(request.AnthropicWorkspaceID) != "" && (!anthropicmodel.UsesEndpoint(request.BaseURL) || !validProjectLLMAnthropicWorkspaceID(request.AnthropicWorkspaceID)) {
		writeProjectError(w, newValidationError("anthropicWorkspaceID requires a valid workspace ID and the official Anthropic Messages endpoint"))
		return
	}
	reasoningEffort := ""
	if request.ReasoningEffort != nil {
		reasoningEffort = *request.ReasoningEffort
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	testErr := verifyProjectLLMConnection(ctx, projectLLMSettings{
		Provider:             request.Provider,
		BaseURL:              request.BaseURL,
		AnthropicWorkspaceID: request.AnthropicWorkspaceID,
		Model:                request.Model,
		ReasoningEffort:      reasoningEffort,
		APIKey:               request.APIKey,
	})
	if savedConnectionProbe != nil {
		outcome := modelProviderConnectionProbeOutcome{
			reachable: metav1.ConditionTrue,
			reason:    "Reachable",
			message:   "The provider accepted the model connection check.",
		}
		if testErr != nil {
			outcome = modelProviderConnectionProbeOutcome{
				reachable: metav1.ConditionFalse,
				reason:    "ProbeFailed",
				message:   "Provider model testing failed; check the model ID, endpoint, and credential.",
				lastError: "Provider model testing failed; check the model ID, endpoint, and credential.",
			}
		}
		if _, publishErr := publishModelProviderConnectionProbe(r.Context(), c, savedConnectionProbe, outcome, time.Now()); publishErr != nil {
			writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "The model check ran, but its verification status could not be saved. Retry the check.")
			return
		}
	}
	if testErr != nil {
		writeProjectLLMConnectionTestError(w, testErr)
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
