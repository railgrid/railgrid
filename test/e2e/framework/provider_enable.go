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

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var tenantLogicalClusterGVR = schema.GroupVersionResource{
	Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
}

// ProviderPermissionClaim is one {group, resource} accepted by the tenant
// during Enable. Verbs and selectors come from the provider's current catalog
// declaration, so callers cannot ask for more than that declaration grants.
type ProviderPermissionClaim struct {
	Group    string `json:"group"`
	Resource string `json:"resource"`
}

// ProviderComposition is a dependency coordinate accepted during Enable.
type ProviderComposition struct {
	Provider string `json:"provider"`
	Group    string `json:"group"`
	Resource string `json:"resource"`
}

// EnableTenantProvider invokes the Hub's tenant-scoped Enable endpoint. It
// reads the org/workspace UUIDs from the caller's own LogicalCluster path,
// then sends those values in the scope headers that the Hub authorizes.
func EnableTenantProvider(
	t testing.TB,
	hubURL string,
	tenant dynamic.Interface,
	token, provider string,
	acceptedClaims []ProviderPermissionClaim,
	acceptedCompositions []ProviderComposition,
) {
	t.Helper()
	orgUUID, workspaceUUID := tenantScope(t, tenant)
	body, err := json.Marshal(struct {
		AcceptedClaims       []ProviderPermissionClaim `json:"acceptedClaims"`
		AcceptedCompositions []ProviderComposition     `json:"acceptedCompositions,omitempty"`
	}{
		AcceptedClaims:       acceptedClaims,
		AcceptedCompositions: acceptedCompositions,
	})
	if err != nil {
		t.Fatalf("marshal %s Enable request: %v", provider, err)
	}

	path := fmt.Sprintf("/api/orgs/%s/workspaces/%s/providers/%s/enable", orgUUID, workspaceUUID, provider)
	deadline := time.Now().Add(2 * time.Minute)
	var lastStatus int
	var lastBody string
	for time.Now().Before(deadline) {
		lastStatus, lastBody = scopedProviderPost(t, hubURL+path, token, orgUUID, workspaceUUID, body)
		if lastStatus == http.StatusOK || lastStatus == http.StatusCreated {
			return
		}
		// The catalog and dependency bindings settle asynchronously after the
		// test fixtures are applied. Retry only those transient lookup states;
		// authorization and validation failures should fail immediately.
		if lastStatus != http.StatusNotFound && lastStatus != http.StatusConflict {
			t.Fatalf("enable provider %s = %d: %s", provider, lastStatus, lastBody)
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("enable provider %s never succeeded: last status %d: %s", provider, lastStatus, lastBody)
}

// DisableTenantProvider invokes the Hub's tenant-scoped Disable endpoint. The
// endpoint removes the tenant APIBinding with Hub credentials and is
// idempotent, including when the binding is already absent.
func DisableTenantProvider(t testing.TB, hubURL string, tenant dynamic.Interface, token, provider string) {
	t.Helper()
	orgUUID, workspaceUUID := tenantScope(t, tenant)
	path := fmt.Sprintf("/api/orgs/%s/workspaces/%s/providers/%s/disable", orgUUID, workspaceUUID, provider)
	status, body := scopedProviderPost(t, hubURL+path, token, orgUUID, workspaceUUID, nil)
	if status != http.StatusNoContent {
		t.Fatalf("disable provider %s = %d: %s", provider, status, body)
	}
}

func tenantScope(t testing.TB, tenant dynamic.Interface) (orgUUID, workspaceUUID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logical, err := tenant.Resource(tenantLogicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read tenant LogicalCluster: %v", err)
	}
	path := logical.GetAnnotations()["kcp.io/path"]
	rest, ok := strings.CutPrefix(path, "root:railgrid:tenants:")
	if !ok {
		t.Fatalf("tenant LogicalCluster path = %q, want root:railgrid:tenants:<org>:<workspace>", path)
	}
	orgUUID, workspaceUUID, found := strings.Cut(rest, ":")
	if !found || orgUUID == "" || workspaceUUID == "" || strings.Contains(workspaceUUID, ":") {
		t.Fatalf("tenant LogicalCluster path = %q, want root:railgrid:tenants:<org>:<workspace>", path)
	}
	return orgUUID, workspaceUUID
}

func scopedProviderPost(t testing.TB, url, token, orgUUID, workspaceUUID string, body []byte) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("create provider request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Railgrid-Org", orgUUID)
	req.Header.Set("X-Railgrid-Workspace", workspaceUUID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := RESTClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close() //nolint:errcheck
	var raw []byte
	if resp.Body != nil {
		raw, _ = io.ReadAll(resp.Body)
	}
	return resp.StatusCode, string(raw)
}
