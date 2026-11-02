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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

func TestOrganizationBootstrapUsesHubCredentialOnlyAtOriginalOrigin(t *testing.T) {
	calls := 0
	redirectCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectCalls++ }))
	defer target.Close()
	redirect := false
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/prefix/api/providers/custom/bootstrap" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer provider-fixture" {
			t.Error("missing provider authentication")
		}
		if redirect {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return
		}
		var body OrganizationBootstrap
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.CatalogEntry.GetName() != "custom" || body.APIExport.GetName() != "custom.example" || len(body.Schemas) != 1 {
			t.Error("missing declarations")
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer hub.Close()
	dir := t.TempDir()
	catalog := filepath.Join(dir, "catalog.yaml")
	if err := os.WriteFile(catalog, []byte("apiVersion: providers.railgrid.ai/v1alpha1\nkind: CatalogEntry\nmetadata:\n  name: custom\n"), 0600); err != nil {
		t.Fatal(err)
	}
	schemaFile := filepath.Join(dir, "schema.yaml")
	if err := os.WriteFile(schemaFile, []byte("apiVersion: apis.kcp.io/v1alpha1\nkind: APIResourceSchema\nmetadata:\n  name: v1.records.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Catalog belongs outside the schemas directory in real charts.
	schemaDir := filepath.Join(dir, "kcp")
	if err := os.Mkdir(schemaDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(schemaFile, filepath.Join(schemaDir, "schema.yaml")); err != nil {
		t.Fatal(err)
	}
	opts := Options{Config: &rest.Config{Host: hub.URL + "/prefix/clusters/provider-cluster", BearerToken: "provider-fixture"}, CatalogEntryFile: catalog, KCPDir: schemaDir}
	export := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExport", "metadata": map[string]any{"name": "custom.example"}}}
	if err := bootstrapOrganization(context.Background(), opts, export, "root:railgrid:tenants:org:providers:custom"); err != nil {
		t.Fatal(err)
	}
	redirect = true
	if err := bootstrapOrganization(context.Background(), opts, export, "root:railgrid:tenants:org:providers:custom"); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls != 2 || redirectCalls != 0 {
		t.Fatal("credential followed redirect")
	}
}
