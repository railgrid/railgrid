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

package appauth

// App access tokens: the only credential a published app's access gate
// accepts in an Authorization header.
//
// A gate may be operated by a party the user does not trust with their hub
// credential — an org's self-hosted infrastructure provider runs its own
// Gateway and gate. So a gate must never see a raw hub token. Instead the
// caller exchanges their hub bearer AT THE HUB (POST /auth/apps/token) for a
// short-lived token bound to one instance; a leaked app token grants that one
// app, to someone the SubjectAccessReview already admits, for at most
// maxAppTokenTTL.
//
// The token is stateless so it verifies on any hub replica: an AES-256-GCM
// sealed claim set, keyed by a subkey HKDF-derived (distinct info label) from
// the hub's cross-replica secret in root:faros:system:controllers (see
// Config.TokenKey). Sealing rather than signing keeps the embedded RBAC
// identity (faros:<email>) unreadable to whoever holds or relays the token.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// AppTokenPrefix marks an app access token. The access proxy mirrors it
	// (providers/infrastructure/accessproxy) and refuses to relay anything
	// else, so a raw hub token presented at a gate never leaves that gate.
	AppTokenPrefix = "fapp_"

	// defaultAppTokenTTL / minAppTokenTTL / maxAppTokenTTL bound a minted
	// token's lifetime. The maximum equals the gate's verdict cache cap, so
	// no credential or cached verdict outlives 15 minutes.
	defaultAppTokenTTL = 10 * time.Minute
	minAppTokenTTL     = time.Minute
	maxAppTokenTTL     = 15 * time.Minute

	// appTokenKeyInfo is the HKDF info label. It domain-separates the app
	// token key from every other use of the hub secret it is derived from.
	appTokenKeyInfo = "faros.sh/app-access-token/aes-256-gcm/v1"
	// appTokenAAD binds the ciphertext to this construction and version.
	appTokenAAD     = "faros.sh/app-access-token/v1"
	appTokenVersion = 1
	minTokenKeyLen  = 32
	appTokenIDBytes = 16
)

// errAppTokenInvalid is every reason a presented token is not a usable app
// token. Callers log the wrapped detail and answer 401.
var errAppTokenInvalid = errors.New("invalid app access token")

// appTokenClaims is the sealed content of an app token. It carries identity
// metadata for the verify-time SubjectAccessReview — never a credential.
type appTokenClaims struct {
	Version      int    `json:"v"`
	ID           string `json:"jti"`
	Cluster      string `json:"c"`
	Group        string `json:"g"`
	Resource     string `json:"r"`
	Name         string `json:"n"`
	UserID       string `json:"u"`
	RBACIdentity string `json:"s"`
	IssuedAt     int64  `json:"iat"`
	ExpiresAt    int64  `json:"exp"`
}

func (c appTokenClaims) ref() InstanceRef {
	return InstanceRef{Cluster: c.Cluster, Group: c.Group, Resource: c.Resource, Name: c.Name}
}

func appTokenAEAD(secret []byte) (cipher.AEAD, error) {
	if len(secret) < minTokenKeyLen {
		return nil, fmt.Errorf("app token key is too short")
	}
	key, err := hkdf.Key(sha256.New, secret, nil, appTokenKeyInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealAppToken mints a token for claims. random supplies the nonce and ID.
func sealAppToken(secret []byte, random io.Reader, claims appTokenClaims) (string, error) {
	aead, err := appTokenAEAD(secret)
	if err != nil {
		return "", err
	}
	id := make([]byte, appTokenIDBytes)
	if _, err := io.ReadFull(random, id); err != nil {
		return "", err
	}
	claims.Version = appTokenVersion
	claims.ID = base64.RawURLEncoding.EncodeToString(id)
	plaintext, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plaintext, []byte(appTokenAAD))
	return AppTokenPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// openAppToken authenticates and decodes token. It does NOT check expiry or
// the instance binding; the caller does, against its own clock and request.
func openAppToken(secret []byte, token string) (appTokenClaims, error) {
	if !strings.HasPrefix(token, AppTokenPrefix) {
		return appTokenClaims{}, fmt.Errorf("%w: not an app access token", errAppTokenInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, AppTokenPrefix))
	if err != nil {
		return appTokenClaims{}, fmt.Errorf("%w: malformed encoding", errAppTokenInvalid)
	}
	aead, err := appTokenAEAD(secret)
	if err != nil {
		return appTokenClaims{}, err
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return appTokenClaims{}, fmt.Errorf("%w: truncated", errAppTokenInvalid)
	}
	nonce, sealed := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, sealed, []byte(appTokenAAD))
	if err != nil {
		return appTokenClaims{}, fmt.Errorf("%w: not issued by this hub", errAppTokenInvalid)
	}
	var claims appTokenClaims
	if err := json.Unmarshal(plaintext, &claims); err != nil {
		return appTokenClaims{}, fmt.Errorf("%w: unreadable claims", errAppTokenInvalid)
	}
	if claims.Version != appTokenVersion || claims.ref().validate() != nil ||
		strings.TrimSpace(claims.UserID) == "" || strings.TrimSpace(claims.RBACIdentity) == "" || claims.ExpiresAt == 0 {
		return appTokenClaims{}, fmt.Errorf("%w: incomplete claims", errAppTokenInvalid)
	}
	return claims, nil
}
