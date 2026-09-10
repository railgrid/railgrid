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

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	fakeUserToken = "user-token"
	fakeMCPToken  = "mcp-token"
)

// fakeHub serves the hub REST endpoints the CLI resolves its tenant from:
// two orgs, the second of which owns workspace "platform" on cluster cl-b.
type fakeHub struct {
	*httptest.Server
	mux *http.ServeMux
	t   *testing.T
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{mux: http.NewServeMux(), t: t}
	h.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth := "Bearer " + fakeUserToken
		if r.URL.Path == "/mcp" {
			wantAuth = "Bearer " + fakeMCPToken
		}
		if got := r.Header.Get("Authorization"); got != wantAuth {
			writeTestStatus(w, http.StatusUnauthorized, "Unauthorized", "bad token "+got)
			return
		}
		h.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(h.Close)

	h.handle("GET /api/orgs", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"items": []map[string]any{
			{"uuid": "org-a", "displayName": "Personal", "personal": true},
			{"uuid": "org-b", "displayName": "Acme"},
		}})
	})
	h.handle("GET /api/orgs/{org}/workspaces", func(w http.ResponseWriter, r *http.Request) {
		org := r.PathValue("org")
		if r.Header.Get("X-Faros-Org") != org {
			writeTestStatus(w, http.StatusBadRequest, "BadRequest", "missing X-Faros-Org")
			return
		}
		items := []map[string]any{{"uuid": "ws-a1", "orgUUID": "org-a", "displayName": "default", "clusterName": "cl-a"}}
		if org == "org-b" {
			items = []map[string]any{
				{"uuid": "ws-b1", "orgUUID": "org-b", "displayName": "platform", "clusterName": "cl-b"},
				{"uuid": "ws-b2", "orgUUID": "org-b", "displayName": "default", "clusterName": "cl-b2"},
			}
		}
		writeTestJSON(w, map[string]any{"items": items})
	})
	h.handle("GET /api/orgs/{org}/workspaces/{ws}/mcpservers/{name}/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Faros-Org") != r.PathValue("org") || r.Header.Get("X-Faros-Workspace") != r.PathValue("ws") {
			writeTestStatus(w, http.StatusBadRequest, "BadRequest", "tenant headers do not match the path")
			return
		}
		writeTestJSON(w, map[string]any{"endpointURL": h.URL + "/mcp", "serverName": "faros", "token": fakeMCPToken, "tokenReady": true})
	})
	return h
}

func (h *fakeHub) handle(pattern string, fn http.HandlerFunc) {
	h.mux.HandleFunc(pattern, fn)
}

// useKubeconfig writes a faros kubeconfig pointing at cluster on the fake hub
// and makes the package-level --kubeconfig use it.
func (h *fakeHub) useKubeconfig(cluster string) string {
	h.t.Helper()
	cfg := clientcmdapi.NewConfig()
	// clientcmd applies user credentials only over TLS.
	cfg.Clusters["faros"] = &clientcmdapi.Cluster{Server: h.URL + "/clusters/" + cluster, InsecureSkipTLSVerify: true}
	cfg.AuthInfos["faros"] = &clientcmdapi.AuthInfo{Token: fakeUserToken}
	cfg.Contexts["faros"] = &clientcmdapi.Context{Cluster: "faros", AuthInfo: "faros"}
	cfg.CurrentContext = "faros"
	path := filepath.Join(h.t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		h.t.Fatal(err)
	}
	prev := kubeconfig
	kubeconfig = path
	h.t.Cleanup(func() { kubeconfig = prev })
	// Belt and braces: NewRootCommand resets the --kubeconfig variable, and
	// the default loading rules must then still land on the fake hub rather
	// than the developer's real kubeconfig.
	h.t.Setenv("KUBECONFIG", path)
	return path
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeTestStatus(w http.ResponseWriter, code int, reason, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": reason, "message": msg, "code": code})
}

func TestNewHubSessionResolvesTenantFromCluster(t *testing.T) {
	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")

	s, err := newHubSession(context.Background(), hubTarget{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Hub != hub.URL || s.Cluster != "cl-b" || s.Org.UUID != "org-b" || s.WS.UUID != "ws-b1" {
		t.Fatalf("got hub=%s cluster=%s org=%s ws=%s", s.Hub, s.Cluster, s.Org.UUID, s.WS.UUID)
	}
	token, err := s.bearerToken(context.Background())
	if err != nil || token != fakeUserToken {
		t.Fatalf("bearerToken = %q, %v", token, err)
	}
}

func TestNewHubSessionOverrides(t *testing.T) {
	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")

	tests := []struct {
		name        string
		target      hubTarget
		wantWS      string
		wantCluster string
		wantErr     string
	}{
		{name: "workspace by name across orgs", target: hubTarget{workspace: "platform"}, wantWS: "ws-b1", wantCluster: "cl-b"},
		{name: "workspace by uuid retargets cluster", target: hubTarget{workspace: "ws-b2"}, wantWS: "ws-b2", wantCluster: "cl-b2"},
		{name: "org and workspace", target: hubTarget{org: "Personal", workspace: "default"}, wantWS: "ws-a1", wantCluster: "cl-a"},
		{name: "name in two orgs is ambiguous", target: hubTarget{workspace: "default"}, wantErr: "pass --org"},
		{name: "org without the kubeconfig cluster", target: hubTarget{org: "org-a"}, wantErr: "not a workspace"},
		{name: "unknown workspace", target: hubTarget{workspace: "nope"}, wantErr: `no workspace matches "nope"`},
		{name: "unknown org", target: hubTarget{org: "nope"}, wantErr: `no organization matches "nope"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := newHubSession(context.Background(), tc.target)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s.WS.UUID != tc.wantWS || s.Cluster != tc.wantCluster {
				t.Fatalf("ws=%s cluster=%s, want %s %s", s.WS.UUID, s.Cluster, tc.wantWS, tc.wantCluster)
			}
		})
	}
}

func TestHubSessionDoSendsTenantHeadersAndDecodesStatus(t *testing.T) {
	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")
	hub.handle("GET /probe", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]string{"org": r.Header.Get("X-Faros-Org"), "ws": r.Header.Get("X-Faros-Workspace")})
	})
	hub.handle("POST /fail", func(w http.ResponseWriter, r *http.Request) {
		writeTestStatus(w, http.StatusConflict, "Conflict", "an assistant turn owns the project")
	})

	s, err := newHubSession(context.Background(), hubTarget{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := s.do(context.Background(), http.MethodGet, hub.URL+"/probe", nil, &got); err != nil {
		t.Fatal(err)
	}
	if got["org"] != "org-b" || got["ws"] != "ws-b1" {
		t.Fatalf("tenant headers = %v", got)
	}
	err = s.do(context.Background(), http.MethodPost, hub.URL+"/fail", map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 409: Conflict: an assistant turn owns the project") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeAPIError(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want string
	}{
		{"kube status", 404, `{"kind":"Status","status":"Failure","reason":"NotFound","message":"project \"x\" not found","code":404}`, `HTTP 404: NotFound: project "x" not found`},
		{"reason already in message", 400, `{"kind":"Status","reason":"BadRequest","message":"BadRequest: nope"}`, "HTTP 400: BadRequest: nope"},
		{"error object", 500, `{"error":"boom"}`, "HTTP 500: boom"},
		{"plain text", 409, "production instances have no dev sandbox\n", "HTTP 409: production instances have no dev sandbox"},
		{"empty body", 502, "", "HTTP 502: Bad Gateway"},
		{"unauthorized gets a hint", 401, "no bearer token", "run 'faros login'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := decodeAPIError(http.MethodGet, "/x", tc.code, []byte(tc.body))
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestParseMCPToolResult(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr string
	}{
		{
			name: "sse with json text",
			body: "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"{\\\"phase\\\":\\\"Succeeded\\\"}\"}]}}\n\n",
			want: `{"phase":"Succeeded"}`,
		},
		{
			name: "sse takes the response after a notification",
			body: "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"ok\":true},\"content\":[]}}\n\n",
			want: `{"ok":true}`,
		},
		{
			name: "plain json with text result",
			body: `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hello"}]}}`,
			want: `"hello"`,
		},
		{
			name:    "tool error",
			body:    "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"isError\":true,\"content\":[{\"type\":\"text\",\"text\":\"repository \\\"shop\\\" not found\"}]}}\n\n",
			wantErr: `repository "shop" not found`,
		},
		{
			name:    "json-rpc error",
			body:    `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown tool"}}`,
			wantErr: "MCP error -32602: unknown tool",
		},
		{
			name:    "garbage",
			body:    "<html>",
			wantErr: "no JSON-RPC result",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMCPToolResult([]byte(tc.body))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMCPCallToolOverSSE(t *testing.T) {
	hub := newFakeHub(t)
	hub.useKubeconfig("cl-b")
	hub.handle("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			http.Error(w, "missing Accept", http.StatusNotAcceptable)
			return
		}
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result map[string]any
		switch req.Params.Name {
		case "code__commit_files":
			text, _ := json.Marshal(map[string]any{"phase": "Succeeded", "commitSHA": "abc123", "echo": req.Params.Arguments})
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}
		default:
			result = map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "tool " + req.Params.Name + " failed"}}}
		}
		msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
	})

	s, err := newHubSession(context.Background(), hubTarget{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.newMCPClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.callTool(context.Background(), "code__commit_files", map[string]string{"repositoryRef": "shop"})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Phase     string            `json:"phase"`
		CommitSHA string            `json:"commitSHA"`
		Echo      map[string]string `json:"echo"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Phase != "Succeeded" || res.CommitSHA != "abc123" || res.Echo["repositoryRef"] != "shop" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := c.callTool(context.Background(), "code__nope", nil); err == nil || !strings.Contains(err.Error(), "tool code__nope failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnvCommand(t *testing.T) {
	hub := newFakeHub(t)
	path := hub.useKubeconfig("cl-b")

	run := func(args ...string) (string, string) {
		t.Helper()
		root := NewRootCommand()
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetArgs(append(args, "--kubeconfig", path))
		if err := root.Execute(); err != nil {
			t.Fatalf("faros %s: %v", strings.Join(args, " "), err)
		}
		return out.String(), errOut.String()
	}

	out, _ := run("env")
	want := strings.Join([]string{
		"export HUB='" + hub.URL + "'",
		"export CLUSTER='cl-b'",
		"export ORG='org-b'",
		"export WS='ws-b1'",
		"export TOKEN='" + fakeUserToken + "'",
		"export AS='" + hub.URL + "/services/providers/app-studio'",
		"export MCP_URL='" + hub.URL + "/mcp'",
		"export MCP_TOKEN='" + fakeMCPToken + "'",
	}, "\n") + "\n"
	if out != want {
		t.Fatalf("env output:\n%s\nwant:\n%s", out, want)
	}

	out, _ = run("env", "--no-mcp", "--json")
	var v envValues
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if v.Org != "org-b" || v.Workspace != "ws-b1" || v.MCPURL != "" || v.MCPToken != "" {
		t.Fatalf("json env = %+v", v)
	}
}

func TestRenderEnvExportsQuotes(t *testing.T) {
	got := renderEnvExports(envValues{Hub: "h", Token: "it's"}, false)
	if !strings.Contains(got, `export TOKEN='it'\''s'`) || strings.Contains(got, "MCP_URL") {
		t.Fatalf("got %q", got)
	}
}
