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

package sharedstore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/railgrid/railgrid/pkg/browsersession"
	"github.com/railgrid/railgrid/pkg/hub/appauth"
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
		UserID: "user-1", Email: "one@example.test", RBACIdentity: "railgrid:one@example.test",
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
	if session.Identity.UserID != "user-1" || session.Identity.RBACIdentity != "railgrid:one@example.test" {
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

// Older hub replicas decode only the legacy session fields and silently
// ignore newer AppScope/AppExpiresAt fields. Preview handles therefore live in
// a distinct collection so even a value copied under the portal cookie name
// cannot resolve as a generic login on an old replica.
func TestPreviewSessionsAreInvisibleToLegacySessionReaders(t *testing.T) {
	clientset := kubefake.NewClientset()
	ctx := context.Background()
	previewBackend := &SessionBackend{store: &Store{
		client: clientset, namespace: testNamespace, kind: PreviewSessionKind, now: time.Now,
	}}
	legacyBackend := &SessionBackend{store: &Store{
		client: clientset, namespace: testNamespace, kind: SessionKind, now: time.Now,
	}}
	preview := browsersession.New(browsersession.Config{Backend: previewBackend})
	legacy := browsersession.New(browsersession.Config{Backend: legacyBackend})

	value, session, err := preview.IssueTransient(ctx, browsersession.Identity{
		UserID: "system:serviceaccount:app-studio:preview-one", AppScope: "cluster/infrastructure.railgrid.ai/instances/preview-one",
		AppExpiresAt: time.Now().Add(time.Minute),
	}, time.Minute)
	if err != nil {
		t.Fatalf("issue preview session: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: browsersession.CookieName, Value: value})
	if _, err := legacy.ResolveRequest(request); !errors.Is(err, browsersession.ErrNotFound) {
		t.Fatalf("legacy portal store resolved preview handle under portal cookie name: %v", err)
	}
	if got, err := preview.Resolve(ctx, value); err != nil || got.Identity.AppScope != session.Identity.AppScope {
		t.Fatalf("preview store resolve = (%+v, %v), want scoped identity", got.Identity, err)
	}

	// Model the old JSON decoder too: unknown scoped fields deserialize to the
	// zero value, which is why storage isolation, not a cookie rename or JSON
	// field, is the compatibility boundary.
	secrets, err := clientset.CoreV1().Secrets(testNamespace).List(ctx, metav1.ListOptions{LabelSelector: LabelKind + "=" + PreviewSessionKind})
	if err != nil || len(secrets.Items) != 1 {
		t.Fatalf("preview session secrets = %d, err=%v; want one", len(secrets.Items), err)
	}
	var legacyWire struct {
		UserID    string    `json:"userID"`
		IssuedAt  time.Time `json:"issuedAt"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(secrets.Items[0].Data[dataKeyValue], &legacyWire); err != nil {
		t.Fatalf("decode preview record with old schema: %v", err)
	}
	if legacyWire.UserID != session.Identity.UserID || legacyWire.ExpiresAt.IsZero() {
		t.Fatalf("legacy decoded record = %+v, want identity metadata with unknown fields ignored", legacyWire)
	}

	portalValue, _, err := legacy.Issue(ctx, browsersession.Identity{UserID: "user-1"})
	if err != nil {
		t.Fatalf("issue ordinary portal session: %v", err)
	}
	portalRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	portalRequest.AddCookie(&http.Cookie{Name: browsersession.CookieName, Value: portalValue})
	if got, err := legacy.ResolveRequest(portalRequest); err != nil || got.Identity.UserID != "user-1" {
		t.Fatalf("legacy ordinary session resolve = (%+v, %v), want normal flow", got.Identity, err)
	}
}

func newTestAppCodeStore(clientset *kubefake.Clientset) *AppCodeStore {
	return &AppCodeStore{store: &Store{
		client: clientset, namespace: testNamespace, kind: AppCodeKind, now: time.Now,
	}}
}

func newTestPreviewAppCodeStore(clientset *kubefake.Clientset) *AppCodeStore {
	return &AppCodeStore{store: &Store{
		client: clientset, namespace: testNamespace, kind: PreviewAppCodeKind, now: time.Now,
	}}
}

// Old app-code decoders ignore Purpose/AppScope and old exchange handlers
// turn any matching legacy code into a fixed-TTL proxy session. Scoped codes
// must be absent from their collection, while ordinary codes stay compatible.
func TestPreviewAppCodesAreInvisibleToLegacyExchangeReaders(t *testing.T) {
	clientset := kubefake.NewClientset()
	legacy := newTestAppCodeStore(clientset)
	preview := newTestPreviewAppCodeStore(clientset)
	ctx := context.Background()
	ref := appauth.InstanceRef{
		Cluster: "abc123cluster", Group: "infrastructure.railgrid.ai",
		Resource: "instances", Name: "preview-one",
	}
	previewCode := "p2a.random-preview-code"
	previewRecord := appauth.CodeRecord{
		Ref: ref, RedirectHost: "preview.example.test",
		Identity: browsersession.Identity{
			UserID:       "system:serviceaccount:app-studio:preview-one",
			AppScope:     ref.Cluster + "/" + ref.Group + "/" + ref.Resource + "/" + ref.Name,
			AppExpiresAt: time.Now().Add(time.Minute),
		},
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := preview.Put(ctx, previewCode, previewRecord); err != nil {
		t.Fatalf("put preview code: %v", err)
	}
	if _, ok := legacy.Take(ctx, previewCode); ok {
		t.Fatal("legacy AppCodeKind consumed a scoped preview code")
	}
	secrets, err := clientset.CoreV1().Secrets(testNamespace).List(ctx, metav1.ListOptions{LabelSelector: LabelKind + "=" + PreviewAppCodeKind})
	if err != nil || len(secrets.Items) != 1 {
		t.Fatalf("preview code secrets = %d, err=%v; want one", len(secrets.Items), err)
	}
	var legacyWire struct {
		Cluster   string    `json:"cluster"`
		UserID    string    `json:"userID"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(secrets.Items[0].Data[dataKeyValue], &legacyWire); err != nil {
		t.Fatalf("decode preview code with old schema: %v", err)
	}
	if legacyWire.UserID != previewRecord.Identity.UserID || legacyWire.ExpiresAt.IsZero() {
		t.Fatalf("legacy decoded code = %+v, want unknown purpose/scope ignored", legacyWire)
	}
	if got, ok := preview.Take(ctx, previewCode); !ok || got.Identity.AppScope != previewRecord.Identity.AppScope {
		t.Fatalf("preview code resolve = (%+v, %v), want scoped record", got.Identity, ok)
	}

	ordinaryCode := "ordinary-code"
	ordinaryRecord := appauth.CodeRecord{
		Ref: ref, RedirectHost: "preview.example.test",
		Identity: browsersession.Identity{UserID: "user-1"}, ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := legacy.Put(ctx, ordinaryCode, ordinaryRecord); err != nil {
		t.Fatalf("put ordinary code: %v", err)
	}
	if _, ok := preview.Take(ctx, ordinaryCode); ok {
		t.Fatal("preview store consumed an ordinary legacy code")
	}
	if got, ok := legacy.Take(ctx, ordinaryCode); !ok || got.Identity.UserID != "user-1" {
		t.Fatalf("ordinary code redeem = (%+v, %v), want normal legacy flow", got.Identity, ok)
	}
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
			Cluster: "abc123cluster", Group: "infrastructure.railgrid.ai",
			Resource: "applications", Name: "my-shop",
		},
		RedirectHost: "my-shop-abcdef123456.apps.test.railgrid",
		Identity: browsersession.Identity{
			UserID: "user-1", Email: "one@example.test", Name: "One",
			RBACIdentity: "railgrid:one@example.test",
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
		got.Identity.RBACIdentity != "railgrid:one@example.test" {
		t.Fatalf("identity = %#v", got.Identity)
	}

	if _, ok := replicaA.Take(ctx, "code-1"); ok {
		t.Fatal("code was redeemable twice")
	}
}
