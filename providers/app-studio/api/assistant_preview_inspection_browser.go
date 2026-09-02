/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Development-preview inspection now rides the shared, infrastructure-provisioned
// headless browser (the Studio's Playwright MCP instance) rather than a browser
// service app-studio owned and deployed itself. The instance is reached over the
// infrastructure provider's data-plane proxy verb — the same path web search
// uses — and the proxy root IS the Playwright MCP endpoint (streamable HTTP).
//
// The old /v1/inspect worker evaluated assertions server-side with live
// getByText/getByRole queries. Playwright MCP exposes only browser_snapshot (an
// accessibility tree), so assertion evaluation moved here and is necessarily an
// approximation over that snapshot: role/name counting and text containment
// against the accessible names the snapshot reports. This is the deliberate
// trade-off of dropping the bespoke worker for the shared, standard tool.

const (
	browserMCPProtocolVersion = "2025-03-26"
	browserMCPToolNavigate    = "browser_navigate"
	browserMCPToolSnapshot    = "browser_snapshot"
	browserMCPToolConsole     = "browser_console_messages"
	browserMCPToolScreenshot  = "browser_take_screenshot"
	browserSessionHandoffPath = "/auth/session/handoff"
	privateAppAuthorizePath   = "/auth/apps/authorize"
	privateAppCallbackPath    = "/__faros/auth/callback"
)

var supportedBrowserMCPProtocolVersions = map[string]struct{}{
	"2025-03-26": {},
	"2025-06-18": {},
}

// resolveBrowserDataPlaneRef resolves the workspace's shared browser instance
// (the Studio's Ready browser backend) into a data-plane target. ok is false
// when the workspace has no Ready browser — the caller then reports the
// inspection unavailable rather than failing obscurely.
func (s *Server) resolveBrowserDataPlaneRef(ctx context.Context, id identity) (dataPlaneRef, bool) {
	client, err := s.clientFor(id)
	if err != nil {
		return dataPlaneRef{}, false
	}
	resource, name := s.browserBackend(ctx, client)
	if strings.TrimSpace(resource) == "" || strings.TrimSpace(name) == "" {
		return dataPlaneRef{}, false
	}
	return dataPlaneRef{Resource: resource, Name: name}, true
}

// inspectPreviewViaBrowserMCP drives the shared Playwright MCP browser to load
// the preview URL, snapshot it, collect console output, evaluate the assertions
// against the snapshot, and (optionally) capture a screenshot. It builds the
// same projectAssistantPreviewInspectionResult the retired worker produced.
func (s *Server) inspectPreviewViaBrowserMCP(ctx context.Context, id identity, ref dataPlaneRef, req projectAssistantPreviewInspectionRequest) (projectAssistantPreviewInspectionResult, error) {
	unlock := lockBrowserInstance(id.clusterID, ref)
	defer unlock()
	if err := s.rejectUnmanagedBrowserSession(id, ref); err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}

	session, err := s.newBrowserMCPSessionWithRole(ctx, id, ref, projectAssistantBrowserSessionRoleLegacyInspection)
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	defer session.closeWithReason("inspection_complete", "inspectPreviewViaBrowserMCP")
	if req.RequiresHubSession {
		if err := s.preparePrivatePreviewBrowserSession(ctx, session, id, req.URL); err != nil {
			return projectAssistantPreviewInspectionResult{}, err
		}
	}

	// Navigate. A navigation error is a hard failure — nothing downstream can
	// observe a page that never loaded.
	nav, err := session.callTool(ctx, browserMCPToolNavigate, map[string]any{"url": req.URL})
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	if nav.isError {
		return projectAssistantPreviewInspectionResult{
			Status:           "failed",
			FailureKind:      "navigation",
			Summary:          browserMCPNavigationSummary(nav.text, "the preview did not load"),
			FinalURL:         req.URL,
			ScreenshotStatus: projectAssistantPreviewScreenshotStatusForUnavailable(req.IncludeScreenshot),
		}, nil
	}

	// Snapshot is the accessibility tree we assert against; navigate already
	// returns one, but a dedicated snapshot is the canonical current state.
	snap, err := session.callTool(ctx, browserMCPToolSnapshot, map[string]any{})
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	snapshotText := snap.text
	if strings.TrimSpace(snapshotText) == "" {
		snapshotText = nav.text
	}

	console, err := session.callTool(ctx, browserMCPToolConsole, map[string]any{})
	if err != nil {
		// Console evidence is best-effort; its absence must not fail inspection.
		console = browserMCPContent{}
	}

	result := projectAssistantPreviewInspectionResult{
		Status:   "succeeded",
		FinalURL: browserMCPParseField(snapshotText, "Page URL", req.URL),
		Title:    browserMCPParseField(snapshotText, "Page Title", ""),
		Snapshot: browserMCPExtractSnapshotTree(snapshotText),
		Console:  browserMCPParseConsole(console.text),
	}

	nodes := browserMCPParseAccessibilityNodes(result.Snapshot)
	failed := 0
	for _, assertion := range req.Assertions {
		outcome := browserMCPEvaluateAssertion(assertion, result.Snapshot, nodes)
		if !outcome.Passed {
			failed++
		}
		result.Assertions = append(result.Assertions, outcome)
	}
	if failed > 0 {
		result.Status = "failed"
		result.FailureKind = "assertion"
		result.Summary = fmt.Sprintf("%d of %d preview assertion(s) did not hold", failed, len(req.Assertions))
	}

	if req.IncludeScreenshot {
		shot, captureErr := session.callTool(ctx, browserMCPToolScreenshot, map[string]any{"type": "png"})
		if captureErr != nil || shot.isError {
			result.ScreenshotStatus = projectAssistantPreviewScreenshotCaptureFailed
		} else {
			result.Screenshot = browserMCPScreenshot(shot)
		}
	}
	return result, nil
}

// preparePrivatePreviewBrowserSession transfers the authenticated App Studio
// caller into the fresh headless browser without exposing its bearer token to
// Chromium. The private app gate supplies the authoritative public hub origin;
// App Studio asks the hub for a one-minute, one-use handoff and navigates the
// browser to redeem it before loading the app normally.
func (s *Server) preparePrivatePreviewBrowserSession(ctx context.Context, session *browserMCPSession, id identity, targetURL string) error {
	hubOrigin, err := s.privatePreviewHubOrigin(ctx, id, targetURL)
	if err != nil {
		return err
	}
	handoffURL, err := s.browserSessionHandoffURL(ctx, id, hubOrigin)
	if err != nil {
		return err
	}
	result, err := session.callTool(ctx, browserMCPToolNavigate, map[string]any{"url": handoffURL})
	if err != nil {
		return err
	}
	if result.isError {
		return fmt.Errorf("preview browser session handoff: %s", browserMCPNavigationSummary(result.text, "the hub handoff did not load"))
	}
	return nil
}

func (s *Server) privatePreviewHubOrigin(ctx context.Context, id identity, targetURL string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil || target.Scheme != "https" || target.Host == "" {
		return nil, errors.New("private preview URL is invalid")
	}
	trustedOrigin, err := s.privatePreviewConfiguredHubOrigin()
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, previewEdgeProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout:   previewEdgeProbeTimeout,
		Transport: projectMCPTransport(s.previewInsecureSkipTLSVerify),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolve private preview authorization: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	location, err := resp.Location()
	if err != nil || resp.StatusCode < 300 || resp.StatusCode >= 400 || location.Scheme != "https" || location.Host == "" || location.Path != privateAppAuthorizePath {
		return nil, errors.New("private preview did not return the platform authorization redirect")
	}
	if !strings.EqualFold(location.Scheme, trustedOrigin.Scheme) || !strings.EqualFold(location.Host, trustedOrigin.Host) {
		return nil, errors.New("private preview authorization redirect did not match the configured public hub origin")
	}
	query := location.Query()
	callback, err := url.Parse(query.Get("redirect_uri"))
	if err != nil || callback.Scheme != target.Scheme || !strings.EqualFold(callback.Host, target.Host) || callback.Path != privateAppCallbackPath {
		return nil, errors.New("private preview authorization callback did not match the preview origin")
	}
	if strings.TrimSpace(query.Get("cluster")) != strings.TrimSpace(id.clusterID) {
		return nil, errors.New("private preview authorization targeted a different workspace")
	}
	return trustedOrigin, nil
}

// privatePreviewConfiguredHubOrigin is the only origin App Studio allows the
// private-preview gate to select for a one-use browser handoff. The provider's
// hubBase can be an internal address, so it is not suitable for browser
// redirects; the public origin is an explicit deployment contract instead.
func (s *Server) privatePreviewConfiguredHubOrigin() (*url.URL, error) {
	raw := strings.TrimRight(strings.TrimSpace(s.hubPublicURL), "/")
	configured, err := url.Parse(raw)
	if err != nil || !configured.IsAbs() || !strings.EqualFold(configured.Scheme, "https") || configured.Host == "" || configured.User != nil || configured.Path != "" || configured.RawQuery != "" || configured.ForceQuery || configured.Fragment != "" || configured.Opaque != "" {
		return nil, errors.New("FAROS_HUB_PUBLIC_URL must be an absolute HTTPS origin")
	}
	return &url.URL{Scheme: "https", Host: configured.Host}, nil
}

func (s *Server) browserSessionHandoffURL(ctx context.Context, id identity, hubOrigin *url.URL) (string, error) {
	configuredOrigin, err := s.privatePreviewConfiguredHubOrigin()
	if err != nil {
		return "", err
	}
	if hubOrigin == nil || hubOrigin.User != nil || hubOrigin.Path != "" || hubOrigin.RawQuery != "" || hubOrigin.ForceQuery || hubOrigin.Fragment != "" || hubOrigin.Opaque != "" || !strings.EqualFold(hubOrigin.Scheme, configuredOrigin.Scheme) || !strings.EqualFold(hubOrigin.Host, configuredOrigin.Host) {
		return "", errors.New("public hub origin does not match FAROS_HUB_PUBLIC_URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.hubBase, "/")+browserSessionHandoffPath, nil)
	if err != nil {
		return "", err
	}
	if token := strings.TrimSpace(id.token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.sandboxDataPlaneClient(dataPlaneCallTimeout).Do(req)
	if err != nil {
		return "", fmt.Errorf("mint browser session handoff: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mint browser session handoff: status %d", resp.StatusCode)
	}
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", errors.New("mint browser session handoff: invalid response")
	}
	reference, err := url.Parse(strings.TrimSpace(payload.Path))
	if err != nil || reference.IsAbs() || reference.Host != "" || reference.Path != browserSessionHandoffPath || strings.TrimSpace(reference.Query().Get("code")) == "" || reference.Fragment != "" {
		return "", errors.New("mint browser session handoff: invalid path")
	}
	return hubOrigin.ResolveReference(reference).String(), nil
}

// browserMCPSession is one initialized Playwright MCP session over the
// data-plane proxy. It echoes the Mcp-Session-Id the server hands back on
// initialize, exactly like hubmcp.Client does for the code tools.
type browserMCPSession struct {
	s       *Server
	id      identity
	ref     dataPlaneRef
	role    string
	traceID uint64

	sessionMu       sync.RWMutex
	sessionID       string
	protocolVersion string
	nextID          int

	streamMu     sync.Mutex
	streamCancel context.CancelFunc
	streamBody   io.ReadCloser
	streamDone   chan struct{}
	streamErr    error

	closeMu sync.Mutex
	closed  bool
}

type browserMCPContent struct {
	text        string
	imageBase64 string
	imageMIME   string
	isError     bool
}

type browserMCPEventStreamResponse struct {
	resp *http.Response
	err  error
}

func (s *Server) newBrowserMCPSession(ctx context.Context, id identity, ref dataPlaneRef) (*browserMCPSession, error) {
	return s.newBrowserMCPSessionWithRole(ctx, id, ref, projectAssistantBrowserSessionRoleUnspecified)
}

func (s *Server) newBrowserMCPSessionWithRole(ctx context.Context, id identity, ref dataPlaneRef, role string) (*browserMCPSession, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		role = projectAssistantBrowserSessionRoleUnspecified
	}
	session := &browserMCPSession{
		s:       s,
		id:      id,
		ref:     ref,
		role:    role,
		traceID: atomic.AddUint64(&projectAssistantBrowserTraceSequence, 1),
		nextID:  1,
	}
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:       "session_create",
		Role:        session.role,
		SessionHash: session.traceSessionHash(),
		RefHash:     projectAssistantBrowserTraceRef(ref),
		CallSite:    "newBrowserMCPSessionWithRole",
	})
	initializeResult, err := session.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": browserMCPProtocolVersion,
		"clientInfo":      map[string]any{"name": "app-studio-preview", "version": "0.1.0"},
		"capabilities":    map[string]any{},
	})
	if err != nil {
		session.closeWithReason("initialize_failed", "newBrowserMCPSessionWithRole")
		return nil, err
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(initializeResult) == 0 || json.Unmarshal(initializeResult, &initialized) != nil {
		session.closeWithReason("initialize_protocol_invalid", "newBrowserMCPSessionWithRole")
		return nil, errors.New("preview browser initialize returned an invalid protocolVersion")
	}
	if err := session.setNegotiatedProtocolVersion(initialized.ProtocolVersion); err != nil {
		session.closeWithReason("initialize_protocol_unsupported", "newBrowserMCPSessionWithRole")
		return nil, err
	}
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:       "session_create_ready",
		Role:        session.role,
		SessionHash: session.traceSessionHash(),
		RefHash:     projectAssistantBrowserTraceRef(ref),
		CallSite:    "newBrowserMCPSessionWithRole",
	})
	// notifications/initialized is a notification (no id, no result). The
	// streamable HTTP transport opens its server-side heartbeat only after the
	// first tool call, but it still requires the client GET stream to be present
	// before that heartbeat can be answered.
	if _, err := session.rpc(ctx, "notifications/initialized", nil); err != nil {
		session.closeWithReason("initialized_notification_failed", "newBrowserMCPSessionWithRole")
		return nil, err
	}
	if err := session.openEventStream(ctx); err != nil {
		session.closeWithReason("event_stream_open_failed", "newBrowserMCPSessionWithRole")
		return nil, err
	}
	return session, nil
}

func (session *browserMCPSession) traceSessionHash() string {
	if session == nil {
		return ""
	}
	session.sessionMu.RLock()
	sessionID := session.sessionID
	session.sessionMu.RUnlock()
	if strings.TrimSpace(sessionID) != "" {
		return projectAssistantBrowserTraceHash(sessionID)
	}
	if session.traceID > 0 {
		return fmt.Sprintf("pending-%d", session.traceID)
	}
	return ""
}

func (session *browserMCPSession) currentSessionID() string {
	if session == nil {
		return ""
	}
	session.sessionMu.RLock()
	defer session.sessionMu.RUnlock()
	return session.sessionID
}

func (session *browserMCPSession) setSessionID(sessionID string) {
	if session == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	session.sessionMu.Lock()
	session.sessionID = strings.TrimSpace(sessionID)
	session.sessionMu.Unlock()
}

func (session *browserMCPSession) currentProtocolVersion() string {
	if session == nil {
		return ""
	}
	session.sessionMu.RLock()
	defer session.sessionMu.RUnlock()
	return session.protocolVersion
}

func (session *browserMCPSession) setNegotiatedProtocolVersion(protocolVersion string) error {
	protocolVersion = strings.TrimSpace(protocolVersion)
	if _, supported := supportedBrowserMCPProtocolVersions[protocolVersion]; !supported {
		return fmt.Errorf("preview browser initialize negotiated unsupported protocolVersion %q", protocolVersion)
	}
	session.sessionMu.Lock()
	session.protocolVersion = protocolVersion
	session.sessionMu.Unlock()
	return nil
}

func (session *browserMCPSession) setProtocolHeaders(req *http.Request) {
	if session == nil || req == nil {
		return
	}
	if sessionID := session.currentSessionID(); sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if protocolVersion := session.currentProtocolVersion(); protocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
}

func (session *browserMCPSession) nextRequestID() int {
	session.sessionMu.Lock()
	defer session.sessionMu.Unlock()
	id := session.nextID
	session.nextID++
	return id
}

func (session *browserMCPSession) isClosed() bool {
	if session == nil {
		return true
	}
	session.closeMu.Lock()
	defer session.closeMu.Unlock()
	return session.closed
}

// eventStreamFailure reports an unexpected end of the long-lived GET stream.
// The MCP session may still accept POSTs after the GET has gone away, but it
// can no longer answer the server heartbeat. Treating that session as live
// would eventually turn the next model call into a stale browser operation.
func (session *browserMCPSession) eventStreamFailure() error {
	if session == nil {
		return errors.New("preview browser session is not configured")
	}
	session.streamMu.Lock()
	defer session.streamMu.Unlock()
	return session.streamErr
}

func (session *browserMCPSession) markEventStreamFailure(readErr error) {
	if session == nil {
		return
	}
	if readErr == nil {
		readErr = io.EOF
	}
	if session.isClosed() {
		return
	}
	session.streamMu.Lock()
	if session.streamErr == nil {
		session.streamErr = fmt.Errorf("preview browser session event stream terminated: %w", readErr)
	}
	session.streamMu.Unlock()
}

// rpc posts one JSON-RPC request to the browser's data-plane proxy root and
// returns the raw result. It captures (and thereafter echoes) the session id.
func (session *browserMCPSession) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if session == nil {
		return nil, errors.New("preview browser session is not configured")
	}
	if streamErr := session.eventStreamFailure(); streamErr != nil {
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "rpc_error",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Method:      method,
			Reason:      "event_stream_terminated",
			CallSite:    "browserMCPSession.rpc",
		})
		return nil, streamErr
	}
	headerBefore := session.currentSessionID()
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:                   "rpc_start",
		Role:                    session.role,
		SessionHash:             session.traceSessionHash(),
		RefHash:                 projectAssistantBrowserTraceRef(session.ref),
		Method:                  method,
		SessionHeaderBeforeHash: projectAssistantBrowserTraceHash(headerBefore),
		CallSite:                "browserMCPSession.rpc",
	})
	envelope := map[string]any{"jsonrpc": "2.0", "method": method}
	if !strings.HasPrefix(method, "notifications/") {
		envelope["id"] = session.nextRequestID()
	}
	if params != nil {
		envelope["params"] = params
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "rpc_error",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Method:      method,
			Reason:      "marshal_error",
			CallSite:    "browserMCPSession.rpc",
		})
		return nil, err
	}
	req, err := session.s.newDataPlaneRequest(ctx, http.MethodPost, session.id, session.ref, dataPlaneVerbProxy, "", strings.NewReader(string(body)))
	if err != nil {
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "rpc_error",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Method:      method,
			Reason:      "request_build_error",
			CallSite:    "browserMCPSession.rpc",
		})
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if headerBefore != "" {
		session.setProtocolHeaders(req)
	}
	resp, err := session.s.sandboxDataPlaneClient(dataPlaneCallTimeout).Do(req)
	if err != nil {
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:                   "rpc_error",
			Role:                    session.role,
			SessionHash:             session.traceSessionHash(),
			RefHash:                 projectAssistantBrowserTraceRef(session.ref),
			Method:                  method,
			SessionHeaderBeforeHash: projectAssistantBrowserTraceHash(headerBefore),
			Reason:                  "transport_error",
			CallSite:                "browserMCPSession.rpc",
		})
		return nil, fmt.Errorf("preview browser %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	headerResponse := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	session.setSessionID(headerResponse)
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:                     "rpc_response",
		Role:                      session.role,
		SessionHash:               session.traceSessionHash(),
		RefHash:                   projectAssistantBrowserTraceRef(session.ref),
		Method:                    method,
		Status:                    resp.StatusCode,
		SessionHeaderBeforeHash:   projectAssistantBrowserTraceHash(headerBefore),
		SessionHeaderResponseHash: projectAssistantBrowserTraceHash(headerResponse),
		SessionHeaderAfterHash:    projectAssistantBrowserTraceHash(session.currentSessionID()),
		CallSite:                  "browserMCPSession.rpc",
	})
	raw, err := io.ReadAll(io.LimitReader(resp.Body, projectAssistantPreviewInspectionMaxResponse))
	if err != nil {
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "rpc_error",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Method:      method,
			Status:      resp.StatusCode,
			Reason:      "read_error",
			CallSite:    "browserMCPSession.rpc",
		})
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("preview browser %s: status %d: %s", method, resp.StatusCode, browserMCPFirstLine(string(raw), ""))
	}
	if len(raw) == 0 {
		return nil, nil
	}
	payload := browserMCPUnwrapSSE(raw, resp.Header.Get("Content-Type"))
	if len(payload) == 0 {
		return nil, nil
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return nil, fmt.Errorf("preview browser %s: bad response: %w", method, err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("preview browser %s: %s", method, rpc.Error.Message)
	}
	return rpc.Result, nil
}

// openEventStream establishes the long-lived GET side of streamable HTTP. The
// Playwright MCP server starts a heartbeat once its first tool backend is
// initialized; without this GET channel, the server's ping cannot reach us and
// it closes the session after its ping timeout. The request is bounded until
// response headers arrive, but the returned response body is deliberately not
// given a client timeout and remains open for the session lifetime.
func (session *browserMCPSession) openEventStream(waitCtx context.Context) error {
	if session == nil || session.s == nil {
		return errors.New("preview browser session is not configured")
	}
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	sessionID := session.currentSessionID()
	if sessionID == "" {
		return errors.New("preview browser event stream requires a negotiated session")
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())
	req, err := session.s.newDataPlaneRequest(streamCtx, http.MethodGet, session.id, session.ref, dataPlaneVerbProxy, "", nil)
	if err != nil {
		streamCancel()
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	session.setProtocolHeaders(req)
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:                   "event_stream_start",
		Role:                    session.role,
		SessionHash:             session.traceSessionHash(),
		RefHash:                 projectAssistantBrowserTraceRef(session.ref),
		Method:                  http.MethodGet,
		SessionHeaderBeforeHash: projectAssistantBrowserTraceHash(sessionID),
		CallSite:                "browserMCPSession.openEventStream",
	})

	responseCh := make(chan browserMCPEventStreamResponse, 1)
	go func() {
		resp, err := session.s.sandboxDataPlaneClient(0).Do(req)
		responseCh <- browserMCPEventStreamResponse{resp: resp, err: err}
	}()
	timer := time.NewTimer(dataPlaneCallTimeout)
	defer timer.Stop()
	var response browserMCPEventStreamResponse
	select {
	case response = <-responseCh:
	case <-waitCtx.Done():
		streamCancel()
		go closeEventStreamResponseBody(responseCh)
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "event_stream_error",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Method:      http.MethodGet,
			Reason:      "context_canceled",
			CallSite:    "browserMCPSession.openEventStream",
		})
		return fmt.Errorf("preview browser event stream: %w", waitCtx.Err())
	case <-timer.C:
		streamCancel()
		go closeEventStreamResponseBody(responseCh)
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "event_stream_error",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Method:      http.MethodGet,
			Reason:      "response_header_timeout",
			CallSite:    "browserMCPSession.openEventStream",
		})
		return fmt.Errorf("preview browser event stream: response headers timed out after %s", dataPlaneCallTimeout)
	}
	if response.err != nil {
		streamCancel()
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "event_stream_error",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Method:      http.MethodGet,
			Reason:      "transport_error",
			CallSite:    "browserMCPSession.openEventStream",
		})
		return fmt.Errorf("preview browser event stream: %w", response.err)
	}
	if response.resp == nil || response.resp.Body == nil {
		streamCancel()
		return errors.New("preview browser event stream: empty response")
	}
	resp := response.resp
	headerResponse := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:                     "event_stream_response",
		Role:                      session.role,
		SessionHash:               session.traceSessionHash(),
		RefHash:                   projectAssistantBrowserTraceRef(session.ref),
		Method:                    http.MethodGet,
		Status:                    resp.StatusCode,
		SessionHeaderBeforeHash:   projectAssistantBrowserTraceHash(sessionID),
		SessionHeaderResponseHash: projectAssistantBrowserTraceHash(headerResponse),
		SessionHeaderAfterHash:    projectAssistantBrowserTraceHash(session.currentSessionID()),
		CallSite:                  "browserMCPSession.openEventStream",
	})
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		_ = resp.Body.Close()
		streamCancel()
		return fmt.Errorf("preview browser event stream: status %d: %s", resp.StatusCode, browserMCPFirstLine(string(raw), ""))
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		_ = resp.Body.Close()
		streamCancel()
		return errors.New("preview browser event stream: response was not text/event-stream")
	}
	if headerResponse != "" && headerResponse != sessionID {
		_ = resp.Body.Close()
		streamCancel()
		return errors.New("preview browser event stream: response changed the session")
	}

	done := make(chan struct{})
	session.streamMu.Lock()
	if session.isClosed() || session.streamBody != nil {
		session.streamMu.Unlock()
		_ = resp.Body.Close()
		streamCancel()
		if session.isClosed() {
			return errors.New("preview browser event stream: session is closed")
		}
		return errors.New("preview browser event stream: already open")
	}
	session.streamCancel = streamCancel
	session.streamBody = resp.Body
	session.streamDone = done
	session.streamMu.Unlock()
	go session.drainEventStream(streamCtx, resp.Body, done)
	return nil
}

func closeEventStreamResponseBody(responseCh <-chan browserMCPEventStreamResponse) {
	response := <-responseCh
	if response.resp != nil && response.resp.Body != nil {
		_ = response.resp.Body.Close()
	}
}

// drainEventStream owns the GET response body until close. Besides consuming
// ordinary server events, it answers MCP ping requests delivered over SSE. A
// blocked read is released by stopEventStream after DELETE has been sent.
func (session *browserMCPSession) drainEventStream(ctx context.Context, body io.ReadCloser, done chan struct{}) {
	defer close(done)
	defer func() {
		_ = body.Close()
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "event_stream_end",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Method:      http.MethodGet,
			CallSite:    "browserMCPSession.drainEventStream",
		})
	}()

	reader := bufio.NewReader(body)
	var data strings.Builder
	flush := func() {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" {
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal([]byte(payload), &request); err != nil || request.Method != "ping" || len(request.ID) == 0 || !json.Valid(request.ID) {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := session.respondToEventStreamRequest(ctx, request.ID, request.Method); err != nil {
			projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
				Event:       "event_stream_ping_error",
				Role:        session.role,
				SessionHash: session.traceSessionHash(),
				RefHash:     projectAssistantBrowserTraceRef(session.ref),
				Method:      request.Method,
				Reason:      "response_error",
				CallSite:    "browserMCPSession.drainEventStream",
			})
		}
	}
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			switch {
			case line == "":
				flush()
			case strings.HasPrefix(line, "data:"):
				value := strings.TrimPrefix(line, "data:")
				value = strings.TrimPrefix(value, " ")
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(value)
			}
		}
		if err != nil {
			session.markEventStreamFailure(err)
			reason := "read_error"
			if err == io.EOF {
				reason = "unexpected_eof"
			}
			projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
				Event:       "event_stream_error",
				Role:        session.role,
				SessionHash: session.traceSessionHash(),
				RefHash:     projectAssistantBrowserTraceRef(session.ref),
				Method:      http.MethodGet,
				Reason:      reason,
				CallSite:    "browserMCPSession.drainEventStream",
			})
			flush()
			return
		}
	}
}

func (session *browserMCPSession) respondToEventStreamRequest(ctx context.Context, id json.RawMessage, method string) error {
	if session == nil || session.s == nil {
		return errors.New("preview browser session is not configured")
	}
	body, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  map[string]any  `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: map[string]any{}})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, dataPlaneCallTimeout)
	defer cancel()
	req, err := session.s.newDataPlaneRequest(requestCtx, http.MethodPost, session.id, session.ref, dataPlaneVerbProxy, "", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	session.setProtocolHeaders(req)
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:       "event_stream_ping_start",
		Role:        session.role,
		SessionHash: session.traceSessionHash(),
		RefHash:     projectAssistantBrowserTraceRef(session.ref),
		Method:      method,
		CallSite:    "browserMCPSession.respondToEventStreamRequest",
	})
	resp, err := session.s.sandboxDataPlaneClient(dataPlaneCallTimeout).Do(req)
	if err != nil {
		return err
	}
	if resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("preview browser %s response: status %d", method, resp.StatusCode)
	}
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:       "event_stream_ping_response",
		Role:        session.role,
		SessionHash: session.traceSessionHash(),
		RefHash:     projectAssistantBrowserTraceRef(session.ref),
		Method:      method,
		Status:      resp.StatusCode,
		CallSite:    "browserMCPSession.respondToEventStreamRequest",
	})
	return nil
}

func (session *browserMCPSession) listTools(ctx context.Context) ([]projectMCPTool, error) {
	result, err := session.rpc(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools      []projectMCPTool `json:"tools"`
		NextCursor string           `json:"nextCursor,omitempty"`
	}
	if len(result) == 0 {
		return nil, errors.New("preview browser tools/list returned an empty result")
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("preview browser tools/list: bad result: %w", err)
	}
	// The upstream Playwright MCP catalog is small enough for one page. Refuse
	// to follow an unbounded cursor supplied by an unexpected implementation.
	for page := 0; strings.TrimSpace(payload.NextCursor) != "" && page < 8; page++ {
		result, err = session.rpc(ctx, "tools/list", map[string]any{"cursor": payload.NextCursor})
		if err != nil {
			return nil, err
		}
		var next struct {
			Tools      []projectMCPTool `json:"tools"`
			NextCursor string           `json:"nextCursor,omitempty"`
		}
		if err := json.Unmarshal(result, &next); err != nil {
			return nil, fmt.Errorf("preview browser tools/list: bad paged result: %w", err)
		}
		payload.Tools = append(payload.Tools, next.Tools...)
		payload.NextCursor = next.NextCursor
	}
	if strings.TrimSpace(payload.NextCursor) != "" {
		return nil, errors.New("preview browser tools/list exceeded the catalog page limit")
	}
	return payload.Tools, nil
}

// callToolReceipt preserves the native MCP result, including structuredContent
// and content blocks. The model and evidence reducer both consume this receipt;
// no server-side assertion evaluator or wrapper envelope is inserted.
func (session *browserMCPSession) callToolReceipt(ctx context.Context, name string, args map[string]any) (string, error) {
	result, err := session.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	if len(result) == 0 {
		return "{}", nil
	}
	if !json.Valid(result) {
		return "", fmt.Errorf("preview browser tool %s: bad result", name)
	}
	return string(result), nil
}

// callTool invokes one Playwright MCP tool and flattens its content blocks into
// text plus an optional first image. A tool that reports isError is returned as
// such (not an error) so the caller can classify it.
func (session *browserMCPSession) callTool(ctx context.Context, name string, args map[string]any) (browserMCPContent, error) {
	receipt, err := session.callToolReceipt(ctx, name, args)
	if err != nil {
		return browserMCPContent{}, err
	}
	var call struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MIMEType string `json:"mimeType"`
		} `json:"content"`
	}
	if len(receipt) > 0 {
		if err := json.Unmarshal([]byte(receipt), &call); err != nil {
			return browserMCPContent{}, fmt.Errorf("preview browser tool %s: bad result: %w", name, err)
		}
	}
	out := browserMCPContent{isError: call.IsError}
	var text strings.Builder
	for _, block := range call.Content {
		switch block.Type {
		case "text":
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(block.Text)
		case "image":
			if out.imageBase64 == "" {
				out.imageBase64 = block.Data
				out.imageMIME = block.MIMEType
			}
		}
	}
	out.text = text.String()
	return out, nil
}

// close ends the MCP session so the shared browser's single Chromium is freed
// for the next inspection. DELETE is sent while the GET event stream is alive;
// only after the server has observed DELETE do we cancel and drain that stream.
// Best effort — the server also reaps idle sessions.
func (session *browserMCPSession) close() {
	session.closeWithReason("unspecified", "browserMCPSession.close")
}

func (session *browserMCPSession) closeWithReason(reason, callSite string) {
	if session == nil {
		return
	}
	session.closeMu.Lock()
	if session.closed {
		session.closeMu.Unlock()
		return
	}
	session.closed = true
	session.closeMu.Unlock()
	defer session.stopEventStream(reason, callSite)

	sessionID := session.currentSessionID()
	if sessionID == "" {
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "session_close_skip",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Reason:      "missing_session_header",
			CallSite:    callSite,
		})
		return
	}
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:       "session_close_start",
		Role:        session.role,
		SessionHash: session.traceSessionHash(),
		RefHash:     projectAssistantBrowserTraceRef(session.ref),
		Reason:      reason,
		CallSite:    callSite,
	})
	ctx, cancel := context.WithTimeout(context.Background(), dataPlaneCallTimeout)
	defer cancel()
	req, err := session.s.newDataPlaneRequest(ctx, http.MethodDelete, session.id, session.ref, dataPlaneVerbProxy, "", nil)
	if err != nil {
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "session_close_error",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Reason:      "request_build_error",
			CallSite:    callSite,
		})
		return
	}
	session.setProtocolHeaders(req)
	if resp, err := session.s.sandboxDataPlaneClient(dataPlaneCallTimeout).Do(req); err == nil {
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "session_close_response",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Status:      resp.StatusCode,
			Reason:      reason,
			CallSite:    callSite,
		})
		_ = resp.Body.Close()
	} else {
		projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
			Event:       "session_close_error",
			Role:        session.role,
			SessionHash: session.traceSessionHash(),
			RefHash:     projectAssistantBrowserTraceRef(session.ref),
			Reason:      "transport_error",
			CallSite:    callSite,
		})
	}
}

func (session *browserMCPSession) stopEventStream(reason, callSite string) {
	if session == nil {
		return
	}
	session.streamMu.Lock()
	cancel := session.streamCancel
	body := session.streamBody
	done := session.streamDone
	session.streamCancel = nil
	session.streamBody = nil
	session.streamDone = nil
	session.streamMu.Unlock()
	if cancel == nil && body == nil && done == nil {
		return
	}
	if cancel != nil {
		cancel()
	}
	if body != nil {
		_ = body.Close()
	}
	if done != nil {
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-done:
			timer.Stop()
		case <-timer.C:
			projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
				Event:       "event_stream_drain_timeout",
				Role:        session.role,
				SessionHash: session.traceSessionHash(),
				RefHash:     projectAssistantBrowserTraceRef(session.ref),
				Method:      http.MethodGet,
				Reason:      reason,
				CallSite:    callSite,
			})
		}
	}
	projectAssistantBrowserTrace(projectAssistantBrowserTraceEvent{
		Event:       "event_stream_close",
		Role:        session.role,
		SessionHash: session.traceSessionHash(),
		RefHash:     projectAssistantBrowserTraceRef(session.ref),
		Method:      http.MethodGet,
		Reason:      reason,
		CallSite:    callSite,
	})
}

// browserMCPUnwrapSSE returns the JSON payload from a streamable-HTTP response,
// unwrapping a single SSE `data:` event when the server answered that way.
func browserMCPUnwrapSSE(raw []byte, contentType string) []byte {
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "event:") || strings.Contains(contentType, "text/event-stream") {
		for _, line := range strings.Split(string(raw), "\n") {
			if data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
				return []byte(strings.TrimSpace(data))
			}
		}
		return nil
	}
	return raw
}

// browserMCPParseField reads a "Label: value" line (Playwright MCP prefixes its
// snapshot with "Page URL:" and "Page Title:"), returning fallback when absent.
func browserMCPParseField(text, label, fallback string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if value, ok := strings.CutPrefix(line, label+":"); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return fallback
}

// browserMCPExtractSnapshotTree returns the YAML accessibility tree from a
// Playwright MCP snapshot, which the server fences after a "Page Snapshot"
// heading. Falls back to the whole text when no fence is present.
func browserMCPExtractSnapshotTree(text string) string {
	if start := strings.Index(text, "```yaml"); start >= 0 {
		rest := text[start+len("```yaml"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	if start := strings.Index(text, "```"); start >= 0 {
		rest := text[start+3:]
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return strings.TrimSpace(text)
}

// browserMCPConsoleLine matches Playwright MCP's "[LEVEL] message" console form.
var browserMCPConsoleLine = regexp.MustCompile(`^\[(?P<level>[A-Za-z]+)\]\s?(?P<msg>.*)$`)

func browserMCPParseConsole(text string) []projectAssistantPreviewInspectionConsoleEvent {
	var events []projectAssistantPreviewInspectionConsoleEvent
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		level, message := "log", line
		if m := browserMCPConsoleLine.FindStringSubmatch(line); m != nil {
			level = strings.ToLower(m[1])
			message = strings.TrimSpace(m[2])
		}
		events = append(events, projectAssistantPreviewInspectionConsoleEvent{Level: level, Message: message})
		if len(events) >= 100 {
			break
		}
	}
	return events
}

// browserMCPNode is one accessibility-tree entry: a role, its accessible name
// (quoted in the snapshot), and the Playwright MCP element ref that interaction
// tools target. Parsed from a line such as
// `  - button "Add to cart" [ref=e7]`.
type browserMCPNode struct {
	role string
	name string
	ref  string
}

var (
	browserMCPNodeLine = regexp.MustCompile(`^-\s+(?P<role>[A-Za-z0-9_]+)(?:\s+"(?P<name>(?:[^"\\]|\\.)*)")?`)
	browserMCPNodeRef  = regexp.MustCompile(`\[ref=([^\]]+)\]`)
)

func browserMCPParseAccessibilityNodes(snapshot string) []browserMCPNode {
	var nodes []browserMCPNode
	for _, line := range strings.Split(snapshot, "\n") {
		trimmed := strings.TrimSpace(line)
		m := browserMCPNodeLine.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		node := browserMCPNode{
			role: strings.ToLower(m[1]),
			name: browserMCPUnquote(m[2]),
		}
		if ref := browserMCPNodeRef.FindStringSubmatch(trimmed); ref != nil {
			node.ref = ref[1]
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func browserMCPUnquote(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}

// browserMCPEvaluateAssertion evaluates one assertion against the accessibility
// snapshot. It approximates the retired worker's getByText/getByRole semantics:
// text_present is containment over accessible names + snapshot text; role_* count
// nodes by role and (optionally) accessible-name filter.
func browserMCPEvaluateAssertion(assertion projectAssistantPreviewInspectionAssertion, snapshot string, nodes []browserMCPNode) projectAssistantPreviewInspectionAssertionResult {
	out := projectAssistantPreviewInspectionAssertionResult{projectAssistantPreviewInspectionAssertion: assertion}
	count := 0
	switch assertion.Kind {
	case "text_present":
		for _, node := range nodes {
			if browserMCPTextMatches(node.name, assertion.Text, assertion.Exact) {
				count++
			}
		}
		// Fall back to raw snapshot containment so text outside an accessible
		// name (e.g. static copy) still counts, matching getByText's reach.
		if count == 0 && !assertion.Exact && strings.Contains(strings.ToLower(snapshot), strings.ToLower(assertion.Text)) {
			count = 1
		}
	case "role_present", "role_count":
		for _, node := range nodes {
			if node.role != strings.ToLower(assertion.Role) {
				continue
			}
			if assertion.Name != "" && !browserMCPTextMatches(node.name, assertion.Name, assertion.Exact) {
				continue
			}
			count++
		}
	}
	actual := count
	out.ActualCount = &actual

	min, max := 1, -1
	if assertion.Kind == "role_count" {
		min = 0
		if assertion.Min != nil {
			min = *assertion.Min
		}
		if assertion.Max != nil {
			max = *assertion.Max
		}
	}
	out.Passed = count >= min && (max < 0 || count <= max)
	if !out.Passed {
		out.Message = browserMCPAssertionMessage(assertion, count, min, max)
	}
	return out
}

func browserMCPTextMatches(have, want string, exact bool) bool {
	if want == "" {
		return true
	}
	if exact {
		return strings.EqualFold(strings.TrimSpace(have), strings.TrimSpace(want))
	}
	return strings.Contains(strings.ToLower(have), strings.ToLower(want))
}

func browserMCPAssertionMessage(assertion projectAssistantPreviewInspectionAssertion, count, min, max int) string {
	switch assertion.Kind {
	case "text_present":
		return fmt.Sprintf("text %q not found in the preview", assertion.Text)
	case "role_present":
		if assertion.Name != "" {
			return fmt.Sprintf("no %q with name %q found", assertion.Role, assertion.Name)
		}
		return fmt.Sprintf("no %q found", assertion.Role)
	default:
		bound := fmt.Sprintf("at least %d", min)
		if max >= 0 {
			bound = fmt.Sprintf("between %d and %d", min, max)
		}
		return fmt.Sprintf("found %d %q, expected %s", count, assertion.Role, bound)
	}
}

// browserMCPScreenshot converts a Playwright MCP image content block into the
// inspection screenshot shape. Width/Height are not reported by MCP, so they
// stay zero; the SHA and byte count are derived from the decoded image.
func browserMCPScreenshot(content browserMCPContent) *projectAssistantPreviewInspectionScreenshot {
	if content.imageBase64 == "" {
		return nil
	}
	mime := content.imageMIME
	if mime == "" {
		mime = "image/png"
	}
	shot := &projectAssistantPreviewInspectionScreenshot{MIMEType: mime, Base64: content.imageBase64}
	if decoded, err := base64.StdEncoding.DecodeString(content.imageBase64); err == nil {
		sum := sha256.Sum256(decoded)
		shot.SHA256 = hex.EncodeToString(sum[:])
		shot.Bytes = len(decoded)
	}
	return shot
}

const browserMCPNavigationSummaryMaxChars = 240

// browserMCPNavigationSummary extracts a useful navigation failure from
// Playwright MCP's Markdown response. The tool commonly starts with headings
// such as "### Result" or "### Error"; those are presentation scaffolding, not
// evidence. Prefer a bounded substantive error line and fall back to the first
// non-heading detail when the server uses a different wording.
func browserMCPNavigationSummary(text, fallback string) string {
	var firstDetail, substantive string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimLeft(line, "-* \t")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "```") {
			continue
		}
		lower := strings.ToLower(line)
		trimmedLabel := strings.TrimSuffix(strings.TrimSpace(lower), ":")
		switch trimmedLabel {
		case "result", "error", "navigation", "navigation failed", "call log", "page url", "page title", "page snapshot":
			continue
		}
		if strings.HasPrefix(lower, "page url:") || strings.HasPrefix(lower, "page title:") || strings.HasPrefix(lower, "page snapshot:") {
			continue
		}
		if firstDetail == "" {
			firstDetail = line
		}
		if strings.HasPrefix(lower, "error:") ||
			strings.Contains(lower, "net::") ||
			strings.Contains(lower, "err_") ||
			strings.Contains(lower, "timed out") ||
			strings.Contains(lower, "timeout") ||
			strings.Contains(lower, "failed") ||
			strings.Contains(lower, "refused") {
			substantive = line
			break
		}
	}
	if substantive == "" {
		substantive = firstDetail
	}
	if substantive == "" {
		return fallback
	}
	return trimProjectAssistantWorkflowString(substantive, browserMCPNavigationSummaryMaxChars)
}

// browserMCPFirstLine returns the first non-empty line of text, or fallback.
func browserMCPFirstLine(text, fallback string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}
