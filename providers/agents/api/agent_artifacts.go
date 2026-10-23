// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-sdk/dataplane"
)

const (
	agentArtifactRequestLimit              = 16 * 1024
	agentArtifactSourceRevalidationTimeout = 10 * time.Second
)

type createAgentArtifactRequest struct {
	RunID      string `json:"runID"`
	SessionID  string `json:"sessionID"`
	ToolCallID string `json:"toolCallID"`
	Title      string `json:"title,omitempty"`
}

type agentArtifactView struct {
	ID             string                      `json:"id"`
	AgentName      string                      `json:"agentName"`
	AgentUID       string                      `json:"agentUID"`
	Owner          string                      `json:"owner"`
	RunID          string                      `json:"runID"`
	SessionID      string                      `json:"sessionID"`
	ToolCallID     string                      `json:"toolCallID"`
	ToolName       string                      `json:"toolName"`
	Title          string                      `json:"title"`
	MediaType      string                      `json:"mediaType"`
	Digest         string                      `json:"digest"`
	Size           int64                       `json:"size"`
	Sources        []store.SourceReference     `json:"sources,omitempty"`
	EvidenceStatus string                      `json:"evidenceStatus"`
	CreatedAt      time.Time                   `json:"createdAt"`
	Input          string                      `json:"input,omitempty"`
	InputDigest    string                      `json:"inputDigest,omitempty"`
	InputSize      int64                       `json:"inputSize,omitempty"`
	Data           string                      `json:"data,omitempty"`
	Presentation   *store.ProviderPresentation `json:"presentation,omitempty"`
}

func agentArtifactViewOf(artifact store.AgentArtifact, includeData, sourceAccessCurrent bool) agentArtifactView {
	status := "captured_unverified"
	if len(artifact.Sources) > 0 {
		status = "source_revalidation_required"
		if sourceAccessCurrent {
			status = "source_identity_and_access_revalidated"
		}
	}
	view := agentArtifactView{
		ID: artifact.ID, AgentName: artifact.AgentName, AgentUID: artifact.AgentUID, Owner: artifact.Owner,
		RunID: artifact.RunID, SessionID: artifact.SessionID, ToolCallID: artifact.ToolCallID,
		ToolName: artifact.ToolName, Title: artifact.Title, MediaType: artifact.MediaType, Digest: artifact.Digest,
		Size: artifact.Size, Sources: artifact.Sources, EvidenceStatus: status, CreatedAt: artifact.CreatedAt,
	}
	if includeData && (len(artifact.Sources) == 0 || sourceAccessCurrent) {
		view.Data = string(artifact.Data)
		view.Input = string(artifact.Input)
		view.InputDigest = artifact.InputDigest
		view.InputSize = artifact.InputSize
		view.Presentation = artifact.Presentation.Clone()
	}
	return view
}

// agentArtifacts serves caller-owned immutable captures. Source-backed detail
// reads are available only through attended MCP, where the caller bearer can
// revalidate each source action and exact object UID.
func (s *Server) agentArtifacts(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	gate, ok := gateFrom(r.Context())
	if !ok || gate.object == nil || gate.object.GetUID() == "" {
		writeStatus(w, http.StatusInternalServerError, "AgentIdentityUnavailable", "the gated Agent has no stable UID")
		return
	}
	if strings.TrimSpace(id.user) == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "a caller principal is required to own an Agent artifact")
		return
	}
	route, ok := dataplane.RouteFrom(r.Context())
	if !ok || strings.Contains(route.Tail, "/") {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	scope := id.scope(r.PathValue("name"))
	agentUID := string(gate.object.GetUID())
	tail := route.Tail
	if tail == "" {
		switch r.Method {
		case http.MethodGet:
			items, err := s.store.ListAgentArtifacts(r.Context(), scope, agentUID, id.user, store.MaxAgentArtifactsPerAgent)
			if err != nil {
				writeStatus(w, http.StatusInternalServerError, "InternalError", "could not list Agent artifacts")
				return
			}
			views := make([]agentArtifactView, 0, len(items))
			for _, item := range items {
				views = append(views, agentArtifactViewOf(item, false, false))
			}
			writeList(w, views)
		case http.MethodPost:
			s.createAgentArtifactFromDataPlane(w, r, id, scope, agentUID)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "artifact collection answers GET and POST")
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		artifact, found, err := s.store.GetAgentArtifact(r.Context(), scope, agentUID, id.user, tail)
		if err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "could not read Agent artifact")
			return
		}
		if !found {
			dataplane.WriteError(w, dataplane.ErrDenied)
			return
		}
		if len(artifact.Sources) > 0 {
			writeStatus(w, http.StatusForbidden, "SourceUnavailable", "source-backed artifacts must be reopened through the caller-attended MCP transport")
			return
		}
		writeJSON(w, http.StatusOK, agentArtifactViewOf(artifact, true, false))
	case http.MethodDelete:
		deleted, err := s.store.DeleteAgentArtifact(r.Context(), scope, agentUID, id.user, tail)
		if err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "could not delete Agent artifact")
			return
		}
		if !deleted {
			dataplane.WriteError(w, dataplane.ErrDenied)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "artifact detail answers GET and DELETE")
	}
}

func (s *Server) createAgentArtifactFromDataPlane(w http.ResponseWriter, r *http.Request, id identity, scope store.Scope, agentUID string) {
	req, err := decodeCreateAgentArtifactRequest(w, r)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	artifact, err := s.captureAgentArtifact(r.Context(), scope, agentUID, id.user, req, nil)
	if err != nil {
		writeArtifactHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, agentArtifactViewOf(artifact, true, false))
}

func decodeCreateAgentArtifactRequest(w http.ResponseWriter, r *http.Request) (createAgentArtifactRequest, error) {
	var req createAgentArtifactRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, agentArtifactRequestLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return req, fmt.Errorf("invalid artifact request: %w", err)
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return req, fmt.Errorf("artifact request must contain one JSON object")
	}
	req.RunID = strings.TrimSpace(req.RunID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.ToolCallID = strings.TrimSpace(req.ToolCallID)
	req.Title = strings.TrimSpace(req.Title)
	if req.RunID == "" || req.SessionID == "" || req.ToolCallID == "" {
		return req, fmt.Errorf("runID, sessionID and toolCallID are required")
	}
	if len(req.Title) > 160 {
		return req, fmt.Errorf("title exceeds 160 bytes")
	}
	return req, nil
}

func writeArtifactHTTPError(w http.ResponseWriter, err error) {
	var failure *artifactMCPFailure
	if errors.As(err, &failure) {
		writeStatus(w, failure.status, failure.reason, failure.message)
		return
	}
	switch {
	case errors.Is(err, store.ErrAgentArtifactResultTooLarge):
		writeStatus(w, http.StatusRequestEntityTooLarge, "ArtifactTooLarge", "tool result exceeds the saved artifact size limit")
	case errors.Is(err, store.ErrAgentArtifactLimit):
		writeStatus(w, http.StatusConflict, "ArtifactLimitReached", err.Error())
	default:
		writeStatus(w, http.StatusInternalServerError, "InternalError", "could not capture Agent artifact")
	}
}

type artifactCallReceipt struct {
	Tool         string
	Args         string
	Sources      []store.SourceReference
	Presentation *store.ProviderPresentation
}

func successfulAgentArtifactReceipt(calls []store.ToolCall, agent, runID, sessionID, callID string) (artifactCallReceipt, error) {
	var found *store.ToolCall
	for i := range calls {
		call := &calls[i]
		if call.CallID != callID || call.SessionID != sessionID || call.RunID != runID {
			continue
		}
		if found != nil {
			return artifactCallReceipt{}, fmt.Errorf("tool-call receipt is ambiguous")
		}
		found = call
	}
	if found == nil || found.AgentName != agent || found.Outcome != "ok" || found.Error != "" || strings.TrimSpace(found.Tool) == "" {
		return artifactCallReceipt{}, artifactMCPNotFound("successful tool receipt is unavailable")
	}
	if found.Tool == "open_analysis_artifact" {
		return artifactCallReceipt{}, artifactMCPBadRequest("an opened saved artifact is not a new source receipt")
	}
	if len(found.Sources) > store.MaxAgentArtifactSources {
		return artifactCallReceipt{}, fmt.Errorf("tool-call receipt has too many sources")
	}
	for i, source := range found.Sources {
		if err := source.Validate(); err != nil {
			return artifactCallReceipt{}, fmt.Errorf("tool-call source %d is invalid: %w", i, err)
		}
	}
	if found.Presentation != nil {
		if err := found.Presentation.Validate(); err != nil {
			return artifactCallReceipt{}, fmt.Errorf("tool-call presentation is invalid: %w", err)
		}
	}
	return artifactCallReceipt{Tool: found.Tool, Args: found.Args, Sources: found.Sources, Presentation: found.Presentation}, nil
}

func (s *Server) captureAgentArtifact(
	ctx context.Context,
	scope store.Scope,
	agentUID, owner string,
	req createAgentArtifactRequest,
	revalidate func(context.Context, []store.SourceReference) error,
) (store.AgentArtifact, error) {
	req.RunID, req.SessionID, req.ToolCallID, req.Title = strings.TrimSpace(req.RunID), strings.TrimSpace(req.SessionID), strings.TrimSpace(req.ToolCallID), strings.TrimSpace(req.Title)
	if req.RunID == "" || req.SessionID == "" || req.ToolCallID == "" {
		return store.AgentArtifact{}, artifactMCPBadRequest("runID, sessionID and toolCallID are required")
	}
	if len(req.Title) > 160 {
		return store.AgentArtifact{}, artifactMCPBadRequest("title exceeds 160 bytes")
	}
	run, err := s.store.GetRun(ctx, scope, req.RunID)
	if err != nil || run.AgentName != scope.AgentName || run.SessionID != req.SessionID {
		return store.AgentArtifact{}, artifactMCPNotFound("successful tool receipt is unavailable")
	}
	calls, err := s.store.ListToolCalls(ctx, scope, req.RunID)
	if err != nil {
		return store.AgentArtifact{}, err
	}
	receipt, err := successfulAgentArtifactReceipt(calls, scope.AgentName, req.RunID, req.SessionID, req.ToolCallID)
	if err != nil {
		return store.AgentArtifact{}, err
	}
	if len(receipt.Sources) > 0 {
		if revalidate == nil || revalidate(ctx, receipt.Sources) != nil {
			return store.AgentArtifact{}, artifactMCPDenied("SourceUnavailable", "source-backed output cannot be saved because the caller's current access or exact source identity could not be verified")
		}
	}
	message, found, err := s.store.GetToolResult(ctx, scope, req.RunID, req.SessionID, req.ToolCallID)
	if err != nil {
		return store.AgentArtifact{}, err
	}
	if !found || len(message.Content) == 0 {
		return store.AgentArtifact{}, artifactMCPNotFound("successful tool output is unavailable")
	}
	data, input := []byte(message.Content), []byte(receipt.Args)
	if len(input) > store.MaxAgentArtifactInputBytes || len(data)+len(input) > store.MaxAgentArtifactBytes {
		return store.AgentArtifact{}, artifactMCPBadRequest("tool output and recorded input exceed the saved artifact size limit")
	}
	if !isValidUTF8(data) || !isValidUTF8(input) {
		return store.AgentArtifact{}, artifactMCPBadRequest("tool output and recorded input must be UTF-8 text")
	}
	title := req.Title
	if title == "" {
		title = receipt.Tool
	}
	mediaType := "text/plain; charset=utf-8"
	if json.Valid(data) {
		mediaType = "application/json"
	}
	digestBytes := sha256.Sum256(data)
	inputDigest := ""
	if len(input) > 0 {
		sum := sha256.Sum256(input)
		inputDigest = hex.EncodeToString(sum[:])
	}
	artifact := store.AgentArtifact{
		ID: agentArtifactCaptureID(scope, agentUID, owner, req), AgentName: scope.AgentName, AgentUID: agentUID, Owner: owner,
		RunID: req.RunID, SessionID: req.SessionID, ToolCallID: req.ToolCallID, ToolName: receipt.Tool, Title: title,
		MediaType: mediaType, Digest: hex.EncodeToString(digestBytes[:]), Size: int64(len(data)), Data: data,
		Input: input, InputDigest: inputDigest, InputSize: int64(len(input)), Sources: receipt.Sources,
		Presentation: receipt.Presentation.Clone(), CreatedAt: time.Now().UTC(),
	}
	if existing, exists, err := s.store.GetAgentArtifact(ctx, scope, agentUID, owner, artifact.ID); err != nil {
		return store.AgentArtifact{}, err
	} else if exists {
		return matchingAgentArtifactCapture(existing, artifact)
	}
	if err := s.store.SaveAgentArtifact(ctx, scope, artifact); err != nil {
		if existing, exists, getErr := s.store.GetAgentArtifact(ctx, scope, agentUID, owner, artifact.ID); getErr == nil && exists {
			return matchingAgentArtifactCapture(existing, artifact)
		}
		return store.AgentArtifact{}, err
	}
	return artifact, nil
}

func agentArtifactCaptureID(scope store.Scope, agentUID, owner string, req createAgentArtifactRequest) string {
	identity, _ := json.Marshal(struct {
		OrgUUID, WorkspaceUUID, AgentName, AgentUID, Owner, RunID, SessionID, ToolCallID string
	}{scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName, agentUID, owner, req.RunID, req.SessionID, req.ToolCallID})
	return uuid.NewSHA1(uuid.NameSpaceOID, identity).String()
}

func matchingAgentArtifactCapture(existing, expected store.AgentArtifact) (store.AgentArtifact, error) {
	if existing.AgentName != expected.AgentName || existing.AgentUID != expected.AgentUID || existing.Owner != expected.Owner ||
		existing.RunID != expected.RunID || existing.SessionID != expected.SessionID || existing.ToolCallID != expected.ToolCallID ||
		existing.ToolName != expected.ToolName || existing.Digest != expected.Digest || existing.InputDigest != expected.InputDigest ||
		!reflect.DeepEqual(existing.Sources, expected.Sources) || !reflect.DeepEqual(existing.Presentation, expected.Presentation) ||
		string(existing.Data) != string(expected.Data) || string(existing.Input) != string(expected.Input) {
		return store.AgentArtifact{}, &artifactMCPFailure{code: "Conflict", reason: "ArtifactReceiptChanged", message: "this tool receipt no longer matches its saved artifact; reload the run before capturing again", status: http.StatusConflict}
	}
	return existing, nil
}

func isValidUTF8(b []byte) bool { return utf8.Valid(b) }

// MCP artifact access is attached to a caller-attended request so source
// actions and source object UIDs can be checked with that caller's own token.
type artifactMCPCaller struct {
	identity identity
	scope    store.Scope
	agent    *agentsv1alpha1.Agent
	client   *agentsclient.Client
	dynamic  dynamic.Interface
}

type artifactMCPFailure struct {
	code, reason, message string
	status                int
}

func (e *artifactMCPFailure) Error() string { return e.message }

func artifactMCPDenied(reason, message string) *artifactMCPFailure {
	return &artifactMCPFailure{code: "Forbidden", reason: reason, message: message, status: http.StatusForbidden}
}

func artifactMCPNotFound(message string) *artifactMCPFailure {
	return &artifactMCPFailure{code: "NotFound", reason: "ArtifactUnavailable", message: message, status: http.StatusNotFound}
}

func artifactMCPBadRequest(message string) *artifactMCPFailure {
	return &artifactMCPFailure{code: "BadRequest", reason: "InvalidArtifactRequest", message: message, status: http.StatusBadRequest}
}

type artifactMCPError struct {
	Code       string `json:"code"`
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"httpStatus"`
}

type artifactMCPOutput struct {
	Artifacts []agentArtifactView `json:"artifacts,omitempty"`
	Artifact  *agentArtifactView  `json:"artifact,omitempty"`
	Deleted   string              `json:"deleted,omitempty"`
	Error     *artifactMCPError   `json:"error,omitempty"`
}

func artifactMCPRefused(err error) (*mcp.CallToolResult, artifactMCPOutput, error) {
	var failure *artifactMCPFailure
	if !errors.As(err, &failure) {
		failure = &artifactMCPFailure{code: "InternalError", reason: "ArtifactOperationFailed", message: "could not complete the Agent artifact operation", status: http.StatusInternalServerError}
	}
	out := artifactMCPOutput{Error: &artifactMCPError{Code: failure.code, Reason: failure.reason, Message: failure.message, HTTPStatus: failure.status}}
	body, _ := json.Marshal(out)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, out, nil
}

func (s *Server) artifactMCPCaller(ctx context.Context, r *http.Request, agentName, verb string) (artifactMCPCaller, error) {
	if !fromServiceProxy(r) {
		return artifactMCPCaller{}, artifactMCPDenied("AttendedTransportRequired", "Agent artifacts are available only through the caller-attended provider MCP transport")
	}
	id := s.mcpIdentity(ctx, r)
	if id.user == "" || id.token == "" || id.clusterID == "" {
		return artifactMCPCaller{}, &artifactMCPFailure{code: "Unauthorized", reason: "CallerIdentityUnavailable", message: "caller identity and bearer token are required", status: http.StatusUnauthorized}
	}
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return artifactMCPCaller{}, artifactMCPBadRequest("agent is required")
	}
	client, err := s.mcpClient(r)
	if err != nil {
		return artifactMCPCaller{}, &artifactMCPFailure{code: "Unauthorized", reason: "CallerWorkspaceUnavailable", message: "could not address the caller's workspace", status: http.StatusUnauthorized}
	}
	agent, err := client.Agents().Get(ctx, agentName, metav1.GetOptions{})
	if err != nil || agent == nil || agent.UID == "" {
		return artifactMCPCaller{}, artifactMCPNotFound("Agent is unavailable to this caller")
	}
	if id.orgUUID == "" || id.workspaceUUID == "" {
		return artifactMCPCaller{}, artifactMCPNotFound("workspace identity is unavailable")
	}
	callerScope, err := s.tenant.For(id.clusterID, id.token)
	if err != nil {
		return artifactMCPCaller{}, &artifactMCPFailure{code: "Unauthorized", reason: "CallerWorkspaceUnavailable", message: "could not address the caller's workspace", status: http.StatusUnauthorized}
	}
	allowed, err := authorizeArtifactResource(ctx, callerScope.Dynamic(), dataplane.ResourceAttributes{
		Group: agentsv1alpha1.GroupName, Version: agentsv1alpha1.Version, Resource: "agents", Subresource: "artifacts", Name: agent.Name, Verb: verb,
	})
	if err != nil || !allowed {
		return artifactMCPCaller{}, artifactMCPDenied("ArtifactAccessDenied", "caller is not authorized for this Agent artifact operation")
	}
	return artifactMCPCaller{identity: id, scope: id.scope(agent.Name), agent: agent, client: client, dynamic: callerScope.Dynamic()}, nil
}

func authorizeArtifactResource(ctx context.Context, caller dynamic.Interface, attrs dataplane.ResourceAttributes) (bool, error) {
	resource := map[string]any{"group": attrs.Group, "version": attrs.Version, "resource": attrs.Resource, "verb": attrs.Verb, "name": attrs.Name}
	if attrs.Subresource != "" {
		resource["subresource"] = attrs.Subresource
	}
	if attrs.Namespace != "" {
		resource["namespace"] = attrs.Namespace
	}
	review := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
		"spec": map[string]any{"resourceAttributes": resource},
	}}
	created, err := caller.Resource(dataplane.SelfSubjectAccessReviews()).Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	allowed, found, err := unstructured.NestedBool(created.Object, "status", "allowed")
	if err != nil || !found {
		return false, fmt.Errorf("caller access review has no status.allowed")
	}
	return allowed, nil
}

func revalidateAgentArtifactSources(ctx context.Context, caller dynamic.Interface, sources []store.SourceReference) error {
	ctx, cancel := context.WithTimeout(ctx, agentArtifactSourceRevalidationTimeout)
	defer cancel()
	for _, source := range sources {
		if err := source.Validate(); err != nil {
			return err
		}
		for _, access := range source.Access {
			allowed, err := authorizeArtifactResource(ctx, caller, dataplane.ResourceAttributes{
				Group: access.Group, Version: access.Version, Resource: access.Resource,
				Subresource: access.Subresource, Namespace: access.Namespace, Name: access.Name, Verb: access.Verb,
			})
			if err != nil || !allowed {
				return fmt.Errorf("source access is unavailable")
			}
		}
		gvr := schema.GroupVersion{Group: source.Object.Group, Version: source.Object.Version}.WithResource(source.Object.Resource)
		var resource dynamic.ResourceInterface
		if source.Object.Namespace != "" {
			resource = caller.Resource(gvr).Namespace(source.Object.Namespace)
		} else {
			resource = caller.Resource(gvr)
		}
		current, err := resource.Get(ctx, source.Object.Name, metav1.GetOptions{})
		if err != nil || string(current.GetUID()) != source.Object.UID {
			return fmt.Errorf("source object identity is unavailable")
		}
	}
	return nil
}

type artifactMCPInput struct {
	Agent      string `json:"agent" jsonschema:"Name of the Agent that owns the saved result"`
	RunID      string `json:"runID,omitempty" jsonschema:"Exact run ID returned by the attended chat turn"`
	SessionID  string `json:"sessionID,omitempty" jsonschema:"Exact chat session ID returned by the attended chat turn"`
	ToolCallID string `json:"toolCallID,omitempty" jsonschema:"Exact successful tool call ID from the run trace"`
	ArtifactID string `json:"artifactID,omitempty" jsonschema:"Saved artifact ID from list_agent_artifacts"`
	Title      string `json:"title,omitempty" jsonschema:"Optional display title for the immutable capture"`
}

func (s *Server) registerAgentArtifactMCPTools(srv *mcp.Server, r *http.Request) {
	if !fromServiceProxy(r) {
		return
	}
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	mutating := &mcp.ToolAnnotations{DestructiveHint: boolPointer(false)}
	deleteAnnotations := &mcp.ToolAnnotations{DestructiveHint: boolPointer(true), IdempotentHint: true}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_agent_artifacts", Title: "List saved results", Description: "List metadata for your immutable saved tool results. This does not return captured data or app HTML.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in artifactMCPInput) (*mcp.CallToolResult, artifactMCPOutput, error) {
		caller, err := s.artifactMCPCaller(ctx, r, in.Agent, "get")
		if err != nil {
			return artifactMCPRefused(err)
		}
		items, err := s.store.ListAgentArtifacts(ctx, caller.scope, string(caller.agent.UID), caller.identity.user, store.MaxAgentArtifactsPerAgent)
		if err != nil {
			return artifactMCPRefused(err)
		}
		out := artifactMCPOutput{Artifacts: make([]agentArtifactView, 0, len(items))}
		for _, item := range items {
			out.Artifacts = append(out.Artifacts, agentArtifactViewOf(item, false, false))
		}
		return nil, out, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "capture_agent_artifact", Title: "Save result", Description: "Save the exact output and redacted input from one successful tool call. Source-backed data is saved only after the current caller's access and exact source UIDs are revalidated.", Annotations: mutating}, func(ctx context.Context, _ *mcp.CallToolRequest, in artifactMCPInput) (*mcp.CallToolResult, artifactMCPOutput, error) {
		caller, err := s.artifactMCPCaller(ctx, r, in.Agent, "create")
		if err != nil {
			return artifactMCPRefused(err)
		}
		artifact, err := s.captureAgentArtifact(ctx, caller.scope, string(caller.agent.UID), caller.identity.user, createAgentArtifactRequest{RunID: in.RunID, SessionID: in.SessionID, ToolCallID: in.ToolCallID, Title: in.Title}, func(ctx context.Context, sources []store.SourceReference) error {
			return revalidateAgentArtifactSources(ctx, caller.dynamic, sources)
		})
		if err != nil {
			return artifactMCPRefused(err)
		}
		view := agentArtifactViewOf(artifact, true, len(artifact.Sources) > 0)
		return artifactMCPAppResult(&view), artifactMCPOutput{Artifact: &view}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "get_agent_artifact", Title: "Open saved result", Description: "Open one saved result. Source-backed data, input, and app HTML are returned only after the caller's current actions and exact source UIDs are revalidated.", Annotations: readOnly}, func(ctx context.Context, _ *mcp.CallToolRequest, in artifactMCPInput) (*mcp.CallToolResult, artifactMCPOutput, error) {
		caller, err := s.artifactMCPCaller(ctx, r, in.Agent, "get")
		if err != nil {
			return artifactMCPRefused(err)
		}
		artifact, found, err := s.store.GetAgentArtifact(ctx, caller.scope, string(caller.agent.UID), caller.identity.user, strings.TrimSpace(in.ArtifactID))
		if err != nil {
			return artifactMCPRefused(err)
		}
		if !found {
			return artifactMCPRefused(artifactMCPNotFound("saved result is unavailable"))
		}
		if err := revalidateAgentArtifactSources(ctx, caller.dynamic, artifact.Sources); err != nil {
			return artifactMCPRefused(artifactMCPDenied("SourceUnavailable", "source-backed result is unavailable because the caller's current access or exact source identity could not be verified"))
		}
		view := agentArtifactViewOf(artifact, true, len(artifact.Sources) > 0)
		return artifactMCPAppResult(&view), artifactMCPOutput{Artifact: &view}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_agent_artifact", Title: "Delete saved result", Description: "Delete one saved result and its pinned app resource.", Annotations: deleteAnnotations}, func(ctx context.Context, _ *mcp.CallToolRequest, in artifactMCPInput) (*mcp.CallToolResult, artifactMCPOutput, error) {
		caller, err := s.artifactMCPCaller(ctx, r, in.Agent, "delete")
		if err != nil {
			return artifactMCPRefused(err)
		}
		id := strings.TrimSpace(in.ArtifactID)
		deleted, err := s.store.DeleteAgentArtifact(ctx, caller.scope, string(caller.agent.UID), caller.identity.user, id)
		if err != nil {
			return artifactMCPRefused(err)
		}
		if !deleted {
			return artifactMCPRefused(artifactMCPNotFound("saved result is unavailable"))
		}
		return nil, artifactMCPOutput{Deleted: id}, nil
	})
}

func artifactMCPAppResult(view *agentArtifactView) *mcp.CallToolResult {
	if view == nil || view.Presentation == nil || view.Presentation.MCPApp == nil {
		return nil
	}
	app := view.Presentation.MCPApp.Clone()
	view.Presentation.MCPApp = nil
	if view.Presentation.Title == "" {
		view.Presentation = nil
	}
	return &mcp.CallToolResult{Meta: mcp.Meta{"railgrid": map[string]any{"mcpApp": app}}}
}

func boolPointer(v bool) *bool { return &v }
