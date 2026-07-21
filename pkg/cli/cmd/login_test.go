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

package cmd

import (
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// loginKubeconfig mimics what the hub's auth handler returns on login: the
// "kedge" cluster pointing at the user's home workspace.
func loginKubeconfig(t *testing.T, server string) []byte {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kedge"] = &clientcmdapi.Cluster{Server: server}
	cfg.AuthInfos["user-abc"] = &clientcmdapi.AuthInfo{Token: "tok"}
	cfg.Contexts["kedge"] = &clientcmdapi.Context{Cluster: "kedge", AuthInfo: "user-abc"}
	cfg.CurrentContext = "kedge"
	out, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatalf("writing kubeconfig: %v", err)
	}
	return out
}

func writeKubeconfigFile(t *testing.T, path, server string) {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kedge"] = &clientcmdapi.Cluster{Server: server}
	cfg.AuthInfos["user-abc"] = &clientcmdapi.AuthInfo{Token: "old"}
	cfg.Contexts["kedge"] = &clientcmdapi.Context{Cluster: "kedge", AuthInfo: "user-abc"}
	cfg.CurrentContext = "kedge"
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
	cluster := cfg.Clusters["kedge"]
	if cluster == nil {
		t.Fatalf("merged kubeconfig has no kedge cluster")
	}
	return cluster.Server
}

func TestMergeKubeconfigPreservesWorkspaceSelection(t *testing.T) {
	tests := []struct {
		name       string
		existing   string // server in the pre-existing kubeconfig; "" = no file
		incoming   string // server in the login response
		wantServer string
	}{
		{
			name:       "relogin same hub keeps kedge use selection",
			existing:   "https://console-dev.faros.sh/clusters/jcb49sm6dkg85xwg",
			incoming:   "https://console-dev.faros.sh/clusters/home111",
			wantServer: "https://console-dev.faros.sh/clusters/jcb49sm6dkg85xwg",
		},
		{
			name:       "different hub takes the new server",
			existing:   "https://console-dev.faros.sh/clusters/jcb49sm6dkg85xwg",
			incoming:   "https://console.faros.sh/clusters/home111",
			wantServer: "https://console.faros.sh/clusters/home111",
		},
		{
			name:       "fresh login takes the new server",
			existing:   "",
			incoming:   "https://console-dev.faros.sh/clusters/home111",
			wantServer: "https://console-dev.faros.sh/clusters/home111",
		},
		{
			name:       "existing server without cluster path takes the new server",
			existing:   "https://console-dev.faros.sh",
			incoming:   "https://console-dev.faros.sh/clusters/home111",
			wantServer: "https://console-dev.faros.sh/clusters/home111",
		},
		{
			name:       "same workspace stays put",
			existing:   "https://console-dev.faros.sh/clusters/home111",
			incoming:   "https://console-dev.faros.sh/clusters/home111",
			wantServer: "https://console-dev.faros.sh/clusters/home111",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			t.Setenv("KUBECONFIG", path)
			if tc.existing != "" {
				writeKubeconfigFile(t, path, tc.existing)
			}
			if err := mergeKubeconfig(loginKubeconfig(t, tc.incoming)); err != nil {
				t.Fatalf("mergeKubeconfig: %v", err)
			}
			if got := mergedServer(t, path); got != tc.wantServer {
				t.Errorf("kedge cluster server = %q, want %q", got, tc.wantServer)
			}
		})
	}
}
