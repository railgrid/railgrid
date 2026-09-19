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
	"testing"

	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/store"
)

func TestProjectLLMRegistryRoundTripsMultipleModelsAndDefault(t *testing.T) {
	runtime := defaultProjectLLMSettings()
	registry := projectLLMRegistry{
		DefaultModelID: "gemini-fast",
		Runtime:        runtime,
		Models: []projectLLMModelSettings{
			{ID: "gpt-high", Name: "GPT High", Settings: projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: "https://api.openai.com/v1", Model: "gpt-test", APIKey: "openai-key"}},
			{ID: "gemini-fast", Name: "Gemini Fast", Settings: projectLLMSettings{Provider: projectLLMProviderGoogle, BaseURL: "https://generativelanguage.googleapis.com", Model: "gemini-test", APIKey: "google-key"}},
		},
	}
	client := asclient.NewFromDynamic(projectSettingsDynamicClient{registry: &registry})
	got, err := readProjectLLMRegistry(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultModelID != "gemini-fast" || len(got.Models) != 2 {
		t.Fatalf("registry = default %q with %d models, want gemini-fast with 2", got.DefaultModelID, len(got.Models))
	}
	selected, err := got.selectedSettings("gpt-high", got.Models[0].RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Model != "gpt-test" || selected.APIKey != "openai-key" {
		t.Fatalf("selected model = %#v, want gpt-test with its credential", selected)
	}
	view := got.view()
	if view.DefaultModelID != "gemini-fast" || len(view.Models) != 2 || !view.Models[0].Default {
		t.Fatalf("registry view = %#v, want default model first", view)
	}
}

func TestProjectLLMRegistryRequiresCredentialForConnectedModels(t *testing.T) {
	model := projectLLMModelSettings{
		ID:   "gpt-high",
		Name: "GPT High",
		Settings: projectLLMSettings{
			Provider: defaultProjectLLMProvider,
			BaseURL:  "https://api.openai.com/v1",
			Model:    "gpt-test",
		},
	}
	if err := validateProjectLLMModelCredential(model); err == nil || err.Error() != "a credential is required to connect this model" {
		t.Fatalf("missing credential error = %v", err)
	}
	model.Settings.APIKey = "workspace-secret"
	if err := validateProjectLLMModelCredential(model); err != nil {
		t.Fatalf("configured model rejected: %v", err)
	}
}

func TestProjectLLMRegistryFallbackDefaultSkipsModelsWithoutCredentials(t *testing.T) {
	models := []projectLLMModelSettings{
		{ID: "deleted", Archived: true, Settings: projectLLMSettings{APIKey: "old-key"}},
		{ID: "incomplete", Settings: projectLLMSettings{}},
		{ID: "ready-z", Name: "Zulu", Settings: projectLLMSettings{APIKey: "ready-key"}},
		{ID: "ready-a", Name: "Alpha", Settings: projectLLMSettings{APIKey: "ready-key"}},
	}
	if got := firstConfiguredProjectLLMModelID(models); got != "ready-a" {
		t.Fatalf("fallback default = %q, want ready-a", got)
	}
	if got := firstConfiguredProjectLLMModelID(models[:2]); got != "" {
		t.Fatalf("fallback default = %q, want empty when no configured model remains", got)
	}
	registry := projectLLMRegistry{DefaultModelID: "incomplete", Models: models}
	if got := configuredProjectLLMDefaultModelID(registry); got != "ready-a" {
		t.Fatalf("repaired default = %q, want ready-a", got)
	}
	registry.DefaultModelID = "ready-z"
	if got := configuredProjectLLMDefaultModelID(registry); got != "ready-z" {
		t.Fatalf("configured default = %q, want ready-z", got)
	}
}

func TestProjectAssistantModelSelectionIsBoundToDurableRun(t *testing.T) {
	run := store.AssistantRun{}
	if err := bindProjectAssistantStartModelAudit(&run, "gpt-high", "revision-one"); err != nil {
		t.Fatal(err)
	}
	if got := projectAssistantModelIDFromRunAudit(run); got != "gpt-high" {
		t.Fatalf("model ID = %q, want gpt-high", got)
	}
	if got := projectAssistantModelRevisionIDFromRunAudit(run); got != "revision-one" {
		t.Fatalf("model revision ID = %q, want revision-one", got)
	}
	if err := validateProjectAssistantStartModelSelection(run, "gpt-high"); err != nil {
		t.Fatalf("same-model replay failed: %v", err)
	}
	if err := validateProjectAssistantStartModelSelection(run, "gemini-fast"); !errors.Is(err, store.ErrAssistantRunConflict) {
		t.Fatalf("different-model replay error = %v, want conflict", err)
	}
}

func TestProjectLLMRegistryResolvesPinnedRevisionAfterPatchAndDelete(t *testing.T) {
	registry := projectLLMRegistry{
		DefaultModelID: "shared",
		Runtime:        defaultProjectLLMSettings(),
		Models: []projectLLMModelSettings{
			{ID: "shared", RevisionID: "revision-old", Archived: true, Name: "Shared", Settings: projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: "https://api.openai.com/v1", Model: "old-model", APIKey: "old-key"}},
			{ID: "shared", RevisionID: "revision-new", Name: "Shared", Settings: projectLLMSettings{Provider: defaultProjectLLMProvider, BaseURL: "https://api.openai.com/v1", Model: "new-model", APIKey: "new-key"}},
		},
	}
	pinned, err := registry.selectedSettings("shared", "revision-old")
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Model != "old-model" || pinned.APIKey != "old-key" {
		t.Fatalf("pinned settings = %#v, want old immutable revision", pinned)
	}
	registry.Models[1].Archived = true
	pinned, err = registry.selectedSettings("shared", "revision-old")
	if err != nil || pinned.Model != "old-model" {
		t.Fatalf("pinned settings after delete = %#v, err %v", pinned, err)
	}
	if _, err := registry.selectedModel("shared"); err == nil {
		t.Fatal("deleted logical model remained selectable for a new run")
	}
	roundTripped, err := readProjectLLMRegistry(context.Background(), asclient.NewFromDynamic(projectSettingsDynamicClient{registry: &registry}))
	if err != nil {
		t.Fatal(err)
	}
	if roundTripped.DefaultModelID != "" || len(roundTripped.view().Models) != 0 {
		t.Fatalf("deleted registry view = %#v, want no active default or selectable models", roundTripped.view())
	}
}
