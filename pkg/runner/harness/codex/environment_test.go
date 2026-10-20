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

package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestLaunchScratchIsIsolatedAndPackageCachesAreReused(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("GOCACHE", "/unwritable/inherited")
	t.Setenv("NPM_CONFIG_CACHE", "/unwritable/npm")
	a := &Adapter{cfg: Config{Home: home}}
	first, err := a.prepareEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(first) }()
	second, err := a.prepareEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(second) }()
	if first == second || strings.HasPrefix(first, home+string(os.PathSeparator)) {
		t.Fatal("cache not isolated from other launches and home")
	}
	a.cfg.cacheDir = first
	env := map[string]string{}
	for _, item := range a.launchEnv() {
		k, v, _ := strings.Cut(item, "=")
		env[k] = v
	}
	for _, key := range []string{"GOPATH", "GOBIN", "GOCACHE", "GOMODCACHE", "npm_config_cache", "GOTMPDIR", "TMPDIR", "XDG_CACHE_HOME"} {
		if !strings.HasPrefix(env[key], a.buildCacheRoot()+string(os.PathSeparator)) {
			t.Fatalf("%s outside cache: %s", key, env[key])
		}
		if err := os.WriteFile(filepath.Join(env[key], "test"), []byte("ok"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a.cfg.cacheDir = second
	secondEnv := map[string]string{}
	for _, item := range a.launchEnv() {
		k, v, _ := strings.Cut(item, "=")
		secondEnv[k] = v
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "npm_config_cache"} {
		if env[key] != secondEnv[key] {
			t.Fatalf("%s not reused", key)
		}
		if _, err := os.Stat(filepath.Join(secondEnv[key], "test")); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"TMPDIR", "GOBIN", "XDG_CACHE_HOME"} {
		if env[key] == secondEnv[key] {
			t.Fatalf("%s shared across launches", key)
		}
	}
	if _, exists := env["NPM_CONFIG_CACHE"]; exists {
		t.Fatal("inherited npm override survived")
	}
}

func TestNetworkOptInAndCacheReachAppServer(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env.json")
	binary := fakeCodexBinaryWithEnvFile(t, "success", envFile)
	home, root, work := trustFixture(t)
	writeTrustConfig(t, home, "[sandbox_workspace_write]\nnetwork_access = true\n")
	a := New(Config{Binary: binary, Home: home, WorktreeRoot: root, RegistryURLs: []string{}, Environment: harness.EnvironmentConfig{PathPrepend: []string{"/opt/browser-tools"}, Variables: map[string]string{"PLAYWRIGHT_BROWSERS_PATH": "/opt/browser-cache"}}})
	_, err := a.Run(context.Background(), harness.Launch{AttemptID: "cache-test", Workdir: work, Instructions: "hello", Credential: testCredential()}, func(harness.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	requests := readJSONLines(t, filepath.Join(filepath.Dir(binary), "requests"))
	params := requests[len(requests)-1]["params"].(map[string]any)
	policy := params["sandboxPolicy"].(map[string]any)
	if policy["networkAccess"] != true {
		t.Fatal("network opt-in lost")
	}
	roots := policy["writableRoots"].([]any)
	if len(roots) != 5 {
		t.Fatalf("roots: %v", roots)
	}
	env := readJSONFile(t, envFile)
	if !strings.HasPrefix(env["PATH"].(string), "/opt/browser-tools:") || env["PLAYWRIGHT_BROWSERS_PATH"] != "/opt/browser-cache" {
		t.Fatal("operator environment did not reach app-server")
	}
	for _, key := range []string{"GOPATH", "GOBIN", "GOCACHE", "GOMODCACHE", "npm_config_cache", "TMPDIR"} {
		value, _ := env[key].(string)
		if !strings.HasPrefix(value, filepath.Clean(home)+".build-cache"+string(os.PathSeparator)) {
			t.Fatalf("app-server %s outside sandbox writable root: %q", key, value)
		}
	}
	if _, err := os.Stat(roots[1].(string)); !os.IsNotExist(err) {
		t.Fatalf("launch cache not cleaned: %v", err)
	}
}

func TestPreflightRejectsUnavailableCacheAndInvalidRegistry(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "home.build-cache"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{cfg: Config{Home: filepath.Join(parent, "home")}}
	if _, err := a.prepareEnvironment(context.Background()); err == nil {
		t.Fatal("unwritable cache accepted")
	}
	for _, endpoint := range []string{"http://example.com", "https://user:password@example.com", "https://example.com?token=secret"} {
		if err := checkRegistry(context.Background(), endpoint); err == nil {
			t.Fatal("unsafe registry accepted")
		}
	}
	a.cfg.RegistryURLs = []string{"https://registry.npmjs.org"}
	if _, err := a.prepareEnvironment(context.Background()); err == nil {
		t.Fatal("registry preflight allowed with network disabled")
	}
}

func TestRegistryPreflightChecksHTTPSStatus(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer server.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	if err := checkRegistry(context.Background(), server.URL); err != nil {
		t.Fatal(err)
	}
	status = http.StatusServiceUnavailable
	if err := checkRegistry(context.Background(), server.URL); err == nil {
		t.Fatal("unavailable registry accepted")
	}
}

func TestCacheRootsArePrivateAndPerHome(t *testing.T) {
	parent := t.TempDir()
	a := &Adapter{cfg: Config{Home: filepath.Join(parent, "first")}}
	b := &Adapter{cfg: Config{Home: filepath.Join(parent, "second")}}
	if a.buildCacheRoot() == b.buildCacheRoot() {
		t.Fatal("different homes share cache")
	}
	if err := os.Symlink(parent, a.buildCacheRoot()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareEnvironment(t.Context()); err == nil {
		t.Fatal("symlink root accepted")
	}
	if err := os.Mkdir(b.buildCacheRoot(), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := b.prepareEnvironment(t.Context()); err == nil {
		t.Fatal("nonprivate root accepted")
	}
}
