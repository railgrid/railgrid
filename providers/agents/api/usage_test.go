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
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/railgrid/provider-sdk/dataplane"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
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

func TestPercentilesUseNearestRank(t *testing.T) {
	for _, test := range []struct {
		name    string
		values  []int64
		wantP50 int64
		wantP95 int64
	}{
		{name: "empty", values: []int64{}, wantP50: 0, wantP95: 0},
		{name: "single", values: []int64{42}, wantP50: 42, wantP95: 42},
		{name: "even", values: []int64{200, 100}, wantP50: 100, wantP95: 200},
		{name: "odd", values: []int64{30, 10, 20}, wantP50: 20, wantP95: 30},
		{
			name:    "twenty values",
			values:  []int64{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
			wantP50: 10, wantP95: 19,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			p50, p95 := percentiles(test.values)
			if p50 != test.wantP50 || p95 != test.wantP95 {
				t.Fatalf("percentiles(%v) = (%d, %d), want (%d, %d)", test.values, p50, p95, test.wantP50, test.wantP95)
			}
		})
	}
}

func TestUsageRollupReportsUnpricedRuns(t *testing.T) {
	for _, test := range []struct {
		name    string
		harness bool
		run     store.Run
		want    int64
	}{
		{name: "metered usage without estimate", run: store.Run{InputTokens: 100}, want: 1},
		{name: "recorded estimate", run: store.Run{InputTokens: 100, USDMicros: 25}},
		{name: "no model usage", run: store.Run{}},
		{name: "external harness billing", harness: true, run: store.Run{}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := map[string]any{"type": "model", "model": map[string]any{}}
			if test.harness {
				backend = map[string]any{"type": "harness", "harness": map[string]any{"credentialRef": "codex"}}
			}
			got := runUsageRollup(t, backend, test.run)
			if got.Total.UnpricedRuns != test.want || got.ByAgent[0].UnpricedRuns != test.want || got.ByModel[0].UnpricedRuns != test.want || got.Series[0].UnpricedRuns != test.want {
				t.Fatalf("coverage must survive every aggregation: %+v", got)
			}
		})
	}
}

func runUsageRollup(t *testing.T, backend map[string]any, supplied ...store.Run) usageResponse {
	return runUsageRollupWithCredentialReview(t, backend, nil, supplied...)
}

func runUsageRollupWithCredentialReview(
	t *testing.T,
	backend map[string]any,
	review func(name string) (bool, error),
	supplied ...store.Run,
) usageResponse {
	t.Helper()
	provider := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), unstructuredAgent(backend))
	provider.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		object, ok := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		if !ok {
			return true, nil, errors.New("SAR request was not unstructured")
		}
		name, _, err := unstructured.NestedString(object.Object, "spec", "resourceAttributes", "name")
		if err != nil {
			return true, nil, err
		}
		allowed := true
		if review != nil {
			allowed, err = review(name)
			if err != nil {
				return true, nil, err
			}
		}
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "authorization.k8s.io/v1",
			"kind":       "SubjectAccessReview",
			"status":     map[string]any{"allowed": allowed},
		}}, nil
	})

	scope := store.Scope{OrgUUID: "org1", WorkspaceUUID: "ws1", AgentName: "coder"}
	st := store.NewMemoryStore()
	now := time.Now().UTC()
	run := store.Run{
		ID: "run-harness", AgentName: "coder", Trigger: "chat", Phase: store.RunPhaseSucceeded,
		InputTokens: 12, OutputTokens: 34, USDMicros: 56,
		CreatedAt: now, UpdatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if len(supplied) == 0 {
		supplied = []store.Run{run}
	}
	for i, input := range supplied {
		item := run
		item.ID = "run-" + strconv.Itoa(i)
		item.ModelSettings = input.ModelSettings
		item.InputTokens = input.InputTokens
		item.OutputTokens = input.OutputTokens
		item.USDMicros = input.USDMicros
		if input.Phase != "" {
			item.Phase = input.Phase
		}
		if !input.CreatedAt.IsZero() {
			item.CreatedAt = input.CreatedAt
		}
		if input.StartedAt != nil && input.FinishedAt != nil {
			item.StartedAt, item.FinishedAt = input.StartedAt, input.FinishedAt
		}
		if err := st.SaveRun(t.Context(), scope, item); err != nil {
			t.Fatal(err)
		}
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

func TestUsageRollupUsesPinnedChatModel(t *testing.T) {
	backend := map[string]any{"type": "model", "model": map[string]any{"credentials": map[string]any{"chat": "default-a"}}}
	start := time.Now().UTC()
	finish := start.Add(2 * time.Second)
	for _, test := range []struct {
		name     string
		settings *store.ModelSettings
		want     string
	}{
		{name: "chat override", settings: &store.ModelSettings{ModelCredential: "chat-b"}, want: "chat-b"},
		{name: "legacy run", want: "default-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := runUsageRollup(t, backend, store.Run{ModelSettings: test.settings, InputTokens: 12, OutputTokens: 34, StartedAt: &start, FinishedAt: &finish})
			if len(got.ByModel) != 1 || got.ByModel[0].Key != test.want {
				t.Fatalf("model buckets = %+v, want %s", got.ByModel, test.want)
			}
			bucket := got.ByModel[0]
			if bucket.InputTokens != 12 || bucket.OutputTokens != 34 || bucket.LatencyP50MS != 2000 || bucket.LatencyP95MS != 2000 {
				t.Fatalf("usage and latency must follow pinned model: %+v", bucket)
			}
		})
	}
}

func TestUsageRollupHidesUnauthorizedPinnedCredentials(t *testing.T) {
	backend := map[string]any{"type": "model", "model": map[string]any{"credentials": map[string]any{"chat": "default-a"}}}
	var reviewCounts = map[string]int{}
	got := runUsageRollupWithCredentialReview(t, backend, func(name string) (bool, error) {
		reviewCounts[name]++
		switch name {
		case "chat-allowed":
			return true, nil
		case "chat-denied":
			return false, nil
		case "chat-review-error":
			return false, errors.New("review service unavailable")
		default:
			return false, nil
		}
	},
		store.Run{ModelSettings: &store.ModelSettings{ModelCredential: "chat-allowed"}, InputTokens: 1},
		store.Run{ModelSettings: &store.ModelSettings{ModelCredential: "chat-denied"}, InputTokens: 2},
		store.Run{ModelSettings: &store.ModelSettings{ModelCredential: "chat-denied"}, InputTokens: 3},
		store.Run{ModelSettings: &store.ModelSettings{ModelCredential: "chat-review-error"}, InputTokens: 4},
		store.Run{ModelSettings: &store.ModelSettings{ModelCredential: "default-a"}, InputTokens: 5},
		store.Run{InputTokens: 6},
	)

	if got.Total.Runs != 6 || got.Total.InputTokens != 21 {
		t.Fatalf("authorization filtering changed aggregate totals: %+v", got.Total)
	}
	byModel := map[string]usageBucket{}
	for _, bucket := range got.ByModel {
		byModel[bucket.Key] = bucket
	}
	if len(byModel) != 3 {
		t.Fatalf("unexpected model buckets: %+v", got.ByModel)
	}
	if byModel["chat-allowed"].Runs != 1 || byModel["chat-allowed"].InputTokens != 1 {
		t.Fatalf("allowed credential was not attributed by name: %+v", byModel["chat-allowed"])
	}
	if byModel["default-a"].Runs != 2 || byModel["default-a"].InputTokens != 11 {
		t.Fatalf("Agent default and legacy runs should keep established attribution: %+v", byModel["default-a"])
	}
	restricted := byModel["(restricted model)"]
	if restricted.Runs != 3 || restricted.InputTokens != 9 {
		t.Fatalf("denied/error credentials should share a redacted bucket: %+v", restricted)
	}
	if reviewCounts["chat-allowed"] != 1 || reviewCounts["chat-denied"] != 1 || reviewCounts["chat-review-error"] != 1 {
		t.Fatalf("credential SARs were not cached by credential: %v", reviewCounts)
	}
	if _, reviewed := reviewCounts["default-a"]; reviewed {
		t.Fatalf("the Agent's configured default should not require a second credential review: %v", reviewCounts)
	}
}
