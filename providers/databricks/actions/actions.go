// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package actions serves Databricks' provider-action endpoints on the
// provider's embedded virtual workspace: resource-addressed verbs reached
// through the hub backend proxy at
//
//	/services/providers/databricks/actions/clusters/{clusterID}/{resource}/{name}/{action}/{version}
//
// The route shape mirrors the infrastructure data plane: the URL is the
// resource reference, the caller's bearer is the only trust root, and
// authorization is delegated to kcp (a visibility SSAR on the resource plus a
// verb SSAR on the action subresource, both as the caller). There is no hub
// router in front of this endpoint; the declared CatalogEntry limits are
// enforced here, by the declaring provider.
package actions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"github.com/faroshq/provider-databricks/queryapi"
)

const (
	// PathPrefix is the mount point for every action verb this provider
	// serves. Everything after it follows the platform data-plane grammar.
	PathPrefix = "/actions/"

	ProviderName    = "databricks"
	ActionQueryName = "query_table"
	ActionQueryV1   = "v1"

	resourceAPIVersion = "databricks.faros.sh/v1alpha1"
	resourceKind       = "Table"
	resourceName       = "tables"

	// Declared CatalogEntry limits for query_table/v1. The manifest and this
	// code must agree; skill_catalog_test cross-checks the manifest values.
	maxInputBytes      = 8 << 10
	maxRequestBytes    = 1 << 20
	defaultActionLimit = 100
	// maxActionDeadline matches the declared limits.timeoutSeconds.
	maxActionDeadline = 45 * time.Second
)

const (
	// Provider action error codes are a deliberately small, provider-neutral
	// surface. Consumers branch on code + retryable, never on incidental
	// provider text; provider-specific internals must never become wire codes.
	ActionErrorCodeUnauthenticated         = "unauthenticated"
	ActionErrorCodeTenantRequired          = "tenant_required"
	ActionErrorCodeActionNotFound          = "action_not_found"
	ActionErrorCodeInvalidRequest          = "invalid_request"
	ActionErrorCodeInvalidDeadline         = "invalid_deadline"
	ActionErrorCodeActionUnavailable       = "action_unavailable"
	ActionErrorCodeActionTimeout           = "action_timeout"
	ActionErrorCodeActionFailed            = "action_failed"
	ActionErrorCodeResourceNotFound        = "resource_not_found"
	ActionErrorCodeResourceForbidden       = "resource_forbidden"
	ActionErrorCodeResourceNotReady        = "resource_not_ready"
	ActionErrorCodeSchemaProjectionInvalid = "schema_projection_invalid"
	ActionErrorCodeBackendFailure          = "backend_failure"
)

const (
	defaultActionFailureMessage = "databricks action failed"
	maxActionErrorMessageBytes  = 512
)

var requestIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,256}$`)

// ActionError is the typed failure contract for the action boundary. Status
// is an HTTP transport detail and is intentionally not serialized in the
// body. Retryable is explicit and must never be inferred from Status.
//
// Cause is for provider-side unwrapping/logging only. It is never sent to a
// caller and must not be used as a public message without sanitization.
type ActionError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Status    int    `json:"-"`
	Cause     error  `json:"-"`
}

// ProviderActionError is retained as a descriptive alias for consumers that
// name the wire contract directly.
type ProviderActionError = ActionError

func (e *ActionError) Error() string {
	if e == nil {
		return defaultActionFailureMessage
	}
	if message := strings.TrimSpace(e.Message); message != "" {
		return message
	}
	if code := strings.TrimSpace(e.Code); code != "" {
		return code
	}
	return defaultActionFailureMessage
}

func (e *ActionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewActionError creates an explicit typed failure. Callers should use one of
// the ActionErrorCode constants and a safe, bounded message.
func NewActionError(code, message string, status int, retryable bool) *ActionError {
	return &ActionError{Code: code, Message: message, Status: status, Retryable: retryable}
}

// NewProviderActionError is an explicit-name alias for NewActionError.
func NewProviderActionError(code, message string, status int, retryable bool) *ProviderActionError {
	return NewActionError(code, message, status, retryable)
}

// ResourceRef is the resource identity addressed by the action route. It is
// derived from the URL path plus the declared bound-resource coordinates —
// never from the request body.
type ResourceRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Name       string `json:"name"`
}

// Route is the parsed action address: which resource, which verb.
type Route struct {
	ClusterID string
	Resource  string
	Name      string
	Action    string
	Version   string
}

// QueryInput is the only caller-controlled portion of query_table/v1.
type QueryInput struct {
	Columns []string `json:"columns,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

// QueryExecutor is implemented by the tenant-scoped direct executor. It
// authorizes as the caller (visibility + verb SSAR), resolves the bound Table
// and its provider-owned dependencies, then invokes the provider backend.
type QueryExecutor interface {
	QueryTable(context.Context, ResourceRef, QueryInput) (queryapi.QueryTableResult, error)
}

// Deps configures the published action endpoint. The executor factory
// receives the route so the tenant identity comes from the path, not from
// proxy-injected headers.
type Deps struct {
	QueryExecutorForRoute func(*http.Request, Route) QueryExecutor
	Logger                logr.Logger
}

// envelope is the stable response contract shared with consumers (App Studio
// validates identity fields against the bound grant). Exactly one of Result
// or Error is set.
type envelope struct {
	RequestID     string       `json:"requestID,omitempty"`
	Provider      string       `json:"provider"`
	Action        string       `json:"action"`
	ActionVersion string       `json:"actionVersion"`
	ResourceRef   *ResourceRef `json:"resourceRef,omitempty"`
	Result        any          `json:"result,omitempty"`
	Error         *wireError   `json:"error,omitempty"`
}

type wireError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// ParseActionPath parses the data-plane action grammar under PathPrefix:
//
//	/actions/clusters/{clusterID}/{resource}/{name}/{action}/{version}
//
// It is exported for the e2e suite and future verbs.
func ParseActionPath(requestPath string) (Route, error) {
	clean := path.Clean("/" + strings.Trim(requestPath, "/"))
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if len(parts) != 7 || parts[0] != "actions" || parts[1] != "clusters" {
		return Route{}, fmt.Errorf("action path must be /actions/clusters/{clusterID}/{resource}/{name}/{action}/{version}")
	}
	route := Route{ClusterID: parts[2], Resource: parts[3], Name: parts[4], Action: parts[5], Version: parts[6]}
	for _, segment := range parts[2:] {
		if segment == "" || segment == "." || segment == ".." || url.PathEscape(segment) != segment {
			return Route{}, fmt.Errorf("action path segments must be non-empty and path-safe")
		}
	}
	return route, nil
}

// ActionPath composes the canonical route for a verb; consumers should build
// action URLs through this helper (or the published grammar), never by hand.
func ActionPath(clusterID, resource, name, action, version string) string {
	return "/actions/clusters/" + url.PathEscape(clusterID) + "/" + url.PathEscape(resource) + "/" +
		url.PathEscape(name) + "/" + url.PathEscape(action) + "/" + url.PathEscape(version)
}

// NewHandler returns the provider action handler for mounting at PathPrefix.
// A nil per-route executor fails closed with 503; the endpoint never falls
// back to MCP or another resource-backed query path.
func NewHandler(deps Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := normalizeRequestID(r.Header.Get("X-Request-ID"))
		if r.Method != http.MethodPost {
			writeEnvelopeError(w, requestID, nil, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if !hasBearer(r.Header.Get("Authorization")) {
			writeEnvelopeError(w, requestID, nil, http.StatusUnauthorized, ActionErrorCodeUnauthenticated, "a bearer credential is required")
			return
		}
		route, err := ParseActionPath(r.URL.Path)
		if err != nil {
			writeEnvelopeError(w, requestID, nil, http.StatusNotFound, ActionErrorCodeActionNotFound, "action endpoint not found")
			return
		}
		if route.Resource != resourceName || route.Action != ActionQueryName || route.Version != ActionQueryV1 {
			writeEnvelopeError(w, requestID, &route, http.StatusNotFound, ActionErrorCodeActionNotFound, "action endpoint not found")
			return
		}
		ref := ResourceRef{APIVersion: resourceAPIVersion, Kind: resourceKind, Resource: resourceName, Name: route.Name}
		if err := queryapi.ValidateTableRef(ref.Name); err != nil {
			writeEnvelopeError(w, requestID, &route, http.StatusBadRequest, ActionErrorCodeInvalidRequest, err.Error())
			return
		}
		input, err := decodeRequest(w, r)
		if err != nil {
			writeEnvelopeError(w, requestID, &route, http.StatusBadRequest, ActionErrorCodeInvalidRequest, err.Error())
			return
		}
		executor := QueryExecutor(nil)
		if deps.QueryExecutorForRoute != nil {
			executor = deps.QueryExecutorForRoute(r, route)
		}
		if executor == nil {
			writeEnvelopeError(w, requestID, &route, http.StatusServiceUnavailable, ActionErrorCodeActionUnavailable, "databricks action executor is unavailable")
			return
		}

		deadline, err := actionDeadline(r)
		if err != nil {
			writeEnvelopeError(w, requestID, &route, http.StatusBadRequest, ActionErrorCodeInvalidDeadline, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), deadline)
		defer cancel()
		result, err := executor.QueryTable(ctx, ref, input)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				writeEnvelopeError(w, requestID, &route, http.StatusGatewayTimeout, ActionErrorCodeActionTimeout, "databricks action timed out")
				return
			}
			failure := normalizeActionError(err)
			logActionFailure(deps.Logger, requestID, started, failure.Code, classifyActionError(err))
			writeFailure(w, requestID, &route, failure)
			return
		}
		// Keep the result identity server-owned even if an executor returns a
		// backend placeholder. The route-derived resourceRef is authoritative.
		result.ActionVersion = queryapi.ActionVersionV1
		result.TableRef = ref.Name
		w.Header().Set("X-Request-ID", requestID)
		writeJSON(w, http.StatusOK, envelope{
			RequestID: requestID, Provider: ProviderName,
			Action: route.Action, ActionVersion: route.Version,
			ResourceRef: &ref, Result: result,
		})
	})
}

func normalizeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if requestIDRE.MatchString(value) {
		return value
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "req-unavailable"
	}
	return "req-" + hex.EncodeToString(buf)
}

func logActionFailure(logger logr.Logger, requestID string, started time.Time, code, class string) {
	if logger.GetSink() == nil {
		return
	}
	logger.Info("databricks provider action failed", "requestID", requestID, "action", ActionQueryName+"/"+ActionQueryV1, "outcome", "error", "code", code, "errorClass", class, "durationMs", time.Since(started).Milliseconds())
}

func classifyActionError(err error) string {
	if err == nil {
		return "unknown"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "not found"):
		return "not_found"
	case strings.Contains(message, "forbidden"), strings.Contains(message, "not allowed"), strings.Contains(message, "unauthorized"):
		return "forbidden"
	case strings.Contains(message, "ready"), strings.Contains(message, "validated"):
		return "dependency_not_ready"
	default:
		return "backend_failure"
	}
}

func normalizeActionError(err error) *ActionError {
	if err == nil {
		return &ActionError{Code: ActionErrorCodeActionFailed, Message: defaultActionFailureMessage, Status: http.StatusBadGateway}
	}
	var typed *ActionError
	if errors.As(err, &typed) && typed != nil {
		code := strings.TrimSpace(typed.Code)
		rawMessage := strings.TrimSpace(typed.Message)
		message := safeActionErrorMessage(rawMessage)
		if !isKnownActionErrorCode(code) || (rawMessage != "" && rawMessage != defaultActionFailureMessage && message == defaultActionFailureMessage) {
			return &ActionError{Code: ActionErrorCodeActionFailed, Message: defaultActionFailureMessage, Status: http.StatusBadGateway}
		}
		status := typed.Status
		if status == 0 {
			status = defaultActionErrorStatus(code, typed.Retryable)
		}
		if !actionErrorStatusAllowed(code, status) {
			if code == ActionErrorCodeBackendFailure || code == ActionErrorCodeActionFailed {
				status = gatewayFailureStatus(typed.Retryable)
			} else {
				return &ActionError{Code: ActionErrorCodeActionFailed, Message: defaultActionFailureMessage, Status: http.StatusBadGateway}
			}
		}
		return &ActionError{Code: code, Message: message, Retryable: typed.Retryable, Status: status, Cause: typed.Cause}
	}
	return &ActionError{
		Code:    ActionErrorCodeActionFailed,
		Message: safeActionError(err),
		Status:  http.StatusBadGateway,
	}
}

func isKnownActionErrorCode(code string) bool {
	switch code {
	case ActionErrorCodeUnauthenticated,
		ActionErrorCodeTenantRequired,
		ActionErrorCodeActionNotFound,
		ActionErrorCodeInvalidRequest,
		ActionErrorCodeInvalidDeadline,
		ActionErrorCodeActionUnavailable,
		ActionErrorCodeActionTimeout,
		ActionErrorCodeActionFailed,
		ActionErrorCodeResourceNotFound,
		ActionErrorCodeResourceForbidden,
		ActionErrorCodeResourceNotReady,
		ActionErrorCodeSchemaProjectionInvalid,
		ActionErrorCodeBackendFailure:
		return true
	default:
		return false
	}
}

func actionErrorStatusAllowed(code string, status int) bool {
	if status < http.StatusBadRequest || status > 599 {
		return false
	}
	switch code {
	case ActionErrorCodeUnauthenticated:
		return status == http.StatusUnauthorized
	case ActionErrorCodeTenantRequired, ActionErrorCodeResourceForbidden:
		return status == http.StatusForbidden
	case ActionErrorCodeActionNotFound, ActionErrorCodeResourceNotFound:
		return status == http.StatusNotFound
	case ActionErrorCodeInvalidRequest, ActionErrorCodeInvalidDeadline, ActionErrorCodeSchemaProjectionInvalid:
		return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
	case ActionErrorCodeActionUnavailable:
		return status == http.StatusServiceUnavailable
	case ActionErrorCodeActionTimeout:
		return status == http.StatusGatewayTimeout || status == http.StatusServiceUnavailable
	case ActionErrorCodeResourceNotReady:
		return status == http.StatusConflict || status == http.StatusServiceUnavailable
	case ActionErrorCodeBackendFailure, ActionErrorCodeActionFailed:
		return status == http.StatusBadGateway || status == http.StatusServiceUnavailable
	default:
		return false
	}
}

func gatewayFailureStatus(retryable bool) int {
	if retryable {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}

func defaultActionErrorStatus(code string, retryable bool) int {
	switch code {
	case ActionErrorCodeUnauthenticated:
		return http.StatusUnauthorized
	case ActionErrorCodeTenantRequired, ActionErrorCodeResourceForbidden:
		return http.StatusForbidden
	case ActionErrorCodeActionNotFound, ActionErrorCodeResourceNotFound:
		return http.StatusNotFound
	case ActionErrorCodeInvalidRequest, ActionErrorCodeInvalidDeadline, ActionErrorCodeSchemaProjectionInvalid:
		return http.StatusBadRequest
	case ActionErrorCodeActionUnavailable:
		return http.StatusServiceUnavailable
	case ActionErrorCodeActionTimeout:
		return http.StatusGatewayTimeout
	case ActionErrorCodeResourceNotReady:
		if retryable {
			return http.StatusServiceUnavailable
		}
		return http.StatusConflict
	case ActionErrorCodeBackendFailure, ActionErrorCodeActionFailed:
		return gatewayFailureStatus(retryable)
	default:
		return http.StatusBadGateway
	}
}

func decodeRequest(w http.ResponseWriter, r *http.Request) (QueryInput, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var wire struct {
		Input json.RawMessage `json:"input"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return QueryInput{}, fmt.Errorf("decode request: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return QueryInput{}, fmt.Errorf("request must contain exactly one JSON value")
		}
		return QueryInput{}, fmt.Errorf("decode trailing request data: %w", err)
	}
	if len(wire.Input) == 0 || string(wire.Input) == "null" {
		wire.Input = json.RawMessage(`{}`)
	}
	if len(wire.Input) > maxInputBytes {
		return QueryInput{}, fmt.Errorf("input exceeds the declared limit of %d bytes", maxInputBytes)
	}
	var input QueryInput
	decInput := json.NewDecoder(strings.NewReader(string(wire.Input)))
	decInput.DisallowUnknownFields()
	if err := decInput.Decode(&input); err != nil {
		return QueryInput{}, fmt.Errorf("decode input: %w", err)
	}
	if err := decInput.Decode(&trailing); err != io.EOF {
		if err == nil {
			return QueryInput{}, fmt.Errorf("input must contain exactly one JSON value")
		}
		return QueryInput{}, fmt.Errorf("decode trailing input data: %w", err)
	}
	if input.Limit == 0 {
		input.Limit = defaultActionLimit
	}
	if input.Limit < 1 || input.Limit > queryapi.MaxQueryLimit {
		return QueryInput{}, fmt.Errorf("input.limit must be between 1 and %d", queryapi.MaxQueryLimit)
	}
	if len(input.Columns) > queryapi.MaxQueryColumns {
		return QueryInput{}, fmt.Errorf("input.columns must contain at most %d entries", queryapi.MaxQueryColumns)
	}
	return input, nil
}

func actionDeadline(r *http.Request) (time.Duration, error) {
	value := strings.TrimSpace(r.Header.Get("X-Faros-Action-Deadline-Ms"))
	if value == "" {
		return maxActionDeadline, nil
	}
	millis, err := strconv.ParseInt(value, 10, 64)
	if err != nil || millis < 1 {
		return 0, fmt.Errorf("X-Faros-Action-Deadline-Ms must be a positive integer")
	}
	if millis >= maxActionDeadline.Milliseconds() {
		return maxActionDeadline, nil
	}
	return time.Duration(millis) * time.Millisecond, nil
}

func hasBearer(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "bearer ") && strings.TrimSpace(strings.TrimSpace(value)[7:]) != ""
}

func safeActionError(err error) string {
	if err == nil {
		return defaultActionFailureMessage
	}
	message := strings.TrimSpace(err.Error())
	return safeActionErrorMessage(message)
}

func safeActionErrorMessage(message string) string {
	message = strings.TrimSpace(message)
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"bearer", "token", "secret", "password", "authorization", "credential",
		"root:faros:tenants:", "/clusters/", "http://", "https://", "://",
	} {
		if strings.Contains(lower, marker) {
			return defaultActionFailureMessage
		}
	}
	for _, r := range message {
		if r < 0x20 || r == 0x7f {
			return defaultActionFailureMessage
		}
	}
	// SQL text and provider target details are never safe to surface. Keep the
	// check intentionally conservative: all normal action diagnostics are
	// short status/readiness messages and do not contain SQL verbs.
	for _, marker := range []string{"select ", "insert ", "update ", "delete ", "drop ", "alter ", "create ", "merge "} {
		if strings.Contains(lower, marker) {
			return defaultActionFailureMessage
		}
	}
	if message == "" {
		return defaultActionFailureMessage
	}
	if len(message) > maxActionErrorMessageBytes {
		return defaultActionFailureMessage
	}
	return message
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func envelopeIdentity(requestID string, route *Route) envelope {
	env := envelope{RequestID: requestID, Provider: ProviderName, Action: ActionQueryName, ActionVersion: ActionQueryV1}
	if route != nil {
		env.Action = route.Action
		env.ActionVersion = route.Version
		if route.Resource == resourceName && route.Name != "" {
			env.ResourceRef = &ResourceRef{APIVersion: resourceAPIVersion, Kind: resourceKind, Resource: resourceName, Name: route.Name}
		}
	}
	return env
}

func writeEnvelopeError(w http.ResponseWriter, requestID string, route *Route, status int, code, message string) {
	env := envelopeIdentity(requestID, route)
	env.Error = &wireError{Code: code, Message: safeActionErrorMessage(message), Retryable: defaultRetryable(code)}
	w.Header().Set("X-Request-ID", requestID)
	writeJSON(w, status, env)
}

func writeFailure(w http.ResponseWriter, requestID string, route *Route, failure *ActionError) {
	if failure == nil {
		failure = &ActionError{Code: ActionErrorCodeActionFailed, Message: defaultActionFailureMessage, Status: http.StatusBadGateway}
	}
	code := strings.TrimSpace(failure.Code)
	if !isKnownActionErrorCode(code) {
		code = ActionErrorCodeActionFailed
	}
	message := safeActionErrorMessage(failure.Message)
	if message == defaultActionFailureMessage && code != ActionErrorCodeActionFailed {
		code = ActionErrorCodeActionFailed
	}
	status := failure.Status
	if status == 0 {
		status = defaultActionErrorStatus(code, failure.Retryable)
	}
	if !actionErrorStatusAllowed(code, status) {
		if code == ActionErrorCodeBackendFailure || code == ActionErrorCodeActionFailed {
			status = gatewayFailureStatus(failure.Retryable)
		} else {
			code = ActionErrorCodeActionFailed
			message = defaultActionFailureMessage
			status = http.StatusBadGateway
			failure.Retryable = false
		}
	}
	env := envelopeIdentity(requestID, route)
	env.Error = &wireError{Code: code, Message: message, Retryable: failure.Retryable}
	w.Header().Set("X-Request-ID", requestID)
	writeJSON(w, status, env)
}

func defaultRetryable(code string) bool {
	switch code {
	case ActionErrorCodeActionUnavailable, ActionErrorCodeActionTimeout:
		return true
	default:
		return false
	}
}
