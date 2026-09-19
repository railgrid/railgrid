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

// Fixtures for the split registry. Tests used to seed one Secret holding the
// model list and its key together; now they seed the two objects production
// reads — the Studio carrying spec.llm, and that model's own credential
// Secret — so a test that accidentally depends on the old coupling fails.

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

// testLLMModelID is the id the single-model fixtures use.
const testLLMModelID = projectLLMLegacyDefaultModelID

func testLLMCredentialSecretName(modelID string) string {
	return "railgrid-projects-llm-" + modelID
}

// projectLLMStudio builds the Studio whose spec.llm holds settings as a single
// active model. It carries no credential — that is the point of the split.
func projectLLMStudio(settings projectLLMSettings) *unstructured.Unstructured {
	return projectLLMRegistryStudio(projectLLMRegistry{
		DefaultModelID: testLLMModelID,
		Runtime:        settings,
		Models: []projectLLMModelSettings{{
			ID: testLLMModelID, RevisionID: "rev-" + testLLMModelID, Name: settings.Model, Settings: settings,
		}},
	})
}

// projectLLMRegistryStudio renders a whole registry as Studio spec.
func projectLLMRegistryStudio(registry projectLLMRegistry) *unstructured.Unstructured {
	models := make([]any, 0, len(registry.Models))
	for _, item := range registry.Models {
		revision := item.RevisionID
		if revision == "" {
			revision = "rev-" + item.ID
		}
		model := map[string]any{
			"id":         item.ID,
			"revisionID": revision,
			"name":       item.Name,
			"model":      item.Settings.Model,
			"secretRef":  map[string]any{"name": testLLMCredentialSecretName(item.ID)},
		}
		if item.Archived {
			model["archived"] = true
		}
		if item.Settings.Provider != "" {
			model["provider"] = item.Settings.Provider
		}
		if item.Settings.BaseURL != "" {
			model["baseURL"] = item.Settings.BaseURL
		}
		models = append(models, model)
	}
	runtime := map[string]any{}
	if registry.Runtime.MaxRetriesConfigured {
		runtime["maxRetries"] = int64(registry.Runtime.MaxRetries)
	}
	if registry.Runtime.RetryBackoff > 0 {
		runtime["retryBackoffMS"] = registry.Runtime.RetryBackoff.Milliseconds()
	}
	if registry.Runtime.StreamIdleTimeout > 0 {
		runtime["streamIdleTimeoutMS"] = registry.Runtime.StreamIdleTimeout.Milliseconds()
	}
	llm := map[string]any{"models": models}
	if registry.DefaultModelID != "" {
		llm["defaultModel"] = registry.DefaultModelID
	}
	if len(runtime) > 0 {
		llm["runtime"] = runtime
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Studio",
		"metadata":   map[string]any{"name": aiv1alpha1.StudioName},
		"spec":       map[string]any{"llm": llm},
	}}
}

// projectLLMRegistryCredentials renders one credential Secret per model that
// has a key, keyed by Secret name.
func projectLLMRegistryCredentials(registry projectLLMRegistry) map[string]*unstructured.Unstructured {
	out := map[string]*unstructured.Unstructured{}
	for _, item := range registry.Models {
		if item.Settings.APIKey == "" {
			continue
		}
		name := testLLMCredentialSecretName(item.ID)
		out[name] = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata":   map[string]any{"name": name, "namespace": projectLLMSecretNamespace},
			"type":       "Opaque",
			"data":       map[string]any{projectLLMCredentialKey: encodeSecretValue(item.Settings.APIKey)},
		}}
	}
	return out
}

// projectLLMCredential builds the credential Secret for the fixture model.
// Returns nil when there is no key, which is how an unconfigured model looks.
func projectLLMCredential(settings projectLLMSettings) *unstructured.Unstructured {
	if settings.APIKey == "" {
		return nil
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      testLLMCredentialSecretName(testLLMModelID),
			"namespace": projectLLMSecretNamespace,
		},
		"type": "Opaque",
		"data": map[string]any{projectLLMCredentialKey: encodeSecretValue(settings.APIKey)},
	}}
}
