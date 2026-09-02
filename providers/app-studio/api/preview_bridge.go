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

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

const (
	previewBridgeSessionTTL      = 15 * time.Minute
	previewBridgeMaxSessions     = 128
	previewBridgeProtocolVersion = 1
)

var errPreviewBridgeCapability = errors.New("invalid preview bridge capability")

type previewBridgeScope struct {
	ClusterID     string
	OrgUUID       string
	WorkspaceUUID string
	ProjectUID    types.UID
	ProjectName   string
	Actor         string
}

func projectPreviewBridgeScope(id identity, project *aiv1alpha1.Project) (previewBridgeScope, error) {
	if project == nil {
		return previewBridgeScope{}, errors.New("project is required")
	}
	if project.UID == "" {
		return previewBridgeScope{}, errors.New("project UID is required")
	}
	if strings.TrimSpace(id.user) == "" {
		return previewBridgeScope{}, errors.New("user identity is required")
	}
	return previewBridgeScope{
		ClusterID:     id.clusterID,
		OrgUUID:       id.orgUUID,
		WorkspaceUUID: id.workspaceUUID,
		ProjectUID:    project.UID,
		ProjectName:   project.Name,
		Actor:         id.user,
	}, nil
}

type previewBridgeSession struct {
	ID               string
	Nonce            string
	PortalInstanceID string
	Scope            previewBridgeScope
	PreviewOrigin    string
	PortalOrigin     string
	Generation       string
	Protocol         int
	ExpiresAt        time.Time
	UpdatedAt        time.Time
	ActivityOrder    uint64
}

type previewBridgeStore struct {
	mu           sync.Mutex
	now          func() time.Time
	sessions     map[string]*previewBridgeSession
	nextActivity uint64
}

func newPreviewBridgeStore() *previewBridgeStore {
	return &previewBridgeStore{
		now:      time.Now,
		sessions: map[string]*previewBridgeSession{},
	}
}

func (s *previewBridgeStore) create(scope previewBridgeScope, portalInstanceID, previewOrigin, portalOrigin, generation string, protocol int, expiresAt time.Time) (*previewBridgeSession, error) {
	if s == nil {
		return nil, errors.New("preview bridge store is not configured")
	}
	sessionID, err := previewBridgeRandomID()
	if err != nil {
		return nil, err
	}
	nonce, err := previewBridgeRandomID()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.cleanupLocked(now)
	// A portal tab owns one current session. Renewal replaces only that tab's
	// previous capability; sibling tabs for the same actor/project remain live.
	for _, previous := range s.sessions {
		if previous.Scope == scope && previous.PortalInstanceID == portalInstanceID {
			s.deleteLocked(previous)
		}
	}
	for len(s.sessions) >= previewBridgeMaxSessions {
		s.evictOldestLocked()
	}
	session := &previewBridgeSession{
		ID:               sessionID,
		Nonce:            nonce,
		PortalInstanceID: portalInstanceID,
		Scope:            scope,
		PreviewOrigin:    previewOrigin,
		PortalOrigin:     portalOrigin,
		Generation:       generation,
		Protocol:         protocol,
		ExpiresAt:        expiresAt.UTC(),
		UpdatedAt:        now,
	}
	s.nextActivity++
	session.ActivityOrder = s.nextActivity
	s.sessions[sessionID] = session
	return clonePreviewBridgeSession(session), nil
}

func (s *previewBridgeStore) delete(sessionID string, scope previewBridgeScope) bool {
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

func (s *previewBridgeStore) cleanupLocked(now time.Time) {
	for _, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			s.deleteLocked(session)
		}
	}
}

func (s *previewBridgeStore) evictOldestLocked() {
	var oldest *previewBridgeSession
	for _, session := range s.sessions {
		if oldest == nil || session.UpdatedAt.Before(oldest.UpdatedAt) {
			oldest = session
		}
	}
	if oldest != nil {
		s.deleteLocked(oldest)
	}
}

func (s *previewBridgeStore) deleteLocked(session *previewBridgeSession) {
	delete(s.sessions, session.ID)
}

func clonePreviewBridgeSession(session *previewBridgeSession) *previewBridgeSession {
	if session == nil {
		return nil
	}
	out := *session
	return &out
}

type previewBridgeCapabilityClaims struct {
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

type previewBridgeCapabilitySigner struct {
	key   *ecdsa.PrivateKey
	keyID string
	now   func() time.Time
}

func newPreviewBridgeCapabilitySigner(privateKeyPEM, keyID string) (*previewBridgeCapabilitySigner, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(privateKeyPEM)))
	if block == nil {
		return nil, errors.New("preview bridge signing key must be PEM encoded")
	}
	var key *ecdsa.PrivateKey
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		var ok bool
		key, ok = parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("preview bridge signing key must be an EC private key")
		}
	} else {
		key, err = x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse preview bridge signing key: %w", err)
		}
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("preview bridge signing key must use P-256")
	}
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return nil, errors.New("preview bridge signing key ID is required")
	}
	return &previewBridgeCapabilitySigner{key: key, keyID: keyID, now: time.Now}, nil
}

func newEphemeralPreviewBridgeCapabilitySigner() (*previewBridgeCapabilitySigner, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate preview bridge capability key: %w", err)
	}
	return &previewBridgeCapabilitySigner{key: key, keyID: "test-key", now: time.Now}, nil
}

func (s *previewBridgeCapabilitySigner) sign(claims previewBridgeCapabilityClaims) (string, error) {
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

func (s *previewBridgeCapabilitySigner) verify(token string) (previewBridgeCapabilityClaims, error) {
	if s == nil || s.key == nil {
		return previewBridgeCapabilityClaims{}, errPreviewBridgeCapability
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return previewBridgeCapabilityClaims{}, errPreviewBridgeCapability
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return previewBridgeCapabilityClaims{}, errPreviewBridgeCapability
	}
	var header map[string]string
	if json.Unmarshal(headerRaw, &header) != nil || header["alg"] != "ES256" || header["typ"] != "JWT" || header["kid"] != s.keyID {
		return previewBridgeCapabilityClaims{}, errPreviewBridgeCapability
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		return previewBridgeCapabilityClaims{}, errPreviewBridgeCapability
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(signature[:32])
	ss := new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(&s.key.PublicKey, digest[:], r, ss) {
		return previewBridgeCapabilityClaims{}, errPreviewBridgeCapability
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return previewBridgeCapabilityClaims{}, errPreviewBridgeCapability
	}
	var claims previewBridgeCapabilityClaims
	if json.Unmarshal(payload, &claims) != nil ||
		claims.Issuer != "app-studio" ||
		claims.Audience != "preview-bridge" ||
		claims.JTI == "" ||
		claims.SessionID == "" ||
		claims.Version != previewBridgeProtocolVersion ||
		claims.ExpiresAt <= s.now().Unix() ||
		claims.IssuedAt > s.now().Add(time.Minute).Unix() {
		return previewBridgeCapabilityClaims{}, errPreviewBridgeCapability
	}
	return claims, nil
}

func previewBridgeRandomID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate preview bridge identifier: %w", err)
	}
	return id.String(), nil
}

func (s *Server) ConfigurePreviewBridge(enabled bool, privateKeyPEM, keyID string) error {
	if !enabled {
		s.mu.Lock()
		s.previewBridgeEnabled = false
		s.previewBridgeStore = nil
		s.previewBridgeSigner = nil
		s.mu.Unlock()
		return nil
	}
	signer, err := newPreviewBridgeCapabilitySigner(privateKeyPEM, keyID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.previewBridgeEnabled = true
	s.previewBridgeStore = newPreviewBridgeStore()
	s.previewBridgeSigner = signer
	s.mu.Unlock()
	return nil
}

func (s *Server) previewBridgeDependencies() (*previewBridgeStore, *previewBridgeCapabilitySigner, bool) {
	if s == nil {
		return nil, nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.previewBridgeStore, s.previewBridgeSigner, s.previewBridgeEnabled && s.previewBridgeStore != nil && s.previewBridgeSigner != nil
}

func (s *Server) requirePreviewBridgeEnabled(w http.ResponseWriter) (*previewBridgeStore, *previewBridgeCapabilitySigner, bool) {
	store, signer, enabled := s.previewBridgeDependencies()
	if !enabled {
		writeStatus(w, http.StatusNotFound, "NotFound", "preview bridge sharing is not enabled")
		return nil, nil, false
	}
	return store, signer, true
}

type previewBridgeSessionCreateRequest struct {
	Generation       string `json:"generation"`
	ProtocolVersion  int    `json:"protocolVersion"`
	PortalInstanceID string `json:"portalInstanceID"`
}

type previewBridgeSessionCreateResponse struct {
	Status        string `json:"status"`
	SessionID     string `json:"sessionID"`
	Generation    string `json:"generation"`
	Capability    string `json:"capability"`
	PreviewOrigin string `json:"previewOrigin"`
	PortalOrigin  string `json:"portalOrigin"`
	ExpiresAt     string `json:"expiresAt"`
}

func previewBridgeProjectSupported(project *aiv1alpha1.Project) bool {
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

func (s *Server) createProjectPreviewBridgeSession(w http.ResponseWriter, r *http.Request) {
	buffer, signer, ok := s.requirePreviewBridgeEnabled(w)
	if !ok {
		return
	}
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if !previewBridgeProjectSupported(project) {
		writeJSON(w, http.StatusOK, previewBridgeSessionCreateResponse{Status: "unsupported"})
		return
	}
	var request previewBridgeSessionCreateRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if _, err := uuid.Parse(request.Generation); err != nil || request.ProtocolVersion != previewBridgeProtocolVersion {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "generation must be a UUID and protocolVersion must be 1")
		return
	}
	if _, err := uuid.Parse(request.PortalInstanceID); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "portalInstanceID must be a UUID")
		return
	}
	portalOrigin, err := normalizePreviewBridgeOrigin(r.Header.Get("Origin"))
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
	_, currentOrigin, err := normalizePreviewBridgeURL(preview.PreviewURL)
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", "development preview returned an invalid URL")
		return
	}
	scope, err := projectPreviewBridgeScope(id, project)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(previewBridgeSessionTTL)
	session, err := buffer.create(scope, request.PortalInstanceID, currentOrigin, portalOrigin, request.Generation, request.ProtocolVersion, expiresAt)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	capability, err := signer.sign(previewBridgeCapabilityClaims{
		Issuer:        "app-studio",
		Audience:      "preview-bridge",
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
		writeStatus(w, http.StatusInternalServerError, "InternalError", "issue preview bridge capability")
		return
	}
	writeJSON(w, http.StatusCreated, previewBridgeSessionCreateResponse{
		Status:        "available",
		SessionID:     session.ID,
		Generation:    request.Generation,
		Capability:    capability,
		PreviewOrigin: currentOrigin,
		PortalOrigin:  portalOrigin,
		ExpiresAt:     expiresAt.Format(time.RFC3339),
	})
}

func (s *Server) deleteProjectPreviewBridgeSession(w http.ResponseWriter, r *http.Request) {
	buffer, _, ok := s.requirePreviewBridgeEnabled(w)
	if !ok {
		return
	}
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	scope, err := projectPreviewBridgeScope(id, project)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
	if !buffer.delete(mux.Vars(r)["session"], scope) {
		writeStatus(w, http.StatusNotFound, "NotFound", "preview bridge session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func normalizePreviewBridgeURL(raw string) (string, string, error) {
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

func normalizePreviewBridgeOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("origin must be an absolute http or https origin")
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), nil
}
