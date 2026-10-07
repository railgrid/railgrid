/*
Copyright 2026 The Railgrid Authors.

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

// Package actionproof signs the human and tenant context of a kcp custom
// subresource request so a provider can make a narrowly authorized hub REST
// call without forwarding the caller's bearer token.
package actionproof

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

const (
	// Header is the ordinary transport header used to carry an action proof.
	// kcp strips client-supplied X-Remote-* identity headers, so the proof is
	// deliberately kept outside the requestheader namespace.
	Header = dataplane.HeaderActionProof

	proofVersion  = 1
	defaultTTL    = 2 * time.Minute
	maxClockSkew  = 15 * time.Second
	maxTokenBytes = 16 * 1024
	proofKeyBytes = 32
)

var (
	proofDomain = []byte("railgrid-hub-action-proof-v1\x00")
	errInvalid  = errors.New("invalid hub action proof")
)

// Claims is the hub-authenticated identity and original action coordinate.
// The hub signs these claims only after authenticating the human and
// authorizing the addressed tenant workspace. The provider may forward the
// opaque token, but cannot create or alter these values.
type Claims struct {
	UserID          string              `json:"userID"`
	RBACIdentity    string              `json:"rbacIdentity"`
	Groups          []string            `json:"groups,omitempty"`
	Extra           map[string][]string `json:"extra,omitempty"`
	OrgUUID         string              `json:"orgUUID"`
	WorkspaceUUID   string              `json:"workspaceUUID"`
	ClusterID       string              `json:"clusterID"`
	Provider        string              `json:"provider"`
	ProviderOrgUUID string              `json:"providerOrgUUID,omitempty"`
	Group           string              `json:"group"`
	Version         string              `json:"version"`
	Resource        string              `json:"resource"`
	ParentName      string              `json:"parentName"`
	Verb            string              `json:"verb"`
	Method          string              `json:"method"`
	// InviteUser binds an App Studio invitation proof to the one email named
	// by the original publishing action. Empty for all other sharing intents.
	InviteUser string `json:"inviteUser,omitempty"`

	ProofVersion int    `json:"proofVersion"`
	IssuedAt     int64  `json:"iat"`
	ExpiresAt    int64  `json:"exp"`
	Nonce        string `json:"nonce"`
}

// Sign creates a short-lived proof using the hub's shared delegated-identity
// proof key. The signing domain is distinct from delegated ServiceAccount
// proofs, and this token is not a ServiceAccount credential.
func Sign(ctx context.Context, keys serviceaccounts.ProofKeySource, claims Claims, now time.Time) (string, error) {
	if now.IsZero() {
		now = time.Now()
	}
	key, err := proofKey(ctx, keys)
	if err != nil {
		return "", err
	}
	claims.ProofVersion = proofVersion
	claims.IssuedAt = now.Unix()
	claims.ExpiresAt = now.Add(defaultTTL).Unix()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate action proof nonce: %w", err)
	}
	claims.Nonce = hex.EncodeToString(nonce[:])
	if err := validateClaims(claims); err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode action proof: %w", err)
	}
	if len(payload) > maxTokenBytes {
		return "", fmt.Errorf("action proof claims exceed %d bytes", maxTokenBytes)
	}
	signature := mac(key, payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
	if len(token) > maxTokenBytes {
		return "", fmt.Errorf("action proof exceeds %d bytes", maxTokenBytes)
	}
	return token, nil
}

// Verifier validates the hub signature and then delegates provider
// authentication and live tenant-scope checks to the hub wiring. Both checks
// are required; a signature by itself never authorizes a request.
type Verifier struct {
	Keys          serviceaccounts.ProofKeySource
	Authenticate  func(context.Context, *http.Request, Claims) error
	ValidateScope func(context.Context, Claims) error
	Now           func() time.Time
}

// Verify reads and validates the one action proof on r. It refuses duplicate
// headers, unbounded tokens, expired proofs, tenant-header mismatches, missing
// live checks, and any failed provider or scope validation.
func (v *Verifier) Verify(ctx context.Context, r *http.Request) (Claims, error) {
	if v == nil || r == nil {
		return Claims{}, errInvalid
	}
	values := r.Header.Values(Header)
	if len(values) != 1 {
		return Claims{}, errInvalid
	}
	token := strings.TrimSpace(values[0])
	if token == "" || len(token) > maxTokenBytes {
		return Claims{}, errInvalid
	}
	key, err := proofKey(ctx, v.Keys)
	if err != nil {
		return Claims{}, fmt.Errorf("load action proof key: %w", err)
	}
	claims, err := verifyToken(token, key)
	if err != nil {
		return Claims{}, err
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	if err := validateTime(claims, now); err != nil {
		return Claims{}, err
	}
	if len(r.Header.Values("X-Railgrid-Org")) != 1 ||
		len(r.Header.Values("X-Railgrid-Workspace")) != 1 ||
		len(r.Header.Values(dataplane.HeaderTenant)) != 1 ||
		len(r.Header.Values(dataplane.HeaderCluster)) != 1 ||
		r.Header.Get("X-Railgrid-Org") != claims.OrgUUID ||
		r.Header.Get("X-Railgrid-Workspace") != claims.WorkspaceUUID ||
		r.Header.Get(dataplane.HeaderTenant) != claims.ClusterID ||
		r.Header.Get(dataplane.HeaderCluster) != claims.ClusterID {
		return Claims{}, errInvalid
	}
	if v.Authenticate == nil || v.ValidateScope == nil {
		return Claims{}, fmt.Errorf("action proof verification is not fully configured")
	}
	if err := v.Authenticate(ctx, r, claims); err != nil {
		return Claims{}, fmt.Errorf("authenticate action proof caller: %w", err)
	}
	if err := v.ValidateScope(ctx, claims); err != nil {
		return Claims{}, fmt.Errorf("validate action proof scope: %w", err)
	}
	return claims, nil
}

func verifyToken(token string, key []byte) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, errInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) == 0 || len(payload) > maxTokenBytes {
		return Claims{}, errInvalid
	}
	presented, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(presented) != sha256.Size {
		return Claims{}, errInvalid
	}
	if !hmac.Equal(presented, mac(key, payload)) {
		return Claims{}, errInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims Claims
	if err := decoder.Decode(&claims); err != nil {
		return Claims{}, errInvalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Claims{}, errInvalid
	}
	if err := validateClaims(claims); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

func validateClaims(claims Claims) error {
	if claims.ProofVersion != proofVersion || claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt || len(claims.Nonce) != 32 {
		return errInvalid
	}
	for _, value := range []string{
		claims.UserID, claims.RBACIdentity, claims.OrgUUID, claims.WorkspaceUUID,
		claims.ClusterID, claims.Provider, claims.Group, claims.Version,
		claims.Resource, claims.ParentName, claims.Verb, claims.Method,
	} {
		if !validField(value) {
			return errInvalid
		}
	}
	switch claims.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return errInvalid
	}
	if strings.HasPrefix(claims.UserID, "system:serviceaccount:") || !validOptionalField(claims.ProviderOrgUUID) {
		return errInvalid
	}
	if !validOptionalField(claims.InviteUser) || len(claims.InviteUser) > 320 {
		return errInvalid
	}
	if claims.InviteUser != "" {
		if !strings.Contains(claims.InviteUser, "@") || strings.ContainsAny(claims.InviteUser, " \t\v\f") ||
			claims.Method != http.MethodPost || claims.Provider != "app-studio" || claims.Group != "ai.railgrid.ai" ||
			claims.Version != "v1alpha1" || claims.Resource != "projects" ||
			(claims.Verb != "publishing-grants" && claims.Verb != "preview-grants") {
			return errInvalid
		}
	}
	if len(claims.Groups) > 128 || len(claims.Extra) > 128 {
		return errInvalid
	}
	for _, group := range claims.Groups {
		if !validField(group) || len(group) > 1024 {
			return errInvalid
		}
	}
	for key, values := range claims.Extra {
		if !validField(key) || len(key) > 256 || len(values) > 128 {
			return errInvalid
		}
		for _, value := range values {
			if !validField(value) || len(value) > 1024 {
				return errInvalid
			}
		}
	}
	return nil
}

func validateTime(claims Claims, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	nowUnix := now.Unix()
	if claims.IssuedAt > now.Add(maxClockSkew).Unix() || claims.ExpiresAt <= nowUnix ||
		claims.ExpiresAt-claims.IssuedAt > int64(defaultTTL/time.Second) {
		return errInvalid
	}
	return nil
}

func validField(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func validOptionalField(value string) bool {
	return value == "" || (value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00"))
}

func proofKey(ctx context.Context, source serviceaccounts.ProofKeySource) ([]byte, error) {
	if source == nil {
		return nil, fmt.Errorf("action proof key source is unavailable")
	}
	key, err := source.DelegatedProofKey(ctx)
	if err != nil {
		return nil, err
	}
	if len(key) < proofKeyBytes {
		return nil, fmt.Errorf("action proof key is too short")
	}
	return key, nil
}

func mac(key, payload []byte) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(proofDomain)
	_, _ = h.Write(payload)
	return h.Sum(nil)
}
