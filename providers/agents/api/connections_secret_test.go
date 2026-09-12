// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Tests for the read-before-merge path that owns connection credentials. The
// merge sends the Secret's whole StringData, so what these assert is that a
// read the workspace could not answer never reaches the apply: the apply would
// carry only the updates and drop every key already stored.

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tenant"
	"github.com/faroshq/provider-agents/tenant/tenanttest"
)

const testSecretConn = "team-chat"

// secretReadError makes every Secret read fail with an internal error
// carrying msg — the shape a proxy or kcp outage takes at the caller.
func secretReadError(msg string) func(tenanttest.Request) *metav1.Status {
	return func(req tenanttest.Request) *metav1.Status {
		if req.Verb == "get" && req.GVR == agentsclient.SecretGVR {
			st := apierrors.NewInternalError(errors.New(msg)).ErrStatus
			return &st
		}
		return nil
	}
}

// storedSecret seeds the connection Secret with data, base64-encoded under
// `data` the way the API server returns it; `stringData` is write-only.
func storedSecret(data map[string]string) *unstructured.Unstructured {
	enc := map[string]any{}
	for k, v := range data {
		enc[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      connectionSecretName(testSecretConn),
			"namespace": llm.SecretNamespace,
		},
		"type": "Opaque",
		"data": enc,
	}}
}

func storedConnection(connType string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agents.faros.sh/v1alpha1",
		"kind":       "Connection",
		"metadata":   map[string]any{"name": testSecretConn},
		"spec":       map[string]any{"type": connType, "channel": "C123"},
		"status":     map[string]any{},
	}}
}

// appliedSecrets returns the StringData of every Secret write the workspace
// accepted, in order.
func appliedSecrets(srv *tenanttest.Server) []map[string]string {
	var out []map[string]string
	for _, w := range srv.Writes() {
		if w.GVR != agentsclient.SecretGVR || w.Object == nil {
			continue
		}
		data := map[string]string{}
		sd, _, _ := unstructured.NestedStringMap(w.Object.Object, "stringData")
		for k, v := range sd {
			data[k] = v
		}
		out = append(out, data)
	}
	return out
}

func workspaceClient(t *testing.T, ws *tenanttest.Server) *agentsclient.Client {
	t.Helper()
	srv := httptest.NewServer(ws)
	t.Cleanup(srv.Close)
	scope, err := tenant.NewClient(srv.URL, false).For("c1", "test-token")
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	return agentsclient.NewFromScope(scope)
}

// A read the workspace could not answer must abort the merge. Falling through
// with an empty map would apply only the update, dropping the bot token and the
// OAuth pair the Secret already held.
func TestMergeConnectionSecretAbortsOnUnreadableSecret(t *testing.T) {
	ws := tenanttest.New()
	ws.Add(storedSecret(map[string]string{"token": "xoxb-existing"}))
	ws.Intercept = secretReadError("connection refused talking to the workspace")
	c := workspaceClient(t, ws)

	err := mergeConnectionSecret(context.Background(), c, testSecretConn,
		map[string]string{signingSecretKey: "s3cr3t"})
	if err == nil {
		t.Fatal("want an error when the existing Secret cannot be read, got nil")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error should carry the read failure, got %v", err)
	}
	if applied := appliedSecrets(ws); len(applied) != 0 {
		t.Fatalf("nothing may be written when the read failed, applied %v", applied)
	}
}

// NotFound is the one error that legitimately means "no Secret yet": the merge
// proceeds and creates it from the updates alone.
func TestMergeConnectionSecretCreatesWhenAbsent(t *testing.T) {
	ws := tenanttest.New()
	c := workspaceClient(t, ws)

	if err := mergeConnectionSecret(context.Background(), c, testSecretConn,
		map[string]string{signingSecretKey: "s3cr3t"}); err != nil {
		t.Fatalf("NotFound must be treated as empty, got %v", err)
	}
	applied := appliedSecrets(ws)
	if len(applied) != 1 {
		t.Fatalf("want one apply, got %d", len(applied))
	}
	if applied[0][signingSecretKey] != "s3cr3t" {
		t.Fatalf("apply should carry the update, got %v", applied[0])
	}
	if got := ws.Get(agentsclient.SecretGVR, llm.SecretNamespace, connectionSecretName(testSecretConn)); got == nil {
		t.Fatal("the Secret should exist in the workspace after the merge")
	}
}

// The documented behaviour: a successful read means every key the merge does
// not mention survives it.
func TestMergeConnectionSecretKeepsUnmentionedKeys(t *testing.T) {
	ws := tenanttest.New()
	ws.Add(storedSecret(map[string]string{
		"token":         "xoxb-existing",
		"client_id":     "cid",
		"client_secret": "csec",
	}))
	c := workspaceClient(t, ws)

	if err := mergeConnectionSecret(context.Background(), c, testSecretConn,
		map[string]string{signingSecretKey: "s3cr3t"}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	applied := appliedSecrets(ws)
	if len(applied) != 1 {
		t.Fatalf("want one apply, got %d", len(applied))
	}
	for k, want := range map[string]string{
		"token":          "xoxb-existing",
		"client_id":      "cid",
		"client_secret":  "csec",
		signingSecretKey: "s3cr3t",
	} {
		if applied[0][k] != want {
			t.Fatalf("key %q: want %q, got %q (full: %v)", k, want, applied[0][k], applied[0])
		}
	}
}

// enableInboundOn drives the handler against the fake workspace.
func enableInboundOn(t *testing.T, ws *tenanttest.Server) *httptest.ResponseRecorder {
	t.Helper()
	srv := httptest.NewServer(ws)
	t.Cleanup(srv.Close)
	s := &Server{
		cfg:        Config{WebhookKey: "unit-test-webhook-key"},
		store:      store.NewMemoryStore(),
		tenant:     tenant.NewClient(srv.URL, false),
		workspaces: staticWorkspaces{"c1": {ClusterID: "c1", Path: "root:faros:tenants:org1:ws1", OrgUUID: "org1", WorkspaceUUID: "ws1"}}.lookup,
	}
	r := httptest.NewRequest(http.MethodPost, "/connections/"+testSecretConn+"/inbound",
		strings.NewReader(`{"publicBaseURL":"https://agents.example.test"}`))
	// The hub identifies the tenant by cluster ID in both headers.
	r.Header.Set("X-Faros-Tenant", "c1")
	r.Header.Set("X-Faros-Cluster", "c1")
	r.Header.Set("Authorization", "Bearer test-token")
	r.SetPathValue("name", testSecretConn)
	w := httptest.NewRecorder()
	s.enableInbound(w, r)
	return w
}

// A Secret read that failed for any reason other than NotFound must stop
// enableInbound. Treating it as "no secret stored" would reject a correctly
// configured Slack connection as missing its signing secret.
func TestEnableInboundSlackFailsClosedOnUnreadableSecret(t *testing.T) {
	ws := tenanttest.New()
	ws.Add(storedConnection("slack"))
	ws.Intercept = secretReadError("connection refused talking to the workspace")
	w := enableInboundOn(t, ws)

	if w.Code == http.StatusBadRequest {
		t.Fatalf("an unreadable Secret must not be reported as a missing signing secret: %s", w.Body.String())
	}
	if w.Code < 500 {
		t.Fatalf("want a server-side failure, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Signing Secret") {
		t.Fatalf("response should describe the read failure, not the Slack setup step: %s", w.Body.String())
	}
}

// The same read failure on Telegram: the handler must not mint and store a new
// secret token over whatever the Secret already holds, because deliveries in
// flight still carry the old one.
func TestEnableInboundTelegramDoesNotOverwriteSecretItCouldNotRead(t *testing.T) {
	ws := tenanttest.New()
	ws.Add(storedConnection("telegram"))
	ws.Intercept = secretReadError("connection refused talking to the workspace")
	w := enableInboundOn(t, ws)

	if w.Code < 500 {
		t.Fatalf("want a server-side failure, got %d: %s", w.Code, w.Body.String())
	}
	for _, applied := range appliedSecrets(ws) {
		if _, ok := applied[signingSecretKey]; ok {
			t.Fatalf("a signing secret was written despite the failed read: %v", applied)
		}
	}
}
