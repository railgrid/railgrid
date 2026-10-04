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

package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
)

// loginKubeconfig mimics what the hub's auth handler returns on login: the
// "railgrid" cluster pointing at the user's home workspace.
func loginKubeconfig(t *testing.T, server string) []byte {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["railgrid"] = &clientcmdapi.Cluster{Server: server}
	cfg.AuthInfos["user-abc"] = &clientcmdapi.AuthInfo{Token: "tok"}
	cfg.Contexts["railgrid"] = &clientcmdapi.Context{Cluster: "railgrid", AuthInfo: "user-abc"}
	cfg.CurrentContext = "railgrid"
	out, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatalf("writing kubeconfig: %v", err)
	}
	return out
}

func writeKubeconfigFile(t *testing.T, path, server string) {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["railgrid"] = &clientcmdapi.Cluster{Server: server}
	cfg.AuthInfos["user-abc"] = &clientcmdapi.AuthInfo{Token: "old"}
	cfg.Contexts["railgrid"] = &clientcmdapi.Context{Cluster: "railgrid", AuthInfo: "user-abc"}
	cfg.CurrentContext = "railgrid"
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatalf("writing kubeconfig file: %v", err)
	}
}

func mergedServer(t *testing.T, path string) string {
	t.Helper()
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatalf("loading merged kubeconfig: %v", err)
	}
	cluster := cfg.Clusters["railgrid"]
	if cluster == nil {
		t.Fatalf("merged kubeconfig has no railgrid cluster")
	}
	return cluster.Server
}

func setLoginKubeconfig(t *testing.T, path string) {
	t.Helper()
	previous := kubeconfig
	kubeconfig = path
	t.Cleanup(func() { kubeconfig = previous })
}

func TestLoginWritesToExplicitKubeconfig(t *testing.T) {
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, "env-kubeconfig")
	explicitPath := filepath.Join(tempDir, "requested-kubeconfig")
	writeKubeconfigFile(t, envPath, "https://existing.example.com/clusters/old")
	t.Setenv("KUBECONFIG", envPath)

	loginBytes := loginKubeconfig(t, "https://hub.example.com/clusters/home")
	loginResponse, err := json.Marshal(tenancyv1alpha1.LoginResponse{
		Kubeconfig: loginBytes,
		Email:      "user@example.com",
		UserID:     "user-abc",
	})
	if err != nil {
		t.Fatalf("marshalling fake login response: %v", err)
	}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiurl.PathAuthTokenLogin {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer dev-token" {
			http.Error(w, "unexpected authorization", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(loginResponse)
	}))
	defer hub.Close()

	previous := kubeconfig
	t.Cleanup(func() { kubeconfig = previous })
	root := NewRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{
		"--kubeconfig", explicitPath,
		"login", "--hub-url", hub.URL, "--token", "dev-token",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v\n%s", err, errOut.String())
	}

	if got := mergedServer(t, explicitPath); got != "https://hub.example.com/clusters/home" {
		t.Errorf("explicit kubeconfig server = %q, want hub login server", got)
	}
	if got := mergedServer(t, envPath); got != "https://existing.example.com/clusters/old" {
		t.Errorf("KUBECONFIG server changed to %q; explicit --kubeconfig should take precedence", got)
	}
}

func TestMergeKubeconfigPreservesWorkspaceSelection(t *testing.T) {
	tests := []struct {
		name       string
		existing   string // server in the pre-existing kubeconfig; "" = no file
		incoming   string // server in the login response
		wantServer string
	}{
		{
			name:       "relogin same hub keeps railgrid use selection",
			existing:   "https://console-dev.railgrid.ai/clusters/jcb49sm6dkg85xwg",
			incoming:   "https://console-dev.railgrid.ai/clusters/home111",
			wantServer: "https://console-dev.railgrid.ai/clusters/jcb49sm6dkg85xwg",
		},
		{
			name:       "different hub takes the new server",
			existing:   "https://console-dev.railgrid.ai/clusters/jcb49sm6dkg85xwg",
			incoming:   "https://console.railgrid.ai/clusters/home111",
			wantServer: "https://console.railgrid.ai/clusters/home111",
		},
		{
			name:       "fresh login takes the new server",
			existing:   "",
			incoming:   "https://console-dev.railgrid.ai/clusters/home111",
			wantServer: "https://console-dev.railgrid.ai/clusters/home111",
		},
		{
			name:       "existing server without cluster path takes the new server",
			existing:   "https://console-dev.railgrid.ai",
			incoming:   "https://console-dev.railgrid.ai/clusters/home111",
			wantServer: "https://console-dev.railgrid.ai/clusters/home111",
		},
		{
			name:       "same workspace stays put",
			existing:   "https://console-dev.railgrid.ai/clusters/home111",
			incoming:   "https://console-dev.railgrid.ai/clusters/home111",
			wantServer: "https://console-dev.railgrid.ai/clusters/home111",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			t.Setenv("KUBECONFIG", path)
			setLoginKubeconfig(t, "")
			if tc.existing != "" {
				writeKubeconfigFile(t, path, tc.existing)
			}
			if _, err := mergeKubeconfig(loginKubeconfig(t, tc.incoming)); err != nil {
				t.Fatalf("mergeKubeconfig: %v", err)
			}
			if got := mergedServer(t, path); got != tc.wantServer {
				t.Errorf("railgrid cluster server = %q, want %q", got, tc.wantServer)
			}
		})
	}
}
