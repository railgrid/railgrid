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
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestEnvRejectsReservedAndSecretNames(t *testing.T) {
	srv := newTestAgent(t, &agentConfig{})
	for _, name := range []string{"KEDGE_DEV_PORT", "SANDBOX_PORT", "API_TOKEN", "MY_SECRET"} {
		if _, err := srv.supervisor.setEnv(map[string]string{name: "v"}); err == nil {
			t.Errorf("setEnv(%s) accepted, want rejection", name)
		}
	}
	if _, err := srv.supervisor.setEnv(map[string]string{"FEATURE_FLAG": "on"}); err != nil {
		t.Errorf("setEnv(FEATURE_FLAG) = %v, want nil", err)
	}
}

func TestMergeChildEnvPortConventions(t *testing.T) {
	out := mergeChildEnv([]string{"PATH=/bin", "KEDGE_DEV_CONTROL_TOKEN=x"}, map[string]string{"FOO": "bar"}, "8080")
	joined := strings.Join(out, "\n")
	for _, want := range []string{"PORT=8080", "SANDBOX_PORT=8080", "FOO=bar", "PATH=/bin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("child env lacks %s: %v", want, out)
		}
	}
	if strings.Contains(joined, "KEDGE_DEV_CONTROL_TOKEN") {
		t.Errorf("control token leaked into child env: %v", out)
	}
	// An explicit PORT wins over the convention.
	out = mergeChildEnv([]string{"PORT=9999"}, nil, "8080")
	if !slices.Contains(out, "PORT=9999") || slices.Contains(out, "PORT=8080") {
		t.Errorf("explicit PORT overridden: %v", out)
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
