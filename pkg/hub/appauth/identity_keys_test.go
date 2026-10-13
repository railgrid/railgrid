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

package appauth

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railgrid/railgrid/pkg/browsersession"
)

func TestIdentityJWKSAndSigningUseOneKeySnapshot(t *testing.T) {
	f := newIdentityFixture(t)
	oldPublic := f.privateKey.Public().(ed25519.PublicKey)
	newKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	snapshot := IdentityKeySnapshot{
		SigningKey: newKey, SubjectKey: make([]byte, 32),
		PublicKeys: []ed25519.PublicKey{newKey.Public().(ed25519.PublicKey), oldPublic},
	}
	loads := 0
	f.handler.identityKeys = func(context.Context) (IdentityKeySnapshot, error) { loads++; return snapshot, nil }
	f.handler.identitySigningKey = func(context.Context) (ed25519.PrivateKey, error) {
		t.Fatal("snapshot flow read signing key separately")
		return nil, nil
	}
	f.handler.identityPublicKeys = func(context.Context) ([]ed25519.PublicKey, error) {
		t.Fatal("snapshot flow read public keys separately")
		return nil, nil
	}
	f.handler.identitySubjectKeySource = func(context.Context) ([]byte, error) {
		t.Fatal("snapshot flow read subject key separately")
		return nil, nil
	}
	response := httptest.NewRecorder()
	f.handler.HandleIdentityJWKS(response, httptest.NewRequest(http.MethodGet, IdentityJWKSPath, nil))
	if response.Code != http.StatusOK || loads != 1 {
		t.Fatalf("JWKS status=%d snapshot reads=%d: %s", response.Code, loads, response.Body.String())
	}
	var jwks identityJWKS
	if err := json.Unmarshal(response.Body.Bytes(), &jwks); err != nil || len(jwks.Keys) != 2 || jwks.Keys[0].KeyID != identityKeyID(snapshot.PublicKeys[0]) {
		t.Fatalf("JWKS did not use coherent rotated snapshot: %s / %v", response.Body.String(), err)
	}
	assertion, err := f.handler.signIdentityAssertion(context.Background(), browsersession.Identity{UserID: "user-abc", Name: "A B"}, f.app)
	if err != nil || loads != 2 {
		t.Fatalf("sign error=%v snapshot reads=%d", err, loads)
	}
	_, claims := decodeIdentityJWT(t, assertion, snapshot.PublicKeys[0])
	if claims["sub"] != identitySubject(snapshot.SubjectKey, "user-abc", f.app.AppID) {
		t.Fatal("assertion used a subject key from another snapshot")
	}
}

func TestIdentityKeySnapshotFailsClosed(t *testing.T) {
	for _, problem := range []string{"unavailable", "wrong-current-public", "short-subject", "short-signing", "short-public", "empty-public"} {
		t.Run(problem, func(t *testing.T) {
			f := newIdentityFixture(t)
			keys := IdentityKeySnapshot{SigningKey: f.privateKey, SubjectKey: make([]byte, 32), PublicKeys: []ed25519.PublicKey{f.privateKey.Public().(ed25519.PublicKey)}}
			var loadErr error
			switch problem {
			case "unavailable":
				loadErr = errors.New("kcp unavailable")
			case "wrong-current-public":
				keys.PublicKeys[0] = ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
			case "short-subject":
				keys.SubjectKey = []byte{1}
			case "short-signing":
				keys.SigningKey = []byte{1}
			case "short-public":
				keys.PublicKeys = append(keys.PublicKeys, []byte{1})
			case "empty-public":
				keys.PublicKeys = nil
			}
			f.handler.identityKeys = func(context.Context) (IdentityKeySnapshot, error) { return keys, loadErr }
			response := httptest.NewRecorder()
			f.handler.HandleIdentityJWKS(response, httptest.NewRequest(http.MethodGet, IdentityJWKSPath, nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("JWKS status=%d, want 503", response.Code)
			}
			assertion, err := f.handler.signIdentityAssertion(context.Background(), browsersession.Identity{UserID: "user-abc"}, f.app)
			if err == nil || assertion != "" {
				t.Fatalf("invalid snapshot signed assertion: %v", err)
			}
		})
	}
}
