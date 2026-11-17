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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

// These are the regression for the first live App Studio → Agents call: the
// start-run action admitted a run, and the run failed at once with "use
// permission is required", because the Project's workload identity stayed on
// the run's context and was asked for `use` on the agent's
// ModelProviderConnection. A start-run run is recorded as delegated and spends
// the agent's configured models as a Schedule or Trigger run does. The `run`
// verb is unchanged and still asks its caller, which is what proves each
// fixture reaches the check at all.

// connectionBoundObjects is a workspace whose agent runs on a model bound to
// the ModelProviderConnection "account", served by baseURL.
func connectionBoundObjects(agent, baseURL string) []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": agentsclient.AgentGVR.GroupVersion().String(), "kind": "Agent",
			"metadata": map[string]any{"name": agent, "uid": agent + "-uid"},
			"spec": map[string]any{
				"displayName": agent, "autonomy": "auto",
				"backend": map[string]any{"type": "model", "model": map[string]any{"credentials": map[string]any{"chat": "bound-model"}}},
			},
		}},
		{Object: map[string]any{
			"apiVersion": agentsclient.ModelCredentialGVR.GroupVersion().String(), "kind": "ModelCredential",
			"metadata": map[string]any{"name": "bound-model"},
			"spec":     map[string]any{"connectionRef": "account", "model": "gpt-4o"},
		}},
		{Object: map[string]any{
			"apiVersion": agentsclient.ModelProviderConnectionGVR.GroupVersion().String(), "kind": "ModelProviderConnection",
			"metadata": map[string]any{"name": "account"},
			"spec": map[string]any{
				"provider": "openai-compatible", "baseURL": baseURL,
				"secretRef": map[string]any{"name": "account-key"}, "secretKey": "apiKey",
			},
		}},
		{Object: map[string]any{
			"apiVersion": "v1", "kind": "Secret",
			"metadata": map[string]any{"name": "account-key", "namespace": llm.SecretNamespace, "labels": map[string]any{"railgrid.ai/owner": "agents"}},
			"data":     map[string]any{"apiKey": "c2stdGVzdA=="},
		}},
	}
}

// TestStartRunDelegatesModelConnectionUse drives both verbs through the real
// serve.New surface, on the production admission path (the provider's view of
// the workspace, as every unattended run), for a caller who may `get` the
// agent but not `use` its connection: an App Studio Project's identity.
func TestStartRunDelegatesModelConnectionUse(t *testing.T) {
	const agent = "researcher"
	model := newFakeLLM(t, "Acme runs its agents on a private cluster.")
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: connectionBoundObjects(agent, model.srv.URL),
		Allow:   func(a conformance.Attributes) bool { return a.Verb == "get" && a.Resource == "agents" },
	}
	s := newGatedTestServer(t, callers)
	s.bg = &background{server: s, scopedFn: func(_ context.Context, clusterID string) (dynamic.Interface, error) {
		return callers.AsProvider(clusterID)
	}}
	scope := actionScope(agent)
	settle := func(runID string) store.Run {
		t.Helper()
		run, settled := s.waitForRun(t.Context(), scope, runID, 10*time.Second)
		if !settled {
			t.Fatalf("run %s did not settle", runID)
		}
		return run
	}

	// The attended verb: the caller lacks `use`, so the run is refused before
	// any model call.
	rec := httptest.NewRecorder()
	gatedHandler(t, s).ServeHTTP(rec, stamped(http.MethodPost, verbBase(conformanceCluster, "agents", agent)+"/run",
		strings.NewReader(`{"task":"Research acme.example","idempotencyKey":"verb-1"}`), conformanceUser))
	var verb invokeRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &verb); err != nil || verb.RunID == "" {
		t.Fatalf("run verb → %d %s", rec.Code, rec.Body.String())
	}
	if run := settle(verb.RunID); run.Phase != store.RunPhaseFailed || !strings.Contains(run.Message, "use permission is required") || run.ModelUseDelegated {
		t.Fatalf("run verb without use = %s %q delegated=%v; want Failed on the use check (the fixture must reach it)", run.Phase, run.Message, run.ModelUseDelegated)
	}
	if model.calls.Load() != 0 {
		t.Fatal("a refused run reached the model")
	}

	// The automation grant: the same caller, recorded as delegated, and the
	// run reaches the agent's configured model and completes.
	status, env := callAction(t, s, agent, ActionStartRun, map[string]any{"task": "Research acme.example", "idempotencyKey": "action-1"})
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("start-run → %d %+v", status, env.Error)
	}
	var out startRunActionOutput
	if err := json.Unmarshal(env.Result, &out); err != nil || out.RunID == "" {
		t.Fatalf("start-run result = %s (%v)", env.Result, err)
	}
	run := settle(out.RunID)
	if !run.ModelUseDelegated {
		t.Fatal("a start-run run was not recorded as delegated")
	}
	if run.Phase != store.RunPhaseSucceeded || model.calls.Load() == 0 {
		t.Fatalf("start-run run = %s %q after %d model calls; want Succeeded through the configured model", run.Phase, run.Message, model.calls.Load())
	}
}

// TestDelegatedRunResumesWithoutAskingWhoeverResumesIt covers the run after
// admission: a delegated run that parked on an approval is resumed by the
// approver's inbox request, whose identity is on the context. The approver
// lacks `use`; the run still reaches the model, because the delegation is
// read off the run, not off whichever request carries it. The same run
// without delegation is refused, so the fixture does reach the check.
func TestDelegatedRunResumesWithoutAskingWhoeverResumesIt(t *testing.T) {
	for _, delegated := range []bool{true, false} {
		t.Run(map[bool]string{true: "delegated", false: "attended"}[delegated], func(t *testing.T) {
			model := newFakeLLM(t, "Acme looks promising.")
			objects := connectionBoundObjects("scout", model.srv.URL)
			agentObj := objects[0]
			var agent agentsv1alpha1.Agent
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(agentObj.Object, &agent); err != nil {
				t.Fatal(err)
			}
			runtimeObjects := make([]runtime.Object, 0, len(objects)-1)
			for _, o := range objects[1:] {
				runtimeObjects = append(runtimeObjects, o)
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				agentsclient.ModelCredentialGVR:         "ModelCredentialList",
				agentsclient.ModelProviderConnectionGVR: "ModelProviderConnectionList",
				agentsclient.SecretGVR:                  "SecretList",
			}, runtimeObjects...)
			dyn.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"allowed": false}}}, nil
			})

			f := newRecoverFixture(t)
			f.s.engine = engine.New()
			at := time.Now().UTC().Add(-time.Minute)
			checkpoint, err := json.Marshal(runCheckpoint{Engine: engine.Checkpoint{
				Messages: []engine.CheckpointMessage{{Role: "user", Content: "Research acme.example"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.store.SaveRun(t.Context(), f.scope, store.Run{
				ID: "parked", AgentName: agent.Name, Trigger: agentsv1alpha1.RunTriggerAPI, Phase: store.RunPhaseRunning,
				AgentUID: string(agent.UID), ModelUseDelegated: delegated, RecoveryIdentity: store.RecoveryIdentityAgent,
				Input: "Research acme.example", Checkpoint: checkpoint, CreatedAt: at, UpdatedAt: at, StartedAt: &at,
			}); err != nil {
				t.Fatal(err)
			}
			f.s.cfg.ProcessID = "agents-replica-1"
			claimed, err := f.s.store.ClaimRecovery(t.Context(), f.scope, "parked", at, newExecutionOwner(f.s.cfg.ProcessID), time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}

			// The approver's request: their identity, not the app's, is what
			// an inbox resume carries.
			approver := dataplane.WithProxiedIdentity(t.Context(), dataplane.ProxiedIdentity{User: "bob@railgrid.test"})
			res, rerr := f.s.resumeRun(approver, f.scope, "parked", runAccess{Creds: vwSecrets{dyn: dyn}, CR: fixedAgentCR{agent: &agent}},
				resumeIntent{FromPhase: store.RunPhaseRunning, RecoveryClaimedAt: claimed.UpdatedAt})

			got := f.phaseOf(t, "parked")
			if delegated {
				if rerr != nil || res.Phase != store.RunPhaseSucceeded || model.calls.Load() == 0 {
					t.Fatalf("delegated resume = %+v, %v after %d model calls (message %q); want Succeeded through the model", res, rerr, model.calls.Load(), got.Message)
				}
				if !got.ModelUseDelegated {
					t.Fatal("the resume cleared the run's delegation")
				}
				return
			}
			if model.calls.Load() != 0 || !strings.Contains(got.Message+errString(rerr), "use permission is required") {
				t.Fatalf("attended resume = %+v, %v (message %q, %d model calls); want the approver refused at the use check", res, rerr, got.Message, model.calls.Load())
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
