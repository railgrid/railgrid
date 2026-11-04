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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-sdk/dataplane"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSavedConnectionActionsAuthorizeBeforeReadingKeySecret(t *testing.T) {
	connection := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "ModelProviderConnection",
		"metadata": map[string]any{"name": "primary", "uid": "connection-uid"},
		"spec": map[string]any{
			"provider": "openai-compatible", "baseURL": "https://api.openai.com/v1",
			"secretRef": map[string]any{"name": "key-primary"},
		},
	}}
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "key-primary", "namespace": projectLLMSecretNamespace},
		"data":     map[string]any{aiv1alpha1.ModelProviderConnectionSecretKey: encodeSecretValue("should-not-be-read")},
	}}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		modelProviderConnectionsGVR: "ModelProviderConnectionList",
		secretGVR:                   "SecretList",
	}, connection, secret)
	c := client.NewFromDynamic(dynamic)
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		projectClientFor: func(identity) (*client.Client, error) { return c, nil },
	}
	server.integrationAccessReviewer = func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
		if attrs.Group != modelProviderConnectionGVR.Group || attrs.Version != modelProviderConnectionGVR.Version ||
			attrs.Resource != modelProviderConnectionGVR.Resource || attrs.Name != "primary" || attrs.Verb != "use" {
			t.Errorf("use authorization attributes = %#v", attrs)
		}
		return false, nil
	}
	for _, action := range []struct {
		name string
		body string
		h    func(http.ResponseWriter, *http.Request)
	}{
		{name: "test", body: `{"model":"gpt-4o"}`, h: server.testModelProviderConnection},
		{name: "discover", body: `{}`, h: server.discoverModelProviderConnection},
	} {
		t.Run(action.name, func(t *testing.T) {
			request := projectLLMDiscoveryRequest(t, action.body)
			request = mux.SetURLVars(request, map[string]string{"name": "primary"})
			response := httptest.NewRecorder()
			action.h(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("denied action returned %d: %s", response.Code, response.Body.String())
			}
			for _, recorded := range dynamic.Actions() {
				if recorded.GetVerb() == "get" && recorded.GetResource().Resource == secretGVR.Resource {
					t.Fatal("unauthorized connection action read the key Secret")
				}
				if recorded.GetVerb() == "update" && recorded.GetResource().Resource == modelProviderConnectionsGVR.Resource && recorded.GetSubresource() == "status" {
					t.Fatal("unauthorized connection action published probe status")
				}
			}
		})
	}
}

func TestNewRunRejectsForgedConnectionSnapshotBeforeSecretRead(t *testing.T) {
	for _, test := range []struct {
		name     string
		baseURL  string
		secret   string
		secretID string
	}{
		{name: "arbitrary endpoint", baseURL: "https://attacker.example/v1", secret: "key-primary", secretID: "primary-secret-uid"},
		{name: "another connection Secret", baseURL: "https://api.openai.com/v1", secret: "key-victim", secretID: "victim-secret-uid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			studio := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio",
				"metadata": map[string]any{"name": aiv1alpha1.StudioName, "uid": "studio-uid"},
				"spec": map[string]any{"llm": map[string]any{
					"defaultModel": "fast",
					"models": []any{map[string]any{
						"id": "fast", "revisionID": "revision-one", "name": "Fast", "model": "gpt-5",
						"connectionRef": "primary",
						"connectionSnapshot": map[string]any{
							"connectionRef": "primary", "connectionUID": "primary-connection-uid",
							"provider": "openai-compatible", "baseURL": test.baseURL,
							"secretRef": map[string]any{"name": test.secret}, "secretKey": "apiKey",
							"secretUID": test.secretID, "secretResourceVersion": "1",
							"connectionFingerprint": "caller-controlled", "generatedSecretID": "",
						},
					}},
				}},
			}}
			connection := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "ModelProviderConnection",
				"metadata": map[string]any{"name": "primary", "uid": "primary-connection-uid", "generation": int64(1)},
				"spec": map[string]any{
					"provider": "openai-compatible", "baseURL": "https://api.openai.com/v1",
					"secretRef": map[string]any{"name": "key-primary"},
				},
			}}
			victimSecret := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Secret",
				"metadata": map[string]any{"name": "key-victim", "namespace": projectLLMSecretNamespace, "uid": "victim-secret-uid", "resourceVersion": "1"},
				"data":     map[string]any{"apiKey": encodeSecretValue("victim-key")},
			}}
			dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				studioResource.GVR:          "StudioList",
				modelProviderConnectionsGVR: "ModelProviderConnectionList",
				secretGVR:                   "SecretList",
			}, studio, connection, victimSecret)
			c := client.NewFromDynamic(dynamic)
			server := &Server{integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
				if attrs.Verb != "use" || attrs.Resource != modelProviderConnectionsGVR.Resource || attrs.Name != "primary" {
					t.Errorf("connection use SAR = %#v", attrs)
				}
				return true, nil
			}}
			_, err := server.readProjectLLMModelForIdentity(context.Background(), identity{clusterID: "cluster-a", user: "alice"}, c, "fast", "revision-one", nil)
			if err == nil {
				t.Fatal("caller-controlled connection snapshot was accepted for a new run")
			}
			for _, action := range dynamic.Actions() {
				if action.GetVerb() == "get" && action.GetResource().Resource == secretGVR.Resource {
					getAction, ok := action.(ktesting.GetAction)
					if ok && getAction.GetName() == "key-victim" {
						t.Fatal("forged snapshot caused a read of another connection's Secret")
					}
					if ok && getAction.GetName() == "key-primary" {
						t.Fatal("forged snapshot triggered a credential read before being rejected")
					}
				}
			}
		})
	}
}

func TestRunSnapshotUsesOldConnectionImmediatelyAfterConnectionPatch(t *testing.T) {
	const oldSecretID = "0123456789abcdef0123456789abcdef"
	oldSecretName := aiv1alpha1.ModelProviderConnectionSecretPrefix + oldSecretID
	oldSnapshot := &aiv1alpha1.StudioLLMConnectionSnapshot{
		ConnectionRef: "primary", ConnectionUID: "connection-uid",
		Provider: "openai-compatible", BaseURL: "https://old-endpoint.example/v1",
		SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: oldSecretName},
		SecretKey: aiv1alpha1.ModelProviderConnectionSecretKey,
		SecretUID: "old-secret-uid", SecretResourceVersion: "1",
		ConnectionFingerprint: "old-fingerprint", GeneratedSecretID: oldSecretID,
	}
	studio := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Studio",
		"metadata":   map[string]any{"name": aiv1alpha1.StudioName, "uid": "studio-uid"},
		"spec": map[string]any{"llm": map[string]any{
			"defaultModel": "fast",
			"models": []any{map[string]any{
				"id": "fast", "revisionID": "revision-started", "name": "Fast", "model": "gpt-5", "reasoningEffort": "high",
				"connectionRef": "primary", "connectionSnapshot": connectionSnapshotMap(oldSnapshot),
			}},
		}},
		"status": map[string]any{"models": []any{map[string]any{"id": "fast", "configured": true}}},
	}}
	connection := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "ModelProviderConnection",
		"metadata": map[string]any{
			"name": "primary", "uid": "connection-uid", "generation": int64(2),
			"annotations": map[string]any{aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation: "abcdef0123456789abcdef0123456789"},
		},
		"spec": map[string]any{
			"provider": "openai-compatible", "baseURL": "https://new-endpoint.example/v1",
			"secretRef": map[string]any{"name": aiv1alpha1.ModelProviderConnectionSecretPrefix + "abcdef0123456789abcdef0123456789"},
		},
	}}
	oldSecret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": oldSecretName, "namespace": projectLLMSecretNamespace, "uid": "old-secret-uid", "resourceVersion": "2",
			"labels":      map[string]any{aiv1alpha1.ModelProviderConnectionSecretOwnerLabel: aiv1alpha1.ModelProviderConnectionSecretOwner},
			"annotations": map[string]any{aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation: oldSecretID},
			"ownerReferences": []any{map[string]any{
				"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio", "name": aiv1alpha1.StudioName, "uid": "studio-uid",
			}},
		},
		"immutable": true,
		"data":      map[string]any{aiv1alpha1.ModelProviderConnectionSecretKey: encodeSecretValue("old-key")},
	}}
	newSecret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name":      aiv1alpha1.ModelProviderConnectionSecretPrefix + "abcdef0123456789abcdef0123456789",
			"namespace": projectLLMSecretNamespace, "uid": "new-secret-uid", "resourceVersion": "1",
		},
		"data": map[string]any{aiv1alpha1.ModelProviderConnectionSecretKey: encodeSecretValue("new-key")},
	}}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		studioResource.GVR:          "StudioList",
		secretGVR:                   "SecretList",
		modelProviderConnectionsGVR: "ModelProviderConnectionList",
	}, studio, connection, oldSecret, newSecret)

	var audit projectAssistantRunAudit
	audit.ModelID = "fast"
	audit.ModelRevisionID = "revision-started"
	audit.ConnectionSnapshot = oldSnapshot
	rawAudit, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := readProjectLLMModelForRun(context.Background(), client.NewFromDynamic(dynamic), store.AssistantRun{Audit: rawAudit})
	if err != nil {
		t.Fatalf("resolve pinned model after connection patch: %v", err)
	}
	if selected.Settings.BaseURL != oldSnapshot.BaseURL || selected.Settings.APIKey != "old-key" || selected.RevisionID != "revision-started" {
		t.Fatalf("resumed run resolved %#v, want the old endpoint, key, and model revision", selected)
	}
	if selected.Settings.BaseURL == "https://new-endpoint.example/v1" || selected.Settings.APIKey == "new-key" {
		t.Fatal("connection patch changed the model used by a run before Studio revision rollover")
	}

	var secretReads []string
	for _, action := range dynamic.Actions() {
		if action.GetVerb() != "get" || action.GetResource().Resource != secretGVR.Resource {
			continue
		}
		getAction, ok := action.(ktesting.GetAction)
		if !ok {
			t.Fatalf("secret GET action has unexpected type %T", action)
		}
		secretReads = append(secretReads, getAction.GetName())
	}
	if len(secretReads) != 1 || secretReads[0] != oldSecretName {
		t.Fatalf("resumed run Secret reads = %#v, want only retained old key %q", secretReads, oldSecretName)
	}
}

func TestInterruptedContinuationUsesPredecessorAuditSnapshotAfterRollover(t *testing.T) {
	const oldSecretID = "0123456789abcdef0123456789abcdef"
	const newSecretID = "abcdef0123456789abcdef0123456789"
	oldSecretName := aiv1alpha1.ModelProviderConnectionSecretPrefix + oldSecretID
	newSecretName := aiv1alpha1.ModelProviderConnectionSecretPrefix + newSecretID
	oldSnapshot := &aiv1alpha1.StudioLLMConnectionSnapshot{
		ConnectionRef: "primary", ConnectionUID: "connection-uid", Provider: "openai-compatible",
		BaseURL: "https://old-endpoint.example/v1", SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: oldSecretName},
		SecretKey: "apiKey", SecretUID: "old-secret-uid", SecretResourceVersion: "1",
		ConnectionFingerprint: "old-fingerprint", GeneratedSecretID: oldSecretID,
	}
	newSnapshot := &aiv1alpha1.StudioLLMConnectionSnapshot{
		ConnectionRef: "primary", ConnectionUID: "connection-uid", Provider: "openai-compatible",
		BaseURL: "https://new-endpoint.example/v1", SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: newSecretName},
		SecretKey: "apiKey", SecretUID: "new-secret-uid", SecretResourceVersion: "1",
		ConnectionFingerprint: "new-fingerprint", GeneratedSecretID: newSecretID,
	}
	studio := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio",
		"metadata": map[string]any{"name": aiv1alpha1.StudioName, "uid": "studio-uid"},
		"spec": map[string]any{"llm": map[string]any{
			"defaultModel": "fast",
			"models": []any{
				map[string]any{"id": "fast", "revisionID": "revision-old", "archived": true, "name": "Fast", "model": "gpt-5", "connectionRef": "primary", "connectionSnapshot": connectionSnapshotMap(oldSnapshot)},
				map[string]any{"id": "fast", "revisionID": "revision-new", "name": "Fast", "model": "gpt-5", "connectionRef": "primary", "connectionSnapshot": connectionSnapshotMap(newSnapshot)},
			},
		}},
	}}
	oldSecret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": oldSecretName, "namespace": projectLLMSecretNamespace, "uid": "old-secret-uid", "resourceVersion": "2",
			"labels":          map[string]any{aiv1alpha1.ModelProviderConnectionSecretOwnerLabel: aiv1alpha1.ModelProviderConnectionSecretOwner},
			"annotations":     map[string]any{aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation: oldSecretID},
			"ownerReferences": []any{map[string]any{"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio", "name": aiv1alpha1.StudioName, "uid": "studio-uid"}},
		},
		"immutable": true, "data": map[string]any{"apiKey": encodeSecretValue("old-key")},
	}}
	newSecret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": newSecretName, "namespace": projectLLMSecretNamespace, "uid": "new-secret-uid", "resourceVersion": "1",
			"labels":          map[string]any{aiv1alpha1.ModelProviderConnectionSecretOwnerLabel: aiv1alpha1.ModelProviderConnectionSecretOwner},
			"annotations":     map[string]any{aiv1alpha1.ModelProviderConnectionGeneratedSecretIDAnnotation: newSecretID},
			"ownerReferences": []any{map[string]any{"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "ModelProviderConnection", "name": "primary", "uid": "connection-uid"}},
		},
		"immutable": true, "data": map[string]any{"apiKey": encodeSecretValue("new-key")},
	}}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		studioResource.GVR:          "StudioList",
		secretGVR:                   "SecretList",
		modelProviderConnectionsGVR: "ModelProviderConnectionList",
	}, studio, oldSecret, newSecret)
	authorized := false
	secretReadBeforeUse := false
	dynamic.PrependReactor("get", secretGVR.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
		if !authorized {
			secretReadBeforeUse = true
		}
		return false, nil, nil
	})
	server := &Server{integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
		if attrs.Verb != "use" || attrs.Resource != modelProviderConnectionGVR.Resource || attrs.Name != "primary" {
			t.Errorf("continuation use SAR = %#v", attrs)
		}
		authorized = true
		return true, nil
	}}
	audit := projectAssistantRunAudit{ModelID: "fast", ModelRevisionID: "revision-old", ConnectionSnapshot: oldSnapshot}
	rawAudit, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := server.readProjectLLMModelForContinuation(context.Background(), identity{clusterID: "cluster-a", user: "alice"}, client.NewFromDynamic(dynamic), store.AssistantRun{Audit: rawAudit})
	if err != nil {
		t.Fatalf("continue interrupted run after connection rollover: %v", err)
	}
	if selected.RevisionID != "revision-old" || selected.Settings.BaseURL != oldSnapshot.BaseURL || selected.Settings.APIKey != "old-key" {
		t.Fatalf("continuation selected %#v, want the predecessor's pinned revision and old key", selected)
	}
	if secretReadBeforeUse {
		t.Fatal("continuation read a pinned Secret before its fresh use authorization")
	}
}

func connectionSnapshotMap(snapshot *aiv1alpha1.StudioLLMConnectionSnapshot) map[string]any {
	return map[string]any{
		"connectionRef": snapshot.ConnectionRef, "connectionUID": snapshot.ConnectionUID,
		"provider": snapshot.Provider, "baseURL": snapshot.BaseURL,
		"secretRef": map[string]any{"name": snapshot.SecretRef.Name},
		"secretKey": snapshot.SecretKey, "secretUID": snapshot.SecretUID,
		"secretResourceVersion": snapshot.SecretResourceVersion,
		"connectionFingerprint": snapshot.ConnectionFingerprint,
		"generatedSecretID":     snapshot.GeneratedSecretID,
	}
}

func TestLegacyModelShapeCannotResolveSavedConnectionKeys(t *testing.T) {
	for _, name := range []string{aiv1alpha1.ModelProviderConnectionSecretPrefix + "0123456789abcdef0123456789abcdef", "external-key"} {
		t.Run(name, func(t *testing.T) {
			studio := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio",
				"metadata": map[string]any{"name": aiv1alpha1.StudioName, "uid": "studio-uid"},
				"spec": map[string]any{"llm": map[string]any{"defaultModel": "forged", "models": []any{map[string]any{
					"id": "forged", "revisionID": "forged-revision", "name": "Forged", "model": "gpt-4o",
					"provider": "openai-compatible", "baseURL": "https://attacker.example/v1", "secretRef": map[string]any{"name": name},
				}}}},
			}}
			connection := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "ModelProviderConnection",
				"metadata": map[string]any{"name": "restricted", "uid": "connection-uid"},
				"spec":     map[string]any{"provider": "openai-compatible", "baseURL": "https://api.openai.com/v1", "secretRef": map[string]any{"name": name}},
			}}
			secret := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": name, "namespace": "default"},
				"data": map[string]any{"apiKey": encodeSecretValue("must-not-be-read")},
			}}
			dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				studioResource.GVR: "StudioList", modelProviderConnectionsGVR: "ModelProviderConnectionList", secretGVR: "SecretList",
			}, studio, connection, secret)
			c := client.NewFromDynamic(dynamic)
			server := &Server{integrationAccessReviewer: func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) { return false, nil }}
			if _, err := server.readProjectLLMModelForIdentity(context.Background(), identity{clusterID: "tenant", user: "denied"}, c, "forged", "", nil); err == nil {
				t.Fatal("legacy-shaped new turn accepted a saved connection key")
			}
			if _, err := readProjectLLMModelForCredentialReuse(context.Background(), c, "forged"); err == nil {
				t.Fatal("legacy-shaped discovery/test accepted a saved connection key")
			}
			for _, action := range dynamic.Actions() {
				if action.GetVerb() == "get" && action.GetResource().Resource == "secrets" {
					t.Fatal("saved connection key was read through the legacy path")
				}
			}
		})
	}
}

func TestLegacyModelShapeCannotResolveActiveExternalKeyDuringRotation(t *testing.T) {
	for _, name := range []string{"external-key"} {
		t.Run(name, func(t *testing.T) {
			studio := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio",
				"metadata": map[string]any{"name": aiv1alpha1.StudioName, "uid": "studio-uid"},
				"spec": map[string]any{"llm": map[string]any{"defaultModel": "forged", "models": []any{map[string]any{
					"id": "forged", "revisionID": "forged-revision", "name": "Forged", "model": "gpt-4o",
					"provider": "openai-compatible", "baseURL": "https://attacker.example/v1", "secretRef": map[string]any{"name": name},
				}}}},
			}}
			connection := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "ModelProviderConnection",
				"metadata": map[string]any{"name": "restricted", "uid": "connection-uid"},
				"spec":     map[string]any{"provider": "openai-compatible", "baseURL": "https://api.openai.com/v1", "secretRef": map[string]any{"name": name}},
			}}
			if err := unstructured.SetNestedField(connection.Object, "replacement-key", "spec", "secretRef", "name"); err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedField(connection.Object, name, "status", "activeSnapshot", "secretRef", "name"); err != nil {
				t.Fatal(err)
			}
			secret := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": name, "namespace": "default"},
				"data": map[string]any{"apiKey": encodeSecretValue("must-not-be-read")},
			}}
			dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				studioResource.GVR: "StudioList", modelProviderConnectionsGVR: "ModelProviderConnectionList", secretGVR: "SecretList",
			}, studio, connection, secret)
			c := client.NewFromDynamic(dynamic)
			server := &Server{integrationAccessReviewer: func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) { return false, nil }}
			if _, err := server.readProjectLLMModelForIdentity(context.Background(), identity{clusterID: "tenant", user: "denied"}, c, "forged", "", nil); err == nil {
				t.Fatal("legacy-shaped new turn accepted a saved connection key")
			}
			if _, err := readProjectLLMModelForCredentialReuse(context.Background(), c, "forged"); err == nil {
				t.Fatal("legacy-shaped discovery/test accepted a saved connection key")
			}
			for _, action := range dynamic.Actions() {
				if action.GetVerb() == "get" && action.GetResource().Resource == "secrets" {
					t.Fatal("saved connection key was read through the legacy path")
				}
			}
		})
	}
}

func TestLegacyContinuationReauthorizesMigratedConnectionBeforeKeyRead(t *testing.T) {
	studio := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio", "metadata": map[string]any{"name": aiv1alpha1.StudioName, "uid": "studio-uid"},
		"spec": map[string]any{"llm": map[string]any{"models": []any{map[string]any{
			"id": "fast", "revisionID": "legacy-r1", "archived": true, "name": "Fast", "model": "gpt-4o", "provider": "openai-compatible", "baseURL": "https://api.openai.com/v1", "secretRef": map[string]any{"name": "legacy-key"},
		}}}},
	}}
	connection := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "ModelProviderConnection", "metadata": map[string]any{"name": "fast", "uid": "connection-uid"},
		"spec": map[string]any{"provider": "openai-compatible", "baseURL": "https://api.openai.com/v1", "secretRef": map[string]any{"name": "legacy-key"}},
	}}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "legacy-key", "namespace": "default"}, "data": map[string]any{"apiKey": encodeSecretValue("private-key")}}}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{studioResource.GVR: "StudioList", modelProviderConnectionsGVR: "ModelProviderConnectionList", secretGVR: "SecretList"}, studio, connection, secret)
	calls := 0
	server := &Server{integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
		calls++
		if attrs.Name != "fast" || attrs.Verb != "use" {
			t.Errorf("wrong authority: %#v", attrs)
		}
		return false, nil
	}}
	raw, _ := json.Marshal(projectAssistantRunAudit{ModelID: "fast", ModelRevisionID: "legacy-r1"})
	if _, err := server.readProjectLLMModelForContinuation(context.Background(), identity{clusterID: "tenant", user: "revoked"}, client.NewFromDynamic(dynamic), store.AssistantRun{Audit: raw}); !apierrors.IsForbidden(err) {
		t.Fatalf("legacy continuation should be forbidden: %v", err)
	}
	if calls != 1 {
		t.Fatalf("use checks = %d", calls)
	}
	for _, action := range dynamic.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("revoked legacy continuation read a key")
		}
	}
}

func TestCheckpointResumeDenialLeavesPausedRunUnclaimed(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	server.integrationAccessReviewer = func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) { return false, nil }
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"}}
	id := identity{orgUUID: "org", workspaceUUID: "workspace", user: "revoked"}
	scope := projectMessageScope(id.orgUUID, id.workspaceUUID, project)
	audit, _ := json.Marshal(projectAssistantRunAudit{ConnectionSnapshot: &aiv1alpha1.StudioLLMConnectionSnapshot{ConnectionRef: "primary"}})
	checkpoint, _ := json.Marshal(projectAssistantCheckpointState{Eino: &projectAssistantEinoCheckpointState{InterruptType: projectAssistantInterruptTypeFollowUp}})
	run, err := messages.CreateAssistantRun(context.Background(), scope, store.Message{ID: "user-message", Role: "user", ActorID: "revoked"}, store.Message{ID: "assistant-message", Role: "assistant"}, store.AssistantRun{ID: "paused", UserMessageID: "user-message", ClientRequestID: "client-request", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusPendingInput, RequestID: "question", ActiveMessageID: "assistant-message", Revision: 1, Audit: audit, Checkpoint: checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.resumeProjectAssistantRunWithRepositoryAndClient(context.Background(), nil, id, nil, project, nil, run.ID, projectAssistantResumeRequest{RequestID: "question", Answer: "yes"})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("checkpoint resume should be forbidden: %v", err)
	}
	stored, err := messages.GetAssistantRun(context.Background(), scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != store.AssistantRunStatusPendingInput || stored.Revision != run.Revision {
		t.Fatal("denied resume consumed or changed the paused run")
	}
}

func TestNewTurnsUseAdmittedSnapshotWhileDesiredRotationIsBlocked(t *testing.T) {
	connection, secret := fingerprintFixtures()
	connection.APIVersion, connection.Kind = aiv1alpha1.SchemeGroupVersion.String(), "ModelProviderConnection"
	secret.SetNamespace("default")
	admitted, err := ModelProviderConnectionSnapshot(connection, secret)
	if err != nil {
		t.Fatal(err)
	}
	connection.Status.ActiveSnapshot = admitted
	connection.Spec.BaseURL = "https://pending.example/v1"
	connection.Spec.SecretRef.Name = "pending-key"
	connection.Generation++
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(connection)
	if err != nil {
		t.Fatal(err)
	}
	connectionObject := &unstructured.Unstructured{Object: object}
	studio := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio", "metadata": map[string]any{"name": aiv1alpha1.StudioName},
		"spec": map[string]any{"llm": map[string]any{"models": []any{map[string]any{"id": "fast", "revisionID": "admitted-r1", "name": "Fast", "model": "gpt-4o", "connectionRef": connection.Name, "connectionSnapshot": connectionSnapshotMap(admitted)}}}},
	}}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{studioResource.GVR: "StudioList", modelProviderConnectionsGVR: "ModelProviderConnectionList", secretGVR: "SecretList"}, studio, connectionObject, secret)
	server := &Server{integrationAccessReviewer: func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) { return true, nil }}
	c := client.NewFromDynamic(dynamic)
	selected, err := server.readProjectLLMModelForIdentity(context.Background(), identity{clusterID: "tenant", user: "alice"}, c, "fast", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Settings.BaseURL != admitted.BaseURL || selected.Settings.APIKey != "fixture-key-never-in-public-metadata" {
		t.Fatal("pending rotation changed the admitted runtime configuration")
	}
	// Saved-model probes also retain the admitted account, not the staged endpoint.
	settings, err := server.resolveSavedConnectionCredential(context.Background(), identity{clusterID: "tenant", user: "alice"}, c, connection.Name, admitted.ConnectionFingerprint, admitted.Provider, admitted.BaseURL, admitted.AnthropicWorkspaceID)
	if err != nil || settings.BaseURL != admitted.BaseURL {
		t.Fatalf("admitted-model probe failed during pending rotation: %v", err)
	}
	// Model rollover can win its Studio CAS just before a second desired
	// connection edit prevents publishing admission. Select the old admitted
	// revision from history rather than making the logical model unusable.
	rows, _, _ := unstructured.NestedSlice(studio.Object, "spec", "llm", "models")
	rows[0].(map[string]any)["archived"] = true
	pending := admitted.DeepCopy()
	pending.BaseURL, pending.ConnectionFingerprint = "https://pending.example/v1", "not-admitted"
	pending.SecretRef = &aiv1alpha1.StudioLLMSecretRef{Name: "pending-key"}
	rows = append(rows, map[string]any{"id": "fast", "revisionID": "unadmitted-r2", "name": "Fast", "model": "gpt-4o", "connectionRef": connection.Name, "connectionSnapshot": connectionSnapshotMap(pending)})
	if err := unstructured.SetNestedSlice(studio.Object, rows, "spec", "llm", "models"); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamic.Resource(studioResource.GVR).Update(context.Background(), studio, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	fallback, err := server.readProjectLLMModelForIdentity(context.Background(), identity{clusterID: "tenant", user: "alice"}, c, "fast", "", nil)
	if err != nil || fallback.RevisionID != "admitted-r1" {
		t.Fatalf("unadmitted rollover lost the previous usable revision: %v", err)
	}
	for _, action := range dynamic.Actions() {
		if action.GetResource().Resource == "secrets" {
			if get, ok := action.(ktesting.GetAction); ok && get.GetName() == "pending-key" {
				t.Fatal("pending credential was used before admission")
			}
		}
	}
}

func TestLegacyRunUsesTrustedSourceAfterInPlaceMigrationAndRotation(t *testing.T) {
	original := aiv1alpha1.StudioLLMModel{ID: "fast", RevisionID: "legacy-r1", Name: "Fast", Model: "gpt-4o", Provider: "openai-compatible", BaseURL: "https://old.example/v1", SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: "legacy-key"}}
	migrated := original
	migrated.ConnectionRef = "caller-allowed"
	migrated.BaseURL = "https://caller-edited.example/v1"
	migrated.ConnectionSnapshot = &aiv1alpha1.StudioLLMConnectionSnapshot{ConnectionRef: "caller-allowed", Provider: "openai-compatible", BaseURL: "https://new.example/v1", SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: "new-key"}}
	typed := &aiv1alpha1.Studio{TypeMeta: metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Studio"}, ObjectMeta: metav1.ObjectMeta{Name: aiv1alpha1.StudioName}, Spec: aiv1alpha1.StudioSpec{LLM: &aiv1alpha1.StudioLLM{Models: []aiv1alpha1.StudioLLMModel{migrated}}}, Status: aiv1alpha1.StudioStatus{LegacyModelMigrations: []aiv1alpha1.StudioLegacyModelMigration{{Model: original, SecretUID: "legacy-key-uid"}}}}
	value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(typed)
	if err != nil {
		t.Fatal(err)
	}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "legacy-key", "namespace": "default", "uid": "legacy-key-uid"}, "data": map[string]any{"apiKey": encodeSecretValue("original-key")}}}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{studioResource.GVR: "StudioList", secretGVR: "SecretList"}, &unstructured.Unstructured{Object: value}, secret)
	calls := 0
	allowed := false
	server := &Server{integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
		calls++
		if attrs.Name != "fast" {
			t.Errorf("untrusted connectionRef changed authority: %s", attrs.Name)
		}
		return allowed || attrs.Name == "caller-allowed", nil
	}}
	audit, _ := json.Marshal(projectAssistantRunAudit{ModelID: "fast", ModelRevisionID: "legacy-r1"})
	if _, err := server.readProjectLLMModelForContinuation(context.Background(), identity{user: "alice"}, client.NewFromDynamic(dynamic), store.AssistantRun{Audit: audit}); !apierrors.IsForbidden(err) {
		t.Fatalf("forged connectionRef bypassed original source authority: %v", err)
	}
	for _, action := range dynamic.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("denied source read a credential")
		}
	}
	allowed = true
	selected, err := server.readProjectLLMModelForContinuation(context.Background(), identity{user: "alice"}, client.NewFromDynamic(dynamic), store.AssistantRun{Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || selected.Settings.BaseURL != original.BaseURL || selected.Settings.APIKey != "original-key" {
		t.Fatal("legacy resume ignored authorization or trusted original transport")
	}
}

func TestRetainedExternalKeyAfterRotationCannotBypassConnectionUse(t *testing.T) {
	for _, name := range []string{"migration-provenance", "protected-history"} {
		t.Run(name, func(t *testing.T) {
			studio := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio",
				"metadata": map[string]any{"name": aiv1alpha1.StudioName, "uid": "studio-uid"},
				"spec": map[string]any{"llm": map[string]any{"defaultModel": "forged", "models": []any{map[string]any{
					"id": "forged", "revisionID": "forged-revision", "name": "Forged", "model": "gpt-4o",
					"provider": "openai-compatible", "baseURL": "https://attacker.example/v1", "secretRef": map[string]any{"name": name},
				}}}},
			}}
			connection := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "ModelProviderConnection",
				"metadata": map[string]any{"name": "restricted", "uid": "connection-uid"},
				"spec":     map[string]any{"provider": "openai-compatible", "baseURL": "https://api.openai.com/v1", "secretRef": map[string]any{"name": name}},
			}}
			if err := unstructured.SetNestedField(connection.Object, "replacement-key", "spec", "secretRef", "name"); err != nil {
				t.Fatal(err)
			}
			if name == "migration-provenance" {
				if err := unstructured.SetNestedSlice(studio.Object, []any{map[string]any{"model": map[string]any{"id": "restricted", "revisionID": "source-revision", "name": "Original", "provider": "openai-compatible", "baseURL": "https://api.openai.com/v1", "model": "gpt-4o", "secretRef": map[string]any{"name": name}}, "secretUID": "original-key-uid"}}, "status", "legacyModelMigrations"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := unstructured.SetNestedSlice(studio.Object, []any{map[string]any{"secretName": name, "secretUID": "original-key-uid"}}, "status", "protectedModelCredentials"); err != nil {
					t.Fatal(err)
				}
				if err := unstructured.SetNestedField(connection.Object, "replacement-key", "status", "activeSnapshot", "secretRef", "name"); err != nil {
					t.Fatal(err)
				}
			}
			secret := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": name, "namespace": "default"},
				"data": map[string]any{"apiKey": encodeSecretValue("must-not-be-read")},
			}}
			dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				studioResource.GVR: "StudioList", modelProviderConnectionsGVR: "ModelProviderConnectionList", secretGVR: "SecretList",
			}, studio, connection, secret)
			c := client.NewFromDynamic(dynamic)
			server := &Server{integrationAccessReviewer: func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) { return false, nil }}
			if _, err := server.readProjectLLMModelForIdentity(context.Background(), identity{clusterID: "tenant", user: "denied"}, c, "forged", "", nil); err == nil {
				t.Fatal("legacy-shaped new turn accepted a saved connection key")
			}
			if _, err := readProjectLLMModelForCredentialReuse(context.Background(), c, "forged"); err == nil {
				t.Fatal("legacy-shaped discovery/test accepted a saved connection key")
			}
			for _, action := range dynamic.Actions() {
				if action.GetVerb() == "get" && action.GetResource().Resource == "secrets" {
					t.Fatal("saved connection key was read through the legacy path")
				}
			}
		})
	}
}

func TestNewModelSelectionPreservesStudioRuntime(t *testing.T) {
	studio := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio", "metadata": map[string]any{"name": "studio"},
		"spec": map[string]any{"llm": map[string]any{
			"runtime": map[string]any{"maxRetries": int64(0), "retryBackoffMS": int64(1234), "streamIdleTimeoutMS": int64(12345)},
			"models":  []any{map[string]any{"id": "legacy", "revisionID": "r1", "name": "Legacy", "provider": "openai-compatible", "baseURL": "https://api.openai.com/v1", "model": "gpt-4o", "secretRef": map[string]any{"name": "legacy-key"}}},
		}},
	}}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "legacy-key", "namespace": "default"}, "data": map[string]any{"apiKey": encodeSecretValue("synthetic-key")}}}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{studioResource.GVR: "StudioList", modelProviderConnectionsGVR: "ModelProviderConnectionList", secretGVR: "SecretList"}, studio, secret)
	selected, err := (&Server{}).readProjectLLMModelForIdentity(context.Background(), identity{clusterID: "tenant", user: "user"}, client.NewFromDynamic(dynamic), "legacy", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Settings.MaxRetries != 0 || selected.Settings.RetryBackoff != 1234*time.Millisecond || selected.Settings.StreamIdleTimeout != 12345*time.Millisecond {
		t.Fatalf("workspace runtime ignored: retries=%d backoff=%v idle=%v", selected.Settings.MaxRetries, selected.Settings.RetryBackoff, selected.Settings.StreamIdleTimeout)
	}
}

func TestUnmarkedConnectionCannotReadAnotherConnectionsGeneratedKey(t *testing.T) {
	keyName := aiv1alpha1.ModelProviderConnectionSecretPrefix + "0123456789abcdef0123456789abcdef"
	connection := &aiv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: "attacker", UID: "attacker-uid"}, Spec: aiv1alpha1.ModelProviderConnectionSpec{SecretRef: aiv1alpha1.StudioLLMSecretRef{Name: keyName}}}
	secret := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": keyName, "namespace": "default"}, "data": map[string]any{"apiKey": encodeSecretValue("synthetic-key")}}}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{secretGVR: "SecretList"}, secret)
	if _, _, err := readModelProviderConnectionSecret(context.Background(), client.NewFromDynamic(dynamic), connection); err == nil {
		t.Fatal("unmarked connection read another account's generated key")
	}
}
