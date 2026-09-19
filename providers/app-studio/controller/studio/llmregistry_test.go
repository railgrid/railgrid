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

package studio

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	appscheme "github.com/railgrid/provider-app-studio/scheme"
)

func model(id, revision, name string) aiv1alpha1.StudioLLMModel {
	return aiv1alpha1.StudioLLMModel{
		ID: id, RevisionID: revision, Name: name, Model: "gpt-5",
		SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: LLMCredentialSecretName(id)},
	}
}

// The credential Secret name is derived from the model ID rather than chosen,
// so a writer and a reader agree without coordinating.
func TestLLMCredentialSecretNameIsDerived(t *testing.T) {
	if got, want := LLMCredentialSecretName("gpt"), "railgrid-projects-llm-gpt"; got != want {
		t.Fatalf("LLMCredentialSecretName = %q, want %q", got, want)
	}
}

// Each case is a rule about the SET of models — the thing a CRD schema, which
// validates one entry at a time, cannot state.
func TestValidateLLMRegistry(t *testing.T) {
	sound := &aiv1alpha1.StudioLLM{
		DefaultModel: "gpt",
		Models: []aiv1alpha1.StudioLLMModel{
			model("gpt", "rev-1", "GPT"),
			model("claude", "rev-2", "Claude"),
			func() aiv1alpha1.StudioLLMModel {
				m := model("gpt", "rev-0", "GPT (old)")
				m.Archived = true
				return m
			}(),
		},
	}

	for _, tc := range []struct {
		name       string
		registry   *aiv1alpha1.StudioLLM
		wantReason string
	}{
		{"a sound registry", sound, ""},
		{"no default set", &aiv1alpha1.StudioLLM{Models: []aiv1alpha1.StudioLLMModel{model("gpt", "r", "GPT")}}, ""},
		{"duplicate revision", &aiv1alpha1.StudioLLM{Models: []aiv1alpha1.StudioLLMModel{
			model("a", "r", "A"), model("b", "r", "B"),
		}}, "DuplicateRevision"},
		{"two active models share an id", &aiv1alpha1.StudioLLM{Models: []aiv1alpha1.StudioLLMModel{
			model("a", "r1", "A"), model("a", "r2", "A again"),
		}}, "DuplicateModelID"},
		{"archived revisions may repeat an id", &aiv1alpha1.StudioLLM{Models: []aiv1alpha1.StudioLLMModel{
			model("a", "r1", "A"),
			func() aiv1alpha1.StudioLLMModel { m := model("a", "r2", "A old"); m.Archived = true; return m }(),
		}}, ""},
		{"default names nothing active", &aiv1alpha1.StudioLLM{
			DefaultModel: "missing", Models: []aiv1alpha1.StudioLLMModel{model("gpt", "r", "GPT")},
		}, "DefaultModelMissing"},
		{"default names an archived revision", &aiv1alpha1.StudioLLM{
			DefaultModel: "gone",
			Models: []aiv1alpha1.StudioLLMModel{
				func() aiv1alpha1.StudioLLMModel { m := model("gone", "r", "Gone"); m.Archived = true; return m }(),
			},
		}, "DefaultModelMissing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, message := validateLLMRegistry(tc.registry)
			if reason != tc.wantReason {
				t.Fatalf("reason = %q (%s), want %q", reason, message, tc.wantReason)
			}
			if reason != "" && strings.TrimSpace(message) == "" {
				t.Fatal("a failing rule must say what to do about it")
			}
		})
	}
}

func TestValidateLLMRegistryBounds(t *testing.T) {
	active := &aiv1alpha1.StudioLLM{}
	for i := range llmRegistryMaxModels + 1 {
		active.Models = append(active.Models, model(fmt.Sprintf("m%d", i), fmt.Sprintf("rev-%d", i), "Model"))
	}
	if reason, _ := validateLLMRegistry(active); reason != "TooManyModels" {
		t.Fatalf("reason = %q, want TooManyModels for %d active models", reason, len(active.Models))
	}

	// Archiving is exactly what exempts a revision from the active limit.
	archived := &aiv1alpha1.StudioLLM{}
	for i, m := range active.Models {
		m.Archived = i > 0
		archived.Models = append(archived.Models, m)
	}
	if reason, _ := validateLLMRegistry(archived); reason != "" {
		t.Fatalf("reason = %q, want archived revisions exempt from the active limit", reason)
	}

	history := &aiv1alpha1.StudioLLM{}
	for i := range llmRegistryMaxStoredRevisions + 1 {
		m := model("a", fmt.Sprintf("rev-%d", i), "A")
		m.Archived = i > 0
		history.Models = append(history.Models, m)
	}
	if reason, _ := validateLLMRegistry(history); reason != "RevisionHistoryFull" {
		t.Fatalf("reason = %q, want RevisionHistoryFull", reason)
	}
}

func studioWith(registry *aiv1alpha1.StudioLLM) *aiv1alpha1.Studio {
	return &aiv1alpha1.Studio{
		ObjectMeta: metav1.ObjectMeta{Name: "studio", Namespace: "default"},
		Spec:       aiv1alpha1.StudioSpec{LLM: registry},
	}
}

func credentialSecret(modelID, key string) *corev1.Secret {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: LLMCredentialSecretName(modelID), Namespace: "default",
	}}
	if key != "" {
		secret.Data = map[string][]byte{LLMCredentialSecretKey: []byte(key)}
	}
	return secret
}

func checkWith(t *testing.T, st *aiv1alpha1.Studio, objects ...runtime.Object) llmRegistryResult {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(appscheme.NewScheme()).WithRuntimeObjects(objects...).Build()
	return (&Reconciler{}).checkLLMRegistry(context.Background(), c, st)
}

// status.models is the whole point of the split: a client learns a model is
// usable without ever reading a Secret.
func TestCheckLLMRegistryPublishesConfiguredWithoutExposingKeys(t *testing.T) {
	st := studioWith(&aiv1alpha1.StudioLLM{
		DefaultModel: "gpt",
		Models:       []aiv1alpha1.StudioLLMModel{model("gpt", "r1", "GPT"), model("claude", "r2", "Claude")},
	})
	got := checkWith(t, st, credentialSecret("gpt", "sk-secret"), credentialSecret("claude", "sk-other"))

	if got.condition.Status != metav1.ConditionTrue || got.condition.Reason != "Valid" {
		t.Fatalf("condition = %+v, want True/Valid", got.condition)
	}
	if len(got.models) != 2 {
		t.Fatalf("status models = %+v, want two", got.models)
	}
	for _, m := range got.models {
		if !m.Configured {
			t.Fatalf("model %q reported unconfigured despite a populated Secret", m.ID)
		}
	}
	// Nothing derived from the key may appear anywhere in the published
	// status — that is the property the old blob Secret could not offer.
	if strings.Contains(fmt.Sprintf("%+v", got), "sk-secret") {
		t.Fatal("key material leaked into the reported status")
	}
}

func TestCheckLLMRegistryReportsCredentialState(t *testing.T) {
	registry := &aiv1alpha1.StudioLLM{Models: []aiv1alpha1.StudioLLMModel{model("gpt", "r1", "GPT")}}

	missing := checkWith(t, studioWith(registry))
	if missing.condition.Reason != "SecretMissing" {
		t.Fatalf("reason = %q, want SecretMissing", missing.condition.Reason)
	}
	if len(missing.models) != 1 || missing.models[0].Configured {
		t.Fatalf("status models = %+v, want gpt reported unconfigured", missing.models)
	}

	empty := checkWith(t, studioWith(registry), credentialSecret("gpt", ""))
	if empty.condition.Reason != "SecretIncomplete" {
		t.Fatalf("reason = %q, want SecretIncomplete", empty.condition.Reason)
	}

	// A model still being filled in has no secretRef at all. That is an
	// unfinished edit, not a dangling pointer, so it must not raise a
	// workspace-level fault.
	unreferenced := *registry
	unreferenced.Models = []aiv1alpha1.StudioLLMModel{{ID: "gpt", RevisionID: "r1", Name: "GPT", Model: "gpt-5"}}
	got := checkWith(t, studioWith(&unreferenced))
	if got.condition.Status != metav1.ConditionTrue {
		t.Fatalf("condition = %+v, want a half-configured model to stay non-faulting", got.condition)
	}
	if len(got.models) != 1 || got.models[0].Configured {
		t.Fatalf("status models = %+v, want gpt present but unconfigured", got.models)
	}
}

func TestCheckLLMRegistryTreatsAnEmptyRegistryAsUnknown(t *testing.T) {
	for _, registry := range []*aiv1alpha1.StudioLLM{nil, {}} {
		got := checkWith(t, studioWith(registry))
		if got.condition.Status != metav1.ConditionUnknown || got.condition.Reason != "NotConfigured" {
			t.Fatalf("condition = %+v, want Unknown/NotConfigured", got.condition)
		}
	}
}
