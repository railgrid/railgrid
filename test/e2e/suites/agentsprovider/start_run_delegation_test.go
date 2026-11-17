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

package agentsprovider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	delegationConnectionGVR = schema.GroupVersionResource{Group: agentsGroup, Version: "v1alpha1", Resource: "modelproviderconnections"}
	delegationCredentialGVR = schema.GroupVersionResource{Group: agentsGroup, Version: "v1alpha1", Resource: "modelcredentials"}
	serviceAccountGVR       = schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
)

// TestNStartRunDelegatesModelConnectionUse is the real-kcp regression for the
// first live App Studio → Agents call. There a Project's workload identity
// called start-run/v1, the run was admitted, and it failed at once with "use
// permission is required" on the agent's ModelProviderConnection: the caller
// kcp stamped stayed on the run and was asked for `use`, which a workload
// identity never holds.
//
// The caller here is a tenant ServiceAccount with exactly that shape: `get` on
// agents and the two verb subresources, and no `use` on the connection. It is
// authenticated and stamped by kcp, routed by the shard to the real provider
// process, and the outcome is read off the real Run objects. The `run` verb,
// which is unchanged, is still refused at the use check, so the fixture does
// reach it; start-run's run is delegated and completes through the agent's
// configured model.
func TestNStartRunDelegatesModelConnectionUse(t *testing.T) {
	requireTenant(t)
	ensureAgentsEnabled(t)

	model := newDelegationModel(t, "Acme runs its agents on a private cluster.")
	const (
		agentName  = "delegation-researcher"
		credName   = "delegation-model"
		connName   = "delegation-account"
		secretName = "delegation-account-key"
		saName     = "delegation-app"
		roleName   = "delegation-app"
	)
	admin := tenant(t)

	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": secretName, "namespace": "default", "labels": map[string]any{"railgrid.ai/owner": "agents"}},
		"type":     "Opaque",
		"data":     map[string]any{"apiKey": base64.StdEncoding.EncodeToString([]byte("sk-e2e"))},
	}}
	if _, err := admin.Resource(secretGVR).Namespace("default").Create(ctxWithTimeout(t, 15*time.Second), secret, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Secret/%s: %v", secretName, err)
	}
	createObject(t, delegationConnectionGVR, "ModelProviderConnection", connName, map[string]any{
		"provider": "openai-compatible", "baseURL": model.srv.URL,
		"secretRef": map[string]any{"name": secretName}, "secretKey": "apiKey",
	})
	createObject(t, delegationCredentialGVR, "ModelCredential", credName, map[string]any{"connectionRef": connName, "model": "gpt-4o"})
	createObject(t, agentGVR, "Agent", agentName, map[string]any{
		"displayName": "Delegation researcher", "autonomy": "auto",
		"backend": map[string]any{"type": "model", "model": map[string]any{"credentials": map[string]any{"chat": credName}}},
		"memory":  map[string]any{"enabled": false},
	})
	t.Cleanup(func() {
		for _, o := range []struct {
			gvr  schema.GroupVersionResource
			name string
		}{{agentGVR, agentName}, {delegationCredentialGVR, credName}, {delegationConnectionGVR, connName}} {
			_ = admin.Resource(o.gvr).Delete(ctxWithTimeout(t, 10*time.Second), o.name, metav1.DeleteOptions{})
		}
		rbacAdmin := kcpDynamic(t, tenantCluster, adminToken)
		_ = rbacAdmin.Resource(clusterRoleBGVR).Delete(ctxWithTimeout(t, 10*time.Second), roleName, metav1.DeleteOptions{})
		_ = rbacAdmin.Resource(clusterRoleGVR).Delete(ctxWithTimeout(t, 10*time.Second), roleName, metav1.DeleteOptions{})
		_ = admin.Resource(serviceAccountGVR).Namespace("default").Delete(ctxWithTimeout(t, 10*time.Second), saName, metav1.DeleteOptions{})
		_ = admin.Resource(secretGVR).Namespace("default").Delete(ctxWithTimeout(t, 10*time.Second), secretName, metav1.DeleteOptions{})
	})
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		agent, err := getObject(t, agentGVR, agentName)
		if err != nil {
			return false, err.Error()
		}
		status, reason, message := condition(agent, "ModelCredentialsReady")
		return status == "True", fmt.Sprintf("ModelCredentialsReady=%s %s %s", status, reason, message)
	}) {
		t.Fatal("the agent's connection-bound model never became ready")
	}

	token := delegationCallerToken(t, saName, roleName)

	// Both verbs, as the ServiceAccount, on the hub's kcp front door.
	start := func(verb string, body map[string]any) string {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		code, resp := hubDo(t, http.MethodPost, verbPath("agents", agentName, verb, "", nil), token, "", "", raw, nil)
		if code != http.StatusOK && code != http.StatusAccepted {
			t.Fatalf("%s as the ServiceAccount = %d: %s", verb, code, truncate(resp))
		}
		var out struct {
			RunID  string `json:"runId"`
			Result struct {
				RunID string `json:"runId"`
			} `json:"result"`
		}
		if err := json.Unmarshal(resp, &out); err != nil {
			t.Fatalf("%s answer: %v: %s", verb, err, truncate(resp))
		}
		if out.Result.RunID != "" {
			return out.Result.RunID
		}
		if out.RunID == "" {
			t.Fatalf("%s returned no run id: %s", verb, truncate(resp))
		}
		return out.RunID
	}
	settle := func(runID string) (string, string) {
		t.Helper()
		var phase, message string
		if !waitForCondition(t, 90*time.Second, func() (bool, string) {
			run, err := getObject(t, runGVR, runID)
			if err != nil {
				return false, err.Error()
			}
			phase, _, _ = unstructured.NestedString(run.Object, "status", "phase")
			message, _, _ = unstructured.NestedString(run.Object, "status", "message")
			return phase == "Succeeded" || phase == "Failed" || phase == "Aborted", phase + " " + message
		}) {
			t.Fatalf("run %s never settled (last %s %q)", runID, phase, message)
		}
		return phase, message
	}

	verbRun := start("run", map[string]any{"task": "Research acme.example", "idempotencyKey": "delegation-verb"})
	if phase, message := settle(verbRun); phase != "Failed" || !strings.Contains(message, "use permission is required") {
		t.Fatalf("run verb without use = %s %q; want Failed at the use check (the fixture must reach it)", phase, message)
	}
	if model.calls.Load() != 0 {
		t.Fatal("a run refused at the use check reached the model")
	}

	actionRun := start("start-run", map[string]any{"input": map[string]any{"task": "Research acme.example", "idempotencyKey": "delegation-action"}})
	if phase, message := settle(actionRun); phase != "Succeeded" || model.calls.Load() == 0 {
		t.Fatalf("start-run run = %s %q after %d model calls; want Succeeded through the agent's configured model", phase, message, model.calls.Load())
	}
}

// ensureAgentsEnabled re-enables agents when an earlier test in source order
// (TestLTenantDisable) has disabled it, and restores that state afterwards.
func ensureAgentsEnabled(t *testing.T) {
	t.Helper()
	_, err := tenant(t).Resource(apiBindingGVR).Get(ctxWithTimeout(t, 10*time.Second), providerName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("read agents binding: %v", err)
	}
	if apierrors.IsNotFound(err) {
		t.Cleanup(func() {
			code, raw := hubDo(t, http.MethodPost, enablePath(providerName, "disable"), staticToken, orgUUID, workspaceUUID, nil, nil)
			if code != http.StatusNoContent && code != http.StatusOK {
				t.Errorf("restore disabled agents: %d: %s", code, truncate(raw))
				return
			}
			waitGone(t, apiBindingGVR, providerName, 90*time.Second)
		})
		enableProvider(t, providerName, []map[string]string{
			{"group": "authorization.k8s.io", "resource": "subjectaccessreviews"},
			{"group": "", "resource": "secrets"},
		})
	}
	if !waitForCondition(t, 90*time.Second, func() (bool, string) {
		for _, gvr := range []schema.GroupVersionResource{agentGVR, delegationConnectionGVR, delegationCredentialGVR} {
			if _, err := tenant(t).Resource(gvr).List(ctxWithTimeout(t, 10*time.Second), metav1.ListOptions{}); err != nil {
				return false, gvr.Resource + ": " + err.Error()
			}
		}
		return true, ""
	}) {
		t.Fatal("agents APIs never became servable")
	}
}

// delegationCallerToken mints a token for a tenant ServiceAccount that may get
// agents and call agents/run and agents/start-run, and holds no `use` on any
// ModelProviderConnection: the shape of an App Studio Project's identity.
func delegationCallerToken(t *testing.T, saName, roleName string) string {
	t.Helper()
	admin := tenant(t)
	sa := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ServiceAccount",
		"metadata": map[string]any{"name": saName, "namespace": "default"},
	}}
	if _, err := admin.Resource(serviceAccountGVR).Namespace("default").Create(ctxWithTimeout(t, 15*time.Second), sa, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ServiceAccount/%s: %v", saName, err)
	}
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": roleName},
		"rules": []any{
			map[string]any{"apiGroups": []any{agentsGroup}, "resources": []any{"agents"}, "verbs": []any{"get"}},
			// kcp maps the verb's POST onto the RBAC verb create.
			map[string]any{"apiGroups": []any{agentsGroup}, "resources": []any{"agents/run", "agents/start-run"}, "verbs": []any{"create"}},
		},
	}}
	// RBAC is written as the kcp admin: the tenant user's own role does not
	// spell these subresources, so Kubernetes' escalation check refuses to let
	// it grant them.
	rbacAdmin := kcpDynamic(t, tenantCluster, adminToken)
	if _, err := rbacAdmin.Resource(clusterRoleGVR).Create(ctxWithTimeout(t, 15*time.Second), role, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ClusterRole/%s: %v", roleName, err)
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": roleName},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": roleName},
		"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": saName, "namespace": "default"}},
	}}
	if _, err := rbacAdmin.Resource(clusterRoleBGVR).Create(ctxWithTimeout(t, 15*time.Second), binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ClusterRoleBinding/%s: %v", roleName, err)
	}
	request := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authentication.k8s.io/v1", "kind": "TokenRequest",
		"metadata": map[string]any{"name": saName, "namespace": "default"},
		"spec":     map[string]any{"expirationSeconds": int64(3600)},
	}}
	minted, err := rbacAdmin.Resource(serviceAccountGVR).Namespace("default").Create(ctxWithTimeout(t, 15*time.Second), request, metav1.CreateOptions{}, "token")
	if err != nil {
		t.Fatalf("mint a token for ServiceAccount/%s: %v", saName, err)
	}
	token, _, _ := unstructured.NestedString(minted.Object, "status", "token")
	if token == "" {
		t.Fatalf("TokenRequest for %s returned no token", saName)
	}
	return token
}

// delegationModel is an OpenAI-compatible endpoint in the test process (the
// provider runs on the same host): GET /models for the connection probe, and
// one streamed answer per chat completion.
type delegationModel struct {
	srv   *httptest.Server
	calls atomic.Int64
}

func newDelegationModel(t *testing.T, reply string) *delegationModel {
	t.Helper()
	m := &delegationModel{}
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o","object":"model"}]}`)
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		m.calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flush, _ := w.(http.Flusher)
		for _, chunk := range []map[string]any{
			{"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": "gpt-4o",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply}}}},
			{"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": "gpt-4o",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 50, "completion_tokens": 10, "total_tokens": 60}},
		} {
			b, _ := json.Marshal(chunk)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			if flush != nil {
				flush.Flush()
			}
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}
