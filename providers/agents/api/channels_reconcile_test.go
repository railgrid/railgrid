// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	agentsclient "github.com/faroshq/provider-agents/client"
)

// reconcileBackground returns a background whose single shard and tenant
// workspace are the same fake client, holding objs.
func reconcileBackground(t *testing.T, objs ...runtime.Object) (*background, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(inboundScheme(), map[schema.GroupVersionResource]string{
		agentsclient.ConnectionGVR: "ConnectionList",
		agentsclient.AgentGVR:      "AgentList",
	}, objs...)
	b := &background{
		shards:   []*vwShard{{url: "https://fake/vw", wildcard: dyn}},
		scopedFn: func(context.Context, string) (dynamic.Interface, error) { return dyn, nil },
	}
	return b, dyn
}

func withCluster(u *unstructured.Unstructured) *unstructured.Unstructured {
	u.SetAnnotations(map[string]string{"kcp.io/cluster": testCluster})
	return u
}

func connectionStatus(t *testing.T, dyn dynamic.Interface) (phase, message string) {
	t.Helper()
	u, err := dyn.Resource(agentsclient.ConnectionGVR).Get(context.Background(), testConn, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
	message, _, _ = unstructured.NestedString(u.Object, "status", "message")
	return phase, message
}

func TestReconcileFlagsSlackConnectionWithoutSigningSecret(t *testing.T) {
	b, dyn := reconcileBackground(t,
		withCluster(inboundConnection("slack", testSlackChan)),
		inboundSecret(map[string]string{"token": "xoxb-1"}),
	)
	b.reconcileChannelSecrets(context.Background())

	phase, msg := connectionStatus(t, dyn)
	if phase != "Error" || msg != connectionSigningSecretMissingMessage {
		t.Fatalf("want Error/%q, got %q/%q", connectionSigningSecretMissingMessage, phase, msg)
	}

	// The user adds the signing secret: the next pass clears the error.
	if err := updateSecretKeys(context.Background(), dyn, connectionSecretName(testConn), map[string]string{signingSecretKey: testSlackSecret}); err != nil {
		t.Fatal(err)
	}
	b.reconcileChannelSecrets(context.Background())
	if phase, msg := connectionStatus(t, dyn); phase != "Ready" || msg != "" {
		t.Fatalf("want Ready after the secret is added, got %q/%q", phase, msg)
	}
}

func TestReconcileLeavesOutboundOnlySlackAlone(t *testing.T) {
	conn := withCluster(inboundConnection("slack", "https://hooks.slack.com/services/x"))
	unstructured.RemoveNestedField(conn.Object, "status", "webhookPath")
	b, dyn := reconcileBackground(t, conn, inboundSecret(map[string]string{}))
	b.reconcileChannelSecrets(context.Background())
	if phase, _ := connectionStatus(t, dyn); phase == "Error" {
		t.Fatal("a connection that never enabled inbound has nothing to verify and must not error")
	}
}

func TestReconcileAdoptsTelegramSecretAndReRegisters(t *testing.T) {
	var setForm map[string]string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getWebhookInfo"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"url":"https://hub.example/services/providers/agents/webhooks/channels/c1/team-chat/tok"}}`))
		case strings.HasSuffix(r.URL.Path, "/setWebhook"):
			_ = r.ParseForm()
			setForm = map[string]string{"url": r.PostForm.Get("url"), "secret_token": r.PostForm.Get("secret_token")}
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, 404)
		}
	}))
	defer api.Close()
	old := telegramAPIBase
	telegramAPIBase = api.URL
	defer func() { telegramAPIBase = old }()

	b, dyn := reconcileBackground(t,
		withCluster(inboundConnection("telegram", testTGChat)),
		inboundSecret(map[string]string{"token": "123:abc"}),
	)
	b.reconcileChannelSecrets(context.Background())

	stored, err := connectionSigningSecret(context.Background(), dyn, testConn)
	if err != nil {
		t.Fatalf("reading the stored secret: %v", err)
	}
	if len(stored) != 64 {
		t.Fatalf("a 32-byte hex secret should have been stored, got %q", stored)
	}
	if setForm == nil || setForm["secret_token"] != stored || !strings.HasSuffix(setForm["url"], "/c1/team-chat/tok") {
		t.Fatalf("webhook should be re-registered with the stored secret at the existing URL, got %v", setForm)
	}
	if phase, _ := connectionStatus(t, dyn); phase == "Error" {
		t.Fatal("successful adoption must not leave the connection in Error")
	}

	// Second pass: the secret exists, nothing is re-registered.
	setForm = nil
	b.reconcileChannelSecrets(context.Background())
	if setForm != nil {
		t.Fatal("reconcile must be idempotent once the secret is stored")
	}
	if again, _ := connectionSigningSecret(context.Background(), dyn, testConn); again != stored {
		t.Fatal("the stored secret must not be rotated on every pass")
	}
}

func TestReconcileReportsTelegramWebhookGone(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"url":""}}`))
	}))
	defer api.Close()
	old := telegramAPIBase
	telegramAPIBase = api.URL
	defer func() { telegramAPIBase = old }()

	b, dyn := reconcileBackground(t,
		withCluster(inboundConnection("telegram", testTGChat)),
		inboundSecret(map[string]string{"token": "123:abc"}),
	)
	b.reconcileChannelSecrets(context.Background())
	phase, msg := connectionStatus(t, dyn)
	if phase != "Error" || !strings.Contains(msg, "Enable inbound") {
		t.Fatalf("a bot with no registered webhook should tell the user to re-enable inbound, got %q/%q", phase, msg)
	}
	// The secret is stored regardless so Enable inbound registers a verified hook.
	if stored, _ := connectionSigningSecret(context.Background(), dyn, testConn); stored == "" {
		t.Fatal("secret should be stored even when re-registration failed")
	}
}
