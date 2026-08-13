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

package sharedstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const testNamespace = "faros-hub"

// newTestStore returns a Store over a fake API, plus a clock the test drives.
// Two Stores sharing one clientset stand in for two hub replicas talking to the
// same kcp workspace.
func newTestStore(t *testing.T, clientset *kubefake.Clientset, now *time.Time) *Store {
	t.Helper()
	return &Store{
		client:    clientset,
		namespace: testNamespace,
		kind:      "test-kind",
		now:       func() time.Time { return *now },
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	now := time.Unix(1000, 0)
	store := newTestStore(t, kubefake.NewClientset(), &now)
	ctx := context.Background()

	if err := store.Put(ctx, "handle-1", []byte("payload"), now.Add(time.Hour)); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.Get(ctx, "handle-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("value = %q, want payload", got)
	}
}

// The whole reason this store exists: an entry written by one replica must be
// readable by another.
func TestEntryIsVisibleToAnotherReplica(t *testing.T) {
	now := time.Unix(1000, 0)
	clientset := kubefake.NewClientset()
	replicaA := newTestStore(t, clientset, &now)
	replicaB := newTestStore(t, clientset, &now)
	ctx := context.Background()

	if err := replicaA.Put(ctx, "handle-1", []byte("payload"), now.Add(time.Hour)); err != nil {
		t.Fatalf("put on replica A: %v", err)
	}
	got, err := replicaB.Get(ctx, "handle-1")
	if err != nil {
		t.Fatalf("get on replica B: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("replica B read %q, want payload", got)
	}
}

func TestGetMissingReportsNotFound(t *testing.T) {
	now := time.Unix(1000, 0)
	store := newTestStore(t, kubefake.NewClientset(), &now)
	if _, err := store.Get(context.Background(), "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// An expired entry must never be served, whether or not the sweeper has run.
func TestExpiredEntryIsRefusedAndRemoved(t *testing.T) {
	now := time.Unix(1000, 0)
	clientset := kubefake.NewClientset()
	store := newTestStore(t, clientset, &now)
	ctx := context.Background()

	if err := store.Put(ctx, "handle-1", []byte("payload"), now.Add(time.Minute)); err != nil {
		t.Fatalf("put: %v", err)
	}
	now = now.Add(2 * time.Minute)

	if _, err := store.Get(ctx, "handle-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	list, err := clientset.CoreV1().Secrets(testNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("expired entry still stored: %d secrets", len(list.Items))
	}
}

// Take is what makes an app authorization code single-use. A second redemption
// must fail even though it runs against a different replica.
func TestTakeRedeemsOnceAcrossReplicas(t *testing.T) {
	now := time.Unix(1000, 0)
	clientset := kubefake.NewClientset()
	replicaA := newTestStore(t, clientset, &now)
	replicaB := newTestStore(t, clientset, &now)
	ctx := context.Background()

	if err := replicaA.Put(ctx, "code-1", []byte("grant"), now.Add(time.Minute)); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := replicaB.Take(ctx, "code-1")
	if err != nil {
		t.Fatalf("first take: %v", err)
	}
	if string(got) != "grant" {
		t.Fatalf("value = %q, want grant", got)
	}
	if _, err := replicaA.Take(ctx, "code-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second take err = %v, want ErrNotFound (a code must be single-use)", err)
	}
}

func TestTakeRefusesExpiredEntry(t *testing.T) {
	now := time.Unix(1000, 0)
	store := newTestStore(t, kubefake.NewClientset(), &now)
	ctx := context.Background()

	if err := store.Put(ctx, "code-1", []byte("grant"), now.Add(time.Minute)); err != nil {
		t.Fatalf("put: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := store.Take(ctx, "code-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	now := time.Unix(1000, 0)
	store := newTestStore(t, kubefake.NewClientset(), &now)
	ctx := context.Background()

	if err := store.Put(ctx, "handle-1", []byte("payload"), now.Add(time.Hour)); err != nil {
		t.Fatalf("put: %v", err)
	}
	for i := range 2 {
		if err := store.Delete(ctx, "handle-1"); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if _, err := store.Get(ctx, "handle-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGCRemovesOnlyExpiredEntriesOfItsKind(t *testing.T) {
	now := time.Unix(1000, 0)
	clientset := kubefake.NewClientset()
	store := newTestStore(t, clientset, &now)
	other := &Store{client: clientset, namespace: testNamespace, kind: "other-kind", now: func() time.Time { return now }}
	ctx := context.Background()

	if err := store.Put(ctx, "stale", []byte("a"), now.Add(time.Minute)); err != nil {
		t.Fatalf("put stale: %v", err)
	}
	if err := store.Put(ctx, "live", []byte("b"), now.Add(time.Hour)); err != nil {
		t.Fatalf("put live: %v", err)
	}
	if err := other.Put(ctx, "stale", []byte("c"), now.Add(time.Minute)); err != nil {
		t.Fatalf("put other: %v", err)
	}

	now = now.Add(2 * time.Minute)
	deleted, err := store.GC(ctx)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := store.Get(ctx, "live"); err != nil {
		t.Fatalf("live entry removed by gc: %v", err)
	}
	list, err := clientset.CoreV1().Secrets(testNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: LabelKind + "=other-kind",
	})
	if err != nil {
		t.Fatalf("list other kind: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("gc crossed into another kind: %d secrets left", len(list.Items))
	}
}

// A handle is a credential-adjacent secret; it must not end up in a resource
// name that shows up in logs, audit records, or metrics.
func TestObjectNameDoesNotContainKey(t *testing.T) {
	now := time.Unix(1000, 0)
	store := newTestStore(t, kubefake.NewClientset(), &now)
	name := store.objectName("super-secret-handle")
	if strings.Contains(name, "super-secret-handle") {
		t.Fatalf("object name leaks the key: %q", name)
	}
	if name != store.objectName("super-secret-handle") {
		t.Fatal("object name is not stable for the same key")
	}
	if name == store.objectName("another-handle") {
		t.Fatal("distinct keys collided")
	}
}

// Two replicas can read the same code before either deletes it. Real kcp
// resolves that with the resource-version precondition on the delete, failing
// the loser with a conflict — the fake clientset does not enforce
// preconditions, so simulate the conflict directly. The loser must come away
// empty-handed rather than treating the value it already read as redeemed.
func TestTakeYieldsNothingWhenAnotherReplicaWinsTheDelete(t *testing.T) {
	now := time.Unix(1000, 0)
	clientset := kubefake.NewClientset()
	store := newTestStore(t, clientset, &now)
	ctx := context.Background()

	if err := store.Put(ctx, "code-1", []byte("grant"), now.Add(time.Minute)); err != nil {
		t.Fatalf("put: %v", err)
	}
	clientset.PrependReactor("delete", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Resource: "secrets"}, action.(k8stesting.DeleteAction).GetName(),
			errors.New("resource version mismatch"))
	})

	if _, err := store.Take(ctx, "code-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound when another replica redeemed first", err)
	}
}
