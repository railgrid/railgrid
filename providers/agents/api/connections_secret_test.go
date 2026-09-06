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
// read the gateway could not answer never reaches the apply: the apply would
// carry only the updates and drop every key already stored.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"sigs.k8s.io/yaml"

	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/llm"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tenant"
)

// fakeGateway is a GraphQL gateway that answers Secret reads however a test
// asks and records every applyYaml it receives.
type fakeGateway struct {
	mu sync.Mutex
	// getErr, when set, is returned as a GraphQL error for a Secret read.
	getErr string
	// secret is the Secret returned for a read when getErr is empty.
	secret map[string]string
	// applied holds the StringData of each applyYaml the gateway saw.
	applied []map[string]string
	// gets counts Secret reads.
	gets int
	// connType, when set, makes the gateway answer Connection reads with a
	// connection of that spec.type.
	connType string
}

func (g *fakeGateway) applies() []map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]string(nil), g.applied...)
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "application/json")

	g.mu.Lock()
	defer g.mu.Unlock()

	switch {
	case strings.Contains(body.Query, "applyYaml"):
		var obj map[string]any
		_ = yaml.Unmarshal([]byte(body.Variables["yaml"].(string)), &obj)
		data := map[string]string{}
		if sd, ok := obj["stringData"].(map[string]any); ok {
			for k, v := range sd {
				data[k], _ = v.(string)
			}
		}
		g.applied = append(g.applied, data)
		out, _ := yaml.Marshal(obj)
		_, _ = w.Write([]byte(`{"data":{"applyYaml":` + jsonString(string(out)) + `}}`))

	case strings.Contains(body.Query, "ConnectionYaml"):
		obj := map[string]any{
			"apiVersion": "agents.faros.sh/v1alpha1",
			"kind":       "Connection",
			"metadata":   map[string]any{"name": testSecretConn},
			"spec":       map[string]any{"type": g.connType, "channel": "C123"},
			"status":     map[string]any{},
		}
		y, _ := yaml.Marshal(obj)
		_, _ = w.Write([]byte(`{"data":{"agents_faros_sh":{"v1alpha1":{"ConnectionYaml":` +
			jsonString(string(y)) + `}}}}`))

	case strings.Contains(body.Query, "applyStatusYaml"):
		_, _ = w.Write([]byte(`{"data":{"applyStatusYaml":"ok"}}`))

	case strings.Contains(body.Query, "SecretYaml"):
		g.gets++
		if g.getErr != "" {
			_, _ = w.Write([]byte(`{"errors":[{"message":` + jsonString(g.getErr) + `}]}`))
			return
		}
		// A stored Secret comes back with base64 `data`, which is what the
		// merge reads; `stringData` is write-only.
		enc := map[string]any{}
		for k, v := range g.secret {
			enc[k] = base64.StdEncoding.EncodeToString([]byte(v))
		}
		obj := map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      connectionSecretName(testSecretConn),
				"namespace": llm.SecretNamespace,
			},
			"type": "Opaque",
			"data": enc,
		}
		y, _ := yaml.Marshal(obj)
		_, _ = w.Write([]byte(`{"data":{"v1":{"SecretYaml":` + jsonString(string(y)) + `}}}`))

	default:
		_, _ = w.Write([]byte(`{"errors":[{"message":"unexpected query"}]}`))
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

const testSecretConn = "team-chat"

func gatewayClient(t *testing.T, g *fakeGateway) *agentsclient.Client {
	t.Helper()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	scope, err := tenant.NewGraphQLClient(srv.URL, false).For("c1", "test-token")
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	return agentsclient.NewFromGraphQL(scope)
}

// A read the gateway could not answer must abort the merge. Falling through
// with an empty map would apply only the update, dropping the bot token and the
// OAuth pair the Secret already held.
func TestMergeConnectionSecretAbortsOnUnreadableSecret(t *testing.T) {
	g := &fakeGateway{getErr: "connection refused talking to the workspace"}
	c := gatewayClient(t, g)

	err := mergeConnectionSecret(context.Background(), c, testSecretConn,
		map[string]string{signingSecretKey: "s3cr3t"})
	if err == nil {
		t.Fatal("want an error when the existing Secret cannot be read, got nil")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error should carry the read failure, got %v", err)
	}
	if applied := g.applies(); len(applied) != 0 {
		t.Fatalf("nothing may be written when the read failed, applied %v", applied)
	}
}

// NotFound is the one error that legitimately means "no Secret yet": the merge
// proceeds and creates it from the updates alone.
func TestMergeConnectionSecretCreatesWhenAbsent(t *testing.T) {
	g := &fakeGateway{getErr: "secrets \"faros-agents-conn-team-chat\" not found"}
	c := gatewayClient(t, g)

	if err := mergeConnectionSecret(context.Background(), c, testSecretConn,
		map[string]string{signingSecretKey: "s3cr3t"}); err != nil {
		t.Fatalf("NotFound must be treated as empty, got %v", err)
	}
	applied := g.applies()
	if len(applied) != 1 {
		t.Fatalf("want one apply, got %d", len(applied))
	}
	if applied[0][signingSecretKey] != "s3cr3t" {
		t.Fatalf("apply should carry the update, got %v", applied[0])
	}
}

// The documented behaviour: a successful read means every key the merge does
// not mention survives it.
func TestMergeConnectionSecretKeepsUnmentionedKeys(t *testing.T) {
	g := &fakeGateway{secret: map[string]string{
		"token":         "xoxb-existing",
		"client_id":     "cid",
		"client_secret": "csec",
	}}
	c := gatewayClient(t, g)

	if err := mergeConnectionSecret(context.Background(), c, testSecretConn,
		map[string]string{signingSecretKey: "s3cr3t"}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	applied := g.applies()
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

// enableInboundOn drives the handler against the fake gateway.
func enableInboundOn(t *testing.T, g *fakeGateway) *httptest.ResponseRecorder {
	t.Helper()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	s := &Server{
		cfg:   Config{WebhookKey: "unit-test-webhook-key"},
		store: store.NewMemoryStore(),
		gql:   tenant.NewGraphQLClient(srv.URL, false),
	}
	r := httptest.NewRequest(http.MethodPost, "/connections/"+testSecretConn+"/inbound",
		strings.NewReader(`{"publicBaseURL":"https://agents.example.test"}`))
	r.Header.Set("X-Faros-Tenant", "root:faros:tenants:org1:ws1")
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
	g := &fakeGateway{connType: "slack", getErr: "connection refused talking to the workspace"}
	w := enableInboundOn(t, g)

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
	g := &fakeGateway{connType: "telegram", getErr: "connection refused talking to the workspace"}
	w := enableInboundOn(t, g)

	if w.Code < 500 {
		t.Fatalf("want a server-side failure, got %d: %s", w.Code, w.Body.String())
	}
	for _, applied := range g.applies() {
		if _, ok := applied[signingSecretKey]; ok {
			t.Fatalf("a signing secret was written despite the failed read: %v", applied)
		}
	}
}
