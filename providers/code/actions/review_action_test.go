// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type reviewActionBackend struct {
	backend.GitBackend
	backend.Collaboration
	backend.ReviewOperations

	reviewErr   error
	commentErr  error
	reviewCalls []backend.ReviewInput
	commentCall []backend.ManagedCommentInput
	commentNext *backend.Comment
}

func (*reviewActionBackend) Name() string { return "github" }

func (*reviewActionBackend) ReadPullRequest(context.Context, *api.Connection, backend.Credential, *api.Repository, int) (*backend.PullRequest, error) {
	return &backend.PullRequest{Number: 7, Repository: "example/product", HeadRepository: "example/product", Head: "feature", Base: "main", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "open"}, nil
}

func (b *reviewActionBackend) SubmitReview(_ context.Context, _ *api.Connection, _ backend.Credential, _ *api.Repository, in backend.ReviewInput) (*backend.ReviewResult, error) {
	b.reviewCalls = append(b.reviewCalls, in)
	if b.reviewErr != nil {
		return nil, b.reviewErr
	}
	return &backend.ReviewResult{Review: backend.Review{ID: 12, Commit: in.ExpectedHead}}, nil
}

func (b *reviewActionBackend) UpsertManagedComment(_ context.Context, _ *api.Connection, _ backend.Credential, _ *api.Repository, in backend.ManagedCommentInput) (*backend.Comment, error) {
	b.commentCall = append(b.commentCall, in)
	if b.commentErr != nil {
		return nil, b.commentErr
	}
	if b.commentNext != nil {
		return b.commentNext, nil
	}
	return &backend.Comment{ID: 101}, nil
}

func reviewActionPullRequest() *api.PullRequest {
	return &api.PullRequest{
		TypeMeta:   metav1.TypeMeta{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "PullRequest"},
		ObjectMeta: metav1.ObjectMeta{Name: "pr", UID: "pr-uid"},
		Spec:       api.PullRequestSpec{RepositoryRef: "product", Branch: "feature", Base: "main"},
		Status:     api.PullRequestStatus{Number: 7},
	}
}

func reviewActionFixture(t *testing.T, b *reviewActionBackend, initial ...*unstructured.Unstructured) (*Server, *conformance.FakeCallers) {
	t.Helper()
	pullRequest := actionObject(t, reviewActionPullRequest())
	for _, object := range initial {
		if object.GetName() == "pr" && object.GetKind() == "PullRequest" {
			pullRequest = object
		}
	}
	if err := unstructured.SetNestedField(pullRequest.Object, int64(7), "status", "number"); err != nil {
		t.Fatal(err)
	}
	objects := []*unstructured.Unstructured{
		pullRequest,
		actionObject(t, testRepository()),
		actionObject(t, testConnection()),
		testSecret(),
	}
	kinds := make(map[schema.GroupVersionResource]string, len(testListKinds)+1)
	for gvr, kind := range testListKinds {
		kinds[gvr] = kind
	}
	kinds[pullRequests] = "PullRequestList"
	callers := &conformance.FakeCallers{
		Cluster:   testCluster,
		User:      testUser,
		Objects:   objects,
		ListKinds: kinds,
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Group == pullRequests.Group && a.Resource == pullRequests.Resource && a.Name == "pr" && a.Subresource == ""
		},
	}
	registry := backend.NewRegistry()
	if err := registry.Register(b); err != nil {
		t.Fatal(err)
	}
	return New(callers, registry), callers
}

func invokeReviewAction(t *testing.T, server *Server, verb string, input any) actionwire.Envelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, actionRequest(http.MethodPost, kubePath(testCluster, pullRequests.Resource, "pr", verb), bytes.NewReader(body), testUser))
	var envelope actionwire.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response %d %s: %v", response.Code, response.Body.String(), err)
	}
	return envelope
}

func TestReviewActionsReleaseClaimsAfterDefinitiveRejection(t *testing.T) {
	reject := &backend.ReviewWriteRejected{Err: errors.New("definitive host rejection"), Permanent: true}
	for _, verb := range []string{"submit-review", "upsert-comment"} {
		t.Run(verb, func(t *testing.T) {
			b := &reviewActionBackend{}
			if verb == "submit-review" {
				b.reviewErr = reject
			} else {
				b.commentErr = reject
			}
			server, callers := reviewActionFixture(t, b)
			input := map[string]any{"repository": "example/product", "repositoryUID": "repo-uid", "connectionUID": "conn-uid", "body": "review text"}
			if verb == "submit-review" {
				input["expectedHead"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				input["idempotencyKey"] = "review-key"
			} else {
				input["commentKey"] = "comment-key"
			}
			for i := 0; i < 2; i++ {
				result := invokeReviewAction(t, server, verb, input)
				if result.Error == nil || result.Error.Code != "upstream_outcome_unconfirmed" {
					t.Fatalf("rejection response = %+v", result.Error)
				}
			}
			if verb == "submit-review" {
				if len(b.reviewCalls) != 2 || b.reviewCalls[0].RecoverOnly || b.reviewCalls[1].RecoverOnly {
					t.Fatalf("review claim was not released for retry: %+v", b.reviewCalls)
				}
			} else if len(b.commentCall) != 2 || b.commentCall[0].RecoverOnly || b.commentCall[1].RecoverOnly {
				t.Fatalf("comment claim was not released for retry: %+v", b.commentCall)
			}
			client, err := callers.AsProvider(testCluster)
			if err != nil {
				t.Fatal(err)
			}
			pr, err := client.Resource(pullRequests).Get(t.Context(), "pr", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			field := "reviewOperations"
			if verb == "upsert-comment" {
				field = "commentOperations"
			}
			ops, _, _ := unstructured.NestedSlice(pr.Object, "status", field)
			if len(ops) != 0 {
				t.Fatalf("definitively rejected receipt remains fenced: %v", ops)
			}
		})
	}
}

func TestUpsertCommentActionForgetsMissingReceiptBeforeRecreate(t *testing.T) {
	input := backend.ManagedCommentInput{Number: 7, Key: "comment-key", Body: "comment body"}
	fingerprint := backend.CommentFingerprint(input)
	pr := actionObject(t, reviewActionPullRequest())
	if err := unstructured.SetNestedSlice(pr.Object, []any{map[string]any{
		"key": "comment-key", "payloadFingerprint": fingerprint, "phase": "Complete", "commentID": int64(99), "updatedAt": "2026-01-01T00:00:00Z",
	}}, "status", "commentOperations"); err != nil {
		t.Fatal(err)
	}
	b := &reviewActionBackend{commentErr: backend.ErrManagedCommentMissing}
	server, _ := reviewActionFixture(t, b, pr)
	request := map[string]any{"repository": "example/product", "repositoryUID": "repo-uid", "connectionUID": "conn-uid", "body": input.Body, "commentKey": input.Key}

	first := invokeReviewAction(t, server, "upsert-comment", request)
	if first.Error == nil || first.Error.Code != "upstream_outcome_unconfirmed" {
		t.Fatalf("confirmed missing comment response = %+v", first.Error)
	}
	b.commentErr = nil
	second := invokeReviewAction(t, server, "upsert-comment", request)
	if second.Error != nil || len(second.Result) == 0 {
		t.Fatalf("replacement create response = %+v result=%s", second.Error, second.Result)
	}
	if len(b.commentCall) != 2 || !b.commentCall[0].RecoverOnly || b.commentCall[0].CommentID != 99 || b.commentCall[1].RecoverOnly || b.commentCall[1].CommentID != 0 {
		t.Fatalf("missing receipt was not forgotten before replacement create: %+v", b.commentCall)
	}
}
