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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestConnectionSecretPolicyAppliesToLiveAndSnapshotReads(t *testing.T) {
	for _, tc := range []struct {
		name, key, owner string
		allowed          bool
	}{
		{"marked apiKey", "apiKey", "app-studio", true},
		{"default apiKey", "", "app-studio", true},
		{"unmarked apiKey", "apiKey", "", false},
		{"foreign owner", "apiKey", "another-provider", false},
		{"arbitrary entry on marked Secret", "token", "app-studio", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c, dynamic, connection, secret := newConnectionProbeTestClient(t, "https://provider.invalid/v1", aiv1alpha1.ModelProviderConnectionStatus{})
			if _, err := dynamic.Resource(studioResource.GVR).Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": aiv1alpha1.SchemeGroupVersion.String(), "kind": "Studio", "metadata": map[string]any{"name": aiv1alpha1.StudioName}}}, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			connection.Spec.SecretKey = tc.key
			secret.SetLabels(map[string]string{aiv1alpha1.ModelProviderConnectionSecretOwnerLabel: tc.owner})
			encoded, err := runtime.DefaultUnstructuredConverter.ToUnstructured(connection)
			if err != nil {
				t.Fatal(err)
			}
			object, err := dynamic.Resource(modelProviderConnectionsGVR).Get(ctx, connection.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			object.Object = encoded
			if _, err = dynamic.Resource(modelProviderConnectionsGVR).Update(ctx, object, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = dynamic.Resource(secretGVR).Namespace(projectLLMSecretNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			_, _, err = readModelProviderConnectionSecret(ctx, c, connection)
			if (err == nil) != tc.allowed {
				t.Fatalf("live read allowed=%v, want %v", err == nil, tc.allowed)
			}
			_, err = ModelProviderConnectionSnapshot(connection, secret)
			if (err == nil) != tc.allowed {
				t.Fatalf("snapshot mint allowed=%v, want %v", err == nil, tc.allowed)
			}
			snapshot := &aiv1alpha1.StudioLLMConnectionSnapshot{ConnectionRef: connection.Name, ConnectionUID: string(connection.UID), SecretRef: &aiv1alpha1.StudioLLMSecretRef{Name: secret.GetName()}, SecretUID: string(secret.GetUID()), SecretResourceVersion: secret.GetResourceVersion(), ConnectionFingerprint: "recorded-fingerprint", SecretKey: tc.key}
			_, err = readConnectionSnapshotCredential(ctx, c, snapshot)
			if (err == nil) != tc.allowed {
				t.Fatalf("snapshot read allowed=%v, want %v: %v", err == nil, tc.allowed, err)
			}
		})
	}
}

func TestSavedConnectionCannotTransmitUnrelatedSecretEntry(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	for _, verb := range []string{"discover", "test"} {
		t.Run(verb, func(t *testing.T) {
			c, dynamic, connection, secret := newConnectionProbeTestClient(t, upstream.URL+"/v1", aiv1alpha1.ModelProviderConnectionStatus{})
			secret.SetLabels(nil)
			secret.Object["data"] = map[string]any{"token": encodeSecretValue("unrelated-synthetic-token")}
			if _, err := dynamic.Resource(secretGVR).Namespace(projectLLMSecretNamespace).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			connection.Spec.SecretKey = "token"
			encoded, err := runtime.DefaultUnstructuredConverter.ToUnstructured(connection)
			if err != nil {
				t.Fatal(err)
			}
			obj, err := dynamic.Resource(modelProviderConnectionsGVR).Get(context.Background(), connection.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			obj.Object = encoded
			if _, err = dynamic.Resource(modelProviderConnectionsGVR).Update(context.Background(), obj, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			s := &Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, projectClientFor: func(identity) (*asclient.Client, error) { return c, nil }, integrationAccessReviewer: func(_ context.Context, _ identity, attrs dataplane.ResourceAttributes) (bool, error) {
				return attrs.Verb == "use", nil
			}, llmDiscoveryHTTPClient: upstream.Client()}
			request := projectLLMDiscoveryRequest(t, `{"model":"synthetic-model"}`)
			if verb == "discover" {
				request = projectLLMDiscoveryRequest(t, `{}`)
			}
			request = mux.SetURLVars(request, map[string]string{"name": connection.Name})
			response := httptest.NewRecorder()
			if verb == "discover" {
				s.discoverModelProviderConnection(response, request)
			} else {
				s.testModelProviderConnection(response, request)
			}
			if response.Code == http.StatusOK {
				t.Fatalf("unrelated Secret accepted: %s", response.Body.String())
			}
			if strings.Contains(response.Body.String(), "unrelated-synthetic-token") {
				t.Fatal("response exposed unrelated Secret")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("unrelated Secret reached upstream %d times", calls)
	}
}
