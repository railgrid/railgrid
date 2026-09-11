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

package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMacOSAgentIsServiceOnly(t *testing.T) {
	opts := NewOptions()
	opts.HubURL = "https://hub.example"
	opts.EdgeName = "macbook-01"
	opts.Type = AgentTypeMacOS
	opts.SSHProxyPort = 2222

	a, err := New(opts)
	if err != nil {
		t.Fatalf("New macOS agent: %v", err)
	}
	if a.agentType != AgentTypeMacOS {
		t.Fatalf("agent type = %q, want %q", a.agentType, AgentTypeMacOS)
	}
	if opts.SSHProxyPort != 0 {
		t.Fatalf("macOS agent retained SSH proxy port %d", opts.SSHProxyPort)
	}
	if a.downstreamConfig != nil {
		t.Fatal("macOS agent unexpectedly built a downstream Kubernetes config")
	}
	if opts.SSHPrivateKeyPath != "" || opts.SSHPassword != "" {
		t.Fatalf("macOS agent unexpectedly configured SSH credentials: key=%q password-set=%v", opts.SSHPrivateKeyPath, opts.SSHPassword != "")
	}
}

func TestAgentConfigPersistenceRoundTripsClusterWithOwnerPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".faros", "agent-macbook-01.json")
	want := AgentConfig{
		HubURL:  "https://hub.example",
		Token:   "durable-token",
		Cluster: "root:faros:tenant",
	}
	if err := SaveAgentConfigAt(path, want); err != nil {
		t.Fatalf("SaveAgentConfigAt: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat persisted config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("persisted config mode = %04o, want 0600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat config directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("config directory mode = %04o, want 0700", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var got AgentConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode persisted config: %v", err)
	}
	if got != want {
		t.Fatalf("persisted config = %+v, want %+v", got, want)
	}
}

func TestSaveAgentConfigAtRepairsPermissionsOnExistingCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".faros", "agent-macbook-01.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hubURL":"https://old.example","token":"old-token"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveAgentConfigAt(path, AgentConfig{HubURL: "https://hub.example", Token: "new-token"}); err != nil {
		t.Fatalf("SaveAgentConfigAt: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("existing credential mode = %04o after rewrite, want 0600", got)
	}
}

func TestSaveAgentKubeconfigRepairsPermissionsOnExistingCredentialFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := AgentKubeconfigPathForHome(home, "macbook-01")
	content := base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nkind: Config\n"))
	if err := SaveAgentKubeconfig("macbook-01", content); err != nil {
		t.Fatalf("initial SaveAgentKubeconfig: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveAgentKubeconfig("macbook-01", content); err != nil {
		t.Fatalf("rewrite SaveAgentKubeconfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("existing kubeconfig mode = %04o after rewrite, want 0600", got)
	}
}

func TestAgentCredentialPathsUseWorkerHome(t *testing.T) {
	home := filepath.Join("/Users", "worker")
	if got, want := AgentConfigPathForHome(home, "macbook-01"), filepath.Join(home, ".faros", "agent-macbook-01.json"); got != want {
		t.Fatalf("AgentConfigPathForHome = %q, want %q", got, want)
	}
	if got, want := AgentKubeconfigPathForHome(home, "macbook-01"), filepath.Join(home, ".faros", "agent-macbook-01.kubeconfig"); got != want {
		t.Fatalf("AgentKubeconfigPathForHome = %q, want %q", got, want)
	}
}
