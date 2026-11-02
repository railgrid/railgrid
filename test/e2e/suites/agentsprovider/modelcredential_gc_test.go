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

package agentsprovider

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	gcModelCredentialGVR = schema.GroupVersionResource{
		Group: agentsGroup, Version: "v1alpha1", Resource: "modelcredentials",
	}
	gcModelProviderConnectionGVR = schema.GroupVersionResource{
		Group: agentsGroup, Version: "v1alpha1", Resource: "modelproviderconnections",
	}
)

const (
	gcModelCredentialFinalizer    = "agents.railgrid.ai/model-credential-secret-protection"
	gcModelCredentialSecretPrefix = "railgrid-agents-model-"
	gcProbeBaseURL                = "http://127.0.0.1:1/v1"
)

// TestDModelCredentialSecretProtectionGarbageCollection exercises the
// ModelCredential protection finalizer through the tenant API and kcp's real
// garbage collector. Each credential starts in the legacy direct-reference
// shape without a finalizer; the provider must persist protection before the
// test asks Kubernetes to delete it.
func TestDModelCredentialSecretProtectionGarbageCollection(t *testing.T) {
	requireTenant(t)

	t.Run("late generated key is collected for a deleted owner", func(t *testing.T) {
		for _, owner := range []struct {
			name      string
			kind      string
			resource  schema.GroupVersionResource
			finalizer string
			spec      map[string]any
		}{
			{
				name: "gc-late-model-owner", kind: "ModelCredential", resource: gcModelCredentialGVR,
				finalizer: gcModelCredentialFinalizer,
				spec:      map[string]any{"provider": "openai-compatible", "baseURL": gcProbeBaseURL, "model": "gc-test-model"},
			},
			{
				name: "gc-late-connection-owner", kind: "ModelProviderConnection", resource: gcModelProviderConnectionGVR,
				finalizer: "agents.railgrid.ai/model-provider-connection-protection",
				spec:      map[string]any{"provider": "openai-compatible", "baseURL": gcProbeBaseURL},
			},
		} {
			t.Run(owner.kind, func(t *testing.T) {
				id := "0123456789abcdef0123456789abcdef"
				secretName := "railgrid-agents-model-" + id
				owner.spec["secretRef"] = map[string]any{"name": secretName}
				object := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "agents.railgrid.ai/v1alpha1",
					"kind":       owner.kind,
					"metadata": map[string]any{
						"name":       owner.name,
						"finalizers": []any{owner.finalizer},
						"annotations": map[string]any{
							"agents.railgrid.ai/pending-key-secret": secretName,
						},
					},
					"spec": owner.spec,
				}}
				created, err := tenant(t).Resource(owner.resource).Create(ctxWithTimeout(t, 15*time.Second), object, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("create protected %s/%s: %v", owner.kind, owner.name, err)
				}
				if created.GetUID() == "" {
					t.Fatal("created key owner has no UID")
				}
				if err := tenant(t).Resource(owner.resource).Delete(ctxWithTimeout(t, 15*time.Second), owner.name, metav1.DeleteOptions{}); err != nil {
					t.Fatalf("delete protected %s/%s before creating its key: %v", owner.kind, owner.name, err)
				}
				waitGone(t, owner.resource, owner.name, 90*time.Second)

				lateSecret := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Secret",
					"metadata": map[string]any{
						"name":      secretName,
						"namespace": "default",
						"labels":    map[string]any{"railgrid.ai/owner": "agents"},
						"annotations": map[string]any{
							"agents.railgrid.ai/generated-secret-id": id,
						},
						"ownerReferences": []any{map[string]any{
							"apiVersion":         "agents.railgrid.ai/v1alpha1",
							"kind":               owner.kind,
							"name":               owner.name,
							"uid":                string(created.GetUID()),
							"controller":         false,
							"blockOwnerDeletion": false,
						}},
					},
					"type":      "Opaque",
					"immutable": true,
					"data":      map[string]any{"apiKey": credentialGCSecretData("gc-late-key")},
				}}
				if _, err := tenant(t).Resource(secretGVR).Namespace("default").Create(ctxWithTimeout(t, 15*time.Second), lateSecret, metav1.CreateOptions{}); err != nil {
					t.Fatalf("create late key with exact %s UID owner reference: %v", owner.kind, err)
				}
				waitGone(t, secretGVR, secretName, 90*time.Second)
			})
		}
	})

	t.Run("shared legacy key survives raw delete", func(t *testing.T) {
		const modelName = "gc-shared-source"
		secretName := gcCredentialSecretName(modelName)

		// An independently-created same-name connection makes migration refuse
		// this legacy credential, leaving the old ModelCredential as the owner.
		createCredentialMigrationCollision(t, modelName)
		createCredentialGCSecret(t, secretName, "gc-shared-key")
		createLegacyGCCredential(t, modelName, secretName)
		credential, secret := waitForCredentialGCProtection(t, modelName, secretName)
		if len(secret.GetOwnerReferences()) != 1 || !hasCredentialGCSecretOwner(secret, credential) {
			t.Fatalf("fixture Secret owners = %+v; want only ModelCredential/%s", secret.GetOwnerReferences(), modelName)
		}

		// This connection references the provider-created Secret by a custom
		// name. It is intentionally not an owner reference: only the model
		// credential's finalizer can detach its owner ref before kcp GC runs.
		createCredentialGCConnection(t, "gc-shared-consumer", secretName)
		if _, err := tenant(t).Resource(gcModelProviderConnectionGVR).Get(
			ctxWithTimeout(t, 15*time.Second), "gc-shared-consumer", metav1.GetOptions{},
		); err != nil {
			t.Fatalf("get shared-key consumer connection: %v", err)
		}
		assertCredentialGCSecretHasOnlyOwner(t, secretName, credential)

		if err := tenant(t).Resource(gcModelCredentialGVR).Delete(
			ctxWithTimeout(t, 15*time.Second), modelName, metav1.DeleteOptions{},
		); err != nil {
			t.Fatalf("raw DELETE ModelCredential/%s: %v", modelName, err)
		}

		if !waitForCondition(t, 90*time.Second, func() (bool, string) {
			_, modelErr := getObject(t, gcModelCredentialGVR, modelName)
			if !apierrors.IsNotFound(modelErr) {
				return false, fmt.Sprintf("ModelCredential still present: %v", modelErr)
			}
			currentSecret, secretErr := getCredentialGCSecret(t, secretName)
			if secretErr != nil {
				return false, fmt.Sprintf("shared Secret read: %v", secretErr)
			}
			if hasCredentialGCSecretOwner(currentSecret, credential) {
				return false, "shared Secret still has the deleted ModelCredential owner reference"
			}
			return true, ""
		}) {
			t.Fatal("raw delete did not preserve and detach the shared Secret before ModelCredential deletion")
		}

		got, err := getCredentialGCSecret(t, secretName)
		if err != nil {
			t.Fatalf("shared Secret was removed by garbage collection: %v", err)
		}
		apiKey, _, err := unstructured.NestedString(got.Object, "data", "apiKey")
		if err != nil || apiKey != credentialGCSecretData("gc-shared-key") {
			t.Fatal("shared Secret data changed during ModelCredential deletion")
		}
	})

	t.Run("unshared legacy key is cleaned up", func(t *testing.T) {
		const modelName = "gc-unshared-source"
		secretName := gcCredentialSecretName(modelName)

		// Keep this legacy object out of the automatic split migration too, so
		// the deletion is specifically guarded by the ModelCredential finalizer.
		createCredentialMigrationCollision(t, modelName)
		createCredentialGCSecret(t, secretName, "gc-unshared-key")
		createLegacyGCCredential(t, modelName, secretName)
		waitForCredentialGCProtection(t, modelName, secretName)

		if err := tenant(t).Resource(gcModelCredentialGVR).Delete(
			ctxWithTimeout(t, 15*time.Second), modelName, metav1.DeleteOptions{},
		); err != nil {
			t.Fatalf("raw DELETE ModelCredential/%s: %v", modelName, err)
		}

		waitGone(t, gcModelCredentialGVR, modelName, 90*time.Second)
		waitGone(t, secretGVR, secretName, 90*time.Second)
		if _, err := getCredentialGCSecret(t, secretName); !apierrors.IsNotFound(err) {
			t.Fatalf("unshared Secret remains after raw delete and garbage collection: %v", err)
		}
	})
}

func createCredentialMigrationCollision(t *testing.T, modelName string) {
	t.Helper()
	createObject(t, gcModelProviderConnectionGVR, "ModelProviderConnection", modelName, map[string]any{
		"provider":  "openai-compatible",
		"baseURL":   gcProbeBaseURL,
		"secretRef": map[string]any{"name": "gc-missing-" + modelName},
	})
}

func createCredentialGCConnection(t *testing.T, connectionName, secretName string) {
	t.Helper()
	createObject(t, gcModelProviderConnectionGVR, "ModelProviderConnection", connectionName, map[string]any{
		"provider":  "openai-compatible",
		"baseURL":   gcProbeBaseURL,
		"secretRef": map[string]any{"name": secretName},
	})
}

func createCredentialGCSecret(t *testing.T, name, apiKey string) {
	t.Helper()
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "default",
			"labels":    map[string]any{"railgrid.ai/owner": "agents"},
		},
		"type": "Opaque",
		"data": map[string]any{"apiKey": credentialGCSecretData(apiKey)},
	}}
	if _, err := tenant(t).Resource(secretGVR).Namespace("default").Create(
		ctxWithTimeout(t, 15*time.Second), secret, metav1.CreateOptions{},
	); err != nil {
		t.Fatalf("create fixture Secret/%s: %v", name, err)
	}
}

func createLegacyGCCredential(t *testing.T, name, secretName string) {
	t.Helper()
	createObject(t, gcModelCredentialGVR, "ModelCredential", name, map[string]any{
		"provider":  "openai-compatible",
		"baseURL":   gcProbeBaseURL,
		"model":     "gc-test-model",
		"secretRef": map[string]any{"name": secretName},
	})
}

func waitForCredentialGCProtection(t *testing.T, name, secretName string) (*unstructured.Unstructured, *unstructured.Unstructured) {
	t.Helper()
	var credential, secret *unstructured.Unstructured
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		var err error
		credential, err = getObject(t, gcModelCredentialGVR, name)
		if err != nil {
			return false, fmt.Sprintf("ModelCredential read: %v", err)
		}
		if !containsString(credential.GetFinalizers(), gcModelCredentialFinalizer) {
			return false, fmt.Sprintf("protection finalizer not persisted yet: %v", credential.GetFinalizers())
		}
		secret, err = getCredentialGCSecret(t, secretName)
		if err != nil {
			return false, fmt.Sprintf("Secret read: %v", err)
		}
		if !hasCredentialGCSecretOwner(secret, credential) {
			return false, fmt.Sprintf("Secret owner reference not reconciled yet: %v", secret.GetOwnerReferences())
		}
		return true, ""
	}) {
		t.Fatalf("ModelCredential/%s protection finalizer and Secret ownership did not settle", name)
	}
	return credential, secret
}

func assertCredentialGCSecretHasOnlyOwner(t *testing.T, secretName string, owner *unstructured.Unstructured) {
	t.Helper()
	secret, err := getCredentialGCSecret(t, secretName)
	if err != nil {
		t.Fatalf("get fixture Secret/%s: %v", secretName, err)
	}
	if len(secret.GetOwnerReferences()) != 1 || !hasCredentialGCSecretOwner(secret, owner) {
		t.Fatalf("Secret/%s owners = %+v; want only ModelCredential/%s", secretName, secret.GetOwnerReferences(), owner.GetName())
	}
}

func getCredentialGCSecret(t *testing.T, name string) (*unstructured.Unstructured, error) {
	t.Helper()
	return tenant(t).Resource(secretGVR).Namespace("default").Get(
		ctxWithTimeout(t, 10*time.Second), name, metav1.GetOptions{},
	)
}

func hasCredentialGCSecretOwner(secret, credential *unstructured.Unstructured) bool {
	owners, _, _ := unstructured.NestedSlice(secret.Object, "metadata", "ownerReferences")
	for _, item := range owners {
		owner, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if owner["apiVersion"] == agentsGroup+"/v1alpha1" &&
			owner["kind"] == "ModelCredential" &&
			owner["name"] == credential.GetName() &&
			owner["uid"] == string(credential.GetUID()) {
			return true
		}
	}
	return false
}

func credentialGCSecretData(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

func gcCredentialSecretName(name string) string {
	return gcModelCredentialSecretPrefix + name
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
