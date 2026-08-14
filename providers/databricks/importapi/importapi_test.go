// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package importapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestListTablesUsesBoundedPageAndPreservesEmptyPageToken(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.URL.Query().Get("max_results"); got != "50" {
			t.Fatalf("max_results = %q, want 50", got)
		}
		if got := request.URL.Query().Get("page_token"); got != "first" {
			t.Fatalf("page_token = %q, want first", got)
		}
		for _, key := range []string{"omit_columns", "omit_properties", "omit_username"} {
			if got := request.URL.Query().Get(key); got != "true" {
				t.Fatalf("%s = %q, want true", key, got)
			}
		}
		if got := request.URL.Query().Get("include_manifest_capabilities"); got != "false" {
			t.Fatalf("include_manifest_capabilities = %q, want false", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("Authorization = %q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tables":[],"next_page_token":"second"}`)), Header: make(http.Header)}, nil
	})})
	page, err := client.ListTables(context.Background(), Connection{Host: "https://dbc-example.cloud.databricks.com", AuthType: "pat", Token: "secret"}, "main", "sales", "first")
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(page.Items) != 0 || page.NextPageToken != "second" {
		t.Fatalf("page = %#v", page)
	}
}

func TestListWarehousesUsesBoundedPageSize(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.URL.Query().Get("page_size"); got != "50" {
			t.Fatalf("page_size = %q, want 50", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"warehouses":[]}`)), Header: make(http.Header)}, nil
	})})
	if _, err := client.ListWarehouses(context.Background(), Connection{Host: "https://dbc-example.cloud.databricks.com", AuthType: "pat", Token: "secret"}, ""); err != nil {
		t.Fatalf("ListWarehouses: %v", err)
	}
}

func TestDiscoveryRejectsOversizedBodyAfterValidJSON(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"warehouses":[]}` + strings.Repeat(" ", maxResponseSize+1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})})
	_, err := client.ListWarehouses(context.Background(), Connection{
		Host:     "https://dbc-example.cloud.databricks.com",
		AuthType: "pat",
		Token:    "secret",
	}, "")
	if err == nil {
		t.Fatal("ListWarehouses accepted a response larger than maxResponseSize")
	}
}

func TestDiscoveryRejectsTrailingJSONOrBytesWithoutEchoingBody(t *testing.T) {
	for _, suffix := range []string{`{"second":"upstream-secret"}`, `upstream-secret`} {
		client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"warehouses":[]}` + suffix)),
				Header:     make(http.Header),
			}, nil
		})})
		_, err := client.ListWarehouses(context.Background(), Connection{
			Host:     "https://dbc-example.cloud.databricks.com",
			AuthType: "pat",
			Token:    "secret",
		}, "")
		if err == nil || strings.Contains(err.Error(), "upstream-secret") {
			t.Fatalf("trailing suffix %q error = %v, want generic rejection", suffix, err)
		}
	}
}

func TestDiscoveryHTTPFailureDoesNotExposeResponseBody(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"token":"upstream-secret"}`)),
			Header:     make(http.Header),
		}, nil
	})})
	_, err := client.ListWarehouses(context.Background(), Connection{
		Host:     "https://dbc-example.cloud.databricks.com",
		AuthType: "pat",
		Token:    "secret",
	}, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "upstream-secret") {
		t.Fatalf("discovery error = %v, want generic HTTP 401 without upstream body", err)
	}
}

func TestWorkspaceURLRejectsUntrustedAndInsecureHosts(t *testing.T) {
	for _, raw := range []string{"http://dbc-example.cloud.databricks.com", "https://127.0.0.1", "https://databricks.example.com", "https://dbc-example.cloud.databricks.com/path", "https://dbc-example.cloud.databricks.com:8443"} {
		if _, err := WorkspaceURL(raw); err == nil {
			t.Fatalf("WorkspaceURL(%q) succeeded", raw)
		}
	}
}

func TestListTablesMarksMissingIdentifiersUnsupported(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"tables":[{"name":"orders","catalog_name":"","schema_name":"gold"}]}`)),
			Header:     make(http.Header),
		}, nil
	})})
	page, err := client.ListTables(context.Background(), Connection{Host: "https://dbc-example.cloud.databricks.com", AuthType: "pat", Token: "secret"}, "main", "gold", "")
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Supported || !page.Items[0].Unsupported {
		t.Fatalf("items = %#v, want one unsupported table", page.Items)
	}
	if page.Items[0].UnsupportedReason == "" {
		t.Fatal("unsupported table should explain the missing identifier")
	}
}

func TestListTablesMarksOnlyMetricViewsUnsupportedForQuerySemantics(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{"tables":[
				{"name":"orders","catalog_name":"main","schema_name":"gold","table_type":"MANAGED"},
				{"name":"order_summary","catalog_name":"main","schema_name":"gold","table_type":"VIEW"},
				{"name":"sales_metrics","catalog_name":"main","schema_name":"gold","table_type":"METRIC_VIEW"}
			]}`)),
			Header: make(http.Header),
		}, nil
	})})
	page, err := client.ListTables(context.Background(), Connection{Host: "https://dbc-example.cloud.databricks.com", AuthType: "pat", Token: "secret"}, "main", "gold", "")
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("items = %#v, want deterministic three-item fixture", page.Items)
	}
	for _, index := range []int{0, 1} {
		if !page.Items[index].Supported || page.Items[index].Unsupported || page.Items[index].UnsupportedReason != "" {
			t.Fatalf("ordinary table/view item %d = %#v, want supported", index, page.Items[index])
		}
	}
	metric := page.Items[2]
	if metric.Supported || !metric.Unsupported || metric.UnsupportedReason != metricViewUnsupportedReason {
		t.Fatalf("metric view = %#v, want exact query_table/v1 unsupported classification", metric)
	}
}

func TestNormalizeAndSpecEqual(t *testing.T) {
	request := Request{Kind: KindTable, ConnectionRef: "sales", WarehouseRef: "sql", Items: []Item{{Name: "orders", Catalog: "main", Schema: "gold", Table: "orders"}}}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	item, err := Normalize(request, 0)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	object := &unstructured.Unstructured{Object: map[string]any{"spec": item.Spec}}
	if !SpecEqual(object, item.Spec) {
		t.Fatal("expected exact spec equality")
	}
	expected := make(map[string]any, len(item.Spec))
	for key, value := range item.Spec {
		expected[key] = value
	}
	object.Object["spec"].(map[string]any)["warehouseRef"] = "other"
	if SpecEqual(object, expected) {
		t.Fatal("different spec must conflict")
	}
}

func TestRequestRejectsOversizedBatchAndMixedFields(t *testing.T) {
	request := Request{Kind: KindWarehouse, ConnectionRef: "sales", Items: make([]Item, MaxItems+1)}
	if err := request.Validate(); err == nil {
		t.Fatal("oversized batch succeeded")
	}
	request.Items = []Item{{Name: "sql", WarehouseID: "wh", Catalog: "main"}}
	if _, err := Normalize(request, 0); err == nil {
		t.Fatal("mixed warehouse and table fields succeeded")
	}
}
