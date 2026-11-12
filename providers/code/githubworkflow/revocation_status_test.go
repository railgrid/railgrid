// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRevokedIssuerFinalStatusSurvivesLostResponseAndNewCommand(t *testing.T) {
	f, repo, input, issuer := taskTestFixture(t)
	allowTaskCallerSAR(f, true, nil)
	if _, err := authorizeTaskTest(t, f, repo, input, issuer); err != nil {
		t.Fatal(err)
	}
	obj, err := f.dyn.Resource(PullRequests).Get(t.Context(), input.PullRequest, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := decode[api.PullRequest](obj)
	if err != nil {
		t.Fatal(err)
	}
	input.GrantRef, input.Proof = pr.Spec.Command.Task.GrantRef, pr.Spec.Command.Task.Proof
	pr.Spec.Number = 7
	u, _ := asU(pr)
	if _, err = f.dyn.Resource(PullRequests).Update(t.Context(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	old := f.s.HTTP.Transport
	patches := 0
	body := "Reviewing\n\n<!-- railgrid-pr-status:task-pr-uid -->"
	loseResponse := true
	f.s.HTTP.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		var value any
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/7/comments") && r.Method == http.MethodGet:
			value = []Comment{{ID: 12, Body: body, User: User{Type: "Bot", Login: "sample[bot]"}}}
		case strings.HasSuffix(r.URL.Path, "/issues/comments/12") && r.Method == http.MethodPatch:
			patches++
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			body = in["body"]
			if loseResponse {
				loseResponse = false
				return nil, errors.New("response lost after status update")
			}
			value = Comment{ID: 12, Body: body}
		default:
			return old.RoundTrip(r)
		}
		data, _ := json.Marshal(value)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})
	input.Op = "revoke"
	input.FinalStatusBody = "**Railgrid · Blocked**\n\nDeadline expired."
	// The captured caller can lose permission and the deadline can expire: only
	// the original authenticated issuer may retire authority and finish status.
	f.s.Now = func() time.Time { return input.Deadline.Add(time.Hour) }
	allowTaskCallerSAR(f, false, nil)
	if _, err := authorizeTaskTest(t, f, repo, input, issuer); err == nil {
		t.Fatal("expected lost publication response")
	}
	obj, _ = f.dyn.Resource(PullRequests).Get(t.Context(), input.PullRequest, metav1.GetOptions{})
	pr, _ = decode[api.PullRequest](obj)
	if pr.Spec.Command != nil || len(pr.Spec.TaskAuthorizationRevocations) != 1 {
		t.Fatal("publication failure left grant active")
	}
	if _, err := authorizeTaskTest(t, f, repo, input, issuer); err != nil {
		t.Fatal(err)
	}
	if patches != 1 || !strings.Contains(body, "Railgrid · Blocked") {
		t.Fatal("retry duplicated or lost status")
	}
	missingProof := input
	missingProof.Proof = ""
	if _, err := authorizeTaskTest(t, f, repo, missingProof, issuer); err == nil {
		t.Fatal("revocation tombstone without original signature authorized publication")
	}
	changedProof := input
	changedProof.Proof = strings.Repeat("0", 64)
	if _, err := authorizeTaskTest(t, f, repo, changedProof, issuer); err == nil {
		t.Fatal("changed signature authorized final publication")
	}
	changed := issuer
	changed.User = "another-issuer"
	if _, err := authorizeTaskTest(t, f, repo, input, changed); err == nil {
		t.Fatal("different issuer posted final status")
	}
	changedInput := input
	changedInput.SourceDigest = strings.Repeat("b", 64)
	if _, err := authorizeTaskTest(t, f, repo, changedInput, issuer); err == nil {
		t.Fatal("changed scope used tombstone")
	}
	scope := input
	scope.Op = "scope"
	scope.FinalStatusBody = ""
	if _, err := authorizeTaskTest(t, f, repo, scope, issuer); err == nil {
		t.Fatal("final status restored worker authority")
	}
	// A new command owns the status surface. Delayed old cleanup is acknowledged
	// without clearing that command or overwriting its status.
	pr.Spec.Command = &api.PullRequestCommand{EventRef: "new-event", EventUID: "new-event-uid", Deadline: metav1.NewTime(input.Deadline.Add(2 * time.Hour))}
	u, _ = asU(pr)
	if _, err = f.dyn.Resource(PullRequests).Update(t.Context(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	body = "New command is reviewing"
	if _, err := authorizeTaskTest(t, f, repo, input, issuer); err != nil {
		t.Fatal(err)
	}
	obj, _ = f.dyn.Resource(PullRequests).Get(t.Context(), input.PullRequest, metav1.GetOptions{})
	pr, _ = decode[api.PullRequest](obj)
	if pr.Spec.Command == nil || pr.Spec.Command.EventUID != "new-event-uid" || body != "New command is reviewing" || patches != 1 {
		t.Fatal("old cleanup changed newer authority/status")
	}
	// A newer task may itself have completed and cleared its live command.
	// The last durable revocation still fences the older final status retry.
	pr.Spec.Command = nil
	newer := pr.Spec.TaskAuthorizationRevocations[0]
	newer.SourceUID = "new-task"
	pr.Spec.TaskAuthorizationRevocations = append(pr.Spec.TaskAuthorizationRevocations, newer)
	u, _ = asU(pr)
	if _, err = f.dyn.Resource(PullRequests).Update(t.Context(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	body = "New command completed"
	if _, err := authorizeTaskTest(t, f, repo, input, issuer); err != nil {
		t.Fatal(err)
	}
	if body != "New command completed" || patches != 1 {
		t.Fatal("old cleanup replaced newer completed status")
	}
}

func TestPreAdmissionRevocationCannotBorrowAppForFinalStatus(t *testing.T) {
	f, repo, input, issuer := taskTestFixture(t)
	obj, err := f.dyn.Resource(PullRequests).Get(t.Context(), input.PullRequest, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := decode[api.PullRequest](obj)
	if err != nil {
		t.Fatal(err)
	}
	pr.Spec.Number = 7
	u, _ := asU(pr)
	if _, err = f.dyn.Resource(PullRequests).Update(t.Context(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	old := f.s.HTTP.Transport
	f.s.HTTP.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/issues/") {
			t.Fatal("unproven grant reached GitHub status publication")
		}
		return old.RoundTrip(r)
	})
	input.Op = "revoke"
	input.FinalStatusBody = "Unproven final status"
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := authorizeTaskTest(t, f, repo, input, issuer); err == nil {
			t.Fatal("pre-admission tombstone authorized an App status update")
		}
	}
	obj, _ = f.dyn.Resource(PullRequests).Get(t.Context(), input.PullRequest, metav1.GetOptions{})
	pr, _ = decode[api.PullRequest](obj)
	if pr.Spec.Command != nil || len(pr.Spec.TaskAuthorizationRevocations) != 1 {
		t.Fatal("failed status publication prevented durable revocation")
	}
	input.FinalStatusBody = ""
	if _, err := authorizeTaskTest(t, f, repo, input, issuer); err != nil {
		t.Fatal("revocation without publication must remain available:", err)
	}
}
