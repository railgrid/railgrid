// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/faroshq/provider-linear/internal/actionapi"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/linearapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var Connections = schema.GroupVersionResource{Group: api.GroupName, Version: api.Version, Resource: "connections"}
var Receipts = schema.GroupVersionResource{Group: "linear.internal.faros.sh", Version: api.Version, Resource: "actionreceipts"}
var secrets = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

type Engine struct {
	Client        dynamic.Interface
	ReceiptClient dynamic.Interface
	Access        TeamAccess
	NewClient     func(string) *linearapi.Client
	Now           func() time.Time
}

func (e Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
func (e Engine) API(key string) *linearapi.Client {
	if e.NewClient != nil {
		return e.NewClient(key)
	}
	return linearapi.New(key)
}
func Decode(u *unstructured.Unstructured, out any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out)
}
func (e Engine) Secret(ctx context.Context, ref api.SecretReference) (string, error) {
	if ref.Name == "" {
		return "", errors.New("credential Secret reference required")
	}
	ns := ref.Namespace
	if ns == "" {
		ns = "default"
	}
	u, err := e.Client.Resource(secrets).Namespace(ns).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", errors.New("credential Secret unavailable")
	}
	key := ref.Key
	if key == "" {
		key = "apiKey"
	}
	s, _, _ := unstructured.NestedString(u.Object, "data", key)
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) == 0 || len(b) > 8192 {
		return "", errors.New("credential Secret key missing or invalid")
	}
	return string(b), nil
}

// ConnectionMetadata checks the resource identity without resolving credentials.
func (e Engine) ConnectionMetadata(ctx context.Context, name string) (api.Connection, error) {
	var conn api.Connection
	u, err := e.Client.Resource(Connections).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return conn, errors.New("connection unavailable")
	}
	if err = Decode(u, &conn); err != nil {
		return conn, err
	}
	if conn.DeletionTimestamp != nil {
		return conn, errors.New("connection is deleting")
	}
	return conn, nil
}
func (e Engine) Connection(ctx context.Context, name string) (api.Connection, string, error) {
	conn, err := e.ConnectionMetadata(ctx, name)
	if err != nil {
		return conn, "", err
	}
	key, err := e.Secret(ctx, conn.Spec.APIKeySecretRef)
	return conn, key, err
}
func Allowed(access TeamAccess, team string) bool { return team != "" && access[team] }
func (e Engine) Probe(ctx context.Context, u *unstructured.Unstructured) error {
	var c api.Connection
	if err := Decode(u, &c); err != nil {
		return err
	}
	if c.Status.CheckedAt != nil && e.now().Sub(c.Status.CheckedAt.Time) < time.Minute && c.Generation == c.Status.ObservedGeneration {
		return nil
	}
	key, err := e.Secret(ctx, c.Spec.APIKeySecretRef)
	if err == nil {
		_, err = e.API(key).Teams(ctx, 1, "")
	}
	now := metav1.NewTime(e.now())
	c.Status = api.ConnectionStatus{Ready: err == nil, ObservedGeneration: c.Generation, CheckedAt: &now}
	if err != nil {
		c.Status.Message = "Credentials unavailable or Linear rejected the readiness check"
	}
	status, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&c.Status)
	if err != nil {
		return err
	}
	u.Object["status"] = status
	_, err = e.Client.Resource(Connections).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	return err
}
func (e Engine) save(ctx context.Context, u *unstructured.Unstructured, s actionapi.Outcome) error {
	v, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&s)
	if err != nil {
		return err
	}
	u.Object["status"] = v
	updated, err := e.receiptClient().Resource(Receipts).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	if err == nil {
		*u = *updated
	}
	return err
}
func mutation(action string) bool {
	return action == "createIssue" || action == "updateIssue" || action == "addComment"
}
func (e Engine) Reconcile(ctx context.Context, u *unstructured.Unstructured) error {
	var op actionapi.Receipt
	if err := Decode(u, &op); err != nil {
		return err
	}
	const finalizer = "linear.internal.faros.sh/receipt-history"
	if op.DeletionTimestamp != nil {
		if op.Status.Phase == "Running" && mutation(op.Spec.Action) {
			// A deletion can invalidate the dispatcher's completion update. On
			// recovery retain the evidence, but do not claim the write is still live.
			op.Status.Phase = "Uncertain"
			op.Status.Message = "Deletion interrupted outcome recording. Inspect Linear; no automatic replay."
			return e.save(ctx, u, op.Status)
		}
		if op.Status.Phase == "Uncertain" {
			return nil
		}
		finalizers := []string{}
		for _, f := range u.GetFinalizers() {
			if f != finalizer {
				finalizers = append(finalizers, f)
			}
		}
		u.SetFinalizers(finalizers)
		_, err := e.receiptClient().Resource(Receipts).Update(ctx, u, metav1.UpdateOptions{})
		return err
	}
	if op.Status.Phase == "Succeeded" || op.Status.Phase == "Failed" || op.Status.Phase == "Uncertain" {
		return nil
	}
	found := false
	for _, f := range u.GetFinalizers() {
		if f == finalizer {
			found = true
		}
	}
	if !found {
		u.SetFinalizers(append(u.GetFinalizers(), finalizer))
		updated, err := e.receiptClient().Resource(Receipts).Update(ctx, u, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		*u = *updated
	}

	if op.Status.Phase == "Running" {
		if mutation(op.Spec.Action) {
			op.Status.Phase = "Uncertain"
			op.Status.Message = "A prior write may have reached Linear. Inspect Linear and this operation; no automatic replay."
			return e.save(ctx, u, op.Status)
		}
	}
	conn, err := e.ConnectionMetadata(ctx, op.Spec.Connection)
	if err != nil {
		return e.finish(ctx, u, op.Status, nil, err, false)
	}
	// The API server clears status on create. The receipt annotation is the
	// durable dispatch binding and must be checked before reading any Secret.
	expectedUID := op.Annotations["linear.internal.faros.sh/connection-uid"]
	if expectedUID == "" || expectedUID != string(conn.UID) || (op.Status.ConnectionUID != "" && op.Status.ConnectionUID != expectedUID) {
		return e.finish(ctx, u, op.Status, nil, errors.New("connection identity missing or replaced"), false)
	}
	key, err := e.Secret(ctx, conn.Spec.APIKeySecretRef)
	var access TeamAccess
	if err == nil {
		if e.Access != nil {
			access = e.Access
		} else {
			access, err = e.effectivePolicy(ctx, conn)
		}
	}
	if err != nil {
		return e.finish(ctx, u, op.Status, nil, err, false)
	}
	// Persist the one-shot dispatch fence before any external request. Conflicting
	// controllers cannot both claim a Pending operation. Reads may safely resume.
	now := metav1.NewTime(e.now())
	op.Status = actionapi.Outcome{Phase: "Running", StartedAt: &now, ConnectionUID: string(conn.UID)}
	if err = e.save(ctx, u, op.Status); err != nil {
		return err
	}
	value, err := Execute(ctx, e.API(key), access, op.Spec)
	var upstream *linearapi.Error
	uncertain := errors.As(err, &upstream) && upstream.Uncertain
	return e.finish(ctx, u, op.Status, value, err, uncertain)
}
func (e Engine) finish(ctx context.Context, u *unstructured.Unstructured, s actionapi.Outcome, value any, err error, uncertain bool) error {
	now := metav1.NewTime(e.now())
	s.CompletedAt = &now
	s.Phase = "Succeeded"
	if err != nil {
		s.Phase = "Failed"
		s.Message = err.Error()
		if uncertain {
			s.Phase = "Uncertain"
		}
	} else {
		b, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return marshalErr
		}
		if len(b) > 512*1024 {
			return errors.New("operation result too large")
		}
		s.Result = &runtime.RawExtension{Raw: b}
	}
	if err := e.save(ctx, u, s); err != nil {
		return err
	}
	// Dispatch inputs are no longer needed once the outcome is known. The
	// action boundary retains their digest for same-key conflict detection.
	if e.ReceiptClient != nil {
		spec, _, _ := unstructured.NestedMap(u.Object, "spec")
		u.Object["spec"] = map[string]any{"connection": spec["connection"], "action": spec["action"], "teamID": spec["teamID"]}
		updated, err := e.receiptClient().Resource(Receipts).Update(ctx, u, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		*u = *updated
	}
	return nil
}
func Execute(ctx context.Context, c *linearapi.Client, access TeamAccess, s actionapi.Input) (any, error) {
	if s.First < 0 || s.First > 50 {
		return nil, errors.New("page size must be 1 to 50")
	}
	if s.Action == "teams" {
		page, err := c.Teams(ctx, s.First, s.After)
		filtered := make([]linearapi.Team, 0, len(page.Nodes))
		for _, t := range page.Nodes {
			if Allowed(access, t.ID) {
				filtered = append(filtered, t)
			}
		}
		page.Nodes = filtered
		return page, err
	}
	team := s.TeamID
	if s.Action == "issue" || s.Action == "comments" || s.Action == "replies" || s.Action == "updateIssue" || s.Action == "addComment" {
		if s.IssueID == "" {
			return nil, errors.New("issueID required")
		}
		if s.Action == "replies" && s.CommentID == "" {
			return nil, errors.New("commentID required")
		}
		issue, err := c.Issue(ctx, s.IssueID)
		if err != nil {
			return nil, err
		}
		team = issue.Team.ID
		if !Allowed(access, team) {
			return nil, errors.New("issue team is outside connection policy")
		}
		if s.Action == "issue" {
			return issue, nil
		}
		if s.Action == "replies" {
			parentIssueID, err := c.CommentIssueID(ctx, s.CommentID)
			if err != nil {
				return nil, err
			}
			if parentIssueID != issue.ID && parentIssueID != s.IssueID {
				return nil, errors.New("comment is outside the requested issue")
			}
		}
	}
	if !Allowed(access, team) {
		return nil, errors.New("teamID required and must be allowed by connection")
	}
	if s.StateID != nil { // Validate state membership against the same team, including pagination.
		after := ""
		found := false
		for i := 0; i < 20; i++ {
			page, err := c.States(ctx, team, 50, after)
			if err != nil {
				return nil, err
			}
			for _, state := range page.Nodes {
				if state.ID == *s.StateID && state.Team.ID == team {
					found = true
				}
			}
			if found || !page.PageInfo.HasNextPage {
				break
			}
			if page.PageInfo.EndCursor == "" || page.PageInfo.EndCursor == after {
				return nil, errors.New("invalid Linear state pagination")
			}
			after = page.PageInfo.EndCursor
		}
		if !found {
			return nil, errors.New("state must belong to the issue team")
		}
	}
	switch s.Action {
	case "states":
		return c.States(ctx, team, s.First, s.After)
	case "issues", "reconcile":
		since := ""
		if s.Since != nil {
			since = s.Since.UTC().Format(time.RFC3339)
		}
		return c.Issues(ctx, team, s.Query, since, s.First, s.After)
	case "comments":
		return c.Comments(ctx, s.IssueID, s.First, s.After)
	case "replies":
		return c.CommentReplies(ctx, s.CommentID, s.First, s.After)
	case "createIssue", "updateIssue":
		input := map[string]any{}
		if s.Title != nil {
			input["title"] = *s.Title
		}
		if s.Description != nil {
			input["description"] = *s.Description
		}
		if s.StateID != nil {
			input["stateId"] = *s.StateID
		}
		if s.Action == "createIssue" {
			if s.Title == nil || *s.Title == "" {
				return nil, errors.New("title required")
			}
			input["teamId"] = team
			return c.CreateIssue(ctx, input)
		}
		if len(input) == 0 {
			return nil, errors.New("at least one update field required")
		}
		return c.UpdateIssue(ctx, s.IssueID, input)
	case "addComment":
		if s.Body == "" {
			return nil, errors.New("comment body required")
		}
		return c.AddComment(ctx, s.IssueID, s.Body)
	default:
		return nil, fmt.Errorf("unsupported operation %q", s.Action)
	}
}

func (e Engine) receiptClient() dynamic.Interface {
	if e.ReceiptClient != nil {
		return e.ReceiptClient
	}
	return e.Client
}
