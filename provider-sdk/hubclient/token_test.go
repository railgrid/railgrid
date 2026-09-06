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

package hubclient

import (
	"os"
	"path/filepath"
	"testing"
)

const kubeconfigWithToken = `apiVersion: v1
kind: Config
clusters:
- name: hub
  cluster:
    server: https://hub.example.test/clusters/abc123
    insecure-skip-tls-verify: true
contexts:
- name: hub
  context:
    cluster: hub
    user: provider
current-context: hub
users:
- name: provider
  user:
    token: sa-token-from-kubeconfig
`

func writeKubeconfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveHubTokenPrefersExplicitToken(t *testing.T) {
	t.Setenv(EnvHubToken, "explicit")
	t.Setenv(EnvProviderKubeconfig, writeKubeconfig(t, kubeconfigWithToken))
	got, err := ResolveHubToken()
	if err != nil || got != "explicit" {
		t.Fatalf("ResolveHubToken() = %q, %v; want the explicit token", got, err)
	}
}

func TestResolveHubTokenFallsBackToProviderKubeconfig(t *testing.T) {
	t.Setenv(EnvHubToken, "")
	t.Setenv(EnvProviderKubeconfig, writeKubeconfig(t, kubeconfigWithToken))
	got, err := ResolveHubToken()
	if err != nil || got != "sa-token-from-kubeconfig" {
		t.Fatalf("ResolveHubToken() = %q, %v; want the kubeconfig bearer", got, err)
	}
}

func TestResolveHubTokenWithNothingConfigured(t *testing.T) {
	t.Setenv(EnvHubToken, "")
	t.Setenv(EnvProviderKubeconfig, "")
	got, err := ResolveHubToken()
	if err != nil || got != "" {
		t.Fatalf("ResolveHubToken() = %q, %v; want empty and no error", got, err)
	}
}

func TestResolveHubTokenReportsBrokenKubeconfig(t *testing.T) {
	t.Setenv(EnvHubToken, "")
	t.Setenv(EnvProviderKubeconfig, filepath.Join(t.TempDir(), "missing"))
	if _, err := ResolveHubToken(); err == nil {
		t.Fatal("missing kubeconfig accepted")
	}

	noToken := `apiVersion: v1
kind: Config
clusters:
- name: hub
  cluster:
    server: https://hub.example.test
contexts:
- name: hub
  context:
    cluster: hub
    user: provider
current-context: hub
users:
- name: provider
  user: {}
`
	t.Setenv(EnvProviderKubeconfig, writeKubeconfig(t, noToken))
	if _, err := ResolveHubToken(); err == nil {
		t.Fatal("kubeconfig without a bearer token accepted")
	}
}

func TestTokenFromKubeconfigReadsTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kc := `apiVersion: v1
kind: Config
clusters:
- name: hub
  cluster:
    server: https://hub.example.test
contexts:
- name: hub
  context:
    cluster: hub
    user: provider
current-context: hub
users:
- name: provider
  user:
    tokenFile: ` + tokenFile + `
`
	got, err := TokenFromKubeconfig(writeKubeconfig(t, kc))
	if err != nil || got != "from-file" {
		t.Fatalf("TokenFromKubeconfig() = %q, %v; want from-file", got, err)
	}
}
