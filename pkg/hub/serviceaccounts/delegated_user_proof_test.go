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

package serviceaccounts

import (
	"bytes"
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const proofTestNamespace = "faros-hub"

// The key has to outlive a hub restart and be identical across replicas, or a
// token minted by one hub stops verifying at the next. It is therefore stored,
// not generated per process: created on first use, read verbatim afterwards.
func TestKCPProofKeySourceCreatesOnceAndIsStable(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()

	first := newKCPProofKeySourceWithClient(cs, proofTestNamespace)
	key, err := first.DelegatedProofKey(ctx)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if len(key) != delegatedProofKeyLength {
		t.Fatalf("key length = %d, want %d", len(key), delegatedProofKeyLength)
	}
	if bytes.Equal(key, make([]byte, delegatedProofKeyLength)) {
		t.Fatal("generated key is all zeroes")
	}

	// A second read on the same source is served from memory.
	again, err := first.DelegatedProofKey(ctx)
	if err != nil || !bytes.Equal(again, key) {
		t.Fatalf("second read = %x, %v; want the same key", again, err)
	}

	// A second replica — a fresh source over the same store — must arrive at
	// the same key rather than minting a competing one.
	second := newKCPProofKeySourceWithClient(cs, proofTestNamespace)
	other, err := second.DelegatedProofKey(ctx)
	if err != nil {
		t.Fatalf("second source: %v", err)
	}
	if !bytes.Equal(other, key) {
		t.Fatalf("second source got a different key (%x vs %x); tokens would not verify across replicas", other, key)
	}

	secrets, err := cs.CoreV1().Secrets(proofTestNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	if len(secrets.Items) != 1 || secrets.Items[0].Name != delegatedProofKeySecretName {
		t.Fatalf("stored secrets = %+v, want one %s", secrets.Items, delegatedProofKeySecretName)
	}
}

// A key that is present but unusable must fail rather than be padded, reused,
// or silently regenerated — regenerating would invalidate every live account.
func TestKCPProofKeySourceRejectsShortKey(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: delegatedProofKeySecretName, Namespace: proofTestNamespace},
		Data:       map[string][]byte{delegatedProofKeySecretKey: []byte("too-short")},
	})
	if _, err := newKCPProofKeySourceWithClient(cs, proofTestNamespace).DelegatedProofKey(context.Background()); err == nil {
		t.Fatal("accepted a short delegated proof key")
	}
}

// A different key must not validate another key's proof: this is what stops a
// proof from one deployment being replayed into another.
func TestDelegatedProofIsKeyBound(t *testing.T) {
	ctx := context.Background()
	tenantPath := tenantPathFor(delegatedTestOrg, delegatedTestWS)
	sa := hubMintedDelegatedServiceAccount(t, tenantPath, delegatedTestUser, "uid-keybound")

	otherKey := StaticProofKeySource(bytes.Repeat([]byte{0x01}, delegatedProofKeyLength))
	if _, err := DelegatedUserFromServiceAccount(ctx, otherKey, sa); err == nil {
		t.Fatal("a proof made with one key verified under another")
	}
}
