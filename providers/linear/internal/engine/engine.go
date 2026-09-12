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

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/linearapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var Connections = schema.GroupVersionResource{Group: api.GroupName, Version: api.Version, Resource: "connections"}
var Operations = schema.GroupVersionResource{Group: api.GroupName, Version: api.Version, Resource: "operations"}
var Events = schema.GroupVersionResource{Group: api.GroupName, Version: api.Version, Resource: "events"}
var secrets = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

type Engine struct {
	Client    dynamic.Interface
	NewClient func(string) *linearapi.Client
	Now       func() time.Time
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
func (e Engine) Secret(ctx context.Context, ns string, ref api.SecretReference) (string, error) {
	if ref.Name == "" {
		return "", errors.New("credential Secret reference required")
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
func (e Engine) Connection(ctx context.Context, ns, name string) (api.Connection, string, error) {
	var conn api.Connection
	u, err := e.Client.Resource(Connections).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return conn, "", errors.New("connection unavailable")
	}
	if err = Decode(u, &conn); err != nil {
		return conn, "", err
	}
	if conn.DeletionTimestamp != nil {
		return conn, "", errors.New("connection is deleting")
	}
	key, err := e.Secret(ctx, ns, conn.Spec.APIKeySecretRef)
	return conn, key, err
}
func Allowed(c api.Connection, team string) bool {
	if team == "" {
		return false
	}
	if len(c.Spec.Teams) == 0 {
		return true
	}
	for _, t := range c.Spec.Teams {
		if t.ID == team {
			return true
		}
	}
	return false
}
func (e Engine) Probe(ctx context.Context, u *unstructured.Unstructured) error {
	var c api.Connection
	if err := Decode(u, &c); err != nil {
		return err
	}
	if c.Status.CheckedAt != nil && e.now().Sub(c.Status.CheckedAt.Time) < time.Minute && c.Generation == c.Status.ObservedGeneration {
		return nil
	}
	key, err := e.Secret(ctx, c.Namespace, c.Spec.APIKeySecretRef)
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
	_, err = e.Client.Resource(Connections).Namespace(c.Namespace).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	return err
}
func (e Engine) save(ctx context.Context, u *unstructured.Unstructured, s api.OperationStatus) error {
	v, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&s)
	if err != nil {
		return err
	}
	u.Object["status"] = v
	updated, err := e.Client.Resource(Operations).Namespace(u.GetNamespace()).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	if err == nil {
		*u = *updated
	}
	return err
}
func mutation(action string) bool {
	return action == "createIssue" || action == "updateIssue" || action == "addComment"
}
func (e Engine) Reconcile(ctx context.Context, u *unstructured.Unstructured) error {
	var op api.Operation
	if err := Decode(u, &op); err != nil {
		return err
	}
	const finalizer = "linear.providers.faros.sh/operation-history"
	if op.DeletionTimestamp != nil {
		if op.Status.Phase == "Running" || op.Status.Phase == "Uncertain" {
			return nil
		}
		finalizers := []string{}
		for _, f := range u.GetFinalizers() {
			if f != finalizer {
				finalizers = append(finalizers, f)
			}
		}
		u.SetFinalizers(finalizers)
		_, err := e.Client.Resource(Operations).Namespace(op.Namespace).Update(ctx, u, metav1.UpdateOptions{})
		return err
	}
	if op.Status.Phase == "Succeeded" || op.Status.Phase == "Failed" {
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
		updated, err := e.Client.Resource(Operations).Namespace(op.Namespace).Update(ctx, u, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		*u = *updated
	}

	if op.Status.Phase == "Running" || op.Status.Phase == "Uncertain" {
		if mutation(op.Spec.Action) {
			op.Status.Phase = "Uncertain"
			op.Status.Message = "A prior write may have reached Linear. Inspect Linear and this operation; no automatic replay."
			return e.save(ctx, u, op.Status)
		}
	}
	conn, key, err := e.Connection(ctx, op.Namespace, op.Spec.Connection)
	if err != nil {
		return e.finish(ctx, u, op.Status, nil, err, false)
	}
	if op.Status.ConnectionUID != "" && op.Status.ConnectionUID != string(conn.UID) {
		return e.finish(ctx, u, op.Status, nil, errors.New("connection was replaced"), false)
	}
	// Persist the one-shot dispatch fence before any external request. Conflicting
	// controllers cannot both claim a Pending operation. Reads may safely resume.
	now := metav1.NewTime(e.now())
	op.Status = api.OperationStatus{Phase: "Running", StartedAt: &now, ConnectionUID: string(conn.UID)}
	if err = e.save(ctx, u, op.Status); err != nil {
		return err
	}
	value, err := Execute(ctx, e.API(key), conn, op.Spec)
	var upstream *linearapi.Error
	uncertain := errors.As(err, &upstream) && upstream.Uncertain
	return e.finish(ctx, u, op.Status, value, err, uncertain)
}
func (e Engine) finish(ctx context.Context, u *unstructured.Unstructured, s api.OperationStatus, value any, err error, uncertain bool) error {
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
	return e.save(ctx, u, s)
}
func Execute(ctx context.Context, c *linearapi.Client, conn api.Connection, s api.OperationSpec) (any, error) {
	if s.First < 0 || s.First > 50 {
		return nil, errors.New("page size must be 1 to 50")
	}
	if s.Action == "teams" {
		page, err := c.Teams(ctx, s.First, s.After)
		filtered := make([]linearapi.Team, 0, len(page.Nodes))
		for _, t := range page.Nodes {
			if Allowed(conn, t.ID) {
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
		if !Allowed(conn, team) {
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
	if !Allowed(conn, team) {
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
