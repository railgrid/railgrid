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

package install

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

func organizationBootstrapOptions(t *testing.T, host string) Options {
	t.Helper()
	dir := writeKCPDir(t, `apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: demo.providers.railgrid.ai
spec:
  resources:
  - group: demo.railgrid.ai
    name: widgets
    schema: v1.widgets.demo.railgrid.ai
    storage:
      crd: {}
`, map[string]string{"widgets.demo.railgrid.ai.yaml": schemaYAML("v1.widgets.demo.railgrid.ai")})
	entry := map[string]any{
		"apiVersion": "providers.railgrid.ai/v1alpha1",
		"kind":       "CatalogEntry",
		"metadata":   map[string]any{"name": "demo"},
	}
	entryBytes, err := yaml.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	entryPath := filepath.Join(t.TempDir(), "manifest.yaml")
	if err := os.WriteFile(entryPath, entryBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	apiGroup := "cache.kcp.io"
	privateCRD := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "widgets.demo.railgrid.ai"},
	}}
	return Options{
		Config:                        &rest.Config{Host: host, BearerToken: "bootstrap-token"},
		ExportName:                    "demo.providers.railgrid.ai",
		KCPDir:                        dir,
		CatalogEntryFile:              entryPath,
		DataPlaneURL:                  "https://untrusted-relay.example.invalid",
		PrivateCRDs:                   []*unstructured.Unstructured{privateCRD},
		APIExportEndpointSliceAliases: []string{"demo"},
		VirtualStorage: map[schema.GroupResource]corev1.TypedLocalObjectReference{
			{Group: "demo.railgrid.ai", Resource: "widgets"}: {APIGroup: &apiGroup, Kind: "ClusterCachedResourceEndpointSlice", Name: "widgets-cache"},
		},
	}
}

func TestBootstrapOrganizationPostsDeclarationWithHubAuthentication(t *testing.T) {
	var received OrganizationBootstrap
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/providers/demo/bootstrap" {
			t.Errorf("request = %s %s, want POST bootstrap endpoint", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer bootstrap-token" {
			t.Errorf("Authorization = %q, want restricted kubeconfig bearer", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if strings.Contains(string(body), "untrusted-relay.example.invalid") {
			t.Error("SDK sent its local data-plane destination to the Hub")
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("decode bootstrap body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	options := organizationBootstrapOptions(t, server.URL+"/clusters/root:railgrid:tenants:org-a:providers:demo")
	if err := bootstrapOrganization(context.Background(), options, mustLoadTestExport(t, options.KCPDir), "root:railgrid:tenants:org-a:providers:demo"); err != nil {
		t.Fatal(err)
	}
	if received.CatalogEntry == nil || received.APIExport == nil || len(received.Schemas) != 1 || len(received.PrivateCRDs) != 1 || len(received.VirtualStorage) != 1 {
		t.Fatalf("incomplete declaration sent to Hub: %+v", received)
	}
	if len(received.APIExportEndpointSliceAliases) != 1 || received.APIExportEndpointSliceAliases[0] != "demo" {
		t.Fatalf("endpoint slice aliases = %v", received.APIExportEndpointSliceAliases)
	}
}

func TestBootstrapOrganizationRejectsUnsafeCacheReference(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	options := organizationBootstrapOptions(t, server.URL+"/clusters/root:railgrid:tenants:org-a:providers:demo")
	wrongGroup := "evil.example"
	options.VirtualStorage[schema.GroupResource{Group: "demo.railgrid.ai", Resource: "widgets"}] = corev1.TypedLocalObjectReference{
		APIGroup: &wrongGroup,
		Kind:     "InternalURL",
		Name:     "metadata",
	}
	err := bootstrapOrganization(context.Background(), options, mustLoadTestExport(t, options.KCPDir), "root:railgrid:tenants:org-a:providers:demo")
	if err == nil || !strings.Contains(err.Error(), "local cache endpoint slice") {
		t.Fatalf("unsafe virtual storage reference error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("sent %d requests before validating virtual storage", calls)
	}
}

func TestBootstrapOrganizationDoesNotForwardCredentialAcrossRedirect(t *testing.T) {
	redirectCalls := 0
	redirect := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectCalls++ }))
	defer redirect.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirect.URL+"/collect", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	options := organizationBootstrapOptions(t, server.URL+"/clusters/root:railgrid:tenants:org-a:providers:demo")
	err := bootstrapOrganization(context.Background(), options, mustLoadTestExport(t, options.KCPDir), "root:railgrid:tenants:org-a:providers:demo")
	if err == nil || !strings.Contains(err.Error(), "returned 307") {
		t.Fatalf("redirect response error = %v", err)
	}
	if redirectCalls != 0 {
		t.Fatalf("followed Hub redirect %d times", redirectCalls)
	}
}

func mustLoadTestExport(t *testing.T, dir string) *unstructured.Unstructured {
	t.Helper()
	export, err := LoadAPIExport(filepath.Join(dir, APIExportFileName))
	if err != nil {
		t.Fatal(err)
	}
	return export
}
