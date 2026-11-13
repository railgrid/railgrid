// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
)

// claimReviewOperation uses PullRequest status and Kubernetes resourceVersion
// CAS to serialize same-key retries across Code replicas before the forge write.
func claimReviewOperation(ctx context.Context, client dynamic.Interface, prName string, uid types.UID, repositoryRef, branch, base string, number int, key, head, fingerprint, claimedAt string) (bool, error) {
	return updateReviewOperation(ctx, client, prName, uid, repositoryRef, branch, base, number, key, head, fingerprint, "Accepted", claimedAt, 0)
}

func completeReviewOperation(ctx context.Context, client dynamic.Interface, prName string, uid types.UID, repositoryRef, branch, base string, number int, key, head, fingerprint string, reviewID int64) error {
	_, err := updateReviewOperation(ctx, client, prName, uid, repositoryRef, branch, base, number, key, head, fingerprint, "Complete", "", reviewID)
	return err
}

func claimCommentOperation(ctx context.Context, client dynamic.Interface, prName string, uid types.UID, repositoryRef, branch, base string, number int, key, fingerprint, claimedAt string) (bool, int64, error) {
	claimed, commentID, err := updateCommentOperation(ctx, client, prName, uid, repositoryRef, branch, base, number, key, fingerprint, "Accepted", claimedAt, 0)
	return claimed, commentID, err
}

func completeCommentOperation(ctx context.Context, client dynamic.Interface, prName string, uid types.UID, repositoryRef, branch, base string, number int, key, fingerprint string, commentID int64) error {
	_, _, err := updateCommentOperation(ctx, client, prName, uid, repositoryRef, branch, base, number, key, fingerprint, "Complete", "", commentID)
	return err
}

func updateCommentOperation(ctx context.Context, client dynamic.Interface, prName string, uid types.UID, repositoryRef, branch, base string, number int, key, fingerprint, phase, claimedAt string, commentID int64) (bool, int64, error) {
	claimed := false
	var knownID int64
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pr, err := client.Resource(pullRequests).Get(ctx, prName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pr.GetUID() != uid || pr.GetDeletionTimestamp() != nil {
			return errors.New("pull request changed during comment operation")
		}
		if ref, _, _ := unstructured.NestedString(pr.Object, "spec", "repositoryRef"); ref != repositoryRef {
			return errors.New("pull request repository changed")
		}
		if value, _, _ := unstructured.NestedString(pr.Object, "spec", "branch"); value != branch {
			return errors.New("pull request branch changed")
		}
		if value, _, _ := unstructured.NestedString(pr.Object, "spec", "base"); value != base {
			return errors.New("pull request base changed")
		}
		if value, _, _ := unstructured.NestedInt64(pr.Object, "status", "number"); int(value) != number {
			return errors.New("pull request number changed")
		}
		ops, _, _ := unstructured.NestedSlice(pr.Object, "status", "commentOperations")
		found := -1
		for i, raw := range ops {
			entry, ok := raw.(map[string]any)
			if !ok {
				return errors.New("invalid comment operation status")
			}
			if entry["key"] == key {
				found = i
				break
			}
		}
		if found >= 0 {
			entry, _ := ops[found].(map[string]any)
			knownID, _ = entry["commentID"].(int64)
			oldPhase, _ := entry["phase"].(string)
			oldFingerprint, _ := entry["payloadFingerprint"].(string)
			switch phase {
			case "ForgetMissing":
				// A Complete receipt has no outstanding write, while an Accepted
				// receipt with a known ID can only be retrying a PATCH. Forgetting
				// either is safe after the backend confirms that ID was deleted:
				// PATCH cannot create a replacement comment.
				if (entry["phase"] != "Complete" && entry["phase"] != "Accepted") || knownID <= 0 || entry["payloadFingerprint"] != fingerprint || knownID != commentID {
					return nil
				}
				ops = append(ops[:found], ops[found+1:]...)
			case "Release":
				if entry["phase"] != "Accepted" || entry["payloadFingerprint"] != fingerprint || entry["updatedAt"] != claimedAt {
					return nil
				}
				ops = append(ops[:found], ops[found+1:]...)
			case "Accepted":
				if oldPhase == "Accepted" && oldFingerprint != fingerprint {
					return errors.New("comment key already has an uncertain different payload")
				}
				if oldPhase == "Complete" && oldFingerprint != fingerprint {
					entry["payloadFingerprint"] = fingerprint
					entry["phase"] = "Accepted"
					entry["updatedAt"] = claimedAt
					ops[found] = entry
					claimed = true
				} else {
					claimed = false
					return nil
				}
			default:
				if oldFingerprint != fingerprint || oldPhase != "Accepted" && oldPhase != "Complete" {
					return errors.New("comment operation is not accepted")
				}
				entry["phase"] = "Complete"
				entry["commentID"] = commentID
				entry["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
				ops[found] = entry
			}
		} else {
			if phase == "Release" || phase == "ForgetMissing" {
				return nil
			}
			if phase != "Accepted" {
				return errors.New("comment operation claim missing")
			}
			if len(ops) >= 64 {
				var evicted bool
				ops, evicted = evictCompletedOperation(ops)
				if !evicted {
					return errors.New("comment operation history is full")
				}
			}
			ops = append(ops, map[string]any{"key": key, "payloadFingerprint": fingerprint, "phase": "Accepted", "updatedAt": claimedAt})
			claimed = true
		}
		if err := unstructured.SetNestedSlice(pr.Object, ops, "status", "commentOperations"); err != nil {
			return err
		}
		_, err = client.Resource(pullRequests).UpdateStatus(ctx, pr, metav1.UpdateOptions{})
		return err
	})
	return claimed, knownID, err
}

func updateReviewOperation(ctx context.Context, client dynamic.Interface, prName string, uid types.UID, repositoryRef, branch, base string, number int, key, head, fingerprint, phase, claimedAt string, reviewID int64) (bool, error) {
	claimed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pr, err := client.Resource(pullRequests).Get(ctx, prName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pr.GetUID() != uid {
			return errors.New("pull request was replaced")
		}
		if pr.GetDeletionTimestamp() != nil {
			return errors.New("pull request is deleting")
		}
		if specRef, _, _ := unstructured.NestedString(pr.Object, "spec", "repositoryRef"); specRef != repositoryRef {
			return errors.New("pull request repository changed")
		}
		if specBranch, _, _ := unstructured.NestedString(pr.Object, "spec", "branch"); specBranch != branch {
			return errors.New("pull request branch changed")
		}
		if specBase, _, _ := unstructured.NestedString(pr.Object, "spec", "base"); specBase != base {
			return errors.New("pull request base changed")
		}
		if statusNumber, _, _ := unstructured.NestedInt64(pr.Object, "status", "number"); int(statusNumber) != number {
			return errors.New("pull request number changed")
		}
		ops, _, _ := unstructured.NestedSlice(pr.Object, "status", "reviewOperations")
		found := -1
		for i, raw := range ops {
			entry, ok := raw.(map[string]any)
			if !ok {
				return errors.New("invalid review operation status")
			}
			if entry["key"] == key {
				found = i
				if entry["expectedHead"] != head || entry["payloadFingerprint"] != fingerprint {
					return fmt.Errorf("idempotency key already used for another request")
				}
				break
			}
		}
		if found >= 0 {
			entry := ops[found].(map[string]any)
			switch phase {
			case "Release":
				if entry["phase"] != "Accepted" || entry["payloadFingerprint"] != fingerprint || entry["updatedAt"] != claimedAt {
					return nil
				}
				ops = append(ops[:found], ops[found+1:]...)
			case "Accepted":
				claimed = false
				return nil
			default:
				if entry["phase"] != "Accepted" && entry["phase"] != "Complete" {
					return errors.New("review operation is not accepted")
				}
				entry["phase"] = "Complete"
				entry["reviewID"] = reviewID
				entry["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
				ops[found] = entry
			}
		} else {
			if phase == "Release" {
				return nil
			}
			if phase != "Accepted" {
				return errors.New("review operation claim missing")
			}
			if len(ops) >= 64 {
				var evicted bool
				ops, evicted = evictCompletedOperation(ops)
				if !evicted {
					return errors.New("review operation history is full")
				}
			}
			ops = append(ops, map[string]any{"key": key, "expectedHead": head, "payloadFingerprint": fingerprint, "phase": "Accepted", "updatedAt": claimedAt})
			claimed = true
		}
		if err := unstructured.SetNestedSlice(pr.Object, ops, "status", "reviewOperations"); err != nil {
			return err
		}
		_, err = client.Resource(pullRequests).UpdateStatus(ctx, pr, metav1.UpdateOptions{})
		return err
	})
	if apierrors.IsNotFound(err) {
		return false, err
	}
	return claimed, err
}

// Only settled operations can be evicted. Their authenticated markers remain on
// the forge, where replay can still recover them after local retention.
func evictCompletedOperation(ops []any) ([]any, bool) {
	oldest := -1
	var updated time.Time
	for i, raw := range ops {
		entry, _ := raw.(map[string]any)
		if entry["phase"] != "Complete" {
			continue
		}
		value, _ := entry["updatedAt"].(string)
		at, _ := time.Parse(time.RFC3339Nano, value)
		if oldest < 0 || at.Before(updated) {
			oldest, updated = i, at
		}
	}
	if oldest < 0 {
		return ops, false
	}
	return append(ops[:oldest], ops[oldest+1:]...), true
}
