// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/importapi"
)

type importFactoryFake struct {
	dyn   dynamic.Interface
	calls []string
}

func (f *importFactoryFake) ResolveConnection(_ context.Context, _, name string) (importapi.Connection, error) {
	f.calls = append(f.calls, "resolve:"+name)
	return importapi.Connection{Host: "https://dbc-example.cloud.databricks.com", AuthType: "pat", Token: "secret"}, nil
}

func (f *importFactoryFake) AuthorizeResource(_ context.Context, _, _, resource, name, verb string) error {
	f.calls = append(f.calls, "authorize:"+resource+":"+name+":"+verb)
	return nil
}

func (f *importFactoryFake) For(_, _ string) (dynamic.Interface, error) {
	f.calls = append(f.calls, "caller-client")
	return f.dyn, nil
}

func TestPreflightTablesRejectsWarehouseFromAnotherConnectionBeforeWrites(t *testing.T) {
	warehouse := &unstructured.Unstructured{Object: map[string]any{"apiVersion": databricksv1alpha1.SchemeGroupVersion.String(), "kind": "Warehouse", "metadata": map[string]any{"name": "sql"}, "spec": map[string]any{"connectionRef": "other"}}}
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme(), warehouse)
	err := preflightTables(context.Background(), dyn, importapi.Request{Kind: importapi.KindTable, ConnectionRef: "sales", WarehouseRef: "sql"})
	if err == nil {
		t.Fatal("cross-connection warehouse passed preflight")
	}
	if typed, ok := err.(*importError); !ok || typed.reason != "ConnectionMismatch" {
		t.Fatalf("error = %#v", err)
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "patch" {
			t.Fatalf("preflight performed write action %s", action.GetVerb())
		}
	}
}

func TestRegisterOneReturnsCreatedExistingAndConflict(t *testing.T) {
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	request := importapi.Request{Kind: importapi.KindWarehouse, ConnectionRef: "sales"}
	item := importapi.NormalizedItem{Index: 0, Name: "sql", Spec: map[string]any{"connectionRef": "sales", "warehouseID": "wh-1"}}
	if result := registerOne(context.Background(), dyn, request, item); result.State != "created" {
		t.Fatalf("created result = %#v", result)
	}
	if result := registerOne(context.Background(), dyn, request, item); result.State != "existing" {
		t.Fatalf("existing result = %#v", result)
	}
	item.Spec["warehouseID"] = "wh-2"
	if result := registerOne(context.Background(), dyn, request, item); result.State != "conflict" {
		t.Fatalf("conflict result = %#v", result)
	}
}

func TestParseDiscoveryQueryRejectsUnknownAndRequiresHierarchy(t *testing.T) {
	if _, err := parseDiscoveryQuery(map[string][]string{"connectionRef": {"sales"}, "unknown": {"x"}}, "tables"); err == nil {
		t.Fatal("unknown query parameter succeeded")
	}
	if _, err := parseDiscoveryQuery(map[string][]string{"connectionRef": {"sales"}, "catalog": {"main"}}, "tables"); err == nil {
		t.Fatal("table query without schema succeeded")
	}
}

func TestRegistrationAuthorizesBeforeAuthorityReadAndReturnsOrderedResults(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databricksv1alpha1.SchemeGroupVersion.String(), "kind": "Warehouse",
		"metadata": map[string]any{"name": "existing"}, "spec": map[string]any{"connectionRef": "sales", "warehouseID": "wh-2"},
	}}
	factory := &importFactoryFake{dyn: fake.NewSimpleDynamicClient(runtime.NewScheme(), existing)}
	handler := NewImportHandler(factory, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/registrations", strings.NewReader(`{"kind":"warehouse","connectionRef":"sales","items":[{"name":"new","warehouseID":"wh-1"},{"name":"existing","warehouseID":"wh-2"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-Tenant", "root:org:workspace")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Results []RegistrationResult `json:"results"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Results) != 2 || payload.Results[0].State != "created" || payload.Results[1].State != "existing" {
		t.Fatalf("results = %#v", payload.Results)
	}
	wantCalls := []string{"authorize:connections:sales:get", "resolve:sales", "authorize:warehouses::get", "authorize:warehouses::create", "caller-client"}
	if strings.Join(factory.calls, "|") != strings.Join(wantCalls, "|") {
		t.Fatalf("calls = %#v, want %#v", factory.calls, wantCalls)
	}
}

func TestRegistrationRejectsDuplicateNamesAndOversizedBodiesBeforeFactoryCalls(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "duplicate names",
			body: `{"kind":"warehouse","connectionRef":"sales","items":[{"name":"same","warehouseID":"wh-1"},{"name":"same","warehouseID":"wh-2"}]}`,
			want: http.StatusBadRequest,
		},
		{
			name: "oversized body",
			body: `{"kind":"warehouse","connectionRef":"sales","items":[{"name":"same","warehouseID":"` + strings.Repeat("x", MaxRequestBytes) + `"}]}`,
			want: http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			factory := &importFactoryFake{dyn: fake.NewSimpleDynamicClient(runtime.NewScheme())}
			handler := NewImportHandler(factory, nil)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/registrations", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer caller")
			request.Header.Set("X-Faros-Cluster", "cluster")
			request.Header.Set("X-Faros-Tenant", "root:org:workspace")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
			if len(factory.calls) != 0 {
				t.Fatalf("factory calls = %#v, want none", factory.calls)
			}
		})
	}
}
