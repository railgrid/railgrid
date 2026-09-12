// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package authority

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestConnectionLookupStaysInRequestedWorkspace(t *testing.T) {
	var endpoint string
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "apiexportendpointslices") {
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "apis.kcp.io/v1alpha1", "kind": "APIExportEndpointSlice", "status": map[string]any{"endpoints": []any{map[string]any{"url": endpoint}}}})
			return
		}
		if r.URL.Path == "/export/clusters/tenant-one/apis/linear.providers.faros.sh/v1alpha1/connections/linear" {
			_, _ = w.Write([]byte(`{"apiVersion":"linear.providers.faros.sh/v1alpha1","kind":"Connection","metadata":{"name":"linear"}}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	endpoint = server.URL + "/export"
	auth := Authority{Config: &rest.Config{Host: server.URL}}
	if _, err := auth.Tenant(context.Background(), "tenant-one", "linear"); err != nil {
		t.Fatal(err)
	}
	paths = nil
	if _, err := auth.Tenant(context.Background(), "tenant-two", "linear"); err == nil {
		t.Fatal("foreign workspace connection resolved")
	}
	for _, path := range paths {
		if strings.Contains(path, "tenant-one") || strings.Contains(path, "/namespaces/") {
			t.Fatalf("lookup escaped workspace: %s", path)
		}
	}
	if _, err := auth.Client(endpoint, "../tenant-one"); err == nil {
		t.Fatal("workspace traversal accepted")
	}
}
