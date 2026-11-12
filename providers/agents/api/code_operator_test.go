// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/railgrid/provider-sdk/identityclient"
)

// httpCodeOperator posts an operation to a test server standing in for Code.
type httpCodeOperator struct{ endpoint string }

func (o httpCodeOperator) Operate(ctx context.Context, _ string, input map[string]any) (string, error) {
	raw, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Code refused operation: %s", body)
	}
	return string(body), nil
}

const codeTestCluster = "23qp2e0jwjeqwp2i"

func codeTestRuns(t *testing.T) func(context.Context, string) (dynamic.Interface, error) {
	t.Helper()
	run := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agents.railgrid.ai/v1alpha1", "kind": "Run",
		"metadata": map[string]any{"name": "run-1", "uid": "run-uid"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), run)
	return func(_ context.Context, cluster string) (dynamic.Interface, error) {
		if cluster != codeTestCluster {
			return nil, fmt.Errorf("unexpected cluster %q", cluster)
		}
		return dyn, nil
	}
}

// The Code tool calls Code as the Run's own identity: owned by the Run, scoped
// to the one PullRequest, sent with no provider credential, and named in the
// operation so Code can recognize it.
func TestCodeOperatorCallsCodeAsTheRunIdentity(t *testing.T) {
	var minted identityclient.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/clusters/"+codeTestCluster+"/apis/code.railgrid.ai/v1alpha1/pullrequests/pr-a/operate" {
			t.Errorf("Code called at %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer run-token" {
			t.Errorf("Authorization = %q, want the Run's token", r.Header.Get("Authorization"))
		}
		var body struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Input["op"] != "observe" || body.Input["invocationRun"] != "run-1" || body.Input["invocationRunUID"] != "run-uid" {
			t.Errorf("operation input = %v", body.Input)
		}
		_, _ = w.Write([]byte(`{"result":{"ok":true}}`))
	}))
	defer server.Close()
	op := &codeOperator{
		clusterID: codeTestCluster, runID: "run-1", runs: codeTestRuns(t), origin: server.URL, client: server.Client(),
		mint: func(_ context.Context, req identityclient.Request) (*identityclient.Token, error) {
			minted = req
			return &identityclient.Token{Token: "run-token"}, nil
		},
	}
	out, err := op.Operate(t.Context(), "pr-a", map[string]any{"op": "observe"})
	if err != nil || out != `{"result":{"ok":true}}` {
		t.Fatalf("Operate = %q, %v", out, err)
	}
	owner := minted.Owner
	if owner.Kind != "Run" || owner.Group != "agents.railgrid.ai" || owner.Resource != "runs" || owner.Name != "run-1" || owner.UID != "run-uid" || minted.ClusterID != codeTestCluster {
		t.Fatalf("identity owner = %+v cluster %q, want the Run", owner, minted.ClusterID)
	}
	if len(minted.Rules) != 2 {
		t.Fatalf("rules = %+v", minted.Rules)
	}
	for _, rule := range minted.Rules {
		if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "code.railgrid.ai" || len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != "pr-a" {
			t.Fatalf("rule not scoped to the one PullRequest: %+v", rule)
		}
	}
}

// Where Code is not enabled, the hub refuses the mint. Only the Code tool
// fails, and it says why, without reaching Code.
func TestCodeOperatorReportsARefusedMint(t *testing.T) {
	op := &codeOperator{
		clusterID: codeTestCluster, runID: "run-1", runs: codeTestRuns(t), origin: "http://unreachable.invalid", client: http.DefaultClient,
		mint: func(context.Context, identityclient.Request) (*identityclient.Token, error) {
			return nil, errors.New("code is not enabled in this workspace")
		},
	}
	if _, err := op.Operate(t.Context(), "pr-a", map[string]any{"op": "observe"}); err == nil || !strings.Contains(err.Error(), "code is not enabled") {
		t.Fatalf("refused mint: %v", err)
	}
	missing := &codeOperator{clusterID: codeTestCluster, runID: "run-2", runs: codeTestRuns(t),
		mint: func(context.Context, identityclient.Request) (*identityclient.Token, error) {
			t.Fatal("minted for a Run that does not exist")
			return nil, nil
		}}
	if _, err := missing.Operate(t.Context(), "pr-a", map[string]any{}); err == nil || !strings.Contains(err.Error(), "reading Run run-2") {
		t.Fatalf("missing Run: %v", err)
	}
	if (&Server{}).codeOperatorFor(codeTestCluster, "") != nil {
		t.Fatal("a run with no ID got a Code operator")
	}
	if _, err := (&Server{}).codeOperatorFor(codeTestCluster, "run-1").Operate(t.Context(), "pr-a", map[string]any{}); err == nil || !strings.Contains(err.Error(), "no identity client") {
		t.Fatalf("operator without an identity client: %v", err)
	}
}
