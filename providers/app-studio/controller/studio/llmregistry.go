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

// The workspace's model registry is spec.llm on the Studio, and each model's
// credential is its own Secret. Two jobs live here.
//
// First, the rules the CRD cannot state: uniqueness across a list, a default
// that has to name a member of that list, a bound on how many entries may be
// active rather than present. The schema catches the shape of one entry; these
// catch the shape of the set.
//
// Second, and the reason the registry was worth splitting at all: whether each
// model is actually usable. A client can read spec.llm freely — it holds no
// credentials — but that tells it a Secret was NAMED, not that one exists. The
// reconciler looks, and publishes one boolean per model in status.models. So
// the portal renders "configured" without ever fetching key material, which is
// what the single-blob Secret made impossible.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

const (
	// LLMCredentialSecretKey is the only entry a model credential Secret has.
	LLMCredentialSecretKey = "apiKey"

	// llmRegistryMaxModels bounds ACTIVE configurations — what a person picks
	// between. Archived revisions are exempt; that is what archiving is for.
	llmRegistryMaxModels = 20
	// llmRegistryMaxStoredRevisions bounds the whole list, active plus
	// history. The CRD carries the same number as MaxItems; this repeats it
	// so the reported reason is about the registry rather than a schema error.
	llmRegistryMaxStoredRevisions = 200
)

// LLMCredentialSecretName is the Secret holding one model's credential. It is
// derived, not chosen, so a client that creates a model and a client that
// reads one agree without coordinating — and so a model ID, which the CRD
// constrains to a DNS label, can never produce a name that is not one.
func LLMCredentialSecretName(modelID string) string {
	return "railgrid-projects-llm-" + strings.TrimSpace(modelID)
}

// llmRegistryResult is what one pass over the registry learned.
type llmRegistryResult struct {
	condition metav1.Condition
	models    []aiv1alpha1.StudioLLMModelStatus
}

// checkLLMRegistry validates spec.llm and resolves each active model's
// credential to a boolean.
func (r *Reconciler) checkLLMRegistry(ctx context.Context, c client.Client, st *aiv1alpha1.Studio) llmRegistryResult {
	condition := func(status metav1.ConditionStatus, reason, message string) metav1.Condition {
		return metav1.Condition{
			Type:    aiv1alpha1.StudioConditionLLMRegistryValid,
			Status:  status,
			Reason:  reason,
			Message: message,
		}
	}

	registry := st.Spec.LLM
	if registry == nil || len(registry.Models) == 0 {
		// A workspace that has configured no model is not a broken one.
		return llmRegistryResult{condition: condition(metav1.ConditionUnknown, "NotConfigured",
			"No LLM models are configured in this workspace yet.")}
	}

	if reason, message := validateLLMRegistry(registry); reason != "" {
		return llmRegistryResult{condition: condition(metav1.ConditionFalse, reason, message)}
	}

	// Structure is sound; now ask whether each active model can actually run.
	models := make([]aiv1alpha1.StudioLLMModelStatus, 0, len(registry.Models))
	seen := map[string]struct{}{}
	var missing, incomplete []string
	for _, model := range registry.Models {
		if model.Archived {
			continue
		}
		if _, dup := seen[model.ID]; dup {
			continue
		}
		seen[model.ID] = struct{}{}

		configured, state := r.credentialState(ctx, c, st.Namespace, model)
		switch state {
		case credentialMissing:
			missing = append(missing, model.ID)
		case credentialEmpty:
			incomplete = append(incomplete, model.ID)
		}
		models = append(models, aiv1alpha1.StudioLLMModelStatus{ID: model.ID, Configured: configured})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	switch {
	case len(missing) > 0:
		return llmRegistryResult{models: models, condition: condition(metav1.ConditionFalse, "SecretMissing",
			fmt.Sprintf("These models name a credential Secret that does not exist: %s.", strings.Join(missing, ", ")))}
	case len(incomplete) > 0:
		return llmRegistryResult{models: models, condition: condition(metav1.ConditionFalse, "SecretIncomplete",
			fmt.Sprintf("These models have a credential Secret with no %q entry: %s.", LLMCredentialSecretKey, strings.Join(incomplete, ", ")))}
	}
	return llmRegistryResult{models: models, condition: condition(metav1.ConditionTrue, "Valid",
		"Every configured model has a credential.")}
}

type credentialState int

const (
	credentialPresent credentialState = iota
	credentialUnreferenced
	credentialMissing
	credentialEmpty
)

// credentialState reports whether one model's credential is usable. A model
// with no secretRef at all is "unreferenced" rather than missing: it is a
// half-finished configuration, not a dangling pointer, and saying so would
// turn an in-progress edit into a workspace-level fault.
func (r *Reconciler) credentialState(ctx context.Context, c client.Client, namespace string, model aiv1alpha1.StudioLLMModel) (bool, credentialState) {
	if model.SecretRef == nil || strings.TrimSpace(model.SecretRef.Name) == "" {
		return false, credentialUnreferenced
	}
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: namespace, Name: model.SecretRef.Name}
	if key.Namespace == "" {
		key.Namespace = "default"
	}
	if err := c.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, credentialMissing
		}
		// Unreadable is not absent. Reporting "missing" on an RBAC or
		// transport error would tell the workspace to recreate a Secret that
		// is already there.
		return false, credentialUnreferenced
	}
	if len(strings.TrimSpace(string(secret.Data[LLMCredentialSecretKey]))) == 0 {
		return false, credentialEmpty
	}
	return true, credentialPresent
}

// validateLLMRegistry returns the first structural rule the registry breaks,
// as (reason, message), or ("", "") when it breaks none. First-failure-wins
// keeps the condition message about one actionable thing.
func validateLLMRegistry(registry *aiv1alpha1.StudioLLM) (string, string) {
	if len(registry.Models) > llmRegistryMaxStoredRevisions {
		return "RevisionHistoryFull", fmt.Sprintf(
			"The registry holds %d model revisions; at most %d are kept. Delete some archived revisions.",
			len(registry.Models), llmRegistryMaxStoredRevisions)
	}

	revisions := map[string]struct{}{}
	activeIDs := map[string]struct{}{}
	active := 0
	for _, model := range registry.Models {
		// Per-field shape (required, length, DNS label) is the CRD's job and
		// is already enforced on write. What is left is the set.
		revision := strings.TrimSpace(model.RevisionID)
		if _, seen := revisions[revision]; seen {
			return "DuplicateRevision", fmt.Sprintf("Revision %q appears more than once; model revisions must be unique.", revision)
		}
		revisions[revision] = struct{}{}
		if model.Archived {
			continue
		}
		active++
		if _, seen := activeIDs[model.ID]; seen {
			return "DuplicateModelID", fmt.Sprintf("Two active model configurations share the ID %q; only archived revisions may repeat one.", model.ID)
		}
		activeIDs[model.ID] = struct{}{}
	}
	if active > llmRegistryMaxModels {
		return "TooManyModels", fmt.Sprintf(
			"The registry has %d active model configurations; at most %d are supported.", active, llmRegistryMaxModels)
	}

	// A default that names nothing active leaves the assistant with no model
	// to pick even though models exist — the confusing failure this prevents.
	if defaultID := strings.TrimSpace(registry.DefaultModel); defaultID != "" {
		if _, ok := activeIDs[defaultID]; !ok {
			return "DefaultModelMissing", fmt.Sprintf(
				"The default model %q is not an active model configuration; pick a different default.", defaultID)
		}
	}
	return "", ""
}
