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

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tenant"
)

func TestRepeatedModelDeletePreservesReferencedSecrets(t *testing.T) {
	tests := []struct {
		name              string
		connectionBinding bool
		wantDeleteRV      []string
	}{
		{
			name:              "connection binding keeps its shared key",
			connectionBinding: true,
			wantDeleteRV:      []string{"model-rv"},
		},
		{
			name:         "direct legacy credential waits for controller",
			wantDeleteRV: []string{"protected-rv", "terminating-rv"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := directCredential("account", "model", "model-rv", "shared-key")
			secret := legacyOwnedSecret("shared-key", "account", "model")
			objects := []runtime.Object{model, secret}
			secretName := secret.Name
			if tt.connectionBinding {
				model.Spec = agentsv1alpha1.ModelCredentialSpec{ConnectionRef: "account", Model: "one"}
				secret.Name = CredentialSecretName("account")
				secret.OwnerReferences = []metav1.OwnerReference{{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection", Name: "account", UID: "connection"}}
				secretName = secret.Name
				objects = append(objects, &agentsv1alpha1.ModelProviderConnection{
					ObjectMeta: metav1.ObjectMeta{Name: "account", UID: "connection", ResourceVersion: "connection-rv", Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}},
					Spec:       agentsv1alpha1.ModelProviderConnectionSpec{BaseURL: "https://provider.example/v1", SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: secretName}},
				})
			}
			c, d := newCredentialDeleteClient(t, objects...)
			if !tt.connectionBinding {
				installFinalizerAwareDeleteReactor(d)
			}

			for range 2 {
				if err := deleteCredential(t.Context(), c, model.Name); err != nil {
					t.Fatal(err)
				}
			}
			if got := getSecretFromTracker(t, d, secretName); got == nil {
				t.Fatal("repeated model deletion removed a referenced Secret")
			}
			assertNoSecretActions(t, d)
			deletes := modelCredentialDeleteActions(d)
			if len(deletes) != len(tt.wantDeleteRV) {
				t.Fatalf("ModelCredential DELETE actions = %d, want %d", len(deletes), len(tt.wantDeleteRV))
			}
			for i, resourceVersion := range tt.wantDeleteRV {
				assertDeletePreconditions(t, deletes[i], string(model.UID), resourceVersion)
			}

			if !tt.connectionBinding {
				stored := getModelCredentialFromTracker(t, d, model.Name)
				if stored.DeletionTimestamp == nil || !slices.Contains(stored.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer) {
					t.Fatalf("repeated delete lost pending protection finalizer: %+v", stored.ObjectMeta)
				}
				if err := d.Tracker().Delete(agentsclient.ModelCredentialGVR, "", model.Name); err != nil {
					t.Fatal(err)
				}
				if err := deleteCredential(t.Context(), c, model.Name); err != nil {
					t.Fatalf("delete after controller completed finalization: %v", err)
				}
			}
		})
	}
}

func TestLegacyModelDeleteProtectsSecretUntilControllerFinalizes(t *testing.T) {
	tests := []struct {
		name               string
		connectionName     string
		migratedCredential string
	}{
		{
			name:           "connection references key held only by legacy model owner",
			connectionName: "new-account",
		},
		{
			name:               "raw legacy name collision during migration",
			connectionName:     "account",
			migratedCredential: "legacy-uid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := directCredential("account", "legacy-uid", "model-rv", "shared-key")
			secret := legacyOwnedSecret("shared-key", "account", "legacy-uid")
			connection := &agentsv1alpha1.ModelProviderConnection{
				ObjectMeta: metav1.ObjectMeta{Name: tt.connectionName, UID: "connection-uid", ResourceVersion: "connection-rv"},
				Spec:       agentsv1alpha1.ModelProviderConnectionSpec{SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: secret.Name}},
			}
			if tt.migratedCredential != "" {
				connection.Annotations = map[string]string{"agents.railgrid.ai/migrated-model-credential-uid": tt.migratedCredential}
			}
			c, d := newCredentialDeleteClient(t, model, connection, secret)
			installFinalizerAwareDeleteReactor(d)

			if err := deleteCredential(t.Context(), c, model.Name); err != nil {
				t.Fatal(err)
			}

			stored := getModelCredentialFromTracker(t, d, model.Name)
			if !slices.Contains(stored.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer) {
				t.Fatalf("model finalizers = %v, want controller protection", stored.Finalizers)
			}
			if stored.DeletionTimestamp == nil {
				t.Fatal("model was not left pending on its protection finalizer")
			}
			got := getSecretFromTracker(t, d, secret.Name)
			if got == nil {
				t.Fatal("API deletion removed a Secret before the provider controller handled ownership")
			}
			if len(got.OwnerReferences) != len(secret.OwnerReferences) || got.OwnerReferences[0].UID != secret.OwnerReferences[0].UID {
				t.Fatalf("API deletion changed Secret owners: got %v, want %v", got.OwnerReferences, secret.OwnerReferences)
			}
			if string(got.Data["apiKey"]) != "test-only" {
				t.Fatal("API deletion changed the shared key data")
			}
			assertNoSecretActions(t, d)

			updated := assertSingleCredentialUpdate(t, d, model)
			if !slices.Contains(updated.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer) {
				t.Fatalf("finalizer update wrote %v", updated.Finalizers)
			}
			deletes := modelCredentialDeleteActions(d)
			if len(deletes) != 1 {
				t.Fatalf("ModelCredential DELETE actions = %d, want one", len(deletes))
			}
			assertDeletePreconditions(t, deletes[0], string(model.UID), "protected-rv")
			updateIndex, deleteIndex := -1, -1
			for index, action := range d.Actions() {
				if action.GetResource() != agentsclient.ModelCredentialGVR {
					continue
				}
				if action.GetVerb() == "update" && updateIndex == -1 {
					updateIndex = index
				}
				if action.GetVerb() == "delete" && deleteIndex == -1 {
					deleteIndex = index
				}
			}
			if updateIndex < 0 || deleteIndex < updateIndex {
				t.Fatalf("finalizer UPDATE action index %d must precede DELETE index %d", updateIndex, deleteIndex)
			}
		})
	}
}

func TestLegacyModelDeleteStopsOnResourceVersionConflictInstallingProtection(t *testing.T) {
	model, secret, c, d := newLegacyDeleteFixture(t, "legacy")
	conflict := apierrors.NewConflict(agentsv1alpha1.Resource("modelcredentials"), model.Name, errors.New("injected concurrent edit"))
	d.PrependReactor("update", "modelcredentials", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, conflict
	})

	err := deleteCredential(t.Context(), c, model.Name)
	if !apierrors.IsConflict(err) {
		t.Fatalf("delete error = %v, want resourceVersion conflict", err)
	}
	assertSingleCredentialUpdate(t, d, model)
	if deletes := modelCredentialDeleteActions(d); len(deletes) != 0 {
		t.Fatalf("DELETE actions = %d after finalizer conflict, want none", len(deletes))
	}
	if getSecretFromTracker(t, d, secret.Name) == nil {
		t.Fatal("resourceVersion conflict removed the Secret")
	}
	assertNoSecretActions(t, d)
}

func TestLegacyModelDeleteStopsIfAdmissionRemovesProtectionFinalizer(t *testing.T) {
	model, _, c, d := newLegacyDeleteFixture(t, "legacy")
	d.PrependReactor("update", "modelcredentials", func(action ktesting.Action) (bool, runtime.Object, error) {
		updated := action.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		updated.SetFinalizers(nil)
		updated.SetResourceVersion("admitted-rv")
		return true, updated, nil
	})

	err := deleteCredential(t.Context(), c, model.Name)
	if err == nil {
		t.Fatal("delete succeeded after the API returned an object without the protection finalizer")
	}
	if deletes := modelCredentialDeleteActions(d); len(deletes) != 0 {
		t.Fatalf("DELETE actions = %d after finalizer was removed, want none", len(deletes))
	}
	assertNoSecretActions(t, d)
}

func TestExistingCredentialUpsertUsesReadUIDAndResourceVersion(t *testing.T) {
	model := directCredential("legacy", "legacy-uid", "model-rv", "shared-key")
	model.Labels = map[string]string{"tenant.example/label": "keep"}
	model.Annotations = map[string]string{"tenant.example/annotation": "keep"}
	model.Finalizers = []string{"tenant.example/protect"}
	model.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Secret", Name: "marker", UID: "marker-uid"}}
	secret := legacyOwnedSecret("shared-key", "legacy", "legacy-uid")
	c, d := newCredentialDeleteClient(t, model, secret)
	conflict := apierrors.NewConflict(agentsv1alpha1.Resource("modelcredentials"), model.Name, errors.New("injected concurrent edit"))
	d.PrependReactor("update", "modelcredentials", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, conflict
	})

	_, err := applyCredentialUpsert(t.Context(), c, &modelCredential{Name: model.Name, Model: "model-two", APIKey: "replacement-key"})
	if !apierrors.IsConflict(err) {
		t.Fatalf("upsert error = %v, want resourceVersion conflict", err)
	}
	updated := assertSingleCredentialUpdate(t, d, model)
	if updated.Labels["tenant.example/label"] != "keep" || updated.Annotations["tenant.example/annotation"] != "keep" {
		t.Fatalf("upsert did not preserve labels and annotations: labels=%v annotations=%v", updated.Labels, updated.Annotations)
	}
	if len(updated.OwnerReferences) != 1 || updated.OwnerReferences[0].UID != "marker-uid" {
		t.Fatalf("upsert did not preserve owner references: %v", updated.OwnerReferences)
	}
	if !slices.Contains(updated.Finalizers, "tenant.example/protect") || !slices.Contains(updated.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer) {
		t.Fatalf("upsert finalizers = %v, want both existing and credential protection finalizers", updated.Finalizers)
	}
	if deletes := modelCredentialDeleteActions(d); len(deletes) != 0 {
		t.Fatalf("unexpected ModelCredential DELETE actions = %d", len(deletes))
	}
}

func TestNewModelCredentialIsCreatedBeforeItsOwnedImmutableKey(t *testing.T) {
	client, dynamic := newCredentialDeleteClient(t)
	var createdCredential *unstructured.Unstructured
	dynamic.PrependReactor("create", "modelcredentials", func(action ktesting.Action) (bool, runtime.Object, error) {
		createdCredential = action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		createdCredential.SetUID("new-model-uid")
		createdCredential.SetResourceVersion("1")
		if err := dynamic.Tracker().Create(agentsclient.ModelCredentialGVR, createdCredential.DeepCopy(), ""); err != nil {
			return true, nil, err
		}
		return true, createdCredential.DeepCopy(), nil
	})
	dynamic.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		created := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		created.SetUID("generated-secret-uid")
		created.SetResourceVersion("1")
		if err := dynamic.Tracker().Create(agentsclient.SecretGVR, created.DeepCopy(), "default"); err != nil {
			return true, nil, err
		}
		return true, created, nil
	})
	var activationOps []struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}
	dynamic.PrependReactor("patch", "modelcredentials", func(action ktesting.Action) (bool, runtime.Object, error) {
		if err := json.Unmarshal(action.(ktesting.PatchAction).GetPatch(), &activationOps); err != nil {
			return true, nil, err
		}
		active := createdCredential.DeepCopy()
		annotations := active.GetAnnotations()
		pendingName := createdCredential.GetAnnotations()[agentsv1alpha1.PendingKeySecretAnnotation]
		id := strings.TrimPrefix(pendingName, "railgrid-agents-model-")
		delete(annotations, agentsv1alpha1.PendingKeySecretAnnotation)
		annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] = id
		active.SetAnnotations(annotations)
		return true, active, nil
	})

	if _, err := applyCredentialUpsert(t.Context(), client, &modelCredential{
		Name: "new-model", Model: "gpt-5", APIKey: "test-key",
	}); err != nil {
		t.Fatal(err)
	}

	if createdCredential == nil {
		t.Fatal("ModelCredential was not created")
	}
	if !slices.Contains(createdCredential.GetFinalizers(), agentsv1alpha1.ModelCredentialSecretProtectionFinalizer) {
		t.Fatal("new ModelCredential did not carry its protection finalizer")
	}
	pendingName := createdCredential.GetAnnotations()[agentsv1alpha1.PendingKeySecretAnnotation]
	if pendingName == "" || !strings.HasPrefix(pendingName, "railgrid-agents-model-") {
		t.Fatalf("new ModelCredential pending key = %q, want a generated Secret name", pendingName)
	}
	if strings.HasSuffix(pendingName, "new-model") {
		t.Fatalf("generated Secret name %q is deterministic from the credential name", pendingName)
	}
	secret := getSecretFromTracker(t, dynamic, pendingName)
	if secret == nil {
		t.Fatal("generated Secret was not created")
	}
	if secret.Immutable == nil || !*secret.Immutable {
		t.Fatal("generated key Secret is not immutable")
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Kind != "ModelCredential" || secret.OwnerReferences[0].Name != "new-model" || secret.OwnerReferences[0].UID != "new-model-uid" {
		t.Fatalf("generated Secret owner references = %+v", secret.OwnerReferences)
	}
	id := strings.TrimPrefix(pendingName, "railgrid-agents-model-")
	if secret.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != id || len(id) != 32 {
		t.Fatalf("generated Secret staged identity = %q, want 32-hex token %q", secret.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation], id)
	}
	var createModelIndex, createSecretIndex, activateIndex = -1, -1, -1
	for i, action := range dynamic.Actions() {
		switch {
		case action.GetVerb() == "create" && action.GetResource() == agentsclient.ModelCredentialGVR:
			createModelIndex = i
		case action.GetVerb() == "create" && action.GetResource() == agentsclient.SecretGVR:
			createSecretIndex = i
		case action.GetVerb() == "patch" && action.GetResource() == agentsclient.ModelCredentialGVR:
			activateIndex = i
		}
	}
	if createModelIndex < 0 || createSecretIndex <= createModelIndex || activateIndex <= createSecretIndex {
		t.Fatalf("write order indexes: ModelCredential=%d Secret=%d activation=%d; want parent, Secret, activation", createModelIndex, createSecretIndex, activateIndex)
	}
	if len(activationOps) < 7 || activationOps[0].Op != "test" || activationOps[0].Path != "/metadata/uid" ||
		activationOps[1].Op != "test" || activationOps[1].Path != "/metadata/resourceVersion" ||
		activationOps[2].Op != "test" || !strings.Contains(activationOps[2].Path, "pending-key-secret") ||
		activationOps[3].Op != "test" || activationOps[3].Path != "/spec" {
		t.Fatalf("activation patch does not guard UID, live resourceVersion, reservation, and original spec: %+v", activationOps)
	}
}

func TestModelCredentialEditStagesNewOwnedKeyWithoutChangingCustomSecret(t *testing.T) {
	model := directCredential("custom-model", "custom-model-uid", "model-rv", "tenant-key")
	oldSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tenant-key", Namespace: "default", UID: "tenant-key-uid", ResourceVersion: "1"}, Data: map[string][]byte{"apiKey": []byte("preserved-key")}}
	client, dynamic := newCredentialDeleteClient(t, model, oldSecret)
	dynamic.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		created := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		created.SetUID("rotated-secret-uid")
		created.SetResourceVersion("1")
		if err := dynamic.Tracker().Create(agentsclient.SecretGVR, created.DeepCopy(), "default"); err != nil {
			return true, nil, err
		}
		return true, created, nil
	})
	var activationOps []map[string]any
	dynamic.PrependReactor("patch", "modelcredentials", func(action ktesting.Action) (bool, runtime.Object, error) {
		if err := json.Unmarshal(action.(ktesting.PatchAction).GetPatch(), &activationOps); err != nil {
			return true, nil, err
		}
		stored, err := dynamic.Tracker().Get(agentsclient.ModelCredentialGVR, "", model.Name)
		if err != nil {
			return true, nil, err
		}
		active, err := asUnstructured(stored)
		if err != nil {
			return true, nil, err
		}
		pending := active.GetAnnotations()[agentsv1alpha1.PendingKeySecretAnnotation]
		id := strings.TrimPrefix(pending, "railgrid-agents-model-")
		annotations := active.GetAnnotations()
		delete(annotations, agentsv1alpha1.PendingKeySecretAnnotation)
		annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] = id
		active.SetAnnotations(annotations)
		for _, operation := range activationOps {
			if operation["op"] == "replace" && operation["path"] == "/spec" {
				if err := unstructured.SetNestedMap(active.Object, operation["value"].(map[string]any), "spec"); err != nil {
					return true, nil, err
				}
			}
		}
		active.SetResourceVersion("activated-rv")
		if err := dynamic.Tracker().Update(agentsclient.ModelCredentialGVR, active.DeepCopy(), ""); err != nil {
			return true, nil, err
		}
		return true, active, nil
	})

	if _, err := applyCredentialUpsert(t.Context(), client, &modelCredential{
		Name: model.Name, Model: "model-two",
	}); err != nil {
		t.Fatal(err)
	}
	current := getModelCredentialFromTracker(t, dynamic, model.Name)
	if current.Spec.Model != "model-two" || current.Spec.SecretRef.Name == oldSecret.Name {
		t.Fatalf("edit did not activate the new key with its model update: spec=%+v", current.Spec)
	}
	id := strings.TrimPrefix(current.Spec.SecretRef.Name, "railgrid-agents-model-")
	if len(id) != 32 || current.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != id || current.Annotations[agentsv1alpha1.PendingKeySecretAnnotation] != "" {
		t.Fatalf("activated parent key identity is inconsistent: annotations=%v ref=%q", current.Annotations, current.Spec.SecretRef.Name)
	}
	newSecret := getSecretFromTracker(t, dynamic, current.Spec.SecretRef.Name)
	if newSecret == nil || newSecret.Immutable == nil || !*newSecret.Immutable || string(newSecret.Data[agentsv1alpha1.DefaultModelSecretKey]) != "preserved-key" {
		t.Fatalf("rotated Secret did not preserve the old key immutably: %+v", newSecret)
	}
	if len(newSecret.OwnerReferences) != 1 || newSecret.OwnerReferences[0].Kind != "ModelCredential" || newSecret.OwnerReferences[0].UID != model.UID {
		t.Fatalf("rotated key owner references = %+v", newSecret.OwnerReferences)
	}
	old := getSecretFromTracker(t, dynamic, oldSecret.Name)
	if old == nil || string(old.Data[agentsv1alpha1.DefaultModelSecretKey]) != "preserved-key" || len(old.OwnerReferences) != 0 {
		t.Fatal("rotating a custom Secret changed or adopted the tenant's original key")
	}
	if len(activationOps) < 7 {
		t.Fatalf("activation did not use the guarded JSON Patch: %+v", activationOps)
	}
}

func newLegacyDeleteFixture(t *testing.T, name string) (*agentsv1alpha1.ModelCredential, *corev1.Secret, *agentsclient.Client, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	model := directCredential(name, "legacy-uid", "model-rv", "shared-key")
	secret := legacyOwnedSecret("shared-key", name, "legacy-uid")
	client, dynamic := newCredentialDeleteClient(t, model, secret)
	return model, secret, client, dynamic
}

func newCredentialDeleteClient(t *testing.T, objects ...runtime.Object) (*agentsclient.Client, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := agentsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	d := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		agentsclient.ModelCredentialGVR:         "ModelCredentialList",
		agentsclient.ModelProviderConnectionGVR: "ModelProviderConnectionList",
	}, objects...)
	d.PrependReactor("update", "modelcredentials", func(action ktesting.Action) (bool, runtime.Object, error) {
		updated := action.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		updated.SetResourceVersion("protected-rv")
		typed, err := asModelCredential(updated)
		if err != nil {
			return true, nil, err
		}
		if err := d.Tracker().Update(agentsclient.ModelCredentialGVR, typed, ""); err != nil {
			return true, nil, err
		}
		return true, updated, nil
	})
	return agentsclient.NewFromScope(tenant.NewScopeFromDynamic("tenant", d)), d
}

func installFinalizerAwareDeleteReactor(d *dynamicfake.FakeDynamicClient) {
	d.PrependReactor("delete", "modelcredentials", func(action ktesting.Action) (bool, runtime.Object, error) {
		stored, err := d.Tracker().Get(agentsclient.ModelCredentialGVR, "", action.(ktesting.DeleteAction).GetName())
		if err != nil {
			return true, nil, err
		}
		object, err := asUnstructured(stored)
		if err != nil {
			return true, nil, err
		}
		if !slices.Contains(object.GetFinalizers(), agentsv1alpha1.ModelCredentialSecretProtectionFinalizer) {
			return false, nil, nil
		}
		if object.GetDeletionTimestamp() == nil {
			now := metav1.NewTime(time.Unix(1, 0))
			object.SetDeletionTimestamp(&now)
			object.SetResourceVersion("terminating-rv")
			typed, err := asModelCredential(object)
			if err != nil {
				return true, nil, err
			}
			if err := d.Tracker().Update(agentsclient.ModelCredentialGVR, typed, ""); err != nil {
				return true, nil, err
			}
		}
		return true, nil, nil
	})
}

func asUnstructured(object runtime.Object) (*unstructured.Unstructured, error) {
	if u, ok := object.(*unstructured.Unstructured); ok {
		return u.DeepCopy(), nil
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: content}, nil
}

func asModelCredential(object runtime.Object) (*agentsv1alpha1.ModelCredential, error) {
	if credential, ok := object.(*agentsv1alpha1.ModelCredential); ok {
		return credential.DeepCopy(), nil
	}
	u, err := asUnstructured(object)
	if err != nil {
		return nil, err
	}
	var credential agentsv1alpha1.ModelCredential
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &credential); err != nil {
		return nil, err
	}
	return &credential, nil
}

func directCredential(name, uid, resourceVersion, secretName string) *agentsv1alpha1.ModelCredential {
	return &agentsv1alpha1.ModelCredential{
		TypeMeta:   metav1.TypeMeta{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential"},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid), ResourceVersion: resourceVersion},
		Spec: agentsv1alpha1.ModelCredentialSpec{
			Provider:  agentsv1alpha1.ModelProviderOpenAICompatible,
			BaseURL:   "https://provider.example/v1",
			Model:     "model-one",
			SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: secretName},
		},
	}
}

func legacyOwnedSecret(name, ownerName, ownerUID string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default", UID: types.UID(name + "-uid"), ResourceVersion: "secret-rv",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelCredential", Name: ownerName, UID: types.UID(ownerUID)}},
		},
		Data: map[string][]byte{"apiKey": []byte("test-only")},
	}
}

func getModelCredentialFromTracker(t *testing.T, d *dynamicfake.FakeDynamicClient, name string) *agentsv1alpha1.ModelCredential {
	t.Helper()
	object, err := d.Tracker().Get(agentsclient.ModelCredentialGVR, "", name)
	if err != nil {
		t.Fatalf("get ModelCredential from tracker: %v", err)
	}
	credential, err := asModelCredential(object)
	if err != nil {
		t.Fatalf("convert ModelCredential from tracker: %v", err)
	}
	return credential
}

func getSecretFromTracker(t *testing.T, d *dynamicfake.FakeDynamicClient, name string) *corev1.Secret {
	t.Helper()
	object, err := d.Tracker().Get(agentsclient.SecretGVR, "default", name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("get Secret from tracker: %v", err)
	}
	if secret, ok := object.(*corev1.Secret); ok {
		return secret.DeepCopy()
	}
	u, err := asUnstructured(object)
	if err != nil {
		t.Fatalf("convert Secret to unstructured: %v", err)
	}
	var secret corev1.Secret
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &secret); err != nil {
		t.Fatalf("convert Secret from tracker: %v", err)
	}
	return &secret
}

func assertNoSecretActions(t *testing.T, d *dynamicfake.FakeDynamicClient) {
	t.Helper()
	for _, action := range d.Actions() {
		if action.GetResource() == agentsclient.SecretGVR {
			t.Fatalf("API deletion made privileged Secret request: %s %s", action.GetVerb(), action.GetResource())
		}
	}
}

func assertSingleCredentialUpdate(t *testing.T, d *dynamicfake.FakeDynamicClient, want *agentsv1alpha1.ModelCredential) *agentsv1alpha1.ModelCredential {
	t.Helper()
	var update ktesting.UpdateAction
	count := 0
	for _, action := range d.Actions() {
		if action.GetResource() == agentsclient.ModelCredentialGVR && action.GetVerb() == "update" {
			update = action.(ktesting.UpdateAction)
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ModelCredential UPDATE actions = %d, want one", count)
	}
	updated, err := asModelCredential(update.GetObject())
	if err != nil {
		t.Fatal(err)
	}
	if updated.UID != want.UID || updated.ResourceVersion != want.ResourceVersion {
		t.Fatalf("ModelCredential UPDATE preconditions = UID %q RV %q, want UID %q RV %q", updated.UID, updated.ResourceVersion, want.UID, want.ResourceVersion)
	}
	return updated
}

func modelCredentialDeleteActions(d *dynamicfake.FakeDynamicClient) []ktesting.DeleteAction {
	var out []ktesting.DeleteAction
	for _, action := range d.Actions() {
		if action.GetResource() == agentsclient.ModelCredentialGVR && action.GetVerb() == "delete" {
			out = append(out, action.(ktesting.DeleteAction))
		}
	}
	return out
}

func assertDeletePreconditions(t *testing.T, action ktesting.DeleteAction, uid, resourceVersion string) {
	t.Helper()
	options := action.GetDeleteOptions()
	if options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil {
		t.Fatal("DELETE lost UID/resourceVersion preconditions")
	}
	if string(*options.Preconditions.UID) != uid || *options.Preconditions.ResourceVersion != resourceVersion {
		t.Fatalf("DELETE preconditions = UID %q RV %q, want UID %q RV %q", *options.Preconditions.UID, *options.Preconditions.ResourceVersion, uid, resourceVersion)
	}
}
