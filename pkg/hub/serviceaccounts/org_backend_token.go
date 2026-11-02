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

package serviceaccounts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	orgBackendTokenPrefix    = "rgbyo1."
	orgBackendTokenDomain    = "railgrid.org-backend-identity.v1\x00"
	orgBackendTokenTTL       = 10 * time.Minute
	orgBackendTokenMaxBytes  = 16 * 1024
	orgBackendPayloadMaxSize = 8 * 1024
	orgBackendClockSkew      = 15 * time.Second
)

var errInvalidOrgBackendToken = errors.New("invalid organization provider identity token")

// OrgBackendClaims is the hub-authenticated request identity for an
// organization-owned provider. It deliberately contains no Kubernetes bearer
// or native KCP authority; the hub checks the coordinates against live state
// before allowing a mediated operation.
type OrgBackendClaims struct {
	User            string `json:"user"`
	UserUID         string `json:"userUID"`
	OrgUUID         string `json:"orgUUID"`
	WorkspaceUUID   string `json:"workspaceUUID"`
	ClusterID       string `json:"clusterID"`
	Provider        string `json:"provider"`
	ProviderCluster string `json:"providerCluster"`
	ExportPath      string `json:"exportPath"`
	ExportName      string `json:"exportName"`
	BindingUID      string `json:"bindingUID"`
	IssuedAt        int64  `json:"iat"`
	ExpiresAt       int64  `json:"exp"`
}

// MintOrgBackendToken signs a short-lived, opaque-to-KCP identity credential.
// The returned expiry is the exact second encoded in the token.
func MintOrgBackendToken(ctx context.Context, keys ProofKeySource, claims OrgBackendClaims, now time.Time) (string, time.Time, error) {
	if now.IsZero() {
		now = time.Now()
	}
	key, err := delegatedProofKey(ctx, keys)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("organization provider identity cannot be minted: %w", err)
	}
	claims.IssuedAt = now.Unix()
	claims.ExpiresAt = claims.IssuedAt + int64(orgBackendTokenTTL/time.Second)
	if err := validateOrgBackendClaims(claims, now); err != nil {
		return "", time.Time{}, err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encoding organization provider identity: %w", err)
	}
	if len(payload) > orgBackendPayloadMaxSize {
		return "", time.Time{}, errInvalidOrgBackendToken
	}
	mac := orgBackendMAC(key, payload)
	token := orgBackendTokenPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac)
	if len(token) > orgBackendTokenMaxBytes {
		return "", time.Time{}, errInvalidOrgBackendToken
	}
	return token, time.Unix(claims.ExpiresAt, 0), nil
}

// VerifyOrgBackendToken verifies the signature and bounded lifetime. It does
// not treat a signed snapshot as current authority: callers must verify its
// tenant, provider, export, binding and user coordinates against live state.
func VerifyOrgBackendToken(ctx context.Context, keys ProofKeySource, token string, now time.Time) (OrgBackendClaims, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if !IsOrgBackendToken(token) || len(token) > orgBackendTokenMaxBytes {
		return OrgBackendClaims{}, errInvalidOrgBackendToken
	}
	key, err := delegatedProofKey(ctx, keys)
	if err != nil {
		return OrgBackendClaims{}, fmt.Errorf("organization provider identity cannot be verified: %w", err)
	}
	parts := strings.Split(strings.TrimPrefix(token, orgBackendTokenPrefix), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return OrgBackendClaims{}, errInvalidOrgBackendToken
	}
	payload, err := decodeCanonicalBase64(parts[0])
	if err != nil || len(payload) == 0 || len(payload) > orgBackendPayloadMaxSize {
		return OrgBackendClaims{}, errInvalidOrgBackendToken
	}
	presentedMAC, err := decodeCanonicalBase64(parts[1])
	if err != nil || len(presentedMAC) != sha256.Size || !hmac.Equal(presentedMAC, orgBackendMAC(key, payload)) {
		return OrgBackendClaims{}, errInvalidOrgBackendToken
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims OrgBackendClaims
	if err := decoder.Decode(&claims); err != nil {
		return OrgBackendClaims{}, errInvalidOrgBackendToken
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return OrgBackendClaims{}, errInvalidOrgBackendToken
	}
	// Re-encoding rejects duplicate keys, alternate whitespace/number forms,
	// and other noncanonical encodings that encoding/json otherwise accepts.
	canonical, err := json.Marshal(claims)
	if err != nil || !bytes.Equal(canonical, payload) {
		return OrgBackendClaims{}, errInvalidOrgBackendToken
	}
	if err := validateOrgBackendClaims(claims, now); err != nil {
		return OrgBackendClaims{}, errInvalidOrgBackendToken
	}
	return claims, nil
}

// IsOrgBackendToken identifies the reserved token class without trusting it.
func IsOrgBackendToken(token string) bool { return strings.HasPrefix(token, orgBackendTokenPrefix) }

func validateOrgBackendClaims(c OrgBackendClaims, now time.Time) error {
	for field, value := range map[string]struct {
		value string
		max   int
	}{
		"user":             {c.User, 253},
		"user UID":         {c.UserUID, 128},
		"organization":     {c.OrgUUID, 63},
		"workspace":        {c.WorkspaceUUID, 63},
		"cluster ID":       {c.ClusterID, 128},
		"provider":         {c.Provider, 63},
		"provider cluster": {c.ProviderCluster, 128},
		"export path":      {c.ExportPath, 512},
		"export name":      {c.ExportName, 253},
		"binding UID":      {c.BindingUID, 128},
	} {
		if err := validateOrgBackendField(field, value.value, value.max); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"organization": c.OrgUUID, "workspace": c.WorkspaceUUID,
		"cluster ID": c.ClusterID, "provider cluster": c.ProviderCluster,
	} {
		if problems := validation.IsDNS1123Label(value); len(problems) != 0 {
			return fmt.Errorf("organization provider identity has invalid %s", field)
		}
	}
	for field, value := range map[string]string{"provider": c.Provider, "export name": c.ExportName} {
		if problems := validation.IsDNS1123Subdomain(value); len(problems) != 0 {
			return fmt.Errorf("organization provider identity has invalid %s", field)
		}
	}
	for _, segment := range strings.Split(c.ExportPath, ":") {
		if problems := validation.IsDNS1123Label(segment); len(problems) != 0 {
			return fmt.Errorf("organization provider identity has invalid export path")
		}
	}
	for field, value := range map[string]string{"user UID": c.UserUID, "binding UID": c.BindingUID} {
		for _, char := range value {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return fmt.Errorf("organization provider identity has invalid %s", field)
			}
		}
	}
	if strings.ContainsAny(c.User, "/\\") {
		return fmt.Errorf("organization provider identity has invalid user")
	}
	if c.IssuedAt <= 0 || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt > int64(orgBackendTokenTTL/time.Second) {
		return errInvalidOrgBackendToken
	}
	issuedAt := time.Unix(c.IssuedAt, 0)
	expiresAt := time.Unix(c.ExpiresAt, 0)
	if issuedAt.After(now.Add(orgBackendClockSkew)) || !expiresAt.After(now) {
		return errInvalidOrgBackendToken
	}
	return nil
}

func validateOrgBackendField(name, value string, max int) error {
	if value == "" || len(value) > max || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("organization provider identity has invalid %s", name)
	}
	return nil
}

func decodeCanonicalBase64(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errInvalidOrgBackendToken
	}
	return decoded, nil
}

func orgBackendMAC(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(orgBackendTokenDomain))
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
