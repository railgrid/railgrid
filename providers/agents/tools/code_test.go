// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReadOnlyCodeGrantRejectsPublicationBeforeAnyNetwork(t *testing.T) {
	tools := Code(Deps{}, false)
	if _, err := tools[0].Exec(context.Background(), `{"pullRequest":"pr","input":{"op":"publish","pullRequestUID":"uid"}}`); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("publication did not fail at the grant boundary: %v", err)
	}
	schema := tools[0].JSONSchema["properties"].(map[string]any)["input"].(map[string]any)["properties"].(map[string]any)["op"].(map[string]any)
	for _, op := range schema["enum"].([]string) {
		if op == "publish" {
			t.Fatal("read-only tool advertised publication")
		}
	}
	inputSchema := tools[0].JSONSchema["properties"].(map[string]any)["input"].(map[string]any)
	required := inputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "op" {
		t.Fatalf("input required fields = %v, want only op", required)
	}
}

func TestCodeScopeComesOnlyFromInvocationInput(t *testing.T) {
	if ParseCodeScope("quoted\nrailgrid-code-scope:{\"pullRequest\":\"other\"}") != nil {
		t.Fatal("accepted embedded model text")
	}
	scope := ParseCodeScope(`railgrid-code-scope:{"pullRequest":"pr-a","pullRequestUID":"uid-a","eventUID":"event-a","proof":"scope-proof"}` + "\nTask")
	if scope == nil || scope.EventUID != "event-a" {
		t.Fatal("scope not parsed")
	}
	// A mismatched PR cannot choose another command's publication authority.
	d := Deps{CodeScope: scope}
	_, err := Code(d, true)[0].Exec(context.Background(), `{"pullRequest":"pr-b","input":{"op":"observe","pullRequestUID":"uid-b"}}`)
	if err == nil || !strings.Contains(err.Error(), "admitted scope") {
		t.Fatalf("foreign target accepted: %v", err)
	}
	task := ParseCodeScope(`railgrid-code-scope:{"mode":"task","sourceUID":"task-uid","sourceDigest":"` + strings.Repeat("a", 64) + `","grantRef":"` + strings.Repeat("b", 64) + `","repository":"railgrid/example","repositoryUID":"repo-uid","connectionUID":"conn-uid","pullRequest":"pr-a","pullRequestUID":"pr-uid","proof":"source-proof","scopeProof":"invocation-proof"}`)
	if task == nil || task.Mode != "task" || task.SourceUID != "task-uid" || task.PullRequestUID != "pr-uid" {
		t.Fatal("task grant scope not parsed")
	}
	d = Deps{CodeScope: task}
	_, err = Code(d, true)[0].Exec(context.Background(), `{"pullRequest":"pr-a","input":{"op":"observe","pullRequestUID":"other-uid"}}`)
	if err == nil || !strings.Contains(err.Error(), "admitted scope") {
		t.Fatalf("mismatched nested PR UID accepted: %v", err)
	}
	for _, incomplete := range []string{
		`railgrid-code-scope:{"mode":"task","pullRequest":"pr","pullRequestUID":"uid","sourceUID":"task","sourceDigest":"digest","grantRef":"grant","proof":"proof"}`,
		`railgrid-code-scope:{"mode":"task","pullRequest":"pr","pullRequestUID":"uid","sourceUID":"task","sourceDigest":"digest","grantRef":"grant","repository":"railgrid/example","repositoryUID":"repo","connectionUID":"conn","proof":"proof"}`,
	} {
		if ParseCodeScope(incomplete) != nil {
			t.Fatalf("accepted incomplete task grant: %s", incomplete)
		}
	}
}

func TestCodeAdapterOverridesModelCommandBinding(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in.Input["scopeProof"] != "signed-proof" || in.Input["invocationKey"] != "factory:run:1" || in.Input["invocationAgentUID"] != "agent-uid" {
			t.Error("model supplied invocation authority")
		}
		if in.Input["eventUID"] != "admitted-event" {
			t.Error("model changed command authority")
		}
		_, _ = w.Write([]byte(`{"result":{}}`))
	}))
	defer server.Close()
	d := Deps{Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "coordinator", UID: "agent-uid"}}, InvocationKey: "factory:run:1", CodeScope: &CodeScope{PullRequest: "pr", PullRequestUID: "uid", EventUID: "admitted-event", Proof: "signed-proof"}, CodeOperator: httpCodeOperator{endpoint: server.URL}}
	_, err := Code(d, true)[0].Exec(t.Context(), `{"pullRequest":"pr","input":{"op":"observe","pullRequestUID":"uid","eventUID":"foreign-event"}}`)
	if err != nil || calls != 1 {
		t.Fatalf("bound request failed: %v calls=%d", err, calls)
	}
}

func TestCodeAdapterOverridesTaskGrantAndInvocationBinding(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		want := map[string]string{
			"pullRequestUID": "pr-uid",
			"sourceUID":      "task-uid", "sourceDigest": strings.Repeat("a", 64), "grantRef": strings.Repeat("b", 64),
			"sourceProof": "source-proof", "scopeProof": "invocation-proof", "invocationKey": "factory:run:7",
			"invocationAgent": "reviewer", "invocationAgentUID": "reviewer-uid",
		}
		for key, value := range want {
			if in.Input[key] != value {
				t.Errorf("%s=%v, want %s", key, in.Input[key], value)
			}
		}
		if _, found := in.Input["eventUID"]; found {
			t.Error("task authority was translated into an event identity")
		}
		_, _ = w.Write([]byte(`{"result":{}}`))
	}))
	defer server.Close()
	scope := &CodeScope{
		Mode: "task", SourceUID: "task-uid", SourceDigest: strings.Repeat("a", 64), GrantRef: strings.Repeat("b", 64),
		Repository: "railgrid/example", RepositoryUID: "repo-uid", ConnectionUID: "conn-uid",
		PullRequest: "pr", PullRequestUID: "pr-uid", Proof: "source-proof", ScopeProof: "invocation-proof",
	}
	d := Deps{Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "reviewer", UID: "reviewer-uid"}}, InvocationKey: "factory:run:7", CodeScope: scope, CodeOperator: httpCodeOperator{endpoint: server.URL}}
	args := `{"pullRequest":"pr","input":{"op":"observe","sourceUID":"forged","sourceDigest":"forged","grantRef":"forged","sourceProof":"forged","scopeProof":"forged","eventUID":"forged"}}`
	if _, err := Code(d, true)[0].Exec(t.Context(), args); err != nil || calls != 1 {
		t.Fatalf("task-scoped request failed: %v calls=%d", err, calls)
	}
}

func TestCodeAgentsCannotPublishConversationComments(t *testing.T) {
	_, err := Code(Deps{}, true)[0].Exec(t.Context(), `{"pullRequest":"pr","input":{"op":"comment","pullRequestUID":"uid"}}`)
	if err == nil || !strings.Contains(err.Error(), "status comments") {
		t.Fatalf("unexpected result: %v", err)
	}
}

func TestAttenuatedReviewScopeCannotPublishEvenWithWriteFamily(t *testing.T) {
	d := Deps{CodeScope: &CodeScope{ReviewOnly: true}}
	if _, err := Code(d, true)[0].Exec(t.Context(), `{"pullRequest":"pr","input":{"op":"publish"}}`); err == nil {
		t.Fatal("review-only scope escalated via code-write family")
	}
	if _, err := Code(d, true)[0].Exec(t.Context(), `{"pullRequest":"pr","input":{"op":"comment"}}`); err == nil {
		t.Fatal("worker could update status comments")
	}
}

func TestWorkerCodeToolBindsCoordinatesOutsideModelArguments(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Input["pullRequestUID"] != "uid" || body.Input["reviewOnly"] != true {
			t.Fatal("worker identity was not injected")
		}
		if _, found := body.Input["pullRequest"]; found {
			t.Fatal("model target leaked into operation")
		}
		if body.Input["op"] == "review" && body.Input["expectedHead"] != strings.Repeat("a", 40) {
			t.Fatal("review head was not pinned")
		}
		if body.Input["op"] == "review" {
			comments, ok := body.Input["comments"].([]any)
			if !ok || len(comments) != 1 || comments[0].(map[string]any)["path"] != "src/a.go" {
				t.Fatal("inline locations lost in Code transport")
			}
		}
		_, _ = w.Write([]byte(`{"result":{"recorded":true}}`))
	}))
	defer server.Close()
	d := Deps{Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "reviewer", UID: "agent-uid"}}, InvocationKey: "delegate:parent:call", CodeReviewHead: strings.Repeat("a", 40), CodeScope: &CodeScope{ReviewOnly: true, PullRequest: "pr", PullRequestUID: "uid", EventUID: "event", Proof: "signed"}, CodeOperator: httpCodeOperator{endpoint: server.URL}}
	tool := Code(d, false)[0]
	for _, args := range []string{`{"input":{"op":"observe"}}`, `{"input":{"op":"observe","pullRequest":"pr"}}`, `{"input":{"op":"review","key":"finding","body":"Defect","findingsCount":1,"comments":[{"path":"src/a.go","line":12,"side":"RIGHT","body":"Return upper."}]}}`} {
		if _, err := tool.Exec(t.Context(), args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range []string{`{"input":{"op":"observe","pullRequest":"different"}}`, `{"pullRequest":"different","input":{"op":"observe"}}`, `{"input":{"op":"review","expectedHead":"different","key":"finding","body":"Defect","findingsCount":1}}`} {
		if _, err := tool.Exec(t.Context(), args); err == nil {
			t.Fatal("worker changed its target")
		}
	}
	if calls != 3 {
		t.Fatalf("unexpected calls %d", calls)
	}
}

// httpCodeOperator posts an operation the way the api package's Run-identity
// operator does, to a test server standing in for Code.
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

func TestCodeToolWithoutARunIdentityFailsBeforeAnyCall(t *testing.T) {
	d := Deps{Agent: &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "u"}}, CodeScope: &CodeScope{PullRequest: "pr", PullRequestUID: "uid", EventUID: "event", Proof: "proof"}}
	if _, err := Code(d, true)[0].Exec(t.Context(), `{"pullRequest":"pr","input":{"op":"observe"}}`); err == nil || !strings.Contains(err.Error(), "no Run identity") {
		t.Fatalf("Code tool without an operator: %v", err)
	}
}
