// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"encoding/json"
	"strings"
	"testing"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReviewProofIsAttenuatedAndBound(t *testing.T) {
	key := []byte("secret")
	parent := "parent-scope"
	review := roleProof(parent, true, key)
	if review == parent || roleProof(parent, false, key) != parent || roleProof("different-child", true, key) == review {
		t.Fatal("role proof was not independently bound")
	}
	if err := validReviewDelegate(Operation{ReviewOnly: true, Delegate: &ReviewDelegate{InvocationKey: "child", Agent: "reviewer", AgentUID: "uid"}}); err == nil {
		t.Fatal("child could delegate again")
	}
}

func TestCodeDelegatedReviewCannotPublishCommentOrDelegate(t *testing.T) {
	f, pr := publicationFixture(t)
	u, _ := asU(pr)
	in := Operation{Op: "delegate-review", PullRequestUID: string(pr.UID), EventUID: pr.Spec.Command.EventUID, InvocationKey: "factory:run:1", InvocationAgent: "coordinator", InvocationAgentUID: "coordinator-uid", Delegate: &ReviewDelegate{InvocationKey: "delegate:run:call", Agent: "reviewer", AgentUID: "reviewer-uid"}}
	in.ScopeProof = publicationProof("cluster", pr, in.InvocationKey, in.InvocationAgent, in.InvocationAgentUID, testSigningKey)
	raw, _ := json.Marshal(in)
	out, err := f.s.Operate(t.Context(), f.dyn, "cluster", u, raw, dataplane.ProxiedIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	proof := out.(map[string]any)["scopeProof"].(string)
	zero := 0
	child := Operation{Op: "review", PullRequestUID: string(pr.UID), EventUID: in.EventUID, InvocationKey: in.Delegate.InvocationKey, InvocationAgent: in.Delegate.Agent, InvocationAgentUID: in.Delegate.AgentUID, ReviewOnly: true, ScopeProof: proof, ExpectedHead: strings.Repeat("a", 40), Key: "review", Body: "Clean", FindingsCount: &zero}
	call := func(op Operation) error {
		raw, _ := json.Marshal(op)
		_, err := f.s.Operate(t.Context(), f.dyn, "cluster", u, raw, dataplane.ProxiedIdentity{})
		return err
	}
	if err := call(child); err != nil {
		t.Fatalf("child could not submit clean review: %v", err)
	}
	for _, op := range []string{"publish", "comment", "delegate-review"} {
		changed := child
		changed.Op = op
		if call(changed) == nil {
			t.Fatalf("child could %s", op)
		}
	}
	forged := child
	forged.ReviewOnly = false
	if call(forged) == nil {
		t.Fatal("dropping role escalated authority")
	}
	forged = child
	forged.InvocationAgentUID = "replacement"
	if call(forged) == nil {
		t.Fatal("recreated Agent reused scope")
	}
	forged = child
	forged.ExpectedHead = strings.Repeat("b", 40)
	if call(forged) == nil {
		t.Fatal("stale head review accepted")
	}
}

func TestTaskGrantDelegatesOnlyReviewAuthority(t *testing.T) {
	f, repo, input, issuer := taskTestFixture(t)
	allowTaskCallerSAR(f, true, nil)
	issued, err := authorizeTaskTest(t, f, repo, input, issuer)
	if err != nil {
		t.Fatal(err)
	}
	grantResult := issued.(TaskAuthorizationResult)
	scope := input
	scope.Op = "scope"
	scope.GrantRef = grantResult.GrantRef
	scope.Proof = grantResult.Proof
	scope.InvocationKey = "task:run:1"
	scope.InvocationAgent = "coordinator"
	scope.InvocationAgentUID = "coordinator-uid"
	result, err := authorizeTaskTest(t, f, repo, scope, issuer)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.dyn.Resource(PullRequests).Get(t.Context(), "task-pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := decode[api.PullRequest](stored)
	if err != nil {
		t.Fatal(err)
	}
	parent := Operation{Op: "delegate-review", PullRequestUID: string(pr.UID), SourceUID: input.SourceUID, SourceDigest: input.SourceDigest, GrantRef: grantResult.GrantRef, SourceProof: grantResult.Proof, InvocationKey: scope.InvocationKey, InvocationAgent: scope.InvocationAgent, InvocationAgentUID: scope.InvocationAgentUID, ScopeProof: result.(TaskAuthorizationResult).ScopeProof, Delegate: &ReviewDelegate{InvocationKey: "delegate:task:call", Agent: "reviewer", AgentUID: "reviewer-uid"}}
	raw, _ := json.Marshal(parent)
	delegated, err := f.s.Operate(t.Context(), f.dyn, "tenant", stored, raw, issuer)
	if err != nil {
		t.Fatal(err)
	}
	child := parent
	child.ReviewOnly = true
	child.InvocationKey = parent.Delegate.InvocationKey
	child.InvocationAgent = parent.Delegate.Agent
	child.InvocationAgentUID = parent.Delegate.AgentUID
	child.ScopeProof = delegated.(map[string]any)["scopeProof"].(string)
	child.Delegate = nil
	for _, op := range []string{"publish", "comment", "delegate-review"} {
		child.Op = op
		raw, _ = json.Marshal(child)
		_, err = f.s.Operate(t.Context(), f.dyn, "tenant", stored, raw, issuer)
		if err == nil || !strings.Contains(err.Error(), "review authority") {
			t.Fatalf("task child %s did not reach its role gate: %v", op, err)
		}
	}
	child.ReviewOnly = false
	raw, _ = json.Marshal(child)
	if _, err := f.s.Operate(t.Context(), f.dyn, "tenant", stored, raw, issuer); err == nil || !strings.Contains(err.Error(), "scope proof") {
		t.Fatalf("removing review role was not rejected: %v", err)
	}
}

// The issuer's status comment has no target until the PR is opened; Code says
// so instead of posting to /issues/0/comments and reporting GitHub's 404.
func TestTaskStatusCommentRequiresAnOpenedPullRequest(t *testing.T) {
	f, repo, input, issuer := taskTestFixture(t)
	allowTaskCallerSAR(f, true, nil)
	issued, err := authorizeTaskTest(t, f, repo, input, issuer)
	if err != nil {
		t.Fatal(err)
	}
	grant := issued.(TaskAuthorizationResult)
	scope := input
	scope.Op, scope.GrantRef, scope.Proof = "scope", grant.GrantRef, grant.Proof
	scope.InvocationKey, scope.InvocationAgent, scope.InvocationAgentUID = "task:run:1", "coordinator", "coordinator-uid"
	scoped, err := authorizeTaskTest(t, f, repo, scope, issuer)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.dyn.Resource(PullRequests).Get(t.Context(), "task-pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{Op: "comment", PullRequestUID: string(stored.GetUID()), SourceUID: input.SourceUID, SourceDigest: input.SourceDigest,
		GrantRef: grant.GrantRef, SourceProof: grant.Proof, InvocationKey: scope.InvocationKey, InvocationAgent: scope.InvocationAgent,
		InvocationAgentUID: scope.InvocationAgentUID, ScopeProof: scoped.(TaskAuthorizationResult).ScopeProof, Body: "Working on it"}
	raw, _ := json.Marshal(op)
	if _, err := f.s.Operate(t.Context(), f.dyn, "tenant", stored, raw, issuer); err == nil || !strings.Contains(err.Error(), "has not been opened yet") {
		t.Fatalf("status comment on an unopened PR: %v", err)
	}
}
