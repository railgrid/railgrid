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
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-sdk/dataplane"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func newConnectionProbeTestClient(t *testing.T, baseURL string, status aiv1alpha1.ModelProviderConnectionStatus) (*asclient.Client, *fake.FakeDynamicClient, *aiv1alpha1.ModelProviderConnection, *unstructured.Unstructured) {
	t.Helper()
	connection := &aiv1alpha1.ModelProviderConnection{
		TypeMeta: metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "primary", UID: "connection-uid", Generation: 1, ResourceVersion: "10",
		},
		Spec: aiv1alpha1.ModelProviderConnectionSpec{
			Provider: "openai-compatible", BaseURL: baseURL,
			SecretRef: aiv1alpha1.StudioLLMSecretRef{Name: "primary-key"},
		},
		Status: status,
	}
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": "primary-key", "namespace": projectLLMSecretNamespace,
			"uid": "primary-key-uid", "resourceVersion": "42",
		},
		"data": map[string]any{aiv1alpha1.ModelProviderConnectionSecretKey: encodeSecretValue("probe-test-api-key")},
	}}
	if connection.Status.ConnectionFingerprint == "" {
		fingerprint, err := ModelProviderConnectionFingerprint(connection, secret)
		if err != nil {
			t.Fatal(err)
		}
		connection.Status.ConnectionFingerprint = fingerprint
	}
	connectionObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(connection)
	if err != nil {
		t.Fatal(err)
	}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		modelProviderConnectionsGVR: "ModelProviderConnectionList",
		secretGVR:                   "SecretList",
	}, &unstructured.Unstructured{Object: connectionObject}, secret)
	return asclient.NewFromDynamic(dynamic), dynamic, connection, secret
}

func TestPublishModelProviderConnectionProbePreservesControllerStatus(t *testing.T) {
	connection, secret := fingerprintFixtures()
	connection.TypeMeta = metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection"}
	connection.ResourceVersion = "10"
	connection.Spec.BaseURL = "https://provider.example/v1"
	connection.Status = aiv1alpha1.ModelProviderConnectionStatus{
		ConnectionFingerprint: "controller-current-fingerprint",
		SecretUID:             "controller-secret-uid",
	}
	ready := metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", Message: "controller-owned"}
	apiMeta.SetStatusCondition(&connection.Status.Conditions, ready)
	secret.SetNamespace(projectLLMSecretNamespace)
	secret.SetUID("key-uid")
	secret.SetResourceVersion("42")
	secret.SetName("account-key")
	secret.Object["data"] = map[string]any{aiv1alpha1.ModelProviderConnectionSecretKey: encodeSecretValue("probe-test-api-key")}
	connection.Spec.SecretRef.Name = secret.GetName()
	connectionObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(connection)
	if err != nil {
		t.Fatal(err)
	}
	dynamic := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		modelProviderConnectionsGVR: "ModelProviderConnectionList",
		secretGVR:                   "SecretList",
	}, &unstructured.Unstructured{Object: connectionObject}, secret)
	c := asclient.NewFromDynamic(dynamic)
	fingerprint, err := ModelProviderConnectionFingerprint(connection, secret)
	if err != nil {
		t.Fatal(err)
	}
	probeTime := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	published, err := publishModelProviderConnectionProbe(context.Background(), c, newModelProviderConnectionProbeTarget(connection, fingerprint), modelProviderConnectionProbeOutcome{
		reachable:     metav1.ConditionTrue,
		reason:        "Reachable",
		message:       "The provider endpoint answered model discovery.",
		models:        []string{"model-a", "model-b"},
		replaceModels: true,
	}, probeTime)
	if err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("probe result was not published for its current fingerprint")
	}
	stored, err := dynamic.Resource(modelProviderConnectionsGVR).Get(context.Background(), connection.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got aiv1alpha1.ModelProviderConnection
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(stored.Object, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.LastProbeFingerprint != fingerprint || got.Status.LastProbeTime == nil || !got.Status.LastProbeTime.Equal(&metav1.Time{Time: probeTime}) {
		t.Fatalf("probe version/time = %q / %v", got.Status.LastProbeFingerprint, got.Status.LastProbeTime)
	}
	if got.Status.ConnectionFingerprint != "controller-current-fingerprint" || got.Status.SecretUID != "controller-secret-uid" {
		t.Fatalf("probe update overwrote controller-owned status: %#v", got.Status)
	}
	if len(got.Status.Models) != 2 || got.Status.Models[0] != "model-a" || got.Status.Models[1] != "model-b" {
		t.Fatalf("published model list = %#v", got.Status.Models)
	}
	if ready := apiMeta.FindStatusCondition(got.Status.Conditions, "Ready"); ready == nil || ready.Message != "controller-owned" {
		t.Fatalf("probe update changed unrelated conditions: %#v", got.Status.Conditions)
	}
	if reachable := apiMeta.FindStatusCondition(got.Status.Conditions, "Reachable"); reachable == nil || reachable.Status != metav1.ConditionTrue {
		t.Fatalf("Reachable condition = %#v", reachable)
	}
	for _, action := range dynamic.Actions() {
		if action.GetVerb() == "update" && action.GetResource().Resource == modelProviderConnectionsGVR.Resource && action.GetSubresource() != "status" {
			t.Fatal("probe publisher updated connection spec instead of status")
		}
	}
	encoded, err := json.Marshal(got.Status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "probe-test-api-key") {
		t.Fatal("published status contains credential bytes")
	}
}

func TestPublishModelProviderConnectionProbeDiscardsStaleFingerprint(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, *fake.FakeDynamicClient)
	}{
		{
			name: "connection generation changed",
			change: func(t *testing.T, dynamic *fake.FakeDynamicClient) {
				t.Helper()
				object, err := dynamic.Resource(modelProviderConnectionsGVR).Get(context.Background(), "primary", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				object.SetGeneration(2)
				object.SetResourceVersion("11")
				if _, err := dynamic.Resource(modelProviderConnectionsGVR).Update(context.Background(), object, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "secret resource version changed",
			change: func(t *testing.T, dynamic *fake.FakeDynamicClient) {
				t.Helper()
				secret, err := dynamic.Resource(secretGVR).Namespace(projectLLMSecretNamespace).Get(context.Background(), "primary-key", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				secret.SetResourceVersion("43")
				if _, err := dynamic.Resource(secretGVR).Namespace(projectLLMSecretNamespace).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, dynamic, connection, secret := newConnectionProbeTestClient(t, "https://provider.example/v1", aiv1alpha1.ModelProviderConnectionStatus{})
			fingerprint, err := ModelProviderConnectionFingerprint(connection, secret)
			if err != nil {
				t.Fatal(err)
			}
			test.change(t, dynamic)
			published, err := publishModelProviderConnectionProbe(context.Background(), c, newModelProviderConnectionProbeTarget(connection, fingerprint), modelProviderConnectionProbeOutcome{
				reachable: metav1.ConditionTrue, reason: "Reachable", message: "The provider endpoint answered model discovery.",
			}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if published {
				t.Fatal("stale probe result was published after the connection or Secret changed")
			}
			for _, action := range dynamic.Actions() {
				if action.GetVerb() == "update" && action.GetResource().Resource == modelProviderConnectionsGVR.Resource && action.GetSubresource() == "status" {
					t.Fatal("stale probe result issued a status write")
				}
			}
		})
	}
}

func TestSavedConnectionDiscoveryPublishesAuthorizedProbeStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("discovery path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer probe-test-api-key" {
			t.Errorf("discovery authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer upstream.Close()
	client, dynamic, _, _ := newConnectionProbeTestClient(t, upstream.URL+"/v1", aiv1alpha1.ModelProviderConnectionStatus{})
	useChecks := 0
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
			useChecks++
			if attrs.Group != modelProviderConnectionGVR.Group || attrs.Resource != modelProviderConnectionGVR.Resource || attrs.Name != "primary" || attrs.Verb != "use" {
				t.Errorf("connection use authorization = %#v", attrs)
			}
			return true, nil
		},
		llmDiscoveryHTTPClient: upstream.Client(),
	}
	request := projectLLMDiscoveryRequest(t, `{}`)
	request = mux.SetURLVars(request, map[string]string{"name": "primary"})
	response := httptest.NewRecorder()
	server.discoverModelProviderConnection(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("saved connection discovery returned %d: %s", response.Code, response.Body.String())
	}
	if useChecks != 1 {
		t.Fatalf("use reviews = %d, want one", useChecks)
	}
	stored, err := dynamic.Resource(modelProviderConnectionsGVR).Get(context.Background(), "primary", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var connection aiv1alpha1.ModelProviderConnection
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(stored.Object, &connection); err != nil {
		t.Fatal(err)
	}
	secret, err := dynamic.Resource(secretGVR).Namespace(projectLLMSecretNamespace).Get(context.Background(), "primary-key", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := ModelProviderConnectionFingerprint(&connection, secret)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status.LastProbeFingerprint != fingerprint || connection.Status.LastProbeTime == nil {
		t.Fatalf("saved discovery was not bound to the current fingerprint: %#v", connection.Status)
	}
	if len(connection.Status.Models) != 1 || connection.Status.Models[0] != "model-a" {
		t.Fatalf("saved discovery models = %#v", connection.Status.Models)
	}
	if reachable := apiMeta.FindStatusCondition(connection.Status.Conditions, "Reachable"); reachable == nil || reachable.Status != metav1.ConditionTrue {
		t.Fatalf("saved discovery Reachable condition = %#v", reachable)
	}
}

func TestSavedConnectionProbeRejectsDeletingConnectionBeforeSecretRead(t *testing.T) {
	client, dynamic, connection, _ := newConnectionProbeTestClient(t, "https://provider.example/v1", aiv1alpha1.ModelProviderConnectionStatus{})
	now := metav1.Now()
	connection.DeletionTimestamp = &now
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dynamic.Resource(modelProviderConnectionsGVR).Update(context.Background(), &unstructured.Unstructured{Object: object}, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		integrationAccessReviewer: func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) {
			t.Fatal("deleting connection reached a `use` review")
			return false, nil
		},
	}
	request := projectLLMDiscoveryRequest(t, `{}`)
	request = mux.SetURLVars(request, map[string]string{"name": "primary"})
	response := httptest.NewRecorder()
	server.discoverModelProviderConnection(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("deleting connection returned %d: %s", response.Code, response.Body.String())
	}
	for _, action := range dynamic.Actions() {
		if action.GetVerb() == "get" && action.GetResource().Resource == secretGVR.Resource {
			t.Fatal("deleting connection probe read its key Secret")
		}
	}
}

func TestStudioSavedConnectionDiscoveryPublishesAuthorizedProbeStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer probe-test-api-key" {
			t.Errorf("saved draft discovery authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"saved-model"}]}`))
	}))
	defer upstream.Close()
	client, dynamic, connection, secret := newConnectionProbeTestClient(t, upstream.URL+"/v1", aiv1alpha1.ModelProviderConnectionStatus{})
	fingerprint, err := ModelProviderConnectionFingerprint(connection, secret)
	if err != nil {
		t.Fatal(err)
	}
	useChecks := 0
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
			useChecks++
			if attrs.Name != "primary" || attrs.Verb != "use" {
				t.Errorf("saved draft connection use authorization = %#v", attrs)
			}
			return true, nil
		},
		llmDiscoveryHTTPClient: upstream.Client(),
	}
	body, err := json.Marshal(DiscoverProjectLLMModelsRequest{
		Provider: defaultProjectLLMProvider, BaseURL: upstream.URL + "/v1",
		ExistingConnectionID: "primary", ConnectionFingerprint: fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.discoverProjectLLMModels(response, projectLLMDiscoveryRequest(t, string(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("saved connection draft discovery returned %d: %s", response.Code, response.Body.String())
	}
	if useChecks != 1 {
		t.Fatalf("saved draft use reviews = %d, want one", useChecks)
	}
	stored, err := dynamic.Resource(modelProviderConnectionsGVR).Get(context.Background(), "primary", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got aiv1alpha1.ModelProviderConnection
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(stored.Object, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.LastProbeFingerprint != fingerprint || len(got.Status.Models) != 1 || got.Status.Models[0] != "saved-model" {
		t.Fatalf("saved connection draft probe status = %#v", got.Status)
	}
}

func TestStudioDraftDiscoveryDoesNotPublishTypedCredentialProbe(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer typed-draft-key-1234567890123456" {
			t.Errorf("typed draft authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"draft-model"}]}`))
	}))
	defer upstream.Close()
	client, dynamic, _, _ := newConnectionProbeTestClient(t, upstream.URL+"/v1", aiv1alpha1.ModelProviderConnectionStatus{})
	useChecks := 0
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		integrationAccessReviewer: func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) {
			useChecks++
			return false, nil
		},
		llmDiscoveryHTTPClient: upstream.Client(),
	}
	body, err := json.Marshal(DiscoverProjectLLMModelsRequest{
		Provider: defaultProjectLLMProvider, BaseURL: upstream.URL + "/v1",
		APIKey: "typed-draft-key-1234567890123456", ExistingConnectionID: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.discoverProjectLLMModels(response, projectLLMDiscoveryRequest(t, string(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("typed draft discovery returned %d: %s", response.Code, response.Body.String())
	}
	if useChecks != 0 {
		t.Fatalf("typed draft discovery performed %d saved-key use reviews", useChecks)
	}
	for _, action := range dynamic.Actions() {
		if action.GetVerb() == "update" && action.GetResource().Resource == modelProviderConnectionsGVR.Resource && action.GetSubresource() == "status" {
			t.Fatal("typed draft key published saved connection status")
		}
	}
}
