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
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	clienttesting "k8s.io/client-go/testing"
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
	reads := countAppIdentitySecretGets(client)
	first := newKCPAppIdentityKeyRingWithClient(client, proofTestNamespace, func() time.Time { return now }, bytes.NewReader(randomMaterial))

	firstSnapshot, err := first.Snapshot(ctx)
	if err != nil {
		t.Fatalf("initial key snapshot: %v", err)
	}
	if len(firstSnapshot.PublicKeys) != 1 {
		t.Fatalf("initial public keys = %+v; want current only", firstSnapshot.PublicKeys)
	}

	// A new hub replica reads the same persisted keys, including the stable
	// subject key used to keep app-local account IDs consistent.
	second := newKCPAppIdentityKeyRingWithClient(client, proofTestNamespace, func() time.Time { return now }, bytes.NewReader(nil))
	secondSnapshot, err := second.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second replica snapshot: %v", err)
	}
	if !bytes.Equal(firstSnapshot.SigningKey, secondSnapshot.SigningKey) || !bytes.Equal(firstSnapshot.SubjectKey, secondSnapshot.SubjectKey) {
		t.Fatal("replicas read different app identity keys")
	}
	if reads.Load() != 2 {
		t.Fatalf("replica snapshots caused %d Secret reads; want one read per replica", reads.Load())
	}

	if err := first.RotateAppIdentitySigningKey(ctx); err != nil {
		t.Fatalf("rotate signing key: %v", err)
	}
	rotationTime := now
	firstRotated, err := first.Snapshot(ctx)
	if err != nil {
		t.Fatalf("read rotated key from same replica: %v", err)
	}
	if bytes.Equal(firstSnapshot.SigningKey, firstRotated.SigningKey) || !bytes.Equal(firstSnapshot.SubjectKey, firstRotated.SubjectKey) {
		t.Fatal("local rotation did not replace the signing key while preserving the subject key")
	}

	// Another replica may serve its cached snapshot until the short cache TTL
	// expires, then observes the rotated key and overlap window from one read.
	now = now.Add(appIdentityKeySnapshotTTL)
	secondRotated, err := second.Snapshot(ctx)
	if err != nil {
		t.Fatalf("read rotated key from second replica: %v", err)
	}
	if bytes.Equal(firstSnapshot.SigningKey, secondRotated.SigningKey) {
		t.Fatal("signing-key rotation kept the previous private key")
	}
	if !bytes.Equal(firstSnapshot.SubjectKey, secondRotated.SubjectKey) {
		t.Fatal("signing-key rotation changed the stable subject key")
	}
	if len(secondRotated.PublicKeys) != 2 {
		t.Fatalf("public keys after rotation = %+v; want current and overlap key", secondRotated.PublicKeys)
	}
	if !bytes.Equal(secondRotated.PublicKeys[0].Key, secondRotated.SigningKey.Public().(ed25519.PublicKey)) ||
		!bytes.Equal(secondRotated.PublicKeys[1].Key, firstSnapshot.SigningKey.Public().(ed25519.PublicKey)) ||
		!secondRotated.PublicKeys[1].ExpiresAt.Equal(rotationTime.Add(appIdentityKeyRotationGrace)) {
		t.Fatalf("unexpected public keys after rotation: %+v", secondRotated.PublicKeys)
	}

	if reads.Load() != 5 {
		t.Fatalf("reads after rotation = %d; want 5 (two snapshots, rotation, two refreshed snapshots)", reads.Load())
	}

	now = rotationTime.Add(appIdentityKeyRotationGrace)
	expiredSnapshot, err := second.Snapshot(ctx)
	if err != nil || len(expiredSnapshot.PublicKeys) != 1 || !bytes.Equal(expiredSnapshot.PublicKeys[0].Key, secondRotated.SigningKey.Public().(ed25519.PublicKey)) {
		t.Fatalf("public keys after overlap expiry = %+v, %v; want current only", expiredSnapshot.PublicKeys, err)
	}
	if reads.Load() != 6 {
		t.Fatalf("reads after overlap expiry = %d; want 6", reads.Load())
	}
}

func TestAppIdentityKeySnapshotCacheCoalescesConcurrentRequests(t *testing.T) {
	client := fake.NewClientset(appIdentityKeyRingSecretForTest(t, proofTestNamespace,
		bytes.Repeat([]byte{0x04}, ed25519.SeedSize),
		bytes.Repeat([]byte{0x05}, delegatedProofKeyLength), nil))
	reads := countAppIdentitySecretGets(client)
	readStarted := make(chan struct{})
	allowRead := make(chan struct{})
	var blockOnce sync.Once
	client.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		blockOnce.Do(func() {
			close(readStarted)
			<-allowRead
		})
		return false, nil, nil
	})
	ring := newKCPAppIdentityKeyRingWithClient(client, proofTestNamespace, time.Now, bytes.NewReader(nil))

	const callers = 24
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			_, err := ring.Snapshot(context.Background())
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	<-readStarted
	close(allowRead)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent snapshot: %v", err)
		}
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("concurrent snapshots caused %d Secret reads; want 1", got)
	}
}

func TestAppIdentityKeySnapshotRefreshOutlivesFirstCallerCancellation(t *testing.T) {
	client := fake.NewClientset(appIdentityKeyRingSecretForTest(t, proofTestNamespace,
		bytes.Repeat([]byte{0x14}, ed25519.SeedSize),
		bytes.Repeat([]byte{0x15}, delegatedProofKeyLength), nil))
	blocking := &contextBlockingKeyringClient{
		Interface: client,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	ring := newKCPAppIdentityKeyRingWithClient(blocking, proofTestNamespace, time.Now, bytes.NewReader(nil))

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := ring.Snapshot(firstCtx)
		firstDone <- err
	}()
	<-blocking.entered

	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		_, err := ring.Snapshot(context.Background())
		secondDone <- err
	}()
	<-secondStarted
	// Let the second request join the in-flight singleflight before cancelling
	// the caller whose request started it.
	time.Sleep(25 * time.Millisecond)
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller error = %v, want context canceled", err)
	}
	select {
	case err := <-secondDone:
		t.Fatalf("shared refresh ended with the first caller: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(blocking.release)
	if err := <-secondDone; err != nil {
		t.Fatalf("second caller did not receive the completed snapshot: %v", err)
	}
	if got := blocking.gets.Load(); got != 1 {
		t.Fatalf("shared refresh performed %d Secret reads, want one", got)
	}
}

type contextBlockingKeyringClient struct {
	kubernetes.Interface
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	gets    atomic.Int32
}

func (c *contextBlockingKeyringClient) CoreV1() corev1client.CoreV1Interface {
	return &contextBlockingCoreV1Client{
		CoreV1Interface: c.Interface.CoreV1(),
		owner:           c,
	}
}

type contextBlockingCoreV1Client struct {
	corev1client.CoreV1Interface
	owner *contextBlockingKeyringClient
}

func (c *contextBlockingCoreV1Client) Secrets(namespace string) corev1client.SecretInterface {
	return &contextBlockingSecretClient{
		SecretInterface: c.CoreV1Interface.Secrets(namespace),
		owner:           c.owner,
	}
}

type contextBlockingSecretClient struct {
	corev1client.SecretInterface
	owner *contextBlockingKeyringClient
}

func (c *contextBlockingSecretClient) Get(ctx context.Context, name string, options metav1.GetOptions) (*corev1.Secret, error) {
	c.owner.gets.Add(1)
	c.owner.once.Do(func() { close(c.owner.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.owner.release:
		return c.SecretInterface.Get(ctx, name, options)
	}
}

func TestAppIdentityKeySnapshotExpiresAtKeyOverlapAndCacheTTL(t *testing.T) {
	now := time.Date(2026, time.October, 12, 10, 0, 0, 0, time.UTC)
	previousKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x07}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	secret := appIdentityKeyRingSecretForTest(t, proofTestNamespace,
		bytes.Repeat([]byte{0x06}, ed25519.SeedSize),
		bytes.Repeat([]byte{0x08}, delegatedProofKeyLength),
		[]storedAppIdentityPublicKey{{Key: previousKey, ExpiresAt: now.Add(4 * time.Second)}})
	client := fake.NewClientset(secret)
	reads := countAppIdentitySecretGets(client)
	ring := newKCPAppIdentityKeyRingWithClient(client, proofTestNamespace, func() time.Time { return now }, bytes.NewReader(nil))

	first, err := ring.Snapshot(context.Background())
	if err != nil || len(first.PublicKeys) != 2 {
		t.Fatalf("initial overlapping snapshot = %+v, %v; want two keys", first.PublicKeys, err)
	}
	first.SigningKey[0] ^= 0xff
	first.SubjectKey[0] ^= 0xff
	first.PublicKeys[0].Key[0] ^= 0xff
	if _, err := ring.Snapshot(context.Background()); err != nil {
		t.Fatalf("cached snapshot: %v", err)
	}
	if reads.Load() != 1 {
		t.Fatalf("cached snapshot caused %d reads; want 1", reads.Load())
	}

	now = now.Add(4 * time.Second)
	afterOverlap, err := ring.Snapshot(context.Background())
	if err != nil || len(afterOverlap.PublicKeys) != 1 {
		t.Fatalf("snapshot after key overlap expiry = %+v, %v; want current only", afterOverlap.PublicKeys, err)
	}
	if reads.Load() != 2 {
		t.Fatalf("key overlap expiry caused %d reads; want 2", reads.Load())
	}

	now = now.Add(appIdentityKeySnapshotTTL)
	if _, err := ring.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot after cache TTL: %v", err)
	}
	if reads.Load() != 3 {
		t.Fatalf("cache TTL expiry caused %d reads; want 3", reads.Load())
	}
}

func TestAppIdentityKeySnapshotDoesNotCacheReadErrors(t *testing.T) {
	client := fake.NewClientset(appIdentityKeyRingSecretForTest(t, proofTestNamespace,
		bytes.Repeat([]byte{0x09}, ed25519.SeedSize),
		bytes.Repeat([]byte{0x0a}, delegatedProofKeyLength), nil))
	var reads atomic.Int32
	client.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		if reads.Add(1) == 1 {
			return true, nil, errors.New("temporary kcp read failure")
		}
		return false, nil, nil
	})
	ring := newKCPAppIdentityKeyRingWithClient(client, proofTestNamespace, time.Now, bytes.NewReader(nil))
	if _, err := ring.Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "temporary kcp read failure") {
		t.Fatalf("first snapshot error = %v; want temporary read failure", err)
	}
	if _, err := ring.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot after a transient error: %v", err)
	}
	if reads.Load() != 2 {
		t.Fatalf("snapshot reads after transient failure = %d; want 2", reads.Load())
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

func appIdentityKeyRingSecretForTest(t *testing.T, namespace string, signingSeed, subjectKey []byte, previous []storedAppIdentityPublicKey) *corev1.Secret {
	t.Helper()
	encodedPrevious, err := json.Marshal(previous)
	if err != nil {
		t.Fatalf("encode previous test keys: %v", err)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: appIdentityKeyRingSecretName, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"version":                    []byte(appIdentityKeyRingVersion),
			appIdentitySigningKeyField:   ed25519.NewKeyFromSeed(signingSeed),
			appIdentitySubjectKeyField:   append([]byte(nil), subjectKey...),
			appIdentityPreviousKeysField: encodedPrevious,
		},
	}
}

func countAppIdentitySecretGets(client *fake.Clientset) *atomic.Int32 {
	count := &atomic.Int32{}
	client.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		count.Add(1)
		return false, nil, nil
	})
	return count
}
