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
	"crypto/ed25519"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAppIdentityKeyRingIsSharedAndRotationOverlapsVerification(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.October, 12, 10, 0, 0, 0, time.UTC)
	randomMaterial := bytes.Join([][]byte{
		bytes.Repeat([]byte{0x01}, ed25519.SeedSize),
		bytes.Repeat([]byte{0x02}, delegatedProofKeyLength),
		bytes.Repeat([]byte{0x03}, ed25519.SeedSize),
	}, nil)
	client := fake.NewClientset()
	first := newKCPAppIdentityKeyRingWithClient(client, proofTestNamespace, func() time.Time { return now }, bytes.NewReader(randomMaterial))

	firstSigningKey, err := first.CurrentAppIdentitySigningKey(ctx)
	if err != nil {
		t.Fatalf("initial signing key: %v", err)
	}
	firstSubjectKey, err := first.AppIdentitySubjectKey(ctx)
	if err != nil {
		t.Fatalf("initial subject key: %v", err)
	}
	firstPublicKeys, err := first.AppIdentityPublicKeys(ctx)
	if err != nil || len(firstPublicKeys) != 1 {
		t.Fatalf("initial public keys = %+v, %v; want current only", firstPublicKeys, err)
	}

	// A new hub replica reads the same persisted keys, including the stable
	// subject key used to keep app-local account IDs consistent.
	second := newKCPAppIdentityKeyRingWithClient(client, proofTestNamespace, func() time.Time { return now }, bytes.NewReader(nil))
	secondSigningKey, err := second.CurrentAppIdentitySigningKey(ctx)
	if err != nil {
		t.Fatalf("second replica signing key: %v", err)
	}
	secondSubjectKey, err := second.AppIdentitySubjectKey(ctx)
	if err != nil {
		t.Fatalf("second replica subject key: %v", err)
	}
	if !bytes.Equal(firstSigningKey, secondSigningKey) || !bytes.Equal(firstSubjectKey, secondSubjectKey) {
		t.Fatal("replicas read different app identity keys")
	}

	if err := first.RotateAppIdentitySigningKey(ctx); err != nil {
		t.Fatalf("rotate signing key: %v", err)
	}
	rotatedSigningKey, err := second.CurrentAppIdentitySigningKey(ctx)
	if err != nil {
		t.Fatalf("read rotated key from second replica: %v", err)
	}
	rotatedSubjectKey, err := second.AppIdentitySubjectKey(ctx)
	if err != nil {
		t.Fatalf("read subject key after rotation: %v", err)
	}
	if bytes.Equal(firstSigningKey, rotatedSigningKey) {
		t.Fatal("signing-key rotation kept the previous private key")
	}
	if !bytes.Equal(firstSubjectKey, rotatedSubjectKey) {
		t.Fatal("signing-key rotation changed the stable subject key")
	}

	rotatedPublicKeys, err := second.AppIdentityPublicKeys(ctx)
	if err != nil || len(rotatedPublicKeys) != 2 {
		t.Fatalf("public keys after rotation = %+v, %v; want current and overlap key", rotatedPublicKeys, err)
	}
	if !bytes.Equal(rotatedPublicKeys[0].Key, rotatedSigningKey.Public().(ed25519.PublicKey)) ||
		!bytes.Equal(rotatedPublicKeys[1].Key, firstSigningKey.Public().(ed25519.PublicKey)) ||
		!rotatedPublicKeys[1].ExpiresAt.Equal(now.Add(appIdentityKeyRotationGrace)) {
		t.Fatalf("unexpected public keys after rotation: %+v", rotatedPublicKeys)
	}

	now = now.Add(appIdentityKeyRotationGrace)
	expiredPublicKeys, err := second.AppIdentityPublicKeys(ctx)
	if err != nil || len(expiredPublicKeys) != 1 || !bytes.Equal(expiredPublicKeys[0].Key, rotatedSigningKey.Public().(ed25519.PublicKey)) {
		t.Fatalf("public keys after overlap expiry = %+v, %v; want current only", expiredPublicKeys, err)
	}
}

func TestAppIdentityKeyRingRejectsMalformedPersistedKeys(t *testing.T) {
	client := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: appIdentityKeyRingSecretName, Namespace: proofTestNamespace},
		Data: map[string][]byte{
			"version":                    []byte(appIdentityKeyRingVersion),
			appIdentitySigningKeyField:   []byte("bad"),
			appIdentitySubjectKeyField:   bytes.Repeat([]byte{0x01}, delegatedProofKeyLength),
			appIdentityPreviousKeysField: []byte("[]"),
		},
	})
	ring := newKCPAppIdentityKeyRingWithClient(client, proofTestNamespace, time.Now, bytes.NewReader(nil))
	if _, err := ring.CurrentAppIdentitySigningKey(context.Background()); err == nil {
		t.Fatal("accepted malformed persisted signing key")
	}
}
