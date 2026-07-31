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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

const (
	previewConsoleSessionTTL       = 15 * time.Minute
	previewConsoleMaxSessions      = 128
	previewConsoleMaxEvents        = 256
	previewConsoleMaxSessionBytes  = 256 << 10
	previewConsoleMaxEventBytes    = 2 << 10
	previewConsoleMaxBatchEvents   = 32
	previewConsoleMaxBatchBytes    = 32 << 10
	previewConsoleMaxToolEvents    = 100
	previewConsoleDefaultToolLimit = 50
	previewConsoleMaxToolBytes     = 32 << 10
	previewConsoleProtocolVersion  = 1
	previewConsoleToolTrust        = "untrusted_application_output"
)

var errPreviewConsoleCapability = errors.New("invalid preview console capability")

type previewConsoleScope struct {
	ClusterID     string
	OrgUUID       string
	WorkspaceUUID string
	ProjectUID    types.UID
	ProjectName   string
	Actor         string
}

func projectPreviewConsoleScope(id identity, project *aiv1alpha1.Project) (previewConsoleScope, error) {
	if project == nil {
		return previewConsoleScope{}, errors.New("project is required")
	}
	if project.UID == "" {
		return previewConsoleScope{}, errors.New("project UID is required")
	}
	if strings.TrimSpace(id.user) == "" {
		return previewConsoleScope{}, errors.New("user identity is required")
	}
	return previewConsoleScope{
		ClusterID:     id.clusterID,
		OrgUUID:       id.orgUUID,
		WorkspaceUUID: id.workspaceUUID,
		ProjectUID:    project.UID,
		ProjectName:   project.Name,
		Actor:         id.user,
	}, nil
}

func (s previewConsoleScope) key() string {
	return strings.Join([]string{s.ClusterID, s.OrgUUID, s.WorkspaceUUID, string(s.ProjectUID), s.Actor}, "\x00")
}

type previewConsoleEvent struct {
	Sequence   uint64 `json:"sequence"`
	Ordinal    uint64 `json:"ordinal"`
	DocumentID string `json:"documentID"`
	Level      string `json:"level"`
	Message    string `json:"message"`
	Stack      string `json:"stack,omitempty"`
	SourceURL  string `json:"sourceURL,omitempty"`
	ClientTime string `json:"clientTime,omitempty"`
	CapturedAt string `json:"capturedAt"`
}

type previewConsoleIncomingEvent struct {
	Sequence   uint64 `json:"sequence"`
	DocumentID string `json:"documentID"`
	Level      string `json:"level"`
	Message    string `json:"message"`
	Stack      string `json:"stack,omitempty"`
	SourceURL  string `json:"sourceURL,omitempty"`
	ClientTime string `json:"clientTime,omitempty"`
}

type previewConsoleSession struct {
	ID            string
	Nonce         string
	Scope         previewConsoleScope
	PreviewOrigin string
	PortalOrigin  string
	Generation    string
	Protocol      int
	ExpiresAt     time.Time
	UpdatedAt     time.Time
	DocumentID    string
	Events        []previewConsoleEvent
	EventBytes    int
	LastSequence  uint64
	NextOrdinal   uint64
	DroppedCount  int
	ReceivedCount int
	RedactedCount int
	LastBatchHash [32]byte
}

type previewConsoleStore struct {
	mu            sync.Mutex
	now           func() time.Time
	sessions      map[string]*previewConsoleSession
	active        map[string]string
	totalReceived uint64
	totalDropped  uint64
	totalRedacted uint64
}

func newPreviewConsoleStore() *previewConsoleStore {
	return &previewConsoleStore{
		now:      time.Now,
		sessions: map[string]*previewConsoleSession{},
		active:   map[string]string{},
	}
}

func (s *previewConsoleStore) create(scope previewConsoleScope, previewOrigin, portalOrigin, generation string, protocol int, expiresAt time.Time) (*previewConsoleSession, error) {
	if s == nil {
		return nil, errors.New("preview console store is not configured")
	}
	sessionID, err := previewConsoleRandomID()
	if err != nil {
		return nil, err
	}
	nonce, err := previewConsoleRandomID()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	if previous := s.active[scope.key()]; previous != "" {
		delete(s.sessions, previous)
	}
	for len(s.sessions) >= previewConsoleMaxSessions {
		s.evictOldestLocked()
	}
	session := &previewConsoleSession{
		ID:            sessionID,
		Nonce:         nonce,
		Scope:         scope,
		PreviewOrigin: previewOrigin,
		PortalOrigin:  portalOrigin,
		Generation:    generation,
		Protocol:      protocol,
		ExpiresAt:     expiresAt.UTC(),
		UpdatedAt:     now,
	}
	s.sessions[sessionID] = session
	s.active[scope.key()] = sessionID
	return clonePreviewConsoleSession(session), nil
}

func (s *previewConsoleStore) append(sessionID string, scope previewConsoleScope, generation string, protocol int, incoming []previewConsoleIncomingEvent, clientDropped ...int) (int, uint64, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	session := s.sessions[sessionID]
	if session == nil {
		return 0, 0, 0, errors.New("preview console session not found")
	}
	if !session.ExpiresAt.After(now) {
		s.deleteLocked(session)
		return 0, 0, 0, errors.New("preview console session expired")
	}
	if session.Scope != scope || session.Generation != generation || session.Protocol != protocol {
		return 0, 0, 0, errors.New("preview console session scope mismatch")
	}
	reportedDropped := 0
	if len(clientDropped) > 0 {
		reportedDropped = clientDropped[0]
	}
	batchRaw, _ := json.Marshal(struct {
		Events       []previewConsoleIncomingEvent `json:"events"`
		DroppedCount int                           `json:"droppedCount"`
	}{Events: incoming, DroppedCount: reportedDropped})
	batchHash := sha256.Sum256(batchRaw)
	if batchHash == session.LastBatchHash {
		return 0, session.NextOrdinal, session.DroppedCount, nil
	}
	session.LastBatchHash = batchHash
	session.DroppedCount += reportedDropped
	s.totalDropped += uint64(reportedDropped)
	accepted := 0
	for _, raw := range incoming {
		session.ReceivedCount++
		s.totalReceived++
		event, ok := sanitizePreviewConsoleEvent(raw, now)
		if !ok || event.DocumentID != session.Generation || event.Sequence <= session.LastSequence {
			session.DroppedCount++
			s.totalDropped++
			continue
		}
		if previewConsoleEventWasRedacted(raw, event) {
			session.RedactedCount++
			s.totalRedacted++
		}
		session.DocumentID = event.DocumentID
		session.LastSequence = event.Sequence
		session.NextOrdinal++
		event.Ordinal = session.NextOrdinal
		size := previewConsoleEventSize(event)
		session.Events = append(session.Events, event)
		session.EventBytes += size
		accepted++
		for len(session.Events) > previewConsoleMaxEvents || session.EventBytes > previewConsoleMaxSessionBytes {
			session.EventBytes -= previewConsoleEventSize(session.Events[0])
			session.Events = session.Events[1:]
			session.DroppedCount++
			s.totalDropped++
		}
	}
	session.UpdatedAt = now
	return accepted, session.NextOrdinal, session.DroppedCount, nil
}

type previewConsoleMetrics struct {
	Received uint64
	Dropped  uint64
	Redacted uint64
}

func (s *previewConsoleStore) metrics() previewConsoleMetrics {
	if s == nil {
		return previewConsoleMetrics{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return previewConsoleMetrics{
		Received: s.totalReceived,
		Dropped:  s.totalDropped,
		Redacted: s.totalRedacted,
	}
}

func (s *previewConsoleStore) delete(sessionID string, scope previewConsoleScope) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[sessionID]
	if session == nil || session.Scope != scope {
		return false
	}
	s.deleteLocked(session)
	return true
}

func (s *previewConsoleStore) redactedCount(sessionID string, scope previewConsoleScope) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[sessionID]
	if session == nil || session.Scope != scope {
		return 0
	}
	return session.RedactedCount
}

func (s *previewConsoleStore) read(scope previewConsoleScope, levels map[string]struct{}, limit int, since uint64) previewConsoleToolResult {
	if s == nil {
		return previewConsoleToolResult{Status: "not_connected", Trust: previewConsoleToolTrust}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[s.active[scope.key()]]
	if session == nil {
		return previewConsoleToolResult{Status: "not_connected", Trust: previewConsoleToolTrust}
	}
	now := s.now().UTC()
	if !session.ExpiresAt.After(now) {
		s.deleteLocked(session)
		return previewConsoleToolResult{Status: "expired", Trust: previewConsoleToolTrust}
	}
	events := make([]previewConsoleEvent, 0, limit)
	for i := len(session.Events) - 1; i >= 0 && len(events) < limit; i-- {
		event := session.Events[i]
		if event.Ordinal <= since {
			continue
		}
		if len(levels) > 0 {
			if _, ok := levels[event.Level]; !ok {
				continue
			}
		}
		events = append(events, event)
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	for len(events) > 0 {
		raw, _ := json.Marshal(events)
		if len(raw) <= previewConsoleMaxToolBytes {
			break
		}
		events = events[1:]
	}
	status := "empty"
	if len(events) > 0 {
		status = "available"
	}
	path := ""
	if len(events) > 0 {
		if parsed, err := url.Parse(events[len(events)-1].SourceURL); err == nil {
			path = parsed.Path
		}
	}
	return previewConsoleToolResult{
		Status:        status,
		Trust:         previewConsoleToolTrust,
		DocumentID:    session.DocumentID,
		Path:          path,
		Events:        events,
		NextSequence:  session.NextOrdinal,
		DroppedCount:  session.DroppedCount,
		ReceivedCount: session.ReceivedCount,
		RedactedCount: session.RedactedCount,
		Summary:       previewConsoleSummary(events),
	}
}

func (s *previewConsoleStore) cleanupLocked(now time.Time) {
	for _, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			s.deleteLocked(session)
		}
	}
}

func (s *previewConsoleStore) evictOldestLocked() {
	var oldest *previewConsoleSession
	for _, session := range s.sessions {
		if oldest == nil || session.UpdatedAt.Before(oldest.UpdatedAt) {
			oldest = session
		}
	}
	if oldest != nil {
		s.deleteLocked(oldest)
	}
}

func (s *previewConsoleStore) deleteLocked(session *previewConsoleSession) {
	delete(s.sessions, session.ID)
	if s.active[session.Scope.key()] == session.ID {
		delete(s.active, session.Scope.key())
	}
}

func clonePreviewConsoleSession(session *previewConsoleSession) *previewConsoleSession {
	if session == nil {
		return nil
	}
	out := *session
	out.Events = append([]previewConsoleEvent(nil), session.Events...)
	return &out
}

func previewConsoleEventSize(event previewConsoleEvent) int {
	raw, _ := json.Marshal(event)
	return len(raw)
}

func sanitizePreviewConsoleEvent(raw previewConsoleIncomingEvent, now time.Time) (previewConsoleEvent, bool) {
	level := strings.ToLower(strings.TrimSpace(raw.Level))
	switch level {
	case "debug", "info", "log", "warn", "error", "pageerror", "unhandledrejection":
	default:
		return previewConsoleEvent{}, false
	}
	documentID := strings.TrimSpace(raw.DocumentID)
	if raw.Sequence == 0 || documentID == "" || len(documentID) > 128 {
		return previewConsoleEvent{}, false
	}
	message := projectEinoAssistantSafeText(raw.Message)
	stack := projectEinoAssistantSafeText(raw.Stack)
	if message == "" && stack == "" {
		return previewConsoleEvent{}, false
	}
	event := previewConsoleEvent{
		Sequence:   raw.Sequence,
		DocumentID: documentID,
		Level:      level,
		Message:    message,
		Stack:      stack,
		SourceURL:  sanitizePreviewConsoleSourceURL(raw.SourceURL),
		ClientTime: sanitizePreviewConsoleClientTime(raw.ClientTime),
		CapturedAt: now.Format(time.RFC3339Nano),
	}
	return boundPreviewConsoleEvent(event), true
}

func boundPreviewConsoleEvent(event previewConsoleEvent) previewConsoleEvent {
	for previewConsoleEventSize(event) > previewConsoleMaxEventBytes && event.Stack != "" {
		event.Stack = truncatePreviewConsoleString(event.Stack, len(event.Stack)-128)
	}
	for previewConsoleEventSize(event) > previewConsoleMaxEventBytes && event.Message != "" {
		event.Message = truncatePreviewConsoleString(event.Message, len(event.Message)-128)
	}
	return event
}

func truncatePreviewConsoleString(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	for len(value) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

func previewConsoleEventWasRedacted(raw previewConsoleIncomingEvent, event previewConsoleEvent) bool {
	return strings.TrimSpace(raw.Message) != event.Message ||
		strings.TrimSpace(raw.Stack) != event.Stack ||
		strings.TrimSpace(raw.SourceURL) != event.SourceURL ||
		sanitizePreviewConsoleClientTime(raw.ClientTime) != event.ClientTime
}

func sanitizePreviewConsoleSourceURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return projectEinoAssistantSafeText(parsed.String())
}

func sanitizePreviewConsoleClientTime(raw string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

func previewConsoleSummary(events []previewConsoleEvent) string {
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Level]++
	}
	parts := make([]string, 0, 3)
	for _, level := range []string{"error", "pageerror", "unhandledrejection", "warn", "info", "log", "debug"} {
		if count := counts[level]; count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, level))
		}
	}
	if len(parts) == 0 {
		return "no matching browser console events"
	}
	return fmt.Sprintf("%d browser console event(s): %s", len(events), strings.Join(parts, ", "))
}

type previewConsoleCapabilityClaims struct {
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	JTI           string `json:"jti"`
	SessionID     string `json:"sid"`
	Version       int    `json:"v"`
	PreviewOrigin string `json:"po"`
	PortalOrigin  string `json:"ao"`
	Generation    string `json:"gen"`
	IssuedAt      int64  `json:"iat"`
	ExpiresAt     int64  `json:"exp"`
}

type previewConsoleCapabilitySigner struct {
	key   *ecdsa.PrivateKey
	keyID string
	now   func() time.Time
}

func newPreviewConsoleCapabilitySigner(privateKeyPEM, keyID string) (*previewConsoleCapabilitySigner, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(privateKeyPEM)))
	if block == nil {
		return nil, errors.New("preview console signing key must be PEM encoded")
	}
	var key *ecdsa.PrivateKey
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		var ok bool
		key, ok = parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("preview console signing key must be an EC private key")
		}
	} else {
		key, err = x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse preview console signing key: %w", err)
		}
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("preview console signing key must use P-256")
	}
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return nil, errors.New("preview console signing key ID is required")
	}
	return &previewConsoleCapabilitySigner{key: key, keyID: keyID, now: time.Now}, nil
}

func newEphemeralPreviewConsoleCapabilitySigner() (*previewConsoleCapabilitySigner, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate preview console capability key: %w", err)
	}
	return &previewConsoleCapabilitySigner{key: key, keyID: "test-key", now: time.Now}, nil
}

func (s *previewConsoleCapabilitySigner) sign(claims previewConsoleCapabilityClaims) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": s.keyID})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	r, ss, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	ss.FillBytes(signature[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *previewConsoleCapabilitySigner) verify(token string) (previewConsoleCapabilityClaims, error) {
	if s == nil || s.key == nil {
		return previewConsoleCapabilityClaims{}, errPreviewConsoleCapability
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return previewConsoleCapabilityClaims{}, errPreviewConsoleCapability
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return previewConsoleCapabilityClaims{}, errPreviewConsoleCapability
	}
	var header map[string]string
	if json.Unmarshal(headerRaw, &header) != nil || header["alg"] != "ES256" || header["typ"] != "JWT" || header["kid"] != s.keyID {
		return previewConsoleCapabilityClaims{}, errPreviewConsoleCapability
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		return previewConsoleCapabilityClaims{}, errPreviewConsoleCapability
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(signature[:32])
	ss := new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(&s.key.PublicKey, digest[:], r, ss) {
		return previewConsoleCapabilityClaims{}, errPreviewConsoleCapability
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return previewConsoleCapabilityClaims{}, errPreviewConsoleCapability
	}
	var claims previewConsoleCapabilityClaims
	if json.Unmarshal(payload, &claims) != nil ||
		claims.Issuer != "app-studio" ||
		claims.Audience != "preview-console-events" ||
		claims.JTI == "" ||
		claims.SessionID == "" ||
		claims.Version != previewConsoleProtocolVersion ||
		claims.ExpiresAt <= s.now().Unix() ||
		claims.IssuedAt > s.now().Add(time.Minute).Unix() {
		return previewConsoleCapabilityClaims{}, errPreviewConsoleCapability
	}
	return claims, nil
}

var _ crypto.Signer = (*ecdsa.PrivateKey)(nil)

func previewConsoleRandomID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate preview console identifier: %w", err)
	}
	return id.String(), nil
}

func (s *Server) ConfigurePreviewConsole(enabled bool, privateKeyPEM, keyID string) error {
	if !enabled {
		s.mu.Lock()
		s.previewConsoleEnabled = false
		s.previewConsoleStore = nil
		s.previewConsoleSigner = nil
		s.mu.Unlock()
		return nil
	}
	signer, err := newPreviewConsoleCapabilitySigner(privateKeyPEM, keyID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.previewConsoleEnabled = true
	s.previewConsoleStore = newPreviewConsoleStore()
	s.previewConsoleSigner = signer
	s.mu.Unlock()
	return nil
}

func (s *Server) previewConsoleDependencies() (*previewConsoleStore, *previewConsoleCapabilitySigner, bool) {
	if s == nil {
		return nil, nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.previewConsoleStore, s.previewConsoleSigner, s.previewConsoleEnabled && s.previewConsoleStore != nil && s.previewConsoleSigner != nil
}

func (s *Server) requirePreviewConsoleEnabled(w http.ResponseWriter) (*previewConsoleStore, *previewConsoleCapabilitySigner, bool) {
	store, signer, enabled := s.previewConsoleDependencies()
	if !enabled {
		writeStatus(w, http.StatusNotFound, "NotFound", "preview console sharing is not enabled")
		return nil, nil, false
	}
	return store, signer, true
}

type previewConsoleSessionCreateRequest struct {
	Generation      string `json:"generation"`
	ProtocolVersion int    `json:"protocolVersion"`
}

type previewConsoleSessionCreateResponse struct {
	Status        string `json:"status"`
	SessionID     string `json:"sessionID"`
	Generation    string `json:"generation"`
	Capability    string `json:"capability"`
	PreviewOrigin string `json:"previewOrigin"`
	PortalOrigin  string `json:"portalOrigin"`
	ExpiresAt     string `json:"expiresAt"`
}

func previewConsoleProjectSupported(project *aiv1alpha1.Project) bool {
	if project == nil || project.Spec.Template == nil {
		return false
	}
	switch strings.TrimSpace(project.Spec.Template.Name) {
	case "simple-webapp", "application":
		return true
	default:
		return false
	}
}

func (s *Server) createProjectPreviewConsoleSession(w http.ResponseWriter, r *http.Request) {
	buffer, signer, ok := s.requirePreviewConsoleEnabled(w)
	if !ok {
		return
	}
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if !previewConsoleProjectSupported(project) {
		writeJSON(w, http.StatusOK, previewConsoleSessionCreateResponse{Status: "unsupported"})
		return
	}
	var request previewConsoleSessionCreateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if _, err := uuid.Parse(request.Generation); err != nil || request.ProtocolVersion != previewConsoleProtocolVersion {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "generation must be a UUID and protocolVersion must be 1")
		return
	}
	portalOrigin, err := normalizePreviewConsoleOrigin(r.Header.Get("Origin"))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "request Origin is required")
		return
	}
	preview, hasBinding := s.resolveProjectSandboxRuntime(r.Context(), c, id, project)
	if !hasBinding || !preview.Ready || strings.TrimSpace(preview.PreviewURL) == "" {
		message := strings.TrimSpace(preview.Message)
		if message == "" {
			message = "development preview is not ready"
		}
		writeStatus(w, http.StatusConflict, "Conflict", message)
		return
	}
	_, currentOrigin, err := normalizePreviewConsoleURL(preview.PreviewURL)
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", "development preview returned an invalid URL")
		return
	}
	scope, err := projectPreviewConsoleScope(id, project)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(previewConsoleSessionTTL)
	session, err := buffer.create(scope, currentOrigin, portalOrigin, request.Generation, request.ProtocolVersion, expiresAt)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	capability, err := signer.sign(previewConsoleCapabilityClaims{
		Issuer:        "app-studio",
		Audience:      "preview-console-events",
		JTI:           session.ID,
		SessionID:     session.ID,
		Version:       request.ProtocolVersion,
		PreviewOrigin: currentOrigin,
		PortalOrigin:  portalOrigin,
		Generation:    request.Generation,
		IssuedAt:      now.Unix(),
		ExpiresAt:     expiresAt.Unix(),
	})
	if err != nil {
		buffer.delete(session.ID, scope)
		writeStatus(w, http.StatusInternalServerError, "InternalError", "issue preview console capability")
		return
	}
	writeJSON(w, http.StatusCreated, previewConsoleSessionCreateResponse{
		Status:        "available",
		SessionID:     session.ID,
		Generation:    request.Generation,
		Capability:    capability,
		PreviewOrigin: currentOrigin,
		PortalOrigin:  portalOrigin,
		ExpiresAt:     expiresAt.Format(time.RFC3339),
	})
}

type previewConsoleAppendRequest struct {
	Generation      string                        `json:"generation"`
	ProtocolVersion int                           `json:"protocolVersion"`
	DroppedCount    int                           `json:"droppedCount,omitempty"`
	Events          []previewConsoleIncomingEvent `json:"events"`
}

type previewConsoleAppendResponse struct {
	Accepted      int    `json:"accepted"`
	NextSequence  uint64 `json:"nextSequence"`
	DroppedCount  int    `json:"droppedCount"`
	RedactedCount int    `json:"redactedCount"`
}

func (s *Server) appendProjectPreviewConsoleEvents(w http.ResponseWriter, r *http.Request) {
	buffer, _, ok := s.requirePreviewConsoleEnabled(w)
	if !ok {
		return
	}
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	scope, err := projectPreviewConsoleScope(id, project)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, previewConsoleMaxBatchBytes)
	var request previewConsoleAppendRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if len(request.Events) > previewConsoleMaxBatchEvents || len(request.Events) == 0 && request.DroppedCount == 0 {
		writeStatus(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("events must contain at most %d entries and an empty batch must report droppedCount", previewConsoleMaxBatchEvents))
		return
	}
	if request.DroppedCount < 0 || request.DroppedCount > 1_000_000 {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "droppedCount must be between 0 and 1000000")
		return
	}
	if _, err := uuid.Parse(request.Generation); err != nil || request.ProtocolVersion != previewConsoleProtocolVersion {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "generation must be a UUID and protocolVersion must be 1")
		return
	}
	accepted, next, dropped, err := buffer.append(mux.Vars(r)["session"], scope, request.Generation, request.ProtocolVersion, request.Events, request.DroppedCount)
	if err != nil {
		if strings.Contains(err.Error(), "scope mismatch") {
			writeStatus(w, http.StatusForbidden, "Forbidden", err.Error())
			return
		}
		writeStatus(w, http.StatusGone, "Gone", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, previewConsoleAppendResponse{
		Accepted:      accepted,
		NextSequence:  next,
		DroppedCount:  dropped,
		RedactedCount: buffer.redactedCount(mux.Vars(r)["session"], scope),
	})
}

func (s *Server) deleteProjectPreviewConsoleSession(w http.ResponseWriter, r *http.Request) {
	buffer, _, ok := s.requirePreviewConsoleEnabled(w)
	if !ok {
		return
	}
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	scope, err := projectPreviewConsoleScope(id, project)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	if !buffer.delete(mux.Vars(r)["session"], scope) {
		writeStatus(w, http.StatusNotFound, "NotFound", "preview console session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func normalizePreviewConsoleURL(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", "", errors.New("previewURL must be an absolute http or https URL without credentials")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	normalized := strings.TrimRight(parsed.String(), "/")
	return normalized, parsed.Scheme + "://" + parsed.Host, nil
}

func normalizePreviewConsoleOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("origin must be an absolute http or https origin")
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), nil
}

type previewConsoleToolResult struct {
	Status        string                `json:"status"`
	Trust         string                `json:"trust"`
	DocumentID    string                `json:"documentID,omitempty"`
	Path          string                `json:"path,omitempty"`
	Events        []previewConsoleEvent `json:"events,omitempty"`
	NextSequence  uint64                `json:"nextSequence,omitempty"`
	DroppedCount  int                   `json:"droppedCount,omitempty"`
	ReceivedCount int                   `json:"receivedCount,omitempty"`
	RedactedCount int                   `json:"redactedCount,omitempty"`
	Summary       string                `json:"summary,omitempty"`
}

func (s *Server) getProjectPreviewConsoleLogs(req projectAssistantToolCallRequest) (previewConsoleToolResult, error) {
	buffer, _, enabled := s.previewConsoleDependencies()
	if !enabled {
		return previewConsoleToolResult{Status: "unsupported", Trust: previewConsoleToolTrust}, nil
	}
	if !previewConsoleProjectSupported(req.Project) {
		return previewConsoleToolResult{Status: "unsupported", Trust: previewConsoleToolTrust}, nil
	}
	scope, err := projectPreviewConsoleScope(req.Identity, req.Project)
	if err != nil {
		return previewConsoleToolResult{}, err
	}
	limit := previewConsoleDefaultToolLimit
	if value, ok := projectToolNumber(req.Arguments["limit"]); ok {
		limit = int(value)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > previewConsoleMaxToolEvents {
		limit = previewConsoleMaxToolEvents
	}
	since := uint64(0)
	if value, ok := projectToolNumber(req.Arguments["sinceSequence"]); ok && value > 0 {
		since = uint64(value)
	}
	levels := map[string]struct{}{}
	for _, level := range projectToolStringList(req.Arguments["levels"]) {
		level = strings.ToLower(strings.TrimSpace(level))
		switch level {
		case "debug", "info", "log", "warn", "error", "pageerror", "unhandledrejection":
			levels[level] = struct{}{}
		}
	}
	return buffer.read(scope, levels, limit, since), nil
}

func (s *Server) previewConsoleMetrics(w http.ResponseWriter, _ *http.Request) {
	buffer, _, _ := s.previewConsoleDependencies()
	metrics := buffer.metrics()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintf(w,
		"# TYPE app_studio_preview_console_events_total counter\n"+
			"app_studio_preview_console_events_total{outcome=\"received\"} %d\n"+
			"app_studio_preview_console_events_total{outcome=\"dropped\"} %d\n"+
			"app_studio_preview_console_events_total{outcome=\"redacted\"} %d\n",
		metrics.Received,
		metrics.Dropped,
		metrics.Redacted,
	)
}
