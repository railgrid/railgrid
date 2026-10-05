// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/tenantaccess"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant/tenanttest"
)

func TestUsageRollupAttributesHarnessRunByModel(t *testing.T) {
	for _, test := range []struct {
		name       string
		model      string
		wantBucket string
	}{
		{name: "explicit harness model", model: "gpt-6-luna", wantBucket: "gpt-6-luna"},
		{name: "harness default", wantBucket: "codex-smoke (harness default)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := map[string]any{
				"type": "harness",
				"harness": map[string]any{
					"credentialRef": "codex-smoke",
					"model":         test.model,
				},
			}
			got := runUsageRollup(t, backend)
			if len(got.ByModel) != 1 || got.ByModel[0].Key != test.wantBucket {
				t.Fatalf("byModel = %+v, want one %q bucket", got.ByModel, test.wantBucket)
			}
			if got.ByModel[0].Runs != 1 || got.ByModel[0].InputTokens != 12 || got.ByModel[0].OutputTokens != 34 {
				t.Fatalf("harness usage bucket = %+v, want one run with 12/34 tokens", got.ByModel[0])
			}
		})
	}
}

func runUsageRollup(t *testing.T, backend map[string]any) usageResponse {
	t.Helper()
	workspace := tenanttest.New()
	workspace.Add(unstructuredAgent(backend))
	workspaceServer := httptest.NewServer(workspace)
	t.Cleanup(workspaceServer.Close)
	provider, err := tenantaccess.NewDynamicClient(workspaceServer.URL, "c1", "provider-token", false)
	if err != nil {
		t.Fatalf("create provider client: %v", err)
	}

	scope := store.Scope{OrgUUID: "org1", WorkspaceUUID: "ws1", AgentName: "coder"}
	st := store.NewMemoryStore()
	now := time.Now().UTC()
	if err := st.SaveRun(t.Context(), scope, store.Run{
		ID: "run-harness", AgentName: "coder", Trigger: "chat", Phase: store.RunPhaseSucceeded,
		InputTokens: 12, OutputTokens: 34, USDMicros: 56,
		CreatedAt: now, UpdatedAt: now, StartedAt: &now, FinishedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{store: st}
	request := dataplane.Request{ClusterID: "c1", Resource: "agents", Name: "coder", Verb: "usage"}
	ctx := dataplane.WithProxiedIdentity(t.Context(), dataplane.ProxiedIdentity{User: "alice"})
	id := identity{tenant: "c1", clusterID: "c1", orgUUID: "org1", workspaceUUID: "ws1", user: "alice"}
	r := httptest.NewRequest(http.MethodGet,
		"/clusters/c1/apis/agents.railgrid.ai/v1alpha1/agents/coder/usage", nil)
	r = r.WithContext(withGate(ctx, &gateInfo{request: request, provider: provider, identity: id}))
	r.SetPathValue("name", "coder")
	w := httptest.NewRecorder()
	s.usageRollup(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("usage status = %d: %s", w.Code, w.Body.String())
	}

	var got usageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode usage response: %v", err)
	}
	return got
}

func unstructuredAgent(backend map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Agent",
		"metadata":   map[string]any{"name": "coder"},
		"spec":       map[string]any{"backend": backend},
	}}
}
