// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-sdk/dataplane"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
)

func taskTestFixture(t *testing.T) (*fixture, *unstructured.Unstructured, TaskAuthorizationInput, dataplane.ProxiedIdentity) {
	t.Helper()
	f := newFixture(t)
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	f.s.Now = func() time.Time { return now }
	pr := &api.PullRequest{
		TypeMeta:   metav1.TypeMeta{APIVersion: api.SchemeGroupVersion.String(), Kind: "PullRequest"},
		ObjectMeta: metav1.ObjectMeta{Name: "task-pr", UID: "task-pr-uid", ResourceVersion: "1"},
		Spec:       api.PullRequestSpec{RepositoryRef: "repo", Branch: "task-branch", Base: "main", Title: "Task work"},
	}
	u, err := asU(pr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.dyn.Resource(PullRequests).Create(t.Context(), u, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	repo, err := f.dyn.Resource(Repositories).Get(t.Context(), "repo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	in := TaskAuthorizationInput{
		Op: "grant", SourceUID: "task-uid", SourceDigest: strings.Repeat("a", 64),
		Repository: "railgrid/example", RepositoryUID: "repo-uid", ConnectionUID: "conn",
		PullRequest: "task-pr", PullRequestUID: "task-pr-uid",
		Caller:   TaskCaller{User: "alice", UID: "alice-uid", Groups: []string{"engineering", "users"}},
		Deadline: now.Add(time.Hour), Mode: "review-fix",
	}
	issuer := dataplane.ProxiedIdentity{User: "system:serviceaccount:factory:controller", Extra: map[string][]string{dataplane.ClusterNameExtra: {"factory"}}}
	return f, repo, in, issuer
}

func taskAuthorizationRaw(t *testing.T, in TaskAuthorizationInput) []byte {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func authorizeTaskTest(t *testing.T, f *fixture, repo *unstructured.Unstructured, in TaskAuthorizationInput, issuer dataplane.ProxiedIdentity) (any, error) {
	t.Helper()
	return f.s.AuthorizeTask(t.Context(), f.dyn, "tenant", "repo", repo, issuer, taskAuthorizationRaw(t, in))
}

func allowTaskCallerSAR(f *fixture, allowed bool, check func(*unstructured.Unstructured) error) {
	f.dyn.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(ktesting.CreateAction)
		if !ok {
			return true, nil, errors.New("unexpected SAR action")
		}
		review, ok := create.GetObject().(*unstructured.Unstructured)
		if !ok {
			return true, nil, errors.New("SAR is not unstructured")
		}
		if check != nil {
			if err := check(review); err != nil {
				return true, nil, err
			}
		}
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "authorization.k8s.io/v1", "kind": "SubjectAccessReview",
			"status": map[string]any{"allowed": allowed},
		}}, nil
	})
}

func installTaskPRVersionCheck(f *fixture) {
	currentVersion := 1
	f.dyn.PrependReactor("update", PullRequests.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
		update, ok := action.(ktesting.UpdateAction)
		if !ok {
			return true, nil, errors.New("unexpected PullRequest update")
		}
		candidate, ok := update.GetObject().(*unstructured.Unstructured)
		if !ok {
			return true, nil, errors.New("PullRequest update is not unstructured")
		}
		if candidate.GetResourceVersion() != strconv.Itoa(currentVersion) {
			return true, nil, apierrors.NewConflict(PullRequests.GroupResource(), candidate.GetName(), fmt.Errorf("resourceVersion changed"))
		}
		currentVersion++
		candidate.SetResourceVersion(strconv.Itoa(currentVersion))
		return false, nil, nil
	})
}

func TestTaskGrantPinsCallerAndIsIdempotent(t *testing.T) {
	f, repo, in, issuer := taskTestFixture(t)
	allowTaskCallerSAR(f, true, func(review *unstructured.Unstructured) error {
		user, _, _ := unstructured.NestedString(review.Object, "spec", "user")
		group, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "group")
		resource, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "resource")
		subresource, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "subresource")
		name, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "name")
		verb, _, _ := unstructured.NestedString(review.Object, "spec", "resourceAttributes", "verb")
		if user != "alice" || group != "code.railgrid.ai" || resource != "pullrequests" || subresource != "operate" || name != "task-pr" || verb != "create" {
			return fmt.Errorf("caller SAR did not ask for scoped Code write access: %s", review)
		}
		return nil
	})
	first, err := authorizeTaskTest(t, f, repo, in, issuer)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := first.(TaskAuthorizationResult)
	if !ok || result.Proof == "" || result.GrantRef == "" || result.Repository != "railgrid/example" || result.PullRequestUID != "task-pr-uid" {
		t.Fatalf("unexpected grant result: %#v", first)
	}
	second, err := authorizeTaskTest(t, f, repo, in, issuer)
	if err != nil || second.(TaskAuthorizationResult).Proof != result.Proof {
		t.Fatalf("grant replay was not idempotent: result=%#v err=%v", second, err)
	}
	stored, err := f.dyn.Resource(PullRequests).Get(t.Context(), "task-pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := decode[api.PullRequest](stored)
	if err != nil || pr.Spec.Command == nil || pr.Spec.Command.Task == nil || pr.Spec.Command.Task.Proof != result.Proof {
		t.Fatalf("grant not stored on the existing PR: pr=%#v err=%v", pr, err)
	}
}

func TestTaskGrantRequiresCallerWritePermissionAndForeignIssuer(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprintf("write-permission-%t", allowed), func(t *testing.T) {
			f, repo, in, issuer := taskTestFixture(t)
			allowTaskCallerSAR(f, allowed, nil)
			_, err := authorizeTaskTest(t, f, repo, in, issuer)
			if (err == nil) != allowed {
				t.Fatalf("allowed=%v err=%v", allowed, err)
			}
		})
	}
	f, repo, in, issuer := taskTestFixture(t)
	allowTaskCallerSAR(f, true, nil)
	issuer.Extra[dataplane.ClusterNameExtra] = []string{"tenant"}
	if _, err := authorizeTaskTest(t, f, repo, in, issuer); err == nil {
		t.Fatal("same-workspace provider identity minted a task grant")
	}
}

func TestTaskAuthorizationScopeAndBranchBinding(t *testing.T) {
	f, repo, in, issuer := taskTestFixture(t)
	allowTaskCallerSAR(f, true, nil)
	issued, err := authorizeTaskTest(t, f, repo, in, issuer)
	if err != nil {
		t.Fatal(err)
	}
	grant := issued.(TaskAuthorizationResult)
	scope := in
	scope.Op, scope.GrantRef, scope.Proof = "scope", grant.GrantRef, grant.Proof
	scope.InvocationKey, scope.InvocationAgent, scope.InvocationAgentUID = "task:run:1", "reviewer", "reviewer-uid"
	first, err := authorizeTaskTest(t, f, repo, scope, issuer)
	if err != nil || first.(TaskAuthorizationResult).ScopeProof == "" {
		t.Fatalf("trusted issuer could not scope an invocation: result=%#v err=%v", first, err)
	}
	other := issuer
	other.User = "system:serviceaccount:other:controller"
	if _, err := authorizeTaskTest(t, f, repo, scope, other); err == nil {
		t.Fatal("another provider identity reused the source grant")
	}
	stored, err := f.dyn.Resource(PullRequests).Get(t.Context(), "task-pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := decode[api.PullRequest](stored)
	if err != nil {
		t.Fatal(err)
	}
	pr.Spec.Branch = "other-branch"
	changed, _ := asU(pr)
	if _, err := f.dyn.Resource(PullRequests).Update(t.Context(), changed, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.activeTaskAuthorization(t.Context(), f.dyn, "tenant", pr); err == nil {
		t.Fatal("branch mutation retained the original task authority")
	}
}

func TestTaskRevokeSurvivesSecretRotationAndBlocksRegrant(t *testing.T) {
	f, repo, in, issuer := taskTestFixture(t)
	allowTaskCallerSAR(f, true, nil)
	issued, err := authorizeTaskTest(t, f, repo, in, issuer)
	if err != nil {
		t.Fatal(err)
	}
	grant := issued.(TaskAuthorizationResult)
	secret, err := f.dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace("default").Get(t.Context(), "app-key", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var secretObject corev1.Secret
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(secret.Object, &secretObject); err != nil {
		t.Fatal(err)
	}
	secretObject.Data["webhookSecret"] = []byte("rotated webhook signing key that is at least 32 bytes")
	rotatedSecret, err := asU(&secretObject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace("default").Update(t.Context(), rotatedSecret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	revoke := in
	revoke.Op, revoke.Caller, revoke.Deadline, revoke.Mode = "revoke", TaskCaller{}, time.Time{}, ""
	revoke.GrantRef, revoke.Proof = "", ""
	revoked, err := authorizeTaskTest(t, f, repo, revoke, issuer)
	if err != nil || !revoked.(TaskAuthorizationResult).Revoked {
		t.Fatalf("revoke failed after key rotation: result=%#v err=%v", revoked, err)
	}
	_, err = authorizeTaskTest(t, f, repo, revoke, issuer)
	if err != nil {
		t.Fatalf("revoke retry was not idempotent: %v", err)
	}
	prObject, err := f.dyn.Resource(PullRequests).Get(t.Context(), "task-pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := decode[api.PullRequest](prObject)
	if err != nil || pr.Spec.Command != nil || pr.Spec.DesiredHead != nil || len(pr.Spec.TaskAuthorizationRevocations) != 1 {
		t.Fatalf("revoke did not clear authority once: pr=%#v err=%v", pr.Spec, err)
	}
	if grant.GrantRef == "" {
		t.Fatal("grant reference unexpectedly empty")
	}
	rotatedGrant := in
	if _, err = authorizeTaskTest(t, f, repo, rotatedGrant, issuer); err == nil {
		t.Fatal("revoked task source re-acquired publication authority")
	}
}

func TestTaskRevokeBeforeGrantClosesLostResponseRace(t *testing.T) {
	f, repo, in, issuer := taskTestFixture(t)
	installTaskPRVersionCheck(f)
	allowTaskCallerSAR(f, true, nil)
	revoke := in
	revoke.Op, revoke.Caller, revoke.Deadline, revoke.Mode = "revoke", TaskCaller{}, time.Time{}, ""
	if _, err := authorizeTaskTest(t, f, repo, revoke, issuer); err != nil {
		t.Fatal(err)
	}
	if _, err := authorizeTaskTest(t, f, repo, in, issuer); err == nil {
		t.Fatal("late grant after pre-grant revocation was accepted")
	}
}

func TestTaskGrantUpdateConflictsWithConcurrentRevocation(t *testing.T) {
	f, repo, in, issuer := taskTestFixture(t)
	installTaskPRVersionCheck(f)
	fixedNow := f.s.Now()
	revokedInsideGrant := false
	var revokeErr error
	f.s.Now = func() time.Time {
		if !revokedInsideGrant {
			revokedInsideGrant = true
			revoke := in
			revoke.Op, revoke.Caller, revoke.Deadline, revoke.Mode = "revoke", TaskCaller{}, time.Time{}, ""
			_, revokeErr = authorizeTaskTest(t, f, repo, revoke, issuer)
		}
		return fixedNow
	}
	allowTaskCallerSAR(f, true, nil)
	if _, err := authorizeTaskTest(t, f, repo, in, issuer); !apierrors.IsConflict(err) {
		t.Fatalf("grant did not lose the resourceVersion race to cancellation: %v", err)
	}
	if revokeErr != nil {
		t.Fatalf("concurrent revoke failed: %v", revokeErr)
	}
	stored, err := f.dyn.Resource(PullRequests).Get(t.Context(), "task-pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := decode[api.PullRequest](stored)
	if err != nil || pr.Spec.Command != nil || len(pr.Spec.TaskAuthorizationRevocations) != 1 {
		t.Fatalf("cancellation tombstone lost to stale grant: pr=%#v err=%v", pr.Spec, err)
	}
	if _, err = authorizeTaskTest(t, f, repo, in, issuer); err == nil {
		t.Fatal("grant retry ignored the committed cancellation tombstone")
	}
}
