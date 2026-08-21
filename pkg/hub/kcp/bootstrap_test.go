/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*/

package kcp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/util/identity"
)

// grantRESTServer is a deliberately small HTTP API server for exercising the
// real dynamic-client implementation in EnsureProviderEdgeProxyGrant. Using
// an HTTP server here keeps the Bootstrapper in the test path while avoiding a
// dependency on a live kcp instance.
type grantRESTServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	objects map[string]map[string]any
}

func TestEnsureProviderEdgeProxyGrantCreatedTransitions(t *testing.T) {
	const (
		orgUUID      = "org-123"
		wsUUID       = "ws-123"
		providerName = "app-studio"
	)
	subject := identity.QualifiedServiceAccount("provider-cluster", "provider", "provider")

	t.Run("fresh grant then idempotent", func(t *testing.T) {
		api := newGrantRESTServer(t)
		bootstrapper := NewBootstrapper(api.config())

		created, err := bootstrapper.EnsureProviderEdgeProxyGrant(context.Background(), orgUUID, wsUUID, providerName, subject)
		if err != nil {
			t.Fatalf("create grant: %v", err)
		}
		if !created {
			t.Fatal("first grant call created=false, want true")
		}
		name := edgeProxyGrantName(providerName)
		api.mu.Lock()
		_, roleExists := api.objects["clusterroles/"+name]
		_, bindingExists := api.objects["clusterrolebindings/"+name]
		api.mu.Unlock()
		if !roleExists || !bindingExists {
			t.Fatalf("first grant objects: role=%t binding=%t, want both", roleExists, bindingExists)
		}

		created, err = bootstrapper.EnsureProviderEdgeProxyGrant(context.Background(), orgUUID, wsUUID, providerName, subject)
		if err != nil {
			t.Fatalf("idempotent grant: %v", err)
		}
		if created {
			t.Fatal("idempotent grant call created=true, want false")
		}
	})

	t.Run("preexisting role missing binding", func(t *testing.T) {
		api := newGrantRESTServer(t)
		bootstrapper := NewBootstrapper(api.config())
		name := edgeProxyGrantName(providerName)
		if created, err := bootstrapper.EnsureProviderEdgeProxyGrant(context.Background(), orgUUID, wsUUID, providerName, subject); err != nil || !created {
			t.Fatalf("seed grant = (%t, %v), want (true, nil)", created, err)
		}
		api.delete("clusterrolebindings", name)

		created, err := bootstrapper.EnsureProviderEdgeProxyGrant(context.Background(), orgUUID, wsUUID, providerName, subject)
		if err != nil {
			t.Fatalf("recover missing binding: %v", err)
		}
		if !created {
			t.Fatal("recovery with missing binding created=false, want true")
		}
		created, err = bootstrapper.EnsureProviderEdgeProxyGrant(context.Background(), orgUUID, wsUUID, providerName, subject)
		if err != nil {
			t.Fatalf("post-recovery idempotent grant: %v", err)
		}
		if created {
			t.Fatal("post-recovery idempotent grant created=true, want false")
		}
	})

	t.Run("preexisting binding missing role", func(t *testing.T) {
		api := newGrantRESTServer(t)
		bootstrapper := NewBootstrapper(api.config())
		name := edgeProxyGrantName(providerName)
		if created, err := bootstrapper.EnsureProviderEdgeProxyGrant(context.Background(), orgUUID, wsUUID, providerName, subject); err != nil || !created {
			t.Fatalf("seed grant = (%t, %v), want (true, nil)", created, err)
		}
		api.delete("clusterroles", name)

		created, err := bootstrapper.EnsureProviderEdgeProxyGrant(context.Background(), orgUUID, wsUUID, providerName, subject)
		if err != nil {
			t.Fatalf("recover missing role: %v", err)
		}
		if !created {
			t.Fatal("recovery with missing role created=false, want true")
		}
		created, err = bootstrapper.EnsureProviderEdgeProxyGrant(context.Background(), orgUUID, wsUUID, providerName, subject)
		if err != nil {
			t.Fatalf("post-recovery idempotent grant: %v", err)
		}
		if created {
			t.Fatal("post-recovery idempotent grant created=true, want false")
		}
	})
}

func newGrantRESTServer(t *testing.T) *grantRESTServer {
	t.Helper()
	s := &grantRESTServer{objects: map[string]map[string]any{}}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for grant API: %v", err)
	}
	s.server = httptest.NewUnstartedServer(s)
	s.server.Listener = listener
	s.server.Start()
	t.Cleanup(s.server.Close)
	return s
}

func (s *grantRESTServer) config() *rest.Config {
	return &rest.Config{Host: s.server.URL}
}

func (s *grantRESTServer) delete(resource, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, resource+"/"+name)
}

func (s *grantRESTServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resource, name, ok := grantResourceAndName(r.URL.Path)
	if !ok {
		writeGrantStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, "resource path not recognized")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := resource + "/" + name
	switch r.Method {
	case http.MethodPost:
		var object map[string]any
		if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
			writeGrantStatus(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		metadata, _ := object["metadata"].(map[string]any)
		objectName, _ := metadata["name"].(string)
		if objectName == "" {
			writeGrantStatus(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, "metadata.name is required")
			return
		}
		key = resource + "/" + objectName
		if _, exists := s.objects[key]; exists {
			writeGrantStatus(w, http.StatusConflict, metav1.StatusReasonAlreadyExists, "already exists")
			return
		}
		s.objects[key] = object
		writeGrantObject(w, http.StatusCreated, object)
	case http.MethodGet:
		object, exists := s.objects[key]
		if !exists || name == "" {
			writeGrantStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, "not found")
			return
		}
		writeGrantObject(w, http.StatusOK, object)
	case http.MethodPut:
		if _, exists := s.objects[key]; !exists || name == "" {
			writeGrantStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, "not found")
			return
		}
		var object map[string]any
		if err := json.NewDecoder(r.Body).Decode(&object); err != nil {
			writeGrantStatus(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		s.objects[key] = object
		writeGrantObject(w, http.StatusOK, object)
	default:
		writeGrantStatus(w, http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed, "method not supported")
	}
}

func grantResourceAndName(path string) (resource, name string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part != "clusterroles" && part != "clusterrolebindings" {
			continue
		}
		if i+1 < len(parts) {
			return part, parts[i+1], true
		}
		return part, "", true
	}
	return "", "", false
}

func writeGrantObject(w http.ResponseWriter, code int, object map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(object)
}

func writeGrantStatus(w http.ResponseWriter, code int, reason metav1.StatusReason, message string) {
	writeGrantStatusObject(w, code, &metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Reason:   reason,
		Message:  message,
		Code:     int32(code),
	})
}

func writeGrantStatusObject(w http.ResponseWriter, code int, status *metav1.Status) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(status)
}

func TestEnsureBuiltinCatalogEntries_DoesNotTouchChartOwnedEntry(t *testing.T) {
	const providerName = "chart-owned-test"
	if _, ok := providers.BuiltinByName(providerName); !ok {
		providers.RegisterBuiltin(providers.BuiltinSpec{
			Name:        providerName,
			DisplayName: "Chart Owned Test",
		})
	}

	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		apiBindingGVR:   "APIBindingList",
		catalogEntryGVR: "CatalogEntryList",
	})

	if _, err := dyn.Resource(apiBindingGVR).Create(context.Background(), &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata": map[string]interface{}{
			"name": "providers.faros.sh",
		},
		"status": map[string]interface{}{
			"phase": "Bound",
		},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding APIBinding: %v", err)
	}

	original := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "providers.faros.sh/v1alpha1",
		"kind":       "CatalogEntry",
		"metadata": map[string]interface{}{
			"name": providerName,
		},
		"spec": map[string]interface{}{
			"displayName": "Provider from Chart",
			"ui": map[string]interface{}{
				"url": "/services/chart-owned-test",
			},
		},
	}}
	if _, err := dyn.Resource(catalogEntryGVR).Create(context.Background(), original, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding CatalogEntry: %v", err)
	}

	if err := ensureBuiltinCatalogEntries(context.Background(), dyn, []string{providerName}); err != nil {
		t.Fatalf("ensureBuiltinCatalogEntries: %v", err)
	}

	got, err := dyn.Resource(catalogEntryGVR).Get(context.Background(), providerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CatalogEntry: %v", err)
	}
	if got.GetAnnotations()[builtinAnnotation] == "true" {
		t.Fatal("expected chart-owned entry to remain unannotated")
	}
	displayName, found, err := unstructured.NestedString(got.Object, "spec", "displayName")
	if err != nil {
		t.Fatalf("reading displayName: %v", err)
	}
	if !found || displayName != "Provider from Chart" {
		t.Fatalf("displayName = %q, want chart-owned value", displayName)
	}
}

// binding builds an unstructured APIBinding with the given deletion state for
// deletionBlockedMessage tests.
func binding(deleting bool, conditions []any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "infrastructure"},
		"status":     map[string]any{"phase": "Bound"},
	}
	if deleting {
		obj["metadata"].(map[string]any)["deletionTimestamp"] = "2026-08-21T10:00:00Z"
	}
	if conditions != nil {
		obj["status"].(map[string]any)["conditions"] = conditions
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestDeletionBlockedMessage(t *testing.T) {
	const finalizerMsg = "Some content in the workspace has finalizers remaining: instances.infrastructure.faros.sh in 3 resource instances"

	tests := []struct {
		name string
		item *unstructured.Unstructured
		want string
	}{
		{
			name: "not terminating",
			item: binding(false, nil),
			want: "",
		},
		{
			name: "terminating without conditions yet",
			item: binding(true, nil),
			want: "",
		},
		{
			name: "terminating, delete condition still true",
			item: binding(true, []any{
				map[string]any{"type": "BindingResourceDeleteSuccess", "status": "True"},
			}),
			want: "",
		},
		{
			name: "blocked on finalizers",
			item: binding(true, []any{
				map[string]any{"type": "Ready", "status": "True"},
				map[string]any{
					"type":    "BindingResourceDeleteSuccess",
					"status":  "False",
					"reason":  "SomeFinalizersRemain",
					"message": finalizerMsg,
				},
			}),
			want: finalizerMsg,
		},
		{
			name: "blocked with reason only",
			item: binding(true, []any{
				map[string]any{
					"type":   "BindingResourceDeleteSuccess",
					"status": "False",
					"reason": "ResourceDeletionFailed",
				},
			}),
			want: "ResourceDeletionFailed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deletionBlockedMessage(tc.item); got != tc.want {
				t.Fatalf("deletionBlockedMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}
