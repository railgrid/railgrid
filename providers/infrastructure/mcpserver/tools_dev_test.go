/*
Copyright 2026 The Faros Authors.

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
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/faroshq/provider-infrastructure/kro"
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

// captureHandler records the request callDataPlane synthesizes.
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

func TestCallDataPlaneSynthesizesHubShapedRequest(t *testing.T) {
	h := &captureHandler{}
	ident := identity{tenant: "root:orgs:acme", clusterID: "abc123xyz", user: "dev@acme.io", token: "tok"}

	body, status, err := callDataPlane(context.Background(), h, ident, http.MethodPost, "simplewebapps", "my-site", "app", "sync", []byte(`{"files":[]}`), nil)
	if err != nil {
		t.Fatalf("callDataPlane: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("status/body = %d %q", status, string(body))
	}
	wantPath := "/dataplane/clusters/abc123xyz/simplewebapps/my-site/components/app/sync"
	if h.req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", h.req.URL.Path, wantPath)
	}
	if got := h.req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want caller bearer", got)
	}
	if got := h.req.Header.Get("X-Faros-Tenant"); got != "root:orgs:acme" {
		t.Errorf("X-Faros-Tenant = %q", got)
	}
	if h.body != `{"files":[]}` {
		t.Errorf("body = %q", h.body)
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

func TestPushDevSyncCallsOnlyComponentsWithFiles(t *testing.T) {
	dp := &scriptedDataPlane{status: http.StatusOK, body: `{"phase":"Synced","sourceRevision":3}`}
	ident := identity{tenant: "root:orgs:acme", clusterID: "abc", token: "tok"}
	target := devTarget{resource: "instances", components: devComponentPaths(map[string]string{"backend": "api", "frontend": "web"})}
	routed := routeDevSyncFiles([]devSyncFile{{Path: "web/src/App.jsx", Content: "x"}}, target.components)

	out, err := pushDevSync(context.Background(), dp, ident, target, "my-app", routed, "auto")
	if err != nil {
		t.Fatalf("pushDevSync: %v", err)
	}
	if len(dp.reqs) != 1 || dp.reqs[0].URL.Path != "/dataplane/clusters/abc/instances/my-app/components/frontend/sync" {
		t.Fatalf("sync calls = %d (first %v), want only frontend", len(dp.reqs), dp.reqs)
	}
	if _, called := out["backend"]; called || out["frontend"].Files != 1 {
		t.Fatalf("sync output = %+v, want only frontend with 1 file", out)
	}
	if !strings.Contains(string(out["frontend"].Response), `"sourceRevision":3`) {
		t.Errorf("frontend response = %s, want the agent's sync evidence passed through", out["frontend"].Response)
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
	if req.Method != http.MethodPost || req.URL.Path != "/dataplane/clusters/abc/instances/my-app/components/backend/exec" {
		t.Fatalf("exec request = %s %s", req.Method, req.URL.Path)
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

func TestCallDataPlaneRequiresClusterID(t *testing.T) {
	h := &captureHandler{}
	_, _, err := callDataPlane(context.Background(), h, identity{token: "tok"}, http.MethodGet, "simplewebapps", "x", "app", "log", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "X-Faros-Cluster") {
		t.Fatalf("missing cluster ID must fail with an addressing error, got %v", err)
	}
	if h.req != nil {
		t.Error("handler must not be invoked without a cluster ID")
	}
}

func TestTemplateDevelopmentFromSpec(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.faros.sh/v1alpha1",
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
	node := map[string]kro.TemplateDevelopmentComponent{
		"backend": {WorkspacePath: "api", Toolchain: "node", StartCommand: "npm run dev || npm start"},
	}

	// The failure this guard exists for: correct directory, wrong runtime.
	err := validateDevSyncToolchains(map[string][]devSyncFile{
		"backend": {{Path: "main.go"}, {Path: "go.mod"}, {Path: "Dockerfile"}},
	}, node)
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
	}, node); err != nil {
		t.Errorf("matching source rejected: %v", err)
	}

	// A nested manifest does not make the component runnable.
	if err := validateDevSyncToolchains(map[string][]devSyncFile{
		"backend": {{Path: "vendor/x/package.json"}},
	}, node); err == nil {
		t.Error("nested package.json accepted, want rejection")
	}

	// Unknown toolchains and untouched components must never block a sync.
	if err := validateDevSyncToolchains(map[string][]devSyncFile{
		"backend": {{Path: "main.ex"}},
	}, map[string]kro.TemplateDevelopmentComponent{
		"backend": {WorkspacePath: "api", Toolchain: "elixir"},
	}); err != nil {
		t.Errorf("unknown toolchain blocked the sync: %v", err)
	}
	if err := validateDevSyncToolchains(map[string][]devSyncFile{}, node); err != nil {
		t.Errorf("empty component blocked the sync: %v", err)
	}
}

func TestDevToolchainFromImageToken(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"${faros.devImage.node}", "node"},
		{"  ${faros.devImage.python}  ", "python"},
		{"docker.io/library/node:22-bookworm", ""},
		{"${faros.devAgentImage}", ""},
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
						"devImage":      "${faros.devImage.node}",
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

func (v *verbDataPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, rest, _ := strings.Cut(r.URL.Path, "/components/")
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
