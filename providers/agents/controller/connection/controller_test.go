// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package connection

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/connsecret"
	"github.com/railgrid/provider-agents/llm"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
)

const (
	testCluster = "tenant-a"
	testConn    = "team-chat"
	webhookPath = "/services/providers/agents/webhooks/channels/c1/team-chat/tok"
)

var now = time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)

type fakeManager struct {
	mcmanager.Manager
	c client.Client
}

func (m fakeManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return fakeCluster{c: m.c}, nil
}

type fakeCluster struct {
	cluster.Cluster
	c client.Client
}

func (c fakeCluster) GetClient() client.Client { return c.c }

// fakeTelegram is the Bot API: what is registered, and what setWebhook saw.
type fakeTelegram struct {
	registered string
	err        error
	setURL     string
	setSecret  string
	setCalls   int
}

func (f *fakeTelegram) WebhookURL(context.Context, string) (string, error) {
	return f.registered, f.err
}

func (f *fakeTelegram) SetWebhook(_ context.Context, _, url, secret string) error {
	f.setCalls++
	f.setURL, f.setSecret = url, secret
	return nil
}

// fakeOAuth hands back a fixed refreshed token.
type fakeOAuth struct {
	calls   int
	err     error
	updates map[string]string
}

func (f *fakeOAuth) Refresh(context.Context, *agentsv1alpha1.Connection, map[string][]byte) (map[string]string, error) {
	f.calls++
	return f.updates, f.err
}

// fakeGateway records the desired session set.
type fakeGateway struct {
	mu        sync.Mutex
	sessions  map[string]string // key -> token
	ensureErr error
	removed   []string
}

func newFakeGateway() *fakeGateway { return &fakeGateway{sessions: map[string]string{}} }

func (g *fakeGateway) Ensure(_ context.Context, cluster, name, token string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ensureErr != nil {
		return g.ensureErr
	}
	g.sessions[cluster+"/"+name] = token
	return nil
}

func (g *fakeGateway) Remove(cluster, name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.sessions, cluster+"/"+name)
	g.removed = append(g.removed, cluster+"/"+name)
}

func connection(typ string, inbound bool) *agentsv1alpha1.Connection {
	c := &agentsv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: testConn}}
	c.Spec.Type = typ
	c.Spec.Channel = "C123"
	if inbound {
		c.Status.WebhookPath = webhookPath
	}
	return c
}

func secret(data map[string]string) *corev1.Secret {
	enc := map[string][]byte{}
	for k, v := range data {
		enc[k] = []byte(v)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: connsecret.Name(testConn), Namespace: llm.SecretNamespace},
		Data:       enc,
	}
}

type harness struct {
	c        client.Client
	r        *Reconciler
	telegram *fakeTelegram
	oauth    *fakeOAuth
	gateway  *fakeGateway
	req      mcreconcile.Request
}

func newHarness(t *testing.T, objs ...client.Object) *harness {
	t.Helper()
	return newHarnessWith(t, nil, objs...)
}

func newHarnessWith(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) *harness {
	t.Helper()
	base := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Connection{}).
		WithObjects(objs...).
		Build()
	var c client.Client = base
	if funcs != nil {
		c = interceptor.NewClient(base, *funcs)
	}
	h := &harness{
		c:        c,
		telegram: &fakeTelegram{registered: "https://hub.example" + webhookPath},
		oauth:    &fakeOAuth{updates: map[string]string{"token": "new-access", "refresh_token": "new-refresh", "expiry": now.Add(time.Hour).Format(time.RFC3339)}},
		gateway:  newFakeGateway(),
		req:      mcreconcile.Request{ClusterName: testCluster, Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: testConn}}},
	}
	h.r = &Reconciler{Manager: fakeManager{c: c}, Telegram: h.telegram, OAuth: h.oauth, Gateway: h.gateway, Now: func() time.Time { return now }}
	return h
}

func (h *harness) reconcile(t *testing.T) reconcile.Result {
	t.Helper()
	res, err := h.r.Reconcile(context.Background(), h.req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func (h *harness) status(t *testing.T) (phase, message string) {
	t.Helper()
	var got agentsv1alpha1.Connection
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: testConn}, &got); err != nil {
		t.Fatal(err)
	}
	return got.Status.Phase, got.Status.Message
}

func (h *harness) secretKey(t *testing.T, key string) string {
	t.Helper()
	var sec corev1.Secret
	if err := h.c.Get(context.Background(), credentialKey(testConn), &sec); err != nil {
		t.Fatal(err)
	}
	return string(sec.Data[key])
}

// ---- slack --------------------------------------------------------------------

func TestSlackWithoutSigningSecretIsFlaggedThenCleared(t *testing.T) {
	h := newHarness(t, connection(agentsv1alpha1.ConnectionTypeSlack, true), secret(map[string]string{"token": "xoxb-1"}))
	res := h.reconcile(t)
	if phase, msg := h.status(t); phase != "Error" || msg != connsecret.SigningSecretMissingMessage {
		t.Fatalf("want Error/%q, got %q/%q", connsecret.SigningSecretMissingMessage, phase, msg)
	}
	if res.RequeueAfter != resyncInterval {
		t.Fatalf("RequeueAfter = %s, want the resync %s", res.RequeueAfter, resyncInterval)
	}

	// The user adds the signing secret: the next pass clears the error.
	var sec corev1.Secret
	if err := h.c.Get(context.Background(), credentialKey(testConn), &sec); err != nil {
		t.Fatal(err)
	}
	sec.Data[connsecret.SigningSecretKey] = []byte("8f742231b10e8888abcd99yyyzzz85a5")
	if err := h.c.Update(context.Background(), &sec); err != nil {
		t.Fatal(err)
	}
	h.reconcile(t)
	if phase, msg := h.status(t); phase != "Ready" || msg != "" {
		t.Fatalf("want Ready after the secret is added, got %q/%q", phase, msg)
	}
}

func TestOutboundOnlySlackIsLeftAlone(t *testing.T) {
	h := newHarness(t, connection(agentsv1alpha1.ConnectionTypeSlack, false), secret(map[string]string{}))
	h.reconcile(t)
	if phase, _ := h.status(t); phase == "Error" {
		t.Fatal("a connection that never enabled inbound has nothing to verify and must not error")
	}
}

// A Secret the reconciler could not read says nothing about whether the secret
// exists; the status must be left alone rather than parking a healthy
// connection in Error.
func TestUnreadableSecretLeavesStatusAlone(t *testing.T) {
	funcs := &interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Secret); ok {
			return apierrors.NewServiceUnavailable("virtual workspace is unavailable")
		}
		return c.Get(ctx, key, obj, opts...)
	}}
	h := newHarnessWith(t, funcs, connection(agentsv1alpha1.ConnectionTypeSlack, true), secret(map[string]string{"token": "xoxb-1", connsecret.SigningSecretKey: "s"}))
	h.reconcile(t)
	if phase, msg := h.status(t); phase == "Error" || msg == connsecret.SigningSecretMissingMessage {
		t.Fatalf("an unreadable Secret must not flag the connection as missing one, got %q/%q", phase, msg)
	}
}

// ---- telegram -----------------------------------------------------------------

func TestTelegramAdoptsSecretAndReRegisters(t *testing.T) {
	h := newHarness(t, connection(agentsv1alpha1.ConnectionTypeTelegram, true), secret(map[string]string{"token": "123:abc"}))
	h.reconcile(t)

	stored := h.secretKey(t, connsecret.SigningSecretKey)
	if len(stored) != 64 {
		t.Fatalf("a 32-byte hex secret should have been stored, got %q", stored)
	}
	if h.telegram.setCalls != 1 || h.telegram.setSecret != stored || !strings.HasSuffix(h.telegram.setURL, webhookPath) {
		t.Fatalf("webhook should be re-registered with the stored secret at the existing URL, got calls=%d url=%q secret=%q", h.telegram.setCalls, h.telegram.setURL, h.telegram.setSecret)
	}
	if phase, _ := h.status(t); phase == "Error" {
		t.Fatal("successful adoption must not leave the connection in Error")
	}

	// Second pass: the secret exists, nothing is re-registered or rotated.
	h.reconcile(t)
	if h.telegram.setCalls != 1 {
		t.Fatal("reconcile must be idempotent once the secret is stored")
	}
	if again := h.secretKey(t, connsecret.SigningSecretKey); again != stored {
		t.Fatal("the stored secret must not be rotated on every pass")
	}
}

func TestTelegramReportsWebhookGone(t *testing.T) {
	h := newHarness(t, connection(agentsv1alpha1.ConnectionTypeTelegram, true), secret(map[string]string{"token": "123:abc"}))
	h.telegram.registered = ""
	h.reconcile(t)
	phase, msg := h.status(t)
	if phase != "Error" || !strings.HasPrefix(msg, connsecret.TelegramRegisterFailedPrefix) || !strings.Contains(msg, "Enable inbound") {
		t.Fatalf("a bot with no registered webhook should tell the user to re-enable inbound, got %q/%q", phase, msg)
	}
	// The secret is stored regardless so Enable inbound registers a verified hook.
	if h.secretKey(t, connsecret.SigningSecretKey) == "" {
		t.Fatal("secret should be stored even when re-registration failed")
	}
}

func TestTelegramReportsForeignWebhook(t *testing.T) {
	h := newHarness(t, connection(agentsv1alpha1.ConnectionTypeTelegram, true), secret(map[string]string{"token": "123:abc"}))
	h.telegram.registered = "https://somewhere.else/hook"
	h.reconcile(t)
	if phase, msg := h.status(t); phase != "Error" || !strings.Contains(msg, errForeignTelegramWebhook.Error()) {
		t.Fatalf("got %q/%q", phase, msg)
	}
	if h.telegram.setCalls != 0 {
		t.Fatal("must not overwrite a webhook that points elsewhere")
	}
}

// A previous failure clears once re-registration succeeds.
func TestTelegramRecoversFromEarlierFailure(t *testing.T) {
	conn := connection(agentsv1alpha1.ConnectionTypeTelegram, true)
	conn.Status.Phase = "Error"
	conn.Status.Message = connsecret.TelegramRegisterFailedPrefix + "boom — click Enable inbound to register it again"
	h := newHarness(t, conn, secret(map[string]string{"token": "123:abc"}))
	h.reconcile(t)
	if phase, msg := h.status(t); phase != "Ready" || msg != "" {
		t.Fatalf("want Ready once the webhook is registered, got %q/%q", phase, msg)
	}
}

// ---- oauth --------------------------------------------------------------------

func oauthConnection() *agentsv1alpha1.Connection {
	c := connection("github", false)
	c.Spec.Auth = "oauth"
	c.Spec.OAuth = &agentsv1alpha1.ConnectionOAuth{Provider: "github"}
	return c
}

func TestOAuthTokenFarFromExpiryIsRequeuedForTheWindow(t *testing.T) {
	expiry := now.Add(40 * time.Minute)
	h := newHarness(t, oauthConnection(), secret(map[string]string{"token": "t", "refresh_token": "r", "expiry": expiry.Format(time.RFC3339)}))
	res := h.reconcile(t)
	if h.oauth.calls != 0 {
		t.Fatal("a token with forty minutes left must not be refreshed")
	}
	if want := 40*time.Minute - oauthRefreshSkew; res.RequeueAfter != want {
		t.Fatalf("RequeueAfter = %s, want %s (expiry minus skew)", res.RequeueAfter, want)
	}

	// An expiry beyond the resync horizon is re-checked at the resync at the
	// latest; the wait never exceeds it.
	h2 := newHarness(t, oauthConnection(), secret(map[string]string{"token": "t", "refresh_token": "r", "expiry": now.Add(48 * time.Hour).Format(time.RFC3339)}))
	if res := h2.reconcile(t); res.RequeueAfter != resyncInterval {
		t.Fatalf("RequeueAfter = %s, want the resync %s", res.RequeueAfter, resyncInterval)
	}
}

func TestOAuthTokenNearExpiryIsRefreshed(t *testing.T) {
	expiry := now.Add(5 * time.Minute)
	h := newHarness(t, oauthConnection(), secret(map[string]string{"token": "old", "refresh_token": "r", "expiry": expiry.Format(time.RFC3339)}))
	res := h.reconcile(t)
	if h.oauth.calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", h.oauth.calls)
	}
	if got := h.secretKey(t, "token"); got != "new-access" {
		t.Fatalf("token = %q, want the refreshed one", got)
	}
	if got := h.secretKey(t, "refresh_token"); got != "new-refresh" {
		t.Fatalf("refresh_token = %q, want rotated", got)
	}
	if want := time.Hour - oauthRefreshSkew; res.RequeueAfter != want {
		t.Fatalf("RequeueAfter = %s, want %s (new expiry minus skew)", res.RequeueAfter, want)
	}
}

func TestOAuthRefreshFailureRetriesSoon(t *testing.T) {
	expiry := now.Add(5 * time.Minute)
	h := newHarness(t, oauthConnection(), secret(map[string]string{"token": "old", "refresh_token": "r", "expiry": expiry.Format(time.RFC3339)}))
	h.oauth.err = errors.New("token endpoint: 500")
	res := h.reconcile(t)
	if got := h.secretKey(t, "token"); got != "old" {
		t.Fatalf("token must be left alone on failure, got %q", got)
	}
	if res.RequeueAfter != oauthRetryInterval {
		t.Fatalf("RequeueAfter = %s, want the retry interval %s", res.RequeueAfter, oauthRetryInterval)
	}
}

func TestOAuthWithoutRefreshTokenIsNotTouched(t *testing.T) {
	h := newHarness(t, oauthConnection(), secret(map[string]string{"token": "xoxb-bot"}))
	res := h.reconcile(t)
	if h.oauth.calls != 0 {
		t.Fatal("nothing to refresh")
	}
	if res.RequeueAfter != resyncInterval {
		t.Fatalf("RequeueAfter = %s, want the resync", res.RequeueAfter)
	}
}

// ---- discord ------------------------------------------------------------------

func TestDiscordBotTokenOpensSession(t *testing.T) {
	h := newHarness(t, connection(agentsv1alpha1.ConnectionTypeDiscord, false), secret(map[string]string{"token": "bot-token"}))
	h.reconcile(t)
	if got := h.gateway.sessions[testCluster+"/"+testConn]; got != "bot-token" {
		t.Fatalf("sessions = %v, want one for the connection", h.gateway.sessions)
	}
}

func TestDiscordWithoutTokenClosesSession(t *testing.T) {
	h := newHarness(t, connection(agentsv1alpha1.ConnectionTypeDiscord, false), secret(map[string]string{}))
	h.gateway.sessions[testCluster+"/"+testConn] = "stale"
	h.reconcile(t)
	if _, ok := h.gateway.sessions[testCluster+"/"+testConn]; ok {
		t.Fatal("a webhook-only discord connection must have no gateway session")
	}
}

func TestDiscordGatewayFailureRetriesSoon(t *testing.T) {
	h := newHarness(t, connection(agentsv1alpha1.ConnectionTypeDiscord, false), secret(map[string]string{"token": "bot-token"}))
	h.gateway.ensureErr = errors.New("MESSAGE CONTENT intent disabled")
	res := h.reconcile(t)
	if res.RequeueAfter != gatewayRetryInterval {
		t.Fatalf("RequeueAfter = %s, want %s", res.RequeueAfter, gatewayRetryInterval)
	}
}

func TestDeletedConnectionClosesSession(t *testing.T) {
	h := newHarness(t)
	h.gateway.sessions[testCluster+"/"+testConn] = "bot-token"
	res := h.reconcile(t)
	if _, ok := h.gateway.sessions[testCluster+"/"+testConn]; ok {
		t.Fatal("a deleted connection's session must be closed")
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("RequeueAfter = %s, want none for a missing object", res.RequeueAfter)
	}
}

// ---- secret watch mapping -----------------------------------------------------

func TestSecretToConnectionMapsOnlyOurSecrets(t *testing.T) {
	ours := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: connsecret.Name("team-chat"), Namespace: llm.SecretNamespace}}
	if got := secretToConnection(context.Background(), ours); len(got) != 1 || got[0].Name != "team-chat" {
		t.Fatalf("got %v, want a request for team-chat", got)
	}
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "railgrid-agents-model-openai", Namespace: llm.SecretNamespace}}
	if got := secretToConnection(context.Background(), other); len(got) != 0 {
		t.Fatalf("a model credential must not map to a connection, got %v", got)
	}
	elsewhere := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: connsecret.Name("team-chat"), Namespace: "other"}}
	if got := secretToConnection(context.Background(), elsewhere); len(got) != 0 {
		t.Fatalf("a secret outside the credentials namespace must not map, got %v", got)
	}
}
