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

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

func TestAppIdentityKeyRotationConfigRequiresExplicitKubeconfigAndScopesWorkspace(t *testing.T) {
	if _, err := appIdentityKeyRotationRESTConfig("", ""); err == nil || !strings.Contains(err.Error(), "--kubeconfig is required") {
		t.Fatalf("empty kubeconfig error = %v; want explicit kubeconfig requirement", err)
	}

	kubeconfigPath := writeAppIdentityRotationKubeconfig(t, "https://kcp.example/clusters/root", false)
	config, err := appIdentityKeyRotationRESTConfig(kubeconfigPath, "operator")
	if err != nil {
		t.Fatalf("build scoped config: %v", err)
	}
	wantHost := "https://kcp.example/clusters/" + kcppaths.SystemControllers
	if config.Host != wantHost {
		t.Fatalf("config host = %q; want %q", config.Host, wantHost)
	}
	if config.BearerToken != "operator-token" {
		t.Fatalf("config token = %q; want the explicitly selected context's token", config.BearerToken)
	}
}

func TestAppIdentityKeyRotationConfigRejectsUnexpectedServerPath(t *testing.T) {
	kubeconfigPath := writeAppIdentityRotationKubeconfig(t, "https://kcp.example/prefix/clusters/root", false)
	if _, err := appIdentityKeyRotationRESTConfig(kubeconfigPath, "operator"); err == nil || !strings.Contains(err.Error(), "refusing to rotate outside") {
		t.Fatalf("unexpected server path error = %v; want refusal", err)
	}
}

func TestAppIdentityKeyRotationConfigRejectsURLCredentialsAndQuery(t *testing.T) {
	for _, serverURL := range []string{
		"https://operator:secret@kcp.example",
		"https://kcp.example?workspace=other",
		"https://kcp.example?",
		"https://kcp.example#other-workspace",
	} {
		t.Run(serverURL, func(t *testing.T) {
			kubeconfigPath := writeAppIdentityRotationKubeconfig(t, serverURL, false)
			if _, err := appIdentityKeyRotationRESTConfig(kubeconfigPath, "operator"); err == nil || !strings.Contains(err.Error(), "invalid server URL") {
				t.Fatalf("server URL %q error = %v; want invalid URL refusal", serverURL, err)
			}
		})
	}
}

func TestAppIdentityKeyRotationCommandRequiresKubeconfigFlag(t *testing.T) {
	command := newAppIdentityKeyRotationCommand()
	command.SilenceErrors = true
	command.SilenceUsage = true
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "required flag(s) \"kubeconfig\"") {
		t.Fatalf("command without kubeconfig error = %v; want required --kubeconfig flag", err)
	}
}

func TestAppIdentityKeyRotationCommandPropagatesKCPRBACDenial(t *testing.T) {
	const token = "operator-token"
	const expectedPath = "/clusters/" + kcppaths.SystemControllers + "/api/v1/namespaces/railgrid-hub/secrets/railgrid-app-identity-keyring"
	var gotPath string
	var gotAuthorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"secrets is forbidden","reason":"Forbidden","code":403}`)
	}))
	defer server.Close()

	kubeconfigPath := writeAppIdentityRotationKubeconfig(t, server.URL, true)
	command := newAppIdentityKeyRotationCommand()
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs([]string{"--kubeconfig", kubeconfigPath, "--context", "operator"})
	err := command.Execute()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "forbidden") {
		t.Fatalf("command error = %v; want propagated Kubernetes RBAC denial", err)
	}
	if gotPath != expectedPath {
		t.Fatalf("request path = %q; want %q", gotPath, expectedPath)
	}
	if gotAuthorization != "Bearer "+token {
		t.Fatalf("authorization = %q; want explicit kubeconfig token", gotAuthorization)
	}
}

func TestAppIdentityKeyRotationCommandHelpDescribesRotationEffects(t *testing.T) {
	command := newAppIdentityKeyRotationCommand()
	if !strings.Contains(command.Long, "15 minutes") || !strings.Contains(command.Long, "stable subject-pseudonym key is preserved") ||
		!strings.Contains(command.Long, "keyring Secret must already exist") {
		t.Fatalf("command help does not describe overlap and subject-key behavior: %q", command.Long)
	}
	for _, flagName := range []string{"kubeconfig", "context"} {
		if command.Flags().Lookup(flagName) == nil {
			t.Fatalf("command is missing --%s flag", flagName)
		}
	}
}

func writeAppIdentityRotationKubeconfig(t *testing.T, serverURL string, insecure bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	config := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cluster": {Server: serverURL, InsecureSkipTLSVerify: insecure},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default-user":  {Token: "default-token"},
			"operator-user": {Token: "operator-token"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default":  {Cluster: "cluster", AuthInfo: "default-user"},
			"operator": {Cluster: "cluster", AuthInfo: "operator-user"},
		},
		CurrentContext: "default",
	}
	if err := clientcmd.WriteToFile(config, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}
