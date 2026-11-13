// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
)

func operationClient() *fake.FakeDynamicClient {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "code.railgrid.ai/v1alpha1", "kind": "PullRequest",
		"metadata": map[string]any{"name": "pr", "uid": "uid-a", "resourceVersion": "1"},
		"spec":     map[string]any{"repositoryRef": "repo", "branch": "feature", "base": "main"},
		"status":   map[string]any{"number": int64(7)},
	}}
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{pullRequests: "PullRequestList"}, obj)
}

func TestReviewReceiptSerializesAndPinsPayloadAndParent(t *testing.T) {
	client := operationClient()
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fingerprint := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	claimed, err := claimReviewOperation(context.Background(), client, "pr", types.UID("uid-a"), "repo", "feature", "main", 7, "opaque-key", head, fingerprint, "2026-01-01T00:00:00Z")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = claimReviewOperation(context.Background(), client, "pr", types.UID("uid-a"), "repo", "feature", "main", 7, "opaque-key", head, fingerprint, "2026-01-01T00:00:00Z")
	if err != nil || claimed {
		t.Fatalf("duplicate claim = %v, %v", claimed, err)
	}
	if _, err = claimReviewOperation(context.Background(), client, "pr", types.UID("uid-a"), "repo", "feature", "main", 7, "opaque-key", head, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("same key with another payload was accepted")
	}
	if _, err = claimReviewOperation(context.Background(), client, "pr", types.UID("uid-b"), "repo", "feature", "main", 7, "other-key", head, fingerprint, "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("replacement PullRequest UID was accepted")
	}
}

func TestCommentReceiptAllowsUpdateOnlyAfterCompletion(t *testing.T) {
	client := operationClient()
	claim, commentID, err := claimCommentOperation(context.Background(), client, "pr", "uid-a", "repo", "feature", "main", 7, "managed-key", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "2026-01-01T00:00:00Z")
	if err != nil || !claim || commentID != 0 {
		t.Fatalf("first comment claim = %v, %d, %v", claim, commentID, err)
	}
	if _, _, err = claimCommentOperation(context.Background(), client, "pr", "uid-a", "repo", "feature", "main", 7, "managed-key", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("uncertain key was reused with changed content")
	}
	if err = completeCommentOperation(context.Background(), client, "pr", "uid-a", "repo", "feature", "main", 7, "managed-key", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 44); err != nil {
		t.Fatal(err)
	}
	claim, commentID, err = claimCommentOperation(context.Background(), client, "pr", "uid-a", "repo", "feature", "main", 7, "managed-key", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "2026-01-01T00:00:00Z")
	if err != nil || !claim || commentID != 44 {
		t.Fatalf("completed comment update claim = %v, %d, %v", claim, commentID, err)
	}
}

func TestReviewReceiptReleaseAllowsRetryAfterConfirmedFailure(t *testing.T) {
	ctx := t.Context()
	client := operationClient()
	claim := func(at string) (bool, error) {
		return claimReviewOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "head", "fingerprint", at)
	}
	if ok, err := claim("2026-01-01T00:00:00Z"); !ok || err != nil {
		t.Fatalf("initial claim = %t, %v", ok, err)
	}
	if _, err := updateReviewOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "head", "fingerprint", "Release", "2026-01-01T00:00:00Z", 0); err != nil {
		t.Fatal(err)
	}
	if ok, err := claim("2026-01-01T00:00:01Z"); !ok || err != nil {
		t.Fatalf("retry claim = %t, %v", ok, err)
	}
	if _, err := updateReviewOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "head", "fingerprint", "Release", "2026-01-01T00:00:00Z", 0); err != nil {
		t.Fatal(err)
	}
	if ok, err := claim("2026-01-01T00:00:02Z"); ok || err != nil {
		t.Fatalf("stale release removed the replacement claim: claimed=%t, err=%v", ok, err)
	}
}

func TestEvictCompletedOperationKeepsUncertainReceipts(t *testing.T) {
	accepted := map[string]any{"key": "accepted", "phase": "Accepted", "updatedAt": "2026-01-01T00:00:00Z"}
	newer := map[string]any{"key": "newer", "phase": "Complete", "updatedAt": "2026-01-01T01:00:00Z"}
	older := map[string]any{"key": "older", "phase": "Complete", "updatedAt": "2026-01-01T00:00:00.1Z"}
	got, ok := evictCompletedOperation([]any{accepted, newer, older})
	if !ok || len(got) != 2 || got[0].(map[string]any)["key"] != "accepted" || got[1].(map[string]any)["key"] != "newer" {
		t.Fatalf("wrong receipt evicted: %v, ok=%t", got, ok)
	}
	if _, ok := evictCompletedOperation([]any{accepted}); ok {
		t.Fatal("evicted an uncertain write")
	}
}

func TestMissingCompletedCommentRequiresNewClaim(t *testing.T) {
	ctx := t.Context()
	client := operationClient()
	const at = "2026-01-01T00:00:00Z"
	if ok, _, err := claimCommentOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "fingerprint", at); !ok || err != nil {
		t.Fatalf("initial claim = %t, %v", ok, err)
	}
	if err := completeCommentOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "fingerprint", 99); err != nil {
		t.Fatal(err)
	}
	if _, _, err := updateCommentOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "fingerprint", "ForgetMissing", "", 99); err != nil {
		t.Fatal(err)
	}
	if ok, id, err := claimCommentOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "fingerprint", at); !ok || id != 0 || err != nil {
		t.Fatalf("replacement claim = %t, %d, %v", ok, id, err)
	}
}

func TestMissingAcceptedEditCanBeForgottenAndRecreated(t *testing.T) {
	ctx := t.Context()
	client := operationClient()
	const at = "2026-01-01T00:00:00Z"
	if ok, _, err := claimCommentOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "fingerprint", at); !ok || err != nil {
		t.Fatalf("initial claim = %t, %v", ok, err)
	}
	// The write may have succeeded upstream before the process lost its
	// response, so persist the comment ID while the payload remains Accepted.
	pr, err := client.Resource(pullRequests).Get(ctx, "pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ops, _, _ := unstructured.NestedSlice(pr.Object, "status", "commentOperations")
	entry := ops[0].(map[string]any)
	entry["commentID"] = int64(99)
	ops[0] = entry
	if err := unstructured.SetNestedSlice(pr.Object, ops, "status", "commentOperations"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(pullRequests).UpdateStatus(ctx, pr, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := updateCommentOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "fingerprint", "ForgetMissing", "", 99); err != nil {
		t.Fatalf("forget missing accepted edit: %v", err)
	}
	if ok, id, err := claimCommentOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", "fingerprint", at); !ok || id != 0 || err != nil {
		t.Fatalf("replacement create claim = %t, %d, %v", ok, id, err)
	}
}

func TestForgetMissingRequiresMatchingPhaseFingerprintAndCommentID(t *testing.T) {
	for _, tc := range []struct {
		name              string
		phase             string
		storedFingerprint string
		storedID          int64
		fingerprint       string
		commentID         int64
	}{
		{name: "invalid phase", phase: "Pending", storedFingerprint: "fingerprint", storedID: 99, fingerprint: "fingerprint", commentID: 99},
		{name: "fingerprint mismatch", phase: "Complete", storedFingerprint: "other", storedID: 99, fingerprint: "fingerprint", commentID: 99},
		{name: "comment ID mismatch", phase: "Complete", storedFingerprint: "fingerprint", storedID: 100, fingerprint: "fingerprint", commentID: 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			client := operationClient()
			pr, err := client.Resource(pullRequests).Get(ctx, "pr", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedSlice(pr.Object, []any{map[string]any{
				"key": "key", "phase": tc.phase, "payloadFingerprint": tc.storedFingerprint, "commentID": tc.storedID,
			}}, "status", "commentOperations"); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Resource(pullRequests).UpdateStatus(ctx, pr, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := updateCommentOperation(ctx, client, "pr", "uid-a", "repo", "feature", "main", 7, "key", tc.fingerprint, "ForgetMissing", "", tc.commentID); err != nil {
				t.Fatalf("ForgetMissing returned error: %v", err)
			}
			pr, err = client.Resource(pullRequests).Get(ctx, "pr", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ops, _, _ := unstructured.NestedSlice(pr.Object, "status", "commentOperations")
			if len(ops) != 1 {
				t.Fatalf("invalid ForgetMissing removed receipt: %v", ops)
			}
			entry := ops[0].(map[string]any)
			if entry["phase"] != tc.phase || entry["payloadFingerprint"] != tc.storedFingerprint || entry["commentID"] != tc.storedID {
				t.Fatalf("invalid ForgetMissing changed receipt: %v", entry)
			}
		})
	}
}
