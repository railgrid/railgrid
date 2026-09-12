// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
)

func TestCallerAuthorizationIsEnforcedByTenantAPI(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/clusters/tenant-one/apis/linear.providers.faros.sh/v1alpha1/namespaces/default/operations" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer caller-token" {
			t.Error("caller identity replaced")
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer upstream.Close()
	s := Server{HubURL: upstream.URL}
	r := httptest.NewRequest("POST", "/api/operations", nil)
	input := Submit{Namespace: "default", Name: "op-one", Spec: api.OperationSpec{Connection: "linear", Action: "teams"}}
	if _, err := s.Submit(context.Background(), r, input); err == nil || calls != 0 {
		t.Fatal("anonymous request reached API")
	}
	r.Header.Set("X-Faros-Cluster", "tenant-one")
	r.Header.Set("Authorization", "Bearer caller-token")
	if _, err := s.Submit(context.Background(), r, input); err == nil || calls != 1 {
		t.Fatal("tenant denial bypassed")
	}
	r.Header.Set("X-Faros-Cluster", "../tenant-two")
	if _, err := s.Submit(context.Background(), r, input); err == nil || calls != 1 {
		t.Fatal("cluster traversal reached API")
	}
}
