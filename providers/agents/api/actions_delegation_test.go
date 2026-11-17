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

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
)

// connectionBoundAgentCallers is a workspace whose agent runs on a model bound
// to a ModelProviderConnection, and whose caller may `get` the agent but not
// `use` the connection: exactly an App Studio Project's workload identity,
// which is granted the agent's actions and nothing else.
func connectionBoundAgentCallers(agent string) *conformance.FakeCallers {
	agentObj := agentObject(agent)
	agentObj.Object["spec"] = map[string]any{
		"displayName": agent,
		"backend":     map[string]any{"type": "model", "model": map[string]any{"credentials": map[string]any{"chat": "bound-model"}}},
	}
	credential := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.ModelCredentialGVR.GroupVersion().String(),
		"kind":       "ModelCredential",
		"metadata":   map[string]any{"name": "bound-model"},
		"spec":       map[string]any{"connectionRef": "account", "model": "test-model"},
	}}
	connection := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.ModelProviderConnectionGVR.GroupVersion().String(),
		"kind":       "ModelProviderConnection",
		"metadata":   map[string]any{"name": "account"},
		"spec": map[string]any{
			"provider": "openai-compatible", "baseURL": "http://127.0.0.1:1/v1",
			"secretRef": map[string]any{"name": "account-key"}, "secretKey": "apiKey",
		},
	}}
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": "account-key", "namespace": llm.SecretNamespace, "labels": map[string]any{"railgrid.ai/owner": "agents"}},
		"data":     map[string]any{"apiKey": "c2stdGVzdA=="},
	}}
	return &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObj, credential, connection, secret},
		Allow:   func(a conformance.Attributes) bool { return a.Verb == "get" && a.Resource == "agents" },
	}
}

// TestStartRunDelegatesModelConnectionUse is the regression for the first live
// App Studio → Agents call: start-run admitted the run, and the run then failed
// at once with "use permission is required", because the Project's identity
// stayed on the run's context and was asked for `use` on the connection. A
// start-run run spends the agent's configured models under the same delegation
// as a Schedule or Trigger run. The `run` verb is unchanged and still asks the
// caller, which is what proves the fixture reaches the check at all.
func TestStartRunDelegatesModelConnectionUse(t *testing.T) {
	const agent = "researcher"
	s := newGatedTestServer(t, connectionBoundAgentCallers(agent))
	scope := actionScope(agent)

	settle := func(runID string) string {
		t.Helper()
		run, settled := s.waitForRun(t.Context(), scope, runID, 10*time.Second)
		if !settled {
			t.Fatalf("run %s did not settle", runID)
		}
		return run.Message
	}

	// The attended verb: the caller lacks `use`, so the run is refused.
	body := `{"task":"Research acme.example","idempotencyKey":"verb-1"}`
	rec := httptest.NewRecorder()
	gatedHandler(t, s).ServeHTTP(rec, stamped(http.MethodPost, verbBase(conformanceCluster, "agents", agent)+"/run", strings.NewReader(body), conformanceUser))
	var verb invokeRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &verb); err != nil || verb.RunID == "" {
		t.Fatalf("run verb → %d %s", rec.Code, rec.Body.String())
	}
	if msg := settle(verb.RunID); !strings.Contains(msg, "use permission is required") {
		t.Fatalf("run verb without use: message %q, want the use refusal (the fixture must reach the check)", msg)
	}

	// The automation grant: the same caller, but the run is delegated. It gets
	// past the use check and fails only on the unreachable test endpoint.
	status, env := callAction(t, s, agent, ActionStartRun, map[string]any{"task": "Research acme.example", "idempotencyKey": "action-1"})
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("start-run → %d %+v", status, env.Error)
	}
	var out startRunActionOutput
	if err := json.Unmarshal(env.Result, &out); err != nil || out.RunID == "" {
		t.Fatalf("start-run result = %s (%v)", env.Result, err)
	}
	if msg := settle(out.RunID); strings.Contains(msg, "use permission") || strings.Contains(msg, "forbidden") {
		t.Fatalf("start-run run was refused model connection use: %q", msg)
	}
}
