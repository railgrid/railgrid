// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/mcpserver"
	"github.com/faroshq/provider-databricks/queryapi"
)

func TestMCPQueryUsesDirectActionExecutorWithoutControlPlaneWrites(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := databricksv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Databricks scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	authority := &trackingAuthorityClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		actionTableObject(),
		actionWarehouseObject(),
		actionConnectionObject(),
		actionSecretObject(),
	).Build()}
	executor := &actionTestExecutor{}
	actionExecutor := &ActionExecutor{
		factory:         &ClientFactory{},
		authorityClient: authority,
		identity:        identity{tenantPath: "root:faros:tenants:org:workspace", clusterID: "cluster-a", token: "caller-token"},
		executor:        executor,
		authorizer:      &actionTestAuthorizer{},
	}
	srv := httptest.NewServer(mcpserver.NewHandler(mcpserver.Deps{
		TableResolver:                 queryapi.UnavailableResolver{Message: "MCP query must not use the table resolver"},
		ActionExecutor:                actionExecutor,
		DisableLocalhostMCPProtection: true,
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		srv.URL,
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_table","arguments":{"actionVersion":"v1","tableRef":"taxi-trips","columns":["trip_id"],"limit":1}}}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST tools/call: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), `"trip_id"`) {
		t.Fatalf("MCP response missing direct action result: %s", string(body))
	}
	if executor.calls != 1 {
		t.Fatalf("direct action backend calls = %d, want 1", executor.calls)
	}
	if authority.creates != 0 {
		t.Fatalf("MCP query created control-plane objects = %d, want 0", authority.creates)
	}
}
