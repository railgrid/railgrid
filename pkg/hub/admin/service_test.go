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

package admin

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

// mintedKubeconfig mirrors the shape Provisioner.MintProviderKubeconfig writes
// into the Secret (pkg/hub/providers/provision.go).
const mintedKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: faros
  cluster:
    server: https://console-dev.faros.sh/clusters/xhk2soqt2d3dujnw
    insecure-skip-tls-verify: true
contexts:
- name: faros
  context:
    cluster: faros
    user: faros
current-context: faros
users:
- name: faros
  user:
    token: sa-token-abc123
`

func serverOf(t *testing.T, kc []byte) string {
	t.Helper()
	cfg, err := clientcmd.Load(kc)
	if err != nil {
		t.Fatalf("loading rewritten kubeconfig: %v", err)
	}
	c, ok := cfg.Clusters["faros"]
	if !ok {
		t.Fatalf("cluster %q missing from rewritten kubeconfig", "faros")
	}
	return c.Server
}

func TestRewriteKubeconfigServer(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{
			name: "in-cluster service",
			base: "https://faros-faros-hub.faros-dev.svc.cluster.local:9443",
			want: "https://faros-faros-hub.faros-dev.svc.cluster.local:9443/clusters/xhk2soqt2d3dujnw",
		},
		{
			name: "external host is a no-op on the path",
			base: "https://console-dev.faros.sh",
			want: "https://console-dev.faros.sh/clusters/xhk2soqt2d3dujnw",
		},
		{
			name: "trailing slash does not double up",
			base: "https://hub.example.com:9443/",
			want: "https://hub.example.com:9443/clusters/xhk2soqt2d3dujnw",
		},
		{
			name: "base path prefix is preserved",
			base: "https://gw.example.com/faros",
			want: "https://gw.example.com/faros/clusters/xhk2soqt2d3dujnw",
		},
		{
			// A base that already carries a /clusters/ suffix must not stack
			// with the entry's own path.
			name: "clusters suffix in base is stripped",
			base: "https://hub.example.com:9443/clusters/someotherws",
			want: "https://hub.example.com:9443/clusters/xhk2soqt2d3dujnw",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := rewriteKubeconfigServer([]byte(mintedKubeconfig), tc.base)
			if err != nil {
				t.Fatalf("rewriteKubeconfigServer: %v", err)
			}
			if got := serverOf(t, out); got != tc.want {
				t.Errorf("server: got %q, want %q", got, tc.want)
			}
		})
	}
}

// The credential and the TLS posture must survive the rewrite — the token is
// the whole point of the file, and insecure-skip-tls-verify is why swapping the
// host needs no cert SAN work.
func TestRewriteKubeconfigServerPreservesCredentials(t *testing.T) {
	out, err := rewriteKubeconfigServer([]byte(mintedKubeconfig), "https://svc.internal:9443")
	if err != nil {
		t.Fatalf("rewriteKubeconfigServer: %v", err)
	}
	cfg, err := clientcmd.Load(out)
	if err != nil {
		t.Fatalf("loading rewritten kubeconfig: %v", err)
	}
	if got := cfg.AuthInfos["faros"].Token; got != "sa-token-abc123" {
		t.Errorf("token: got %q, want %q", got, "sa-token-abc123")
	}
	if !cfg.Clusters["faros"].InsecureSkipTLSVerify {
		t.Error("insecure-skip-tls-verify was dropped")
	}
	if cfg.CurrentContext != "faros" {
		t.Errorf("current-context: got %q, want %q", cfg.CurrentContext, "faros")
	}
}

func TestRewriteKubeconfigServerRejectsBadBase(t *testing.T) {
	for _, base := range []string{"", "not-a-url", "https://"} {
		if _, err := rewriteKubeconfigServer([]byte(mintedKubeconfig), base); err == nil {
			t.Errorf("base %q: expected an error, got none", base)
		}
	}
}

func TestParseKubeconfigServerMode(t *testing.T) {
	tests := []struct {
		in      string
		want    KubeconfigServerMode
		wantErr bool
	}{
		{in: "", want: ServerModeAsMinted},
		{in: "external", want: ServerModeExternal},
		{in: "internal", want: ServerModeInternal},
		{in: "Internal", wantErr: true},
		{in: "in-cluster", wantErr: true},
	}
	for _, tc := range tests {
		got, err := ParseKubeconfigServerMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseKubeconfigServerMode(%q): expected an error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseKubeconfigServerMode(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseKubeconfigServerMode(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAvailableKubeconfigServerModes(t *testing.T) {
	tests := []struct {
		name     string
		external string
		internal string
		want     []KubeconfigServerMode
	}{
		{
			name:     "both configured",
			external: "https://console-dev.faros.sh",
			internal: "https://svc.internal:9443",
			want:     []KubeconfigServerMode{ServerModeExternal, ServerModeInternal},
		},
		{
			name:     "internal unset",
			external: "https://console-dev.faros.sh",
			want:     []KubeconfigServerMode{ServerModeExternal},
		},
		{
			name: "neither configured",
			want: []KubeconfigServerMode{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{hubExternalURL: tc.external, hubInternalURL: tc.internal}
			got := s.AvailableKubeconfigServerModes()
			if len(got) != len(tc.want) {
				t.Fatalf("modes: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("modes[%d]: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// Asking for an address the hub was not started with is the caller's mistake,
// and the handler maps it to 400 — so it has to be distinguishable by errors.Is.
func TestServerBaseForUnavailable(t *testing.T) {
	s := &Service{hubExternalURL: "https://console-dev.faros.sh"}

	if _, err := s.serverBaseFor(ServerModeInternal); !errors.Is(err, ErrServerModeUnavailable) {
		t.Errorf("internal without --hub-internal-url: got %v, want ErrServerModeUnavailable", err)
	}
	got, err := s.serverBaseFor(ServerModeExternal)
	if err != nil {
		t.Fatalf("external: %v", err)
	}
	if got != "https://console-dev.faros.sh" {
		t.Errorf("external base: got %q", got)
	}

	empty := &Service{}
	if _, err := empty.serverBaseFor(ServerModeExternal); !errors.Is(err, ErrServerModeUnavailable) {
		t.Errorf("external without --hub-external-url: got %v, want ErrServerModeUnavailable", err)
	}
	// The message must name the flag an operator has to set.
	if _, err := empty.serverBaseFor(ServerModeInternal); err == nil || !strings.Contains(err.Error(), "--hub-internal-url") {
		t.Errorf("expected the internal error to name --hub-internal-url, got %v", err)
	}
}
