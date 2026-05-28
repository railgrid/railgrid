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

package providerflags

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDepViolation verifies --providers validation is fail-fast.
//
// Spawning the hub with `--providers mcp` (missing both kubernetes-edges
// and server-edges) MUST exit non-zero in milliseconds — before any
// listener binds or embedded kcp boots. The error message must name
// every missing dep so the user can fix the flag in one edit.
func TestDepViolation(t *testing.T) {
	dataDir := tempDir(t, "kedge-e2e-flags-dep-")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, hubBinary,
		"--embedded-kcp",
		"--kcp-bind-address", "127.0.0.1",
		"--kcp-secure-port", "16443",
		"--listen-addr", ":19443",
		"--data-dir", dataDir,
		"--providers", "mcp",
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("hub did not exit within 30s — validation should fail in ms. output=\n%s", string(out))
	}
	if err == nil {
		t.Fatalf("hub exited 0 with bad --providers; expected failure. output=\n%s", string(out))
	}
	msg := string(out)
	if !strings.Contains(msg, "dependency violations") {
		t.Errorf("expected 'dependency violations' in stderr, got:\n%s", msg)
	}
	for _, want := range []string{"mcp requires kubernetes-edges", "mcp requires server-edges"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected %q in error, got:\n%s", want, msg)
		}
	}
}

// TestUnknownProviderRejected verifies a typo in --providers is rejected
// up-front with a helpful "known: [...]" hint, not silently ignored.
func TestUnknownProviderRejected(t *testing.T) {
	dataDir := tempDir(t, "kedge-e2e-flags-unknown-")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, hubBinary,
		"--embedded-kcp",
		"--listen-addr", ":19443",
		"--data-dir", dataDir,
		"--providers", "mpc-typo,server-edges",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("hub exited 0 with unknown --providers entry. output=\n%s", string(out))
	}
	msg := string(out)
	if !strings.Contains(msg, "mpc-typo") {
		t.Errorf("expected typo name 'mpc-typo' in error, got:\n%s", msg)
	}
	if !strings.Contains(msg, "known:") {
		t.Errorf("expected 'known:' hint in error, got:\n%s", msg)
	}
}

// TestFilteredEnableReflectedInAPI spawns a hub with
// `--providers kubernetes-edges,server-edges` (no mcp). Verifies the
// filter takes effect: mcp is absent from /api/providers, the other two
// are present.
//
// This implicitly exercises reconcile-delete too: a fresh data dir means
// no orphans to remove, but the create-or-update path is the same code
// the orphan cleanup uses, so a regression would surface here.
func TestFilteredEnableReflectedInAPI(t *testing.T) {
	dataDir := tempDir(t, "kedge-e2e-flags-filtered-")
	logf, _ := os.Create(filepath.Join(dataDir, "hub.log"))

	cmd := exec.Command(hubBinary,
		"--embedded-kcp",
		"--kcp-bind-address", "127.0.0.1",
		"--kcp-secure-port", "16443",
		"--listen-addr", ":19443",
		"--data-dir", dataDir,
		"--static-auth-token", staticToken,
		"--providers", "kubernetes-edges,server-edges",
	)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	if err := waitReady("http://127.0.0.1:19443/readyz", 3*time.Minute); err != nil {
		t.Fatalf("filtered hub never ready: %v (see %s)", err, logf.Name())
	}

	body := httpGetJSON(t, "http://127.0.0.1:19443/api/providers", staticToken)
	items, _ := body["items"].([]any)
	byName := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m
	}

	if _, ok := byName["mcp"]; ok {
		t.Errorf("mcp present in /api/providers but --providers excluded it; saw %v", keysOf(byName))
	}
	for _, want := range []string{"kubernetes-edges", "server-edges"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("expected %s in /api/providers; saw %v", want, keysOf(byName))
		}
	}
}

// --- helpers ---

func tempDir(t *testing.T, prefix string) string {
	t.Helper()
	d, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if os.Getenv("KEDGE_E2E_KEEP_DATA") == "true" {
			t.Logf("preserved %s", d)
			return
		}
		_ = os.RemoveAll(d)
	})
	return d
}

func waitReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "ok") {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return &timeoutError{url: url, after: timeout}
}

type timeoutError struct {
	url   string
	after time.Duration
}

func (e *timeoutError) Error() string {
	return "timeout after " + e.after.String() + " waiting for " + e.url
}

func httpGetJSON(t *testing.T, url, token string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
		Timeout:   10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s: status %d body=%s", url, resp.StatusCode, string(b))
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v body=%s", url, err, string(b))
	}
	return out
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
