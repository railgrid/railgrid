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
	"fmt"
)

// IdentityKeySnapshot contains keys from one persisted key-ring version.
// PublicKeys starts with the public key matching SigningKey.
type IdentityKeySnapshot struct {
	SigningKey ed25519.PrivateKey
	SubjectKey []byte
	PublicKeys []ed25519.PublicKey
}

func (h *Handler) identityKeySnapshot(ctx context.Context) (IdentityKeySnapshot, error) {
	keys, err := h.identityKeys(ctx)
	if err != nil {
		return IdentityKeySnapshot{}, fmt.Errorf("loading app identity key snapshot: %w", err)
	}
	if len(keys.SigningKey) != ed25519.PrivateKeySize || len(keys.SubjectKey) < identitySubjectKeyMinLength || len(keys.PublicKeys) == 0 {
		return IdentityKeySnapshot{}, fmt.Errorf("app identity key snapshot is incomplete")
	}
	for _, key := range keys.PublicKeys {
		if len(key) != ed25519.PublicKeySize {
			return IdentityKeySnapshot{}, fmt.Errorf("app identity public key has an invalid length")
		}
	}
	if !keys.PublicKeys[0].Equal(keys.SigningKey.Public()) {
		return IdentityKeySnapshot{}, fmt.Errorf("app identity key snapshot is inconsistent")
	}
	return keys, nil
}
