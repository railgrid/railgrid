/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	"github.com/railgrid/provider-infrastructure/kro"
	sdkdataplane "github.com/railgrid/provider-sdk/dataplane"
)

// devComponentPaths builds a development contract from name → workspacePath
// for tests that only exercise path routing. Toolchain-contract tests build
// kro.TemplateDevelopmentComponent values directly.
func devComponentPaths(paths map[string]string) map[string]kro.TemplateDevelopmentComponent {
	out := make(map[string]kro.TemplateDevelopmentComponent, len(paths))
	for name, wp := range paths {
		out[name] = kro.TemplateDevelopmentComponent{WorkspacePath: wp}
	}
	return out
}

func TestRouteDevSyncFilesRootComponentReceivesEverything(t *testing.T) {
	files := []devSyncFile{
		{Path: "src/index.js", Content: "a"},
		{Path: "package.json", Content: "b"},
	}
	routed := routeDevSyncFiles(files, devComponentPaths(map[string]string{"app": "."}))
	if len(routed["app"]) != 2 {
		t.Fatalf("app routed %d files, want 2", len(routed["app"]))
	}
	if routed["app"][0].Path != "src/index.js" {
		t.Errorf("root component must keep paths as-is, got %q", routed["app"][0].Path)
	}
}

func TestRouteDevSyncFilesStripsComponentPrefix(t *testing.T) {
	components := devComponentPaths(map[string]string{"backend": "api", "frontend": "web"})
	files := []devSyncFile{
		{Path: "api/index.js", Content: "a"},
		{Path: "web/src/App.jsx", Content: "b"},
		{Path: "README.md", Content: "c"},
		{Path: "apixel/trap.js", Content: "d"}, // prefix of a prefix — must NOT match "api/"
	}
	routed := routeDevSyncFiles(files, components)
	if got := countRoutedDevFiles(routed); got != 2 {
		t.Fatalf("routed %d files, want 2 (README + apixel outside every component)", got)
	}
	if len(routed["backend"]) != 1 || routed["backend"][0].Path != "index.js" {
		t.Errorf("backend routed = %+v, want [index.js]", routed["backend"])
	}
	if len(routed["frontend"]) != 1 || routed["frontend"][0].Path != "src/App.jsx" {
		t.Errorf("frontend routed = %+v, want [src/App.jsx]", routed["frontend"])
	}
}

func TestRequireDevComponentDefaultsWhenSingle(t *testing.T) {
	target := devTarget{components: devComponentPaths(map[string]string{"app": "."})}
	got, err := requireDevComponent(target, "")
	if err != nil || got != "app" {
		t.Fatalf("single-component default = (%q, %v), want (app, nil)", got, err)
	}

	multi := devTarget{components: devComponentPaths(map[string]string{"frontend": "web", "backend": "api"})}
	if _, err := requireDevComponent(multi, ""); err == nil || !strings.Contains(err.Error(), "backend, frontend") {
		t.Errorf("multi-component empty pick must list components, got %v", err)
	}
	if _, err := requireDevComponent(multi, "db"); err == nil || !strings.Contains(err.Error(), "backend, frontend") {
		t.Errorf("unknown component must list components, got %v", err)
	}
}

// serveVerb stands in for the hub front door in tests: it drives an
// http.Handler with the request a VerbCaller would put on the wire — the kube
// path, the verb's own headers and the caller's bearer, set last so nothing in
// the extras can override it.
func serveVerb(h http.Handler, ctx context.Context, token, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	req := httptest.NewRequest(method, path, body).WithContext(ctx)
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// captureHandler records the request callDataPlane puts on the wire.
type captureHandler struct {
	req  *http.Request
	body string
}

func (c *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.req = r
	b, _ := io.ReadAll(r.Body)
	c.body = string(b)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (c *captureHandler) DoVerb(ctx context.Context, token, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	return serveVerb(c, ctx, token, method, path, body, headers)
}

// verbBase is the kube path prefix of this provider's instance verbs in a
// cluster, as the tests expect callDataPlane to spell it.
func verbBase(cluster string) string {
	return "/clusters/" + cluster + "/apis/" + infrav1alpha1.GroupName + "/" + infrav1alpha1.Version + "/"
}

// callDataPlane addresses the verb the one way it exists: the kcp
// custom-subresource path on the front door, component as a query parameter,
// authenticated with the caller's own bearer and nothing else.
func TestCallDataPlaneAddressesTheKubePath(t *testing.T) {
	h := &captureHandler{}
	ident := identity{tenant: "root:orgs:acme", clusterID: "abc123xyz", user: "dev@acme.io", token: "tok"}

	body, status, err := callDataPlane(context.Background(), h, ident, http.MethodPost, "simplewebapps", "my-site", "app", "sync", []byte(`{"files":[]}`), nil)
	if err != nil {
		t.Fatalf("callDataPlane: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("status/body = %d %q", status, string(body))
	}
	wantPath := verbBase("abc123xyz") + "simplewebapps/my-site/sync"
	if h.req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", h.req.URL.Path, wantPath)
	}
	if got := h.req.URL.Query()[sdkdataplane.ComponentQuery]; len(got) != 1 || got[0] != "app" {
		t.Errorf("component query = %v, want [app]", got)
	}
	if got := h.req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want caller bearer", got)
	}
	// The hub-proxy identity headers are not part of a verb: kcp
	// authenticates the bearer itself and stamps its own.
	for _, name := range []string{"X-Railgrid-Tenant", "X-Railgrid-Cluster", "X-Railgrid-User"} {
		if got := h.req.Header.Get(name); got != "" {
			t.Errorf("%s = %q, want none on a verb", name, got)
		}
	}
	if got := h.req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if h.body != `{"files":[]}` {
		t.Errorf("body = %q", h.body)
	}

	// An instance-level verb has no component parameter at all.
	h = &captureHandler{}
	if _, _, err := callDataPlane(context.Background(), h, ident, http.MethodGet, "instances", "my-site", "", "runtime-status", nil, nil); err != nil {
		t.Fatal(err)
	}
	if h.req.URL.RawQuery != "" || h.req.URL.Path != verbBase("abc123xyz")+"instances/my-site/runtime-status" {
		t.Errorf("instance verb = %s?%s, want no query", h.req.URL.Path, h.req.URL.RawQuery)
	}
}

// A component name or verb the grammar refuses never leaves the process as a
// mangled URL.
func TestCallDataPlaneRefusesUnaddressableCoordinates(t *testing.T) {
	h := &captureHandler{}
	ident := identity{clusterID: "abc", token: "tok"}
	for _, tc := range []struct{ name, component, verb string }{
		{"my-app", "../other", "sync"},
		{"my-app", "app", "status"},
		{"", "app", "sync"},
	} {
		if _, _, err := callDataPlane(context.Background(), h, ident, http.MethodPost, "instances", tc.name, tc.component, tc.verb, nil, nil); err == nil {
			t.Errorf("name=%q component=%q verb=%q: want an error", tc.name, tc.component, tc.verb)
		}
	}
	if h.req != nil {
		t.Error("nothing may be sent for an unaddressable coordinate")
	}
}

func TestCallDataPlaneExtraHeadersCannotOverrideIdentity(t *testing.T) {
	h := &captureHandler{}
	ident := identity{tenant: "root:orgs:acme", clusterID: "abc", user: "dev@acme.io", token: "tok"}
	extra := http.Header{"Idempotency-Key": []string{"key-1"}, "Authorization": []string{"Bearer forged"}}
	if _, _, err := callDataPlane(context.Background(), h, ident, http.MethodPost, "instances", "x", "app", "exec", []byte(`{}`), extra); err != nil {
		t.Fatal(err)
	}
	if got := h.req.Header.Get("Idempotency-Key"); got != "key-1" {
		t.Errorf("Idempotency-Key = %q, want key-1", got)
	}
	if got := h.req.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer tok" {
		t.Errorf("Authorization = %v, want only the caller bearer", got)
	}
}

// scriptedDataPlane answers every data-plane call with a fixed status/body
// and records each request.
type scriptedDataPlane struct {
	status int
	body   string
	reqs   []*http.Request
	bodies []string
}

func (s *scriptedDataPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.reqs = append(s.reqs, r)
	s.bodies = append(s.bodies, string(raw))
	w.WriteHeader(s.status)
	_, _ = w.Write([]byte(s.body))
}

func (s *scriptedDataPlane) DoVerb(ctx context.Context, token, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	return serveVerb(s, ctx, token, method, path, body, headers)
}

func TestPushDevSyncCallsOnlyComponentsWithFiles(t *testing.T) {
	dp := &scriptedDataPlane{status: http.StatusOK, body: `{"phase":"Synced","sourceRevision":3}`}
	ident := identity{tenant: "root:orgs:acme", clusterID: "abc", token: "tok"}
	target := devTarget{resource: "instances", components: devComponentPaths(map[string]string{"backend": "api", "frontend": "web"})}
	routed := routeDevSyncFiles([]devSyncFile{{Path: "web/src/App.jsx", Content: "x"}}, target.components)

	out, err := pushDevSync(context.Background(), dp, ident, target, "my-app", routed, "auto")
	if err != nil {
		t.Fatalf("pushDevSync: %v", err)
	}
	if len(dp.reqs) != 1 || dp.reqs[0].URL.Path != verbBase("abc")+"instances/my-app/sync" || dp.reqs[0].URL.Query().Get(sdkdataplane.ComponentQuery) != "frontend" {
		t.Fatalf("sync calls = %d (first %v), want only frontend", len(dp.reqs), dp.reqs)
	}
	if _, called := out["backend"]; called || out["frontend"].Files != 1 {
		t.Fatalf("sync output = %+v, want only frontend with 1 file", out)
	}
	if resp, ok := out["frontend"].Response.(map[string]any); !ok || resp["sourceRevision"] != float64(3) {
		t.Errorf("frontend response = %#v, want the agent's sync evidence passed through as an object", out["frontend"].Response)
	}
}

func TestRunDevExecUsesRunActionAndAppliedRevision(t *testing.T) {
	dp := &scriptedDataPlane{status: http.StatusOK, body: `{"sessionID":"s1","requestID":"key-1","state":"succeeded","exitCode":0,"stdout":"ok\n","sourceRevision":4,"sourceDigest":"abc"}`}
	ident := identity{tenant: "root:orgs:acme", clusterID: "abc", token: "tok"}
	out, err := runDevExec(context.Background(), dp, ident, "instances", "my-app", "backend", devExecInput{
		Argv: []string{"sh", "-c", "npm test"}, Workdir: " src ", TimeoutSeconds: 30, IdempotencyKey: "key-1",
	})
	if err != nil {
		t.Fatalf("runDevExec: %v", err)
	}
	if len(dp.reqs) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(dp.reqs))
	}
	req := dp.reqs[0]
	if req.Method != http.MethodPost || req.URL.Path != verbBase("abc")+"instances/my-app/exec" || req.URL.Query().Get(sdkdataplane.ComponentQuery) != "backend" {
		t.Fatalf("exec request = %s %s?%s", req.Method, req.URL.Path, req.URL.RawQuery)
	}
	if got := req.Header.Get("Idempotency-Key"); got != "key-1" {
		t.Errorf("Idempotency-Key = %q, want key-1", got)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(dp.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["action"] != "run" || sent["workdir"] != "src" || sent["timeoutSeconds"] != float64(30) {
		t.Errorf("exec body = %v, want action run with workdir and timeout", sent)
	}
	if _, has := sent["sourceRevision"]; has {
		t.Errorf("exec body = %v, want no sourceRevision so the applied revision is used", sent)
	}
	if out.Instance != "my-app" || out.Component != "backend" || out.State != "succeeded" || out.ExitCode == nil || *out.ExitCode != 0 || out.Stdout != "ok\n" || out.SourceRevision != 4 || out.Hint != "" {
		t.Errorf("exec output = %+v", out)
	}
}

func TestRunDevExecGeneratesNoKeyAndHintsWhileRunning(t *testing.T) {
	dp := &scriptedDataPlane{status: http.StatusOK, body: `{"sessionID":"s1","requestID":"generated","state":"running"}`}
	ident := identity{clusterID: "abc", token: "tok"}
	out, err := runDevExec(context.Background(), dp, ident, "instances", "my-app", "backend", devExecInput{Argv: []string{"sleep", "100"}})
	if err != nil {
		t.Fatalf("runDevExec: %v", err)
	}
	if got := dp.reqs[0].Header.Get("Idempotency-Key"); got != "" {
		t.Errorf("Idempotency-Key = %q, want none (the provider generates one)", got)
	}
	if !strings.Contains(out.Hint, "generated") || !strings.Contains(out.Hint, "dev_exec") {
		t.Errorf("running hint = %q, want guidance to re-call dev_exec with the returned requestID", out.Hint)
	}
}

func TestRunDevExecRejectsEmptyArgvAndSurfacesErrors(t *testing.T) {
	dp := &scriptedDataPlane{status: http.StatusBadRequest, body: "sourceRevision is required for run: sync first"}
	ident := identity{clusterID: "abc", token: "tok"}
	if _, err := runDevExec(context.Background(), dp, ident, "instances", "x", "app", devExecInput{}); err == nil || !strings.Contains(err.Error(), "argv is required") {
		t.Fatalf("empty argv error = %v", err)
	}
	if len(dp.reqs) != 0 {
		t.Fatal("empty argv reached the data plane")
	}
	if _, err := runDevExec(context.Background(), dp, ident, "instances", "x", "app", devExecInput{Argv: []string{"true"}}); err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "sync first") {
		t.Fatalf("data-plane error = %v, want status and message", err)
	}
}

func TestDevExecToolIsRegisteredAsDestructive(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerDevTools(srv, Deps{}, identity{})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "dev_exec" {
			continue
		}
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint || tool.Annotations.IdempotentHint {
			t.Fatalf("dev_exec annotations = %+v, want destructive and not idempotent", tool.Annotations)
		}
		for _, want := range []string{"PORT", "NOT the app's own environment", "no shell", "sh\",\"-c"} {
			if !strings.Contains(tool.Description, want) && !strings.Contains(strings.ToLower(tool.Description), strings.ToLower(want)) {
				t.Errorf("dev_exec description lacks %q", want)
			}
		}
		return
	}
	t.Fatal("dev_exec is not registered")
}

// The dev agent answers sync and restart with a JSON object. When the output
// field was json.RawMessage the reflected schema said {"type":["null","array"]},
// so every successful dev_sync / dev_restart failed output validation.
func TestDevToolOutputSchemasAcceptAgentObjects(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerDevTools(srv, Deps{}, identity{})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, tool := range tools.Tools {
		if tool.Name != "dev_sync" && tool.Name != "dev_restart" {
			continue
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		responses := findPropertySchemas(schema, "response")
		if len(responses) == 0 {
			t.Fatalf("%s output schema has no response property: %s", tool.Name, raw)
		}
		for _, response := range responses {
			if m, ok := response.(map[string]any); ok && m["type"] != nil {
				t.Errorf("%s output schema constrains response to type %v, want any JSON value: %s", tool.Name, m["type"], raw)
			}
		}
		checked++
	}
	if checked != 2 {
		t.Fatalf("checked %d dev tool schemas, want dev_sync and dev_restart", checked)
	}

	for body, want := range map[string]any{
		`{"phase":"Synced","restarted":true}`: map[string]any{"phase": "Synced", "restarted": true},
		"  restarted\n":                       "restarted",
		"":                                    nil,
	} {
		got, _ := json.Marshal(devAgentResponse([]byte(body)))
		exp, _ := json.Marshal(want)
		if string(got) != string(exp) {
			t.Errorf("devAgentResponse(%q) = %s, want %s", body, got, exp)
		}
	}
}

// findPropertySchemas returns every schema declared for property name at any
// depth of a decoded JSON schema.
func findPropertySchemas(schema any, name string) []any {
	var out []any
	switch v := schema.(type) {
	case map[string]any:
		if props, ok := v["properties"].(map[string]any); ok {
			if s, ok := props[name]; ok {
				out = append(out, s)
			}
		}
		for _, child := range v {
			out = append(out, findPropertySchemas(child, name)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, findPropertySchemas(child, name)...)
		}
	}
	return out
}

func TestCallDataPlaneRequiresClusterID(t *testing.T) {
	h := &captureHandler{}
	_, _, err := callDataPlane(context.Background(), h, identity{token: "tok"}, http.MethodGet, "simplewebapps", "x", "app", "log", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "X-Railgrid-Cluster") {
		t.Fatalf("missing cluster ID must fail with an addressing error, got %v", err)
	}
	if h.req != nil {
		t.Error("handler must not be invoked without a cluster ID")
	}
}

func TestTemplateDevelopmentFromSpec(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
		"kind":       "Template",
		"metadata":   map[string]any{"name": "application"},
		"spec": map[string]any{
			"development": map[string]any{
				"components": map[string]any{
					"frontend": map[string]any{"workspacePath": "web", "imageInput": "frontendImage"},
					"backend":  map[string]any{"workspacePath": "api", "imageInput": "backendImage"},
				},
			},
		},
	}}
	dev := templateDevelopmentFromSpec(u)
	if dev == nil {
		t.Fatal("development block must be projected")
	}
	if dev.Components["frontend"].WorkspacePath != "web" || dev.Components["backend"].WorkspacePath != "api" {
		t.Errorf("components = %+v", dev.Components)
	}

	plain := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"displayName": "db"},
	}}
	if got := templateDevelopmentFromSpec(plain); got != nil {
		t.Errorf("template without development block must project nil, got %+v", got)
	}
}

func TestValidateDevSyncToolchains(t *testing.T) {
	fresh := func(string) bool { return false }
	node := map[string]kro.TemplateDevelopmentComponent{
		"backend": {WorkspacePath: "api", Toolchain: "node", StartCommand: "npm run dev || npm start"},
	}

	// The failure this guard exists for: correct directory, wrong runtime.
	err := validateDevSyncToolchains(map[string][]devSyncFile{
		"backend": {{Path: "main.go"}, {Path: "go.mod"}, {Path: "Dockerfile"}},
	}, node, fresh)
	if err == nil {
		t.Fatal("validateDevSyncToolchains = nil, want an error for Go source in a node component")
	}
	for _, want := range []string{"backend", "node", "api/", "package.json", "npm run dev || npm start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}

	if err := validateDevSyncToolchains(map[string][]devSyncFile{
		"backend": {{Path: "package.json"}, {Path: "server.js"}},
	}, node, fresh); err != nil {
		t.Errorf("matching source rejected: %v", err)
	}

	// A nested manifest does not make the component runnable.
	if err := validateDevSyncToolchains(map[string][]devSyncFile{
		"backend": {{Path: "vendor/x/package.json"}},
	}, node, fresh); err == nil {
		t.Error("nested package.json accepted, want rejection")
	}

	// Unknown toolchains and untouched components must never block a sync.
	if err := validateDevSyncToolchains(map[string][]devSyncFile{
		"backend": {{Path: "main.ex"}},
	}, map[string]kro.TemplateDevelopmentComponent{
		"backend": {WorkspacePath: "api", Toolchain: "elixir"},
	}, fresh); err != nil {
		t.Errorf("unknown toolchain blocked the sync: %v", err)
	}
	if err := validateDevSyncToolchains(map[string][]devSyncFile{}, node, fresh); err != nil {
		t.Errorf("empty component blocked the sync: %v", err)
	}

	// dev_sync is incremental: a partial sync (only server.mjs) into a
	// component that already runs applied source keeps its package.json.
	var asked []string
	running := func(component string) bool { asked = append(asked, component); return true }
	if err := validateDevSyncToolchains(map[string][]devSyncFile{
		"backend": {{Path: "server.mjs"}},
	}, node, running); err != nil {
		t.Errorf("partial sync into an established component rejected: %v", err)
	}
	if len(asked) != 1 || asked[0] != "backend" {
		t.Errorf("established consulted for %v, want only backend", asked)
	}
	// A sync that carries its manifest never needs the status lookup.
	asked = nil
	if err := validateDevSyncToolchains(map[string][]devSyncFile{
		"backend": {{Path: "package.json"}},
	}, node, running); err != nil || len(asked) != 0 {
		t.Errorf("manifest sync = %v with status lookups %v, want no lookup", err, asked)
	}
}

func TestDevSyncEstablishedComponentFromAgentStatus(t *testing.T) {
	ident := identity{tenant: "root:orgs:acme", clusterID: "abc", token: "tok"}
	for name, tc := range map[string]struct {
		response *scriptedResponse
		want     bool
	}{
		"applied source":   {&scriptedResponse{http.StatusOK, `{"configured":true,"running":false,"sourceRevision":3}`}, true},
		"running process":  {&scriptedResponse{http.StatusOK, `{"configured":true,"running":true}`}, true},
		"fresh sandbox":    {&scriptedResponse{http.StatusOK, `{"configured":true,"running":false}`}, false},
		"status unhealthy": {&scriptedResponse{http.StatusBadGateway, "runtime supervisor unavailable"}, false},
		"no status verb":   {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			dp := &verbDataPlane{responses: map[string]scriptedResponse{}}
			if tc.response != nil {
				dp.responses["app/process"] = *tc.response
			}
			agent, _, ok := readDevAgentStatus(context.Background(), dp, ident, "instances", "my-app", "app")
			if got := ok && (agent.SourceRevision > 0 || agent.Running); got != tc.want {
				t.Errorf("established = %v, want %v (status %+v, ok %v)", got, tc.want, agent, ok)
			}
		})
	}
}

func TestDevToolchainFromImageToken(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"${railgrid.devImage.node}", "node"},
		{"  ${railgrid.devImage.python}  ", "python"},
		{"docker.io/library/node:22-bookworm", ""},
		{"${railgrid.devAgentImage}", ""},
		{"", ""},
	} {
		if got := devToolchainFromImageToken(tc.in); got != tc.want {
			t.Errorf("devToolchainFromImageToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// describe_template is where an MCP agent learns what a template's sandbox can
// run. Projecting only workspacePath (as this DTO once did) leaves the agent
// choosing a language blind.
func TestTemplateDevelopmentFromSpecCarriesRuntimeContract(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"development": map[string]any{
				"components": map[string]any{
					"backend": map[string]any{
						"workspacePath": "api",
						"devImage":      "${railgrid.devImage.node}",
						"startCommand":  "npm run dev || npm start",
						"port":          "backend",
					},
				},
			},
		},
	}}
	dev := templateDevelopmentFromSpec(u)
	if dev == nil {
		t.Fatal("templateDevelopmentFromSpec = nil, want a development contract")
	}
	got := dev.Components["backend"]
	want := kro.TemplateDevelopmentComponent{
		WorkspacePath: "api",
		Toolchain:     "node",
		StartCommand:  "npm run dev || npm start",
		Port:          "backend",
	}
	if got != want {
		t.Errorf("backend component = %#v, want %#v", got, want)
	}
}

// verbDataPlane answers data-plane calls per "<component>/<verb>" and records
// every request as "METHOD <component>/<verb>" plus its body.
type verbDataPlane struct {
	responses map[string]scriptedResponse
	calls     []string
	bodies    map[string]string
}

type scriptedResponse struct {
	status int
	body   string
}

func (v *verbDataPlane) DoVerb(ctx context.Context, token, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	return serveVerb(v, ctx, token, method, path, body, headers)
}

func (v *verbDataPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// "<component>/<verb>" from the kube path: the verb is the last segment,
	// the component the query parameter.
	rest := r.URL.Query().Get(sdkdataplane.ComponentQuery) + r.URL.Path[strings.LastIndex(r.URL.Path, "/"):]
	raw, _ := io.ReadAll(r.Body)
	v.calls = append(v.calls, r.Method+" "+rest)
	if v.bodies == nil {
		v.bodies = map[string]string{}
	}
	v.bodies[rest] = string(raw)
	response, ok := v.responses[rest]
	if !ok {
		http.Error(w, "method "+r.Method+" not allowed for verb "+rest, http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(response.status)
	_, _ = w.Write([]byte(response.body))
}

func TestNormalizeDevSyncFiles(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}
	encoded := base64.StdEncoding.EncodeToString(png)
	got, err := normalizeDevSyncFiles([]devSyncFile{
		{Path: "web/a.txt", Content: "a", Encoding: "utf-8"},
		{Path: "web/b.txt", Content: "b"},
		{Path: "web/logo.png", Content: encoded, Encoding: "base64"},
	})
	if err != nil {
		t.Fatalf("normalizeDevSyncFiles: %v", err)
	}
	if got[0].Encoding != "" || got[1].Encoding != "" || got[2].Encoding != "base64" || got[2].Content != encoded {
		t.Fatalf("normalized = %+v, want text without encoding and base64 passed through verbatim", got)
	}

	tooMany := make([]devSyncFile, devSyncMaxFiles+1)
	for i := range tooMany {
		tooMany[i] = devSyncFile{Path: "f", Content: "x"}
	}
	half := strings.Repeat("a", devSyncMaxBytes/2)
	for name, tc := range map[string]struct {
		files []devSyncFile
		want  string
	}{
		"unknown encoding":  {[]devSyncFile{{Path: "a.bin", Content: "00", Encoding: "hex"}}, "unsupported encoding"},
		"invalid base64":    {[]devSyncFile{{Path: "a.bin", Content: "@@@@", Encoding: "base64"}}, "invalid base64"},
		"line breaks":       {[]devSyncFile{{Path: "a.bin", Content: "AAAA\nAAAA", Encoding: "base64"}}, "line breaks"},
		"binary file bytes": {[]devSyncFile{{Path: "a.glb", Content: base64.StdEncoding.EncodeToString(make([]byte, devSyncMaxFileBytes+1)), Encoding: "base64"}}, "per-file limit"},
		"decoded total":     {[]devSyncFile{{Path: "a", Content: half}, {Path: "b", Content: half}, {Path: "c", Content: "x"}}, "limit (decoded)"},
		"file count":        {tooMany, "file limit"},
	} {
		if _, err := normalizeDevSyncFiles(tc.files); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	// The cap is on decoded bytes: base64 expansion alone must not reject a
	// payload whose decoded size fits.
	fits := base64.StdEncoding.EncodeToString(make([]byte, devSyncMaxFileBytes))
	if _, err := normalizeDevSyncFiles([]devSyncFile{{Path: "a.glb", Content: fits, Encoding: "base64"}, {Path: "b.txt", Content: strings.Repeat("b", devSyncMaxBytes-devSyncMaxFileBytes)}}); err != nil {
		t.Fatalf("payload at the decoded limit rejected: %v", err)
	}
}

func TestDevSyncBinaryFilesAreGatedOnAgentSyncEncodings(t *testing.T) {
	ident := identity{tenant: "root:orgs:acme", clusterID: "abc", token: "tok"}
	target := devTarget{resource: "instances", components: devComponentPaths(map[string]string{"api": "api", "web": "web"})}
	logo := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 0x00, 0xff})
	files, err := normalizeDevSyncFiles([]devSyncFile{
		{Path: "web/src/App.jsx", Content: "export default 1"},
		{Path: "web/public/logo.png", Content: logo, Encoding: "base64"},
		{Path: "api/index.js", Content: "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	routed := routeDevSyncFiles(files, target.components)

	t.Run("supported", func(t *testing.T) {
		dp := &verbDataPlane{responses: map[string]scriptedResponse{
			"web/process": {http.StatusOK, `{"configured":true,"syncEncodings":["utf-8","base64"]}`},
			"web/sync":    {http.StatusOK, `{"phase":"Synced"}`},
			"api/sync":    {http.StatusOK, `{"phase":"Synced"}`},
		}}
		if err := requireDevSyncEncodings(context.Background(), dp, ident, target, "my-app", routed); err != nil {
			t.Fatalf("requireDevSyncEncodings: %v", err)
		}
		if len(dp.calls) != 1 || dp.calls[0] != "GET web/process" {
			t.Fatalf("status calls = %v, want only web (api has no binary files)", dp.calls)
		}
		if _, err := pushDevSync(context.Background(), dp, ident, target, "my-app", routed, "auto"); err != nil {
			t.Fatalf("pushDevSync: %v", err)
		}
		var sent devSandboxRequest
		if err := json.Unmarshal([]byte(dp.bodies["web/sync"]), &sent); err != nil {
			t.Fatal(err)
		}
		want := []devSyncFile{{Path: "src/App.jsx", Content: "export default 1"}, {Path: "public/logo.png", Content: logo, Encoding: "base64"}}
		if len(sent.Files) != 2 || sent.Files[0] != want[0] || sent.Files[1] != want[1] {
			t.Fatalf("web sync files = %+v, want %+v", sent.Files, want)
		}
		if strings.Contains(dp.bodies["api/sync"], "encoding") {
			t.Errorf("text-only sync carries an encoding field: %s", dp.bodies["api/sync"])
		}
	})

	for name, response := range map[string]*scriptedResponse{
		"old agent":        {http.StatusOK, `{"configured":true,"running":true}`},
		"no status verb":   nil,
		"status unhealthy": {http.StatusBadGateway, "runtime supervisor unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			dp := &verbDataPlane{responses: map[string]scriptedResponse{}}
			if response != nil {
				dp.responses["web/process"] = *response
			}
			err := requireDevSyncEncodings(context.Background(), dp, ident, target, "my-app", routed)
			if err == nil || !strings.Contains(err.Error(), "web/public/logo.png") || !strings.Contains(err.Error(), `component "web"`) || !strings.Contains(err.Error(), "nothing was synced") {
				t.Fatalf("err = %v, want a refusal naming web/public/logo.png", err)
			}
			if strings.Contains(err.Error(), "App.jsx") {
				t.Errorf("err names text files that could be sent: %v", err)
			}
			for _, call := range dp.calls {
				if strings.HasSuffix(call, "/sync") {
					t.Fatalf("a sync was sent despite the refusal: %v", dp.calls)
				}
			}
		})
	}

	t.Run("text only never checks status", func(t *testing.T) {
		dp := &verbDataPlane{}
		textOnly := routeDevSyncFiles([]devSyncFile{{Path: "web/a.txt", Content: "a"}}, target.components)
		if err := requireDevSyncEncodings(context.Background(), dp, ident, target, "my-app", textOnly); err != nil || len(dp.calls) != 0 {
			t.Fatalf("text-only gating = %v with calls %v, want no status call", err, dp.calls)
		}
	})
}
