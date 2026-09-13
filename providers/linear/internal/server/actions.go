// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"time"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/actionapi"
	"github.com/faroshq/provider-linear/internal/engine"
	"github.com/faroshq/provider-sdk/actionwire"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var actionSlots = make(chan struct{}, 16)

var actionNames = map[string]string{"states": "states", "issues": "issues", "issue": "issue", "comments": "comments", "replies": "replies", "create_issue": "createIssue", "update_issue": "updateIssue", "add_comment": "addComment"}

type ActionRequest struct {
	RequestID string          `json:"requestId,omitempty"`
	Input     actionapi.Input `json:"input"`
}

// Action resolves the resource using the caller and credentials using the export.
// Only this provider can read or write the private receipt store.
func (s Server) Action(ctx context.Context, r *http.Request, teamName, action string, req ActionRequest, inspect bool) (actionapi.Outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var out actionapi.Outcome
	if !inspect && !validActionInput(action, req.Input) {
		return out, errActionInput
	}
	select {
	case actionSlots <- struct{}{}:
		defer func() { <-actionSlots }()
	case <-ctx.Done():
		return out, ctx.Err()
	}
	upstream, ok := actionNames[action]
	if !ok {
		return out, errors.New("unknown action")
	}
	caller, err := s.Caller(r)
	if err != nil {
		return out, err
	}
	u, err := caller.Resource(engine.Teams).Get(ctx, teamName, metav1.GetOptions{})
	if err != nil {
		return out, errActionForbidden
	}
	var team api.Team
	if engine.Decode(u, &team) != nil || team.DeletionTimestamp != nil {
		return out, errors.New("team unavailable")
	}
	review, err := caller.Resource(schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}).Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "spec": map[string]any{"resourceAttributes": map[string]any{"group": api.GroupName, "resource": "teams", "name": teamName, "verb": "invoke", "subresource": action}}}}, metav1.CreateOptions{})
	if err != nil {
		return out, errActionForbidden
	}
	allowed, _, _ := unstructured.NestedBool(review.Object, "status", "allowed")
	if !allowed {
		return out, errActionForbidden
	}
	tenant, err := s.Authority.Tenant(ctx, r.Header.Get("X-Faros-Cluster"), team.Spec.Connection)
	if err != nil {
		return out, err
	}
	current, err := tenant.Resource(engine.Teams).Get(ctx, teamName, metav1.GetOptions{})
	if err != nil || current.GetUID() != team.UID || !reflect.DeepEqual(current.Object["spec"], u.Object["spec"]) || current.GetDeletionTimestamp() != nil {
		return out, errActionConflict
	}
	e := engine.Engine{Client: tenant, Access: engine.TeamAccess{team.Spec.TeamID: true}}
	conn, err := e.ConnectionMetadata(ctx, team.Spec.Connection)
	if err != nil || string(conn.UID) != team.Spec.ConnectionUID {
		return out, errActionConflict
	}
	input := req.Input
	// Routing owns the binding; body fields may never widen it.
	if input.Connection != "" || input.TeamID != "" || input.Action != "" {
		return out, errors.New("connection, teamID and action are derived from the resource route")
	}
	input.Connection, input.TeamID, input.Action = conn.Name, team.Spec.TeamID, upstream
	write := upstream == "createIssue" || upstream == "updateIssue" || upstream == "addComment"
	if !write {
		if inspect {
			return out, errors.New("reads have no retained outcome")
		}
		key, err := e.Secret(ctx, conn.Spec.APIKeySecretRef)
		if err != nil {
			return out, err
		}
		result, err := engine.Execute(ctx, e.API(key), e.Access, input)
		if err != nil {
			return out, err
		}
		data, err := json.Marshal(result)
		if len(data) > 512*1024 {
			return out, errors.New("action result exceeds limit")
		}
		return actionapi.Outcome{Phase: "Succeeded", ConnectionUID: string(conn.UID), Result: &runtime.RawExtension{Raw: data}}, err
	}
	return s.write(ctx, r, caller, team, conn, e, action, input, req, inspect)
}

func (s Server) actionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.PathValue("cluster") != r.Header.Get("X-Faros-Cluster") {
		http.Error(w, "tenant mismatch", http.StatusForbidden)
		return
	}
	envelope := actionwire.New(r, "linear", r.PathValue("action"), actionwire.ResourceRef{APIVersion: api.GroupName + "/v1alpha1", Kind: "Team", Resource: "teams", Name: r.PathValue("team")})
	w.Header().Set("X-Request-ID", envelope.RequestID)
	var req ActionRequest
	inspect := r.Method == http.MethodGet
	if inspect {
		req.RequestID = r.URL.Query().Get("requestId")
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 32768)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF {
			envelope.Failure(w, 400, "invalid_input", "invalid action input", false)
			return
		}
	}
	if !inspect {
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			if req.RequestID != "" && req.RequestID != key {
				envelope.Failure(w, 400, "invalid_input", "conflicting write keys", false)
				return
			}
			req.RequestID = key
		}
	}
	out, err := s.Action(r.Context(), r, r.PathValue("team"), r.PathValue("action"), req, inspect)
	if err != nil {
		status, code := http.StatusUnprocessableEntity, "action_failed"
		switch {
		case errors.Is(err, errActionInput):
			status, code = 400, "invalid_input"
		case errors.Is(err, errActionForbidden):
			status, code = 403, "action_forbidden"
		case errors.Is(err, errActionConflict):
			status, code = 409, "identity_conflict"
		case errors.Is(err, context.DeadlineExceeded):
			status, code = 503, "outcome_unavailable"
		}
		envelope.Failure(w, status, code, err.Error(), false)
		return
	}
	data, err := envelope.Success(out)
	if err != nil || len(data) > 512*1024 {
		envelope.Failure(w, 502, "result_limit", "action result exceeds limit", false)
		return
	}
	_, _ = w.Write(data)
}
