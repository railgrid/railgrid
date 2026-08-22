/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReloadRulesFromEnv(t *testing.T) {
	rules, err := reloadRulesFromEnv(`[{"paths":["package.json","*.lock"],"command":"npm install"}]`)
	if err != nil {
		t.Fatalf("reloadRulesFromEnv: %v", err)
	}
	if len(rules) != 1 || rules[0].Command != "npm install" {
		t.Fatalf("rules = %+v", rules)
	}

	if _, err := reloadRulesFromEnv(`[{"paths":[],"command":"x"}]`); err == nil {
		t.Error("expected error for a rule without paths")
	}
	if _, err := reloadRulesFromEnv(`{"not":"a list"}`); err == nil {
		t.Error("expected error for non-list rules")
	}
	if rules, err := reloadRulesFromEnv(""); err != nil || rules != nil {
		t.Errorf("empty env: rules=%v err=%v, want nil/nil", rules, err)
	}
}

func TestRunHealthcheckUsesContainerLocalTCPAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := runHealthcheck(address); err != nil {
		t.Fatalf("runHealthcheck(%q): %v", address, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runHealthcheck(address); err == nil {
		t.Fatalf("runHealthcheck(%q) succeeded after listener closed", address)
	}
	if err := runHealthcheck(""); err == nil {
		t.Fatal("runHealthcheck accepted an empty address")
	}
}

func TestBootstrapControlTokenUsesOnlyNamedSecret(t *testing.T) {
	var requests []*http.Request
	var patchBody []byte
	client := &http.Client{Transport: actionsTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r)
		if r.Method == http.MethodGet {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"metadata":{"name":"control","resourceVersion":"1"},"data":{}}`)),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}
		var err error
		patchBody, err = io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: r}, nil
	})}
	if err := bootstrapControlToken(context.Background(), client, "https://kubernetes.default.svc:443", "tenant-a", "control", "service-account"); err != nil {
		t.Fatalf("bootstrapControlToken: %v", err)
	}
	if len(requests) != 2 || requests[0].Method != http.MethodGet || requests[1].Method != http.MethodPatch {
		t.Fatalf("requests = %v, want GET then PATCH", requests)
	}
	wantURL := "https://kubernetes.default.svc:443/api/v1/namespaces/tenant-a/secrets/control"
	for _, request := range requests {
		if request.URL.String() != wantURL {
			t.Errorf("request URL = %q, want %q", request.URL, wantURL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer service-account" {
			t.Errorf("authorization = %q", got)
		}
	}
	if got := requests[1].Header.Get("Content-Type"); got != "application/merge-patch+json" {
		t.Fatalf("patch content type = %q", got)
	}
	var document struct {
		Metadata   map[string]string `json:"metadata"`
		Immutable  bool              `json:"immutable"`
		StringData map[string]string `json:"stringData"`
	}
	if err := json.Unmarshal(patchBody, &document); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if document.Metadata["resourceVersion"] != "1" || !document.Immutable {
		t.Fatalf("patch precondition/immutability = %#v/%v, want resourceVersion 1 and immutable=true", document.Metadata, document.Immutable)
	}
	if token := document.StringData["token"]; len(token) != 64 || strings.Trim(token, "0123456789abcdef") != "" {
		t.Fatalf("generated token = %q, want 64 lowercase hex characters", token)
	}
}

func TestBootstrapControlTokenDoesNotPatchImmutableExistingSecret(t *testing.T) {
	patches := 0
	client := &http.Client{Transport: actionsTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPatch {
			patches++
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"metadata":{"resourceVersion":"7"},"immutable":true,"data":{"token":"already-set"}}`)), Header: make(http.Header), Request: r}, nil
	})}
	if err := bootstrapControlToken(context.Background(), client, "https://kubernetes.default.svc:443", "tenant-a", "control", "service-account"); err != nil {
		t.Fatalf("bootstrapControlToken: %v", err)
	}
	if patches != 0 {
		t.Fatalf("existing control token triggered %d patch requests, want none", patches)
	}
}

func TestBootstrapControlTokenSealsExistingMutableSecretWithoutRotation(t *testing.T) {
	var patchBody []byte
	client := &http.Client{Transport: actionsTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"metadata":{"resourceVersion":"7"},"data":{"token":"already-set"}}`)), Header: make(http.Header), Request: r}, nil
		}
		var err error
		patchBody, err = io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: r}, nil
	})}
	if err := bootstrapControlToken(context.Background(), client, "https://kubernetes.default.svc:443", "tenant-a", "control", "service-account"); err != nil {
		t.Fatalf("bootstrapControlToken: %v", err)
	}
	var document struct {
		Metadata  map[string]string `json:"metadata"`
		Immutable bool              `json:"immutable"`
		Data      map[string]string `json:"stringData"`
	}
	if err := json.Unmarshal(patchBody, &document); err != nil {
		t.Fatalf("decode seal patch: %v", err)
	}
	if document.Metadata["resourceVersion"] != "7" || !document.Immutable || len(document.Data) != 0 {
		t.Fatalf("seal patch = %#v, want only immutable=true with resourceVersion 7", document)
	}
}

func TestBootstrapControlTokenConcurrentWritersFirstPatchWins(t *testing.T) {
	type patchDocument struct {
		Metadata   map[string]string `json:"metadata"`
		Immutable  bool              `json:"immutable"`
		StringData map[string]string `json:"stringData"`
	}
	type secretState struct {
		sync.Mutex
		token       string
		resourceVer string
		immutable   bool
		gets        int
		patches     []patchDocument
		getsReady   chan struct{}
		releaseGets chan struct{}
	}
	state := &secretState{
		resourceVer: "1",
		getsReady:   make(chan struct{}),
		releaseGets: make(chan struct{}),
	}
	client := &http.Client{Transport: actionsTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			state.Lock()
			state.gets++
			if state.gets == 2 {
				close(state.getsReady)
			}
			token, resourceVersion, immutable := state.token, state.resourceVer, state.immutable
			state.Unlock()
			data := "{}"
			if token != "" {
				data = `{"token":"` + token + `"}`
			}
			body := `{"metadata":{"resourceVersion":"` + resourceVersion + `"},"immutable":` + fmt.Sprint(immutable) + `,"data":` + data + `}`
			<-state.releaseGets
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
		case http.MethodPatch:
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			var document patchDocument
			if err := json.Unmarshal(raw, &document); err != nil {
				return nil, err
			}
			state.Lock()
			state.patches = append(state.patches, document)
			if document.Metadata["resourceVersion"] != state.resourceVer || state.token != "" {
				state.Unlock()
				return &http.Response{StatusCode: http.StatusConflict, Status: "409 Conflict", Body: io.NopCloser(strings.NewReader(`{"reason":"Conflict"}`)), Header: make(http.Header), Request: r}, nil
			}
			state.token = document.StringData["token"]
			state.immutable = document.Immutable
			state.resourceVer = "2"
			state.Unlock()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: r}, nil
		default:
			return nil, fmt.Errorf("unexpected method %s", r.Method)
		}
	})}

	errs := make(chan error, 2)
	for range 2 {
		go func() {
			errs <- bootstrapControlToken(context.Background(), client, "https://kubernetes.default.svc:443", "tenant-a", "control", "service-account")
		}()
	}
	<-state.getsReady
	close(state.releaseGets)
	var successes, conflicts int
	for range 2 {
		if err := <-errs; err == nil {
			successes++
		} else if strings.Contains(err.Error(), "Conflict") {
			conflicts++
		} else {
			t.Fatalf("unexpected bootstrap error: %v", err)
		}
	}
	state.Lock()
	defer state.Unlock()
	if successes != 1 || conflicts != 1 || len(state.patches) != 2 {
		t.Fatalf("concurrent results = successes %d conflicts %d patches %d, want 1/1/2", successes, conflicts, len(state.patches))
	}
	if len(state.token) != 64 || !state.immutable || state.resourceVer != "2" {
		t.Fatalf("secret state = token length %d immutable %v resourceVersion %q, want 64/true/2", len(state.token), state.immutable, state.resourceVer)
	}
	for _, patch := range state.patches {
		if patch.Metadata["resourceVersion"] != "1" || !patch.Immutable || len(patch.StringData["token"]) != 64 {
			t.Fatalf("concurrent patch = %#v, want resourceVersion 1, immutable=true, generated token", patch)
		}
	}
}

func TestBootstrapControlTokenRedactsSecretResponseBodies(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*http.Request) (*http.Response, error)
	}{
		{
			name: "get",
			fn: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Status:     "403 Forbidden",
					Body:       io.NopCloser(strings.NewReader(`{"reason":"Forbidden","message":"token=super-secret"}`)),
					Header:     make(http.Header), Request: r,
				}, nil
			},
		},
		{
			name: "patch",
			fn: func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"metadata":{"name":"control","resourceVersion":"1"},"data":{}}`)),
						Header:     make(http.Header), Request: r,
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusConflict,
					Status:     "409 Conflict",
					Body:       io.NopCloser(strings.NewReader(`{"reason":"Conflict","message":"token=super-secret"}`)),
					Header:     make(http.Header), Request: r,
				}, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: actionsTestRoundTripper(test.fn)}
			err := bootstrapControlToken(context.Background(), client, "https://kubernetes.default.svc:443", "tenant-a", "control", "service-account")
			if err == nil {
				t.Fatal("bootstrapControlToken succeeded, want Kubernetes API error")
			}
			if !strings.Contains(err.Error(), "reason") || !strings.Contains(err.Error(), "Forbidden") && !strings.Contains(err.Error(), "Conflict") {
				t.Fatalf("error = %q, want status and safe reason", err)
			}
			if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "token=") {
				t.Fatalf("error leaked Secret response body: %q", err)
			}
		})
	}
}

func TestMatchReloadRules(t *testing.T) {
	rules := []reloadRule{
		{Paths: []string{"package.json", "package-lock.json"}, Command: "npm install"},
		{Paths: []string{"requirements*.txt"}, Command: "pip install -r requirements.txt"},
		{Paths: []string{"migrations/*.sql"}, Command: "make migrate"},
	}

	for _, tc := range []struct {
		changed []string
		want    []string
	}{
		{[]string{"src/app.js"}, nil},
		{[]string{"package.json"}, []string{"npm install"}},
		// Basename matching: a slash-less pattern matches nested paths.
		{[]string{"web/package.json"}, []string{"npm install"}},
		{[]string{"requirements-dev.txt", "package.json"}, []string{"npm install", "pip install -r requirements.txt"}},
		// Slashed patterns match the relative path only.
		{[]string{"migrations/001.sql"}, []string{"make migrate"}},
		{[]string{"other/migrations/001.sql"}, nil},
	} {
		got := matchReloadRules(rules, tc.changed)
		if !slices.Equal(got, tc.want) {
			t.Errorf("matchReloadRules(%v) = %v, want %v", tc.changed, got, tc.want)
		}
	}
}

func TestInstallSelf(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(previewConsoleJWKSEnv, testPreviewConsoleJWKS())
	if err := installSelf(dir); err != nil {
		t.Fatalf("installSelf: %v", err)
	}
	target := filepath.Join(dir, agentBinaryName)
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat installed binary: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", info.Mode())
	}
	self, _ := os.Executable()
	selfInfo, _ := os.Stat(self)
	if info.Size() != selfInfo.Size() {
		t.Errorf("installed binary size %d != executable size %d", info.Size(), selfInfo.Size())
	}
	plugin, err := os.ReadFile(filepath.Join(dir, previewConsolePluginName))
	if err != nil {
		t.Fatalf("read installed plugin: %v", err)
	}
	if !bytes.Equal(plugin, previewConsolePlugin) {
		t.Error("installed preview console plugin differs from embedded asset")
	}
	rawJWKS, err := os.ReadFile(filepath.Join(dir, previewConsoleJWKSName))
	if err != nil {
		t.Fatalf("read installed JWKS: %v", err)
	}
	if strings.Contains(string(rawJWKS), `"d"`) {
		t.Errorf("installed JWKS contains private material: %s", rawJWKS)
	}
	for _, name := range []string{previewConsolePluginName, previewConsoleJWKSName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %v, want 0644", name, info.Mode())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read install dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Errorf("temporary install file was not cleaned up: %s", entry.Name())
		}
	}
}

func TestInstallSelfInvalidJWKSFailsOpenAndRemovesStaleConfig(t *testing.T) {
	for _, raw := range []string{"", `{"keys":[{"kty":"EC","crv":"P-256","kid":"attacker","x":"x","y":"y","d":"private"}]}`} {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			stale := filepath.Join(dir, previewConsoleJWKSName)
			if err := os.WriteFile(stale, []byte(testPreviewConsoleJWKS()), 0o644); err != nil {
				t.Fatalf("write stale config: %v", err)
			}
			t.Setenv(previewConsoleJWKSEnv, raw)
			if err := installSelf(dir); err != nil {
				t.Fatalf("installSelf should leave app available: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, previewConsolePluginName)); err != nil {
				t.Errorf("plugin was not installed: %v", err)
			}
			if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("stale JWKS still exists: %v", err)
			}
		})
	}
}
func TestNormalizePreviewConsoleJWKSRejectsPrivateOrMalformedKeys(t *testing.T) {
	for _, raw := range []string{
		`{"keys":[]}`,
		`{"keys":[{"kty":"EC","crv":"P-256","kid":"a","x":"x","y":"y","d":"private"}]}`,
		`{"keys":[{"kty":"RSA","kid":"a","x":"x","y":"y"}]}`,
		`{"keys":[{"kty":"EC","crv":"P-256","kid":"a","x":"eA","y":"eQ"}]}`,
	} {
		if _, err := normalizePreviewConsoleJWKS([]byte(raw)); err == nil {
			t.Errorf("normalizePreviewConsoleJWKS(%s) succeeded, want rejection", raw)
		}
	}
}

func testPreviewConsoleJWKS() string {
	coordinate := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	return `{"keys":[{"kty":"EC","crv":"P-256","kid":"current","x":"` + coordinate + `","y":"` + coordinate + `","alg":"ES256","use":"sig"}]}`
}

func newTestAgent(t *testing.T, cfg *agentConfig) *agentServer {
	t.Helper()
	if cfg.WorkDir == "" {
		cfg.WorkDir = t.TempDir()
	}
	if cfg.ControlToken == "" {
		cfg.ControlToken = "test-token"
	}
	return newAgentServer(context.Background(), cfg)
}

func doSync(t *testing.T, srv *agentServer, body syncRequest) (*httptest.ResponseRecorder, syncResponse) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/sync", bytes.NewReader(raw))
	req.Header.Set(controlTokenHeader, "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var resp syncResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec, resp
}

func TestSyncWritesFilesAndRunsReloadRules(t *testing.T) {
	workdir := t.TempDir()
	marker := filepath.Join(workdir, "reload-ran")
	srv := newTestAgent(t, &agentConfig{
		WorkDir: workdir,
		// A start command makes "auto" eligible; sleep keeps it running.
		StartCommand:   "sleep 60",
		ReloadStrategy: "process",
		ReloadRules:    []reloadRule{{Paths: []string{"package.json"}, Command: "touch " + marker}},
	})
	if err := srv.supervisor.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = srv.supervisor.stop() }()
	waitFor(t, func() bool { return srv.supervisor.isRunning() })

	rec, resp := doSync(t, srv, syncRequest{
		Files:   []syncFile{{Path: "package.json", Content: `{"name":"x"}`}, {Path: "src/app.js", Content: "console.log(1)"}},
		Restart: "auto",
	})
	if rec.Code != 200 {
		t.Fatalf("sync status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(resp.Changed) != 2 || !resp.Restarted {
		t.Errorf("sync response = %+v, want 2 changed + restarted", resp)
	}
	if !slices.Equal(resp.ReloadRuns, []string{"touch " + marker}) {
		t.Errorf("reloadRuns = %v", resp.ReloadRuns)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("reload command did not run: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(workdir, "src", "app.js")); err != nil || string(got) != "console.log(1)" {
		t.Errorf("synced file content = %q err=%v", got, err)
	}

	// A non-rule change with the process running does not restart.
	_, resp = doSync(t, srv, syncRequest{
		Files:   []syncFile{{Path: "src/app.js", Content: "console.log(2)"}},
		Restart: "auto",
	})
	if resp.Restarted || len(resp.ReloadRuns) != 0 {
		t.Errorf("plain source sync restarted: %+v", resp)
	}
}

func TestSyncRejectsEscapes(t *testing.T) {
	srv := newTestAgent(t, &agentConfig{})
	for _, p := range []string{"../evil", "/abs/path", "a/../../b"} {
		rec, _ := doSync(t, srv, syncRequest{Files: []syncFile{{Path: p, Content: "x"}}})
		if rec.Code != 400 {
			t.Errorf("sync %q status = %d, want 400", p, rec.Code)
		}
	}
}

func TestAuthoritativeSyncConvergesManagedDeletesAndReturnsEvidence(t *testing.T) {
	workdir := t.TempDir()
	srv := newTestAgent(t, &agentConfig{WorkDir: workdir})
	first := []syncFile{{Path: "main.go", Content: "package main\n"}, {Path: "old.go", Content: "old\n"}}
	digest, err := digestSyncFiles(first)
	if err != nil {
		t.Fatal(err)
	}
	rec, resp := doSync(t, srv, syncRequest{Files: first, SourceRevision: 1, SourceDigest: digest})
	if rec.Code != http.StatusOK || resp.SourceRevision != 1 || resp.SourceDigest != digest {
		t.Fatalf("first sync = status %d response %+v", rec.Code, resp)
	}
	firstDigest := digest
	if err := os.MkdirAll(filepath.Join(workdir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "node_modules", "generated.js"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "runtime.log"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := []syncFile{{Path: "main.go", Content: "package main\nfunc main() {}\n"}}
	digest, err = digestSyncFiles(second)
	if err != nil {
		t.Fatal(err)
	}
	rec, resp = doSync(t, srv, syncRequest{Files: second, SourceRevision: 2, SourceDigest: digest})
	if rec.Code != http.StatusOK || resp.SourceRevision != 2 || !slices.Equal(resp.Deleted, []string{"old.go"}) {
		t.Fatalf("second sync = status %d response %+v", rec.Code, resp)
	}
	if _, err := os.Stat(filepath.Join(workdir, "old.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old managed file still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "node_modules", "generated.js")); err != nil {
		t.Fatalf("runtime-generated file was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "runtime.log")); err != nil {
		t.Fatalf("unmanaged runtime file was deleted: %v", err)
	}

	// A same-revision retry verifies actual bytes before treating the request
	// as an idempotent no-op; an app-side mutation is repaired from the source.
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, _ = doSync(t, srv, syncRequest{Files: second, SourceRevision: 2, SourceDigest: digest})
	if rec.Code != http.StatusOK {
		t.Fatalf("same-revision repair status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(workdir, "main.go"))
	if err != nil || string(got) != second[0].Content {
		t.Fatalf("same-revision repair content = %q err=%v", got, err)
	}

	rec, _ = doSync(t, srv, syncRequest{Files: first, SourceRevision: 1, SourceDigest: firstDigest})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale revision status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
}

func TestAuthoritativeSyncRebuildsInvalidManifestWithoutBroadDeletes(t *testing.T) {
	workdir := t.TempDir()
	srv := newTestAgent(t, &agentConfig{WorkDir: workdir})
	if err := os.WriteFile(filepath.Join(workdir, workspaceManifestName), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "runtime-generated.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "stale.txt"), []byte("remove"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []syncFile{{Path: "main.go", Content: "package main\n"}}
	digest, err := digestSyncFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := doSync(t, srv, syncRequest{Files: files, DeletePaths: []string{"stale.txt"}, SourceRevision: 1, SourceDigest: digest})
	if rec.Code != http.StatusOK {
		t.Fatalf("rebuild status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(workdir, "runtime-generated.txt")); err != nil {
		t.Fatalf("unknown path was deleted while rebuilding manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "stale.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit stale path was not converged: %v", err)
	}
	manifest, found, err := readWorkspaceManifest(mustOpenWorkspaceRoot(t, workdir))
	if err != nil || !found || manifest.SourceRevision != 1 {
		t.Fatalf("rebuilt manifest = %+v found=%t err=%v", manifest, found, err)
	}
}

func TestAuthoritativeSyncReloadHookCannotMutateSourceManifest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		command    string
		wantReload bool
	}{
		{name: "success", command: "printf mutated > package-lock.json; printf corrupted > .faros-workspace-manifest.json"},
		{name: "failure", command: "printf mutated > package-lock.json; printf corrupted > .faros-workspace-manifest.json; exit 7", wantReload: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workdir := t.TempDir()
			srv := newTestAgent(t, &agentConfig{
				WorkDir:        workdir,
				ReloadStrategy: "process",
				ReloadRules:    []reloadRule{{Paths: []string{"package-lock.json"}, Command: tc.command}},
			})
			files := []syncFile{
				{Path: "main.sh", Content: "#!/bin/sh\nexit 0\n"},
				{Path: "package-lock.json", Content: `{"lockfileVersion":3}`},
			}
			digest, err := digestSyncFiles(files)
			if err != nil {
				t.Fatal(err)
			}
			recorder, response := doSync(t, srv, syncRequest{
				Files: files, Restart: "always", SourceRevision: 1, SourceDigest: digest,
			})
			if recorder.Code != http.StatusOK {
				t.Fatalf("sync status = %d body=%s", recorder.Code, recorder.Body.String())
			}
			if response.SourceRevision != 1 || response.SourceDigest != digest || len(response.ReloadRuns) != 1 {
				t.Fatalf("sync response = %+v", response)
			}
			if tc.wantReload && response.ReloadError == "" {
				t.Fatalf("failed reload response = %+v, want reloadError", response)
			}
			if !tc.wantReload && response.ReloadError != "" {
				t.Fatalf("successful reload response = %+v", response)
			}

			root := mustOpenWorkspaceRoot(t, workdir)
			manifest, found, err := readWorkspaceManifest(root)
			if err != nil || !found {
				t.Fatalf("manifest = %+v found=%t err=%v", manifest, found, err)
			}
			if err := verifyWorkspaceManifest(root, manifest); err != nil {
				t.Fatalf("manifest verification after reload = %v", err)
			}
			lock, err := os.ReadFile(filepath.Join(workdir, "package-lock.json"))
			if err != nil || string(lock) != files[1].Content {
				t.Fatalf("package-lock after reload = %q err=%v, want authoritative content", lock, err)
			}

			result, err := runPersistentExec(context.Background(), workdir, persistentExecRequest{
				Argv: []string{"/bin/true"}, WorkDir: ".", SourceRevision: 1, SourceDigest: digest,
			})
			if tc.wantReload {
				if err == nil || !strings.Contains(err.Error(), "dependency reload is still pending") {
					t.Fatalf("exec while reload pending = %+v err=%v", result, err)
				}
				return
			}
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("exec after reload = %+v err=%v", result, err)
			}
		})
	}
}

func TestAuthoritativeSyncRetriesPendingReloadForSameRevision(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "fail-once"), []byte("fail"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "if [ -f fail-once ]; then rm fail-once; exit 7; fi; mkdir -p node_modules/example"
	srv := newTestAgent(t, &agentConfig{
		WorkDir:        workdir,
		ReloadStrategy: "process",
		ReloadRules:    []reloadRule{{Paths: []string{"package.json"}, Command: command}},
	})
	files := []syncFile{{Path: "package.json", Content: "{\"scripts\":{\"start\":\"node server.mjs\"}}"}}
	digest, err := digestSyncFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	request := syncRequest{Files: files, Restart: "always", SourceRevision: 1, SourceDigest: digest}

	firstRecorder, first := doSync(t, srv, request)
	if firstRecorder.Code != http.StatusOK || first.ReloadError == "" || !slices.Equal(first.ReloadRuns, []string{command}) {
		t.Fatalf("first sync status = %d response=%+v body=%s", firstRecorder.Code, first, firstRecorder.Body.String())
	}
	root := mustOpenWorkspaceRoot(t, workdir)
	manifest, found, err := readWorkspaceManifest(root)
	if err != nil || !found || !slices.Equal(manifest.PendingReloadCommands, []string{command}) {
		t.Fatalf("failed-reload manifest = %+v found=%t err=%v", manifest, found, err)
	}
	if result, err := runPersistentExec(context.Background(), workdir, persistentExecRequest{
		Argv: []string{"/bin/true"}, WorkDir: ".", SourceRevision: 1, SourceDigest: digest,
	}); err == nil || !strings.Contains(err.Error(), "dependency reload is still pending") {
		t.Fatalf("exec while reload pending = %+v err=%v", result, err)
	}

	secondRecorder, second := doSync(t, srv, request)
	if secondRecorder.Code != http.StatusOK || second.ReloadError != "" || !slices.Equal(second.ReloadRuns, []string{command}) {
		t.Fatalf("retry sync status = %d response=%+v body=%s", secondRecorder.Code, second, secondRecorder.Body.String())
	}
	manifest, found, err = readWorkspaceManifest(root)
	if err != nil || !found || len(manifest.PendingReloadCommands) != 0 {
		t.Fatalf("successful-retry manifest = %+v found=%t err=%v", manifest, found, err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "node_modules", "example")); err != nil {
		t.Fatalf("retry did not preserve installed runtime output: %v", err)
	}
	if result, err := runPersistentExec(context.Background(), workdir, persistentExecRequest{
		Argv: []string{"/bin/true"}, WorkDir: ".", SourceRevision: 1, SourceDigest: digest,
	}); err != nil || result.ExitCode != 0 {
		t.Fatalf("exec after successful reload retry = %+v err=%v", result, err)
	}
}

func TestAuthoritativeSyncReloadHookCannotRecreateDeletedManagedFile(t *testing.T) {
	workdir := t.TempDir()
	srv := newTestAgent(t, &agentConfig{
		WorkDir:        workdir,
		ReloadStrategy: "process",
		ReloadRules:    []reloadRule{{Paths: []string{"package-lock.json"}, Command: "printf recreated > old.go"}},
	})
	first := []syncFile{
		{Path: "main.sh", Content: "#!/bin/sh\nexit 0\n"},
		{Path: "old.go", Content: "managed source\n"},
		{Path: "package-lock.json", Content: `{"lockfileVersion":2}`},
	}
	firstDigest, err := digestSyncFiles(first)
	if err != nil {
		t.Fatal(err)
	}
	if recorder, _ := doSync(t, srv, syncRequest{Files: first, SourceRevision: 1, SourceDigest: firstDigest}); recorder.Code != http.StatusOK {
		t.Fatalf("initial sync status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := os.WriteFile(filepath.Join(workdir, "runtime.log"), []byte("runtime-only"), 0o644); err != nil {
		t.Fatal(err)
	}

	second := []syncFile{
		{Path: "main.sh", Content: "#!/bin/sh\nexit 0\n"},
		{Path: "package-lock.json", Content: `{"lockfileVersion":3}`},
	}
	secondDigest, err := digestSyncFiles(second)
	if err != nil {
		t.Fatal(err)
	}
	recorder, response := doSync(t, srv, syncRequest{
		Files: second, Restart: "always", SourceRevision: 2, SourceDigest: secondDigest,
	})
	if recorder.Code != http.StatusOK || response.ReloadError != "" || !slices.Equal(response.Deleted, []string{"old.go"}) {
		t.Fatalf("reload sync status = %d response=%+v body=%s", recorder.Code, response, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(workdir, "old.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reload hook recreated deleted managed file: %v", err)
	}
	root := mustOpenWorkspaceRoot(t, workdir)
	manifest, found, err := readWorkspaceManifest(root)
	if err != nil || !found || manifest.SourceRevision != 2 {
		t.Fatalf("manifest = %+v found=%t err=%v", manifest, found, err)
	}
	if err := verifyWorkspaceManifest(root, manifest); err != nil {
		t.Fatalf("manifest verification after recreated-file cleanup = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "runtime.log")); err != nil {
		t.Fatalf("runtime-only file was removed: %v", err)
	}
	result, err := runPersistentExec(context.Background(), workdir, persistentExecRequest{
		Argv: []string{"/bin/true"}, WorkDir: ".", SourceRevision: 2, SourceDigest: secondDigest,
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("exec after recreated-file cleanup = %+v err=%v", result, err)
	}
}

func TestAuthoritativeSyncRejectsSymlinkWrite(t *testing.T) {
	workdir := t.TempDir()
	outside := t.TempDir()
	srv := newTestAgent(t, &agentConfig{WorkDir: workdir})
	if err := os.Symlink(outside, filepath.Join(workdir, "link")); err != nil {
		t.Fatal(err)
	}
	files := []syncFile{{Path: "link/main.go", Content: "package main\n"}}
	digest, err := digestSyncFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := doSync(t, srv, syncRequest{Files: files, SourceRevision: 1, SourceDigest: digest})
	if rec.Code != http.StatusConflict && rec.Code != http.StatusBadRequest {
		t.Fatalf("symlink sync status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "main.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target was written: %v", err)
	}
}

func mustOpenWorkspaceRoot(t *testing.T, workdir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(workdir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestControlAuth(t *testing.T) {
	srv := newTestAgent(t, &agentConfig{})
	req := httptest.NewRequest("GET", "/logs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
	req = httptest.NewRequest("GET", "/logs", nil)
	req.Header.Set(controlTokenHeader, "wrong")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("bad token: status = %d, want 401", rec.Code)
	}
}

func TestRingLogScopesOutputToCurrentProcessAttempt(t *testing.T) {
	logs := newRingLog(10)
	first := logs.beginAttempt()
	logs.appendAttempt(first, `npm error Missing script: "dev"`)
	second := logs.beginAttempt()
	logs.appendAttempt(first, "late output from stopped process")
	logs.appendAttempt(second, "server listening")

	if got := logs.lines(); !slices.Equal(got, []string{"server listening"}) {
		t.Fatalf("current attempt logs = %#v", got)
	}
}

func TestReloadCannotWriteAcrossRestartEpoch(t *testing.T) {
	workdir := t.TempDir()
	started := filepath.Join(workdir, "reload-started")
	release := filepath.Join(workdir, "reload-release")
	srv := newTestAgent(t, &agentConfig{
		WorkDir:      workdir,
		StartCommand: "echo current-process; sleep 60",
	})
	if err := srv.supervisor.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = srv.supervisor.stop() }()

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- srv.supervisor.runReloadCommands(context.Background(), []string{
			"touch " + started + "; while [ ! -f " + release + " ]; do sleep 0.01; done; echo old-reload-output",
		})
	}()
	waitFor(t, func() bool {
		_, err := os.Stat(started)
		return err == nil
	})

	restartDone := make(chan error, 1)
	go func() { restartDone <- srv.supervisor.restart(context.Background()) }()
	select {
	case err := <-restartDone:
		t.Fatalf("restart completed before reload serialization boundary: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(release, []byte("go"), 0o644); err != nil {
		t.Fatalf("release reload: %v", err)
	}
	if err := <-reloadDone; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := <-restartDone; err != nil {
		t.Fatalf("restart: %v", err)
	}
	waitFor(t, func() bool {
		return slices.Contains(srv.logs.lines(), "current-process")
	})
	for _, line := range srv.logs.lines() {
		if strings.Contains(line, "old-reload-output") {
			t.Fatalf("old reload output crossed restart epoch: %#v", srv.logs.lines())
		}
	}
}

func TestStatusReportsCurrentAttemptAndDeclaredPortReadiness(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port

	workdir := t.TempDir()
	srv := newTestAgent(t, &agentConfig{
		WorkDir:      workdir,
		StartCommand: "sleep 60",
		Port:         fmt.Sprint(port),
	})
	if err := srv.supervisor.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = srv.supervisor.stop() }()
	files := []syncFile{{Path: "app.txt", Content: "current"}}
	digest, err := digestSyncFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	if syncRecorder, _ := doSync(t, srv, syncRequest{Files: files, SourceRevision: 9, SourceDigest: digest}); syncRecorder.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", syncRecorder.Code, syncRecorder.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set(controlTokenHeader, "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", rec.Code, rec.Body.String())
	}
	var got processStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got.AttemptID != 1 || got.AttemptStartedUnixMilli == 0 || !got.Configured || !got.Running || !got.PortReachable || got.Port != fmt.Sprint(port) || got.SourceRevision != 9 || got.SourceDigest != digest {
		t.Fatalf("status = %+v", got)
	}
	if err := os.WriteFile(filepath.Join(workdir, "app.txt"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req.Clone(context.Background()))
	got = processStatusResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode stale status: %v", err)
	}
	if got.SourceRevision != 0 || got.SourceDigest != "" {
		t.Fatalf("tampered workspace reported current source evidence: %+v", got)
	}
}

func TestEnvRejectsReservedAndSecretNames(t *testing.T) {
	srv := newTestAgent(t, &agentConfig{})
	for _, name := range []string{"FAROS_DEV_PORT", "API_TOKEN", "MY_SECRET"} {
		if _, err := srv.supervisor.setEnv(map[string]string{name: "v"}); err == nil {
			t.Errorf("setEnv(%s) accepted, want rejection", name)
		}
	}
	if _, err := srv.supervisor.setEnv(map[string]string{"FEATURE_FLAG": "on"}); err != nil {
		t.Errorf("setEnv(FEATURE_FLAG) = %v, want nil", err)
	}
}

func TestMergeChildEnvPortConventions(t *testing.T) {
	out := mergeChildEnv([]string{"PATH=/bin", "FAROS_DEV_CONTROL_TOKEN=x", "FAROS_DEV_STATE_DIR=/state"}, map[string]string{"FOO": "bar"}, "8080")
	joined := strings.Join(out, "\n")
	for _, want := range []string{"PORT=8080", "FOO=bar", "PATH=/bin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("child env lacks %s: %v", want, out)
		}
	}
	if strings.Contains(joined, "FAROS_DEV_") {
		t.Errorf("coordinator/runtime configuration leaked into child env: %v", out)
	}
	// An explicit PORT wins over the convention.
	out = mergeChildEnv([]string{"PORT=9999"}, nil, "8080")
	if !slices.Contains(out, "PORT=9999") || slices.Contains(out, "PORT=8080") {
		t.Errorf("explicit PORT overridden: %v", out)
	}
}

func TestRuntimeSupervisorInternalAPIIsNarrowAndBounded(t *testing.T) {
	workdir := t.TempDir()
	logs := newRingLog(10)
	supervisor := newSupervisor(t.Context(), &agentConfig{WorkDir: workdir, StartCommand: "sleep 60"}, logs)
	server := newRuntimeSupervisorServer(supervisor, logs, nil)
	defer func() { _ = supervisor.stop() }()

	for _, tc := range []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/exec", "", http.StatusNotFound},
		{http.MethodPost, "/internal/reload", `{"commands":[]}`, http.StatusBadRequest},
		{http.MethodPost, "/internal/env", `{"env":{"API_TOKEN":"secret"}}`, http.StatusBadRequest},
		{http.MethodPost, "/internal/restart", `{}`, http.StatusOK},
		{http.MethodGet, "/internal/status", "", http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d body=%s, want %d", tc.method, tc.path, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

func TestContainerReloadExitsRuntimeSupervisorAndKeepsCoordinatorAlive(t *testing.T) {
	workdir := t.TempDir()
	logs := newRingLog(10)
	supervisor := newSupervisor(t.Context(), &agentConfig{WorkDir: workdir}, logs)
	exited := make(chan int, 1)
	runtimeServer := httptest.NewServer(newRuntimeSupervisorServer(supervisor, logs, func(code int) { exited <- code }))
	defer runtimeServer.Close()
	runtime := &httpRuntimeClient{baseURL: runtimeServer.URL, client: runtimeServer.Client()}
	coordinator := newCoordinatorServer(&agentConfig{
		WorkDir: workdir, ControlToken: "test-token", ReloadStrategy: "container",
	}, runtime, &sync.Mutex{})

	recorder, response := doSync(t, coordinator, syncRequest{
		Files: []syncFile{{Path: "main.go", Content: "package main\n"}}, Restart: "always",
	})
	if recorder.Code != http.StatusOK || !response.Restarted {
		t.Fatalf("container sync = status %d response %+v body=%s", recorder.Code, response, recorder.Body.String())
	}
	healthRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthResponse := httptest.NewRecorder()
	coordinator.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("coordinator health after runtime exit request = %d", healthResponse.Code)
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("runtime exit code = %d, want 0", code)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime supervisor did not exit after acknowledgement")
	}
}

func TestConfigSeparatesCoordinatorSecretsFromProcessEnvironment(t *testing.T) {
	t.Setenv("FAROS_DEV_CONTROL_TOKEN", "top-secret")
	t.Setenv("FAROS_DEV_STATE_DIR", t.TempDir())
	cfg, err := configFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ControlToken != "top-secret" || cfg.StateDir == "" {
		t.Fatalf("coordinator config = %+v", cfg)
	}
	if _, present := os.LookupEnv("FAROS_DEV_CONTROL_TOKEN"); present {
		t.Fatal("control token remains in process environment")
	}
	env := strings.Join(mergeChildEnv(os.Environ(), nil, ""), "\n")
	if strings.Contains(env, "FAROS_DEV_STATE_DIR") || strings.Contains(env, "top-secret") {
		t.Fatalf("runtime child environment contains coordinator state or secret: %s", env)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
