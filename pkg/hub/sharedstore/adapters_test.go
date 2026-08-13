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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/faroshq/faros/pkg/browsersession"
	"github.com/faroshq/faros/pkg/hub/appauth"
)

// twoReplicaSessions returns two browsersession Stores backed by one API, which
// is exactly the shape of a hub scaled to two pods.
func twoReplicaSessions(t *testing.T) (*browsersession.Store, *browsersession.Store) {
	t.Helper()
	clientset := kubefake.NewClientset()
	newReplica := func() *browsersession.Store {
		backend := &SessionBackend{store: &Store{
			client: clientset, namespace: testNamespace, kind: SessionKind, now: time.Now,
		}}
		return browsersession.New(browsersession.Config{Backend: backend})
	}
	return newReplica(), newReplica()
}

// The portal cookie is minted by whichever replica served the login and
// presented to whichever replica the load balancer picks next.
func TestSessionIssuedOnOneReplicaResolvesOnAnother(t *testing.T) {
	replicaA, replicaB := twoReplicaSessions(t)

	response := httptest.NewRecorder()
	if _, err := replicaA.IssueHTTP(context.Background(), response, browsersession.Identity{
		UserID: "user-1", Email: "one@example.test", RBACIdentity: "faros:one@example.test",
	}); err != nil {
		t.Fatalf("issue on replica A: %v", err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookies[0])
	session, err := replicaB.ResolveRequest(req)
	if err != nil {
		t.Fatalf("resolve on replica B: %v", err)
	}
	if session.Identity.UserID != "user-1" || session.Identity.RBACIdentity != "faros:one@example.test" {
		t.Fatalf("identity = %#v", session.Identity)
	}
}

// Logout has to mean logout everywhere; a per-process revocation would leave
// the cookie live on every other replica.
func TestRevokeOnOneReplicaAppliesToAnother(t *testing.T) {
	replicaA, replicaB := twoReplicaSessions(t)
	ctx := context.Background()

	value, _, err := replicaA.Issue(ctx, browsersession.Identity{UserID: "user-1"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := replicaB.Resolve(ctx, value); err != nil {
		t.Fatalf("resolve before revoke: %v", err)
	}
	if err := replicaA.Revoke(ctx, value); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := replicaB.Resolve(ctx, value); !errors.Is(err, browsersession.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound after revocation on a peer", err)
	}
}

func TestSessionExpiryIsEnforcedAcrossReplicas(t *testing.T) {
	clientset := kubefake.NewClientset()
	now := time.Unix(2000, 0)
	backend := &SessionBackend{store: &Store{
		client: clientset, namespace: testNamespace, kind: SessionKind,
		now: func() time.Time { return now },
	}}
	writer := browsersession.New(browsersession.Config{
		TTL: time.Minute, Backend: backend, Now: func() time.Time { return now },
	})
	reader := browsersession.New(browsersession.Config{
		TTL: time.Minute, Backend: backend, Now: func() time.Time { return now },
	})

	value, _, err := writer.Issue(context.Background(), browsersession.Identity{UserID: "user-1"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := reader.Resolve(context.Background(), value); err == nil {
		t.Fatal("expired session resolved on a peer replica")
	}
}

func newTestAppCodeStore(clientset *kubefake.Clientset) *AppCodeStore {
	return &AppCodeStore{store: &Store{
		client: clientset, namespace: testNamespace, kind: AppCodeKind, now: time.Now,
	}}
}

// A published-app code is minted during the browser's authorize hop and
// redeemed by the access proxy in a separate server-to-server request, which a
// scaled hub will serve from a different replica.
func TestAppCodeMintedOnOneReplicaRedeemsOnAnother(t *testing.T) {
	clientset := kubefake.NewClientset()
	replicaA := newTestAppCodeStore(clientset)
	replicaB := newTestAppCodeStore(clientset)
	ctx := context.Background()

	record := appauth.CodeRecord{
		Ref: appauth.InstanceRef{
			Cluster: "abc123cluster", Group: "infrastructure.faros.sh",
			Resource: "applications", Name: "my-shop",
		},
		RedirectHost: "my-shop-abcdef123456.apps.test.faros",
		Identity: browsersession.Identity{
			UserID: "user-1", Email: "one@example.test", Name: "One",
			RBACIdentity: "faros:one@example.test",
		},
		ExpiresAt: time.Now().Add(2 * time.Minute),
	}
	if err := replicaA.Put(ctx, "code-1", record); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, ok := replicaB.Take(ctx, "code-1")
	if !ok {
		t.Fatal("code minted on replica A could not be redeemed on replica B")
	}
	if got.Ref != record.Ref {
		t.Fatalf("ref = %#v, want %#v", got.Ref, record.Ref)
	}
	if got.RedirectHost != record.RedirectHost {
		t.Fatalf("redirectHost = %q, want %q", got.RedirectHost, record.RedirectHost)
	}
	if got.Identity.UserID != "user-1" || got.Identity.Name != "One" ||
		got.Identity.RBACIdentity != "faros:one@example.test" {
		t.Fatalf("identity = %#v", got.Identity)
	}

	if _, ok := replicaA.Take(ctx, "code-1"); ok {
		t.Fatal("code was redeemable twice")
	}
}
