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

package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/pkg/agent/tunnel"
	"github.com/railgrid/railgrid/pkg/apiurl"
)

func TestRestartWithSavedCredentialUsesItBeforeEdgeRegistration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const edgeName = "linux-restart"
	const sshCredentialsPath = "/clusters/tenant-cluster/apis/edges.railgrid.ai/v1alpha1/linuxservers/linux-restart/ssh-credentials"
	type observedRequest struct {
		path          string
		authorization string
		credentials   tunnel.SSHCredentials
	}
	gotRequests := make(chan observedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed := observedRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization")}
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read SSH credential request body: %v", err)
		} else if err := json.Unmarshal(payload, &observed.credentials); err != nil {
			t.Errorf("decode SSH credential request body: %v", err)
		}
		gotRequests <- observed
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	hubURL := server.URL
	issued := tunnel.Credential{
		Token:              "synthetic-issued-test-token",
		TokenType:          "Bearer",
		ExpiresAt:          time.Now().Add(time.Hour),
		HubURL:             hubURL,
		Provider:           "edges-connectivity",
		ClusterID:          "tenant-cluster",
		Resource:           "linuxservers",
		Name:               edgeName,
		SSHCredentialsPath: sshCredentialsPath,
	}
	if err := SaveAgentCredential(edgeName, issued); err != nil {
		t.Fatalf("saving test credential: %v", err)
	}

	options := NewOptions()
	options.EdgeName = edgeName
	options.HubURL = hubURL
	// Installed service arguments may retain the bootstrap token after the
	// first successful enrollment. The saved scoped credential takes precedence
	// on restart, so it must still be usable for operator credential rotation.
	options.Token = "stale-bootstrap-token"
	options.Type = AgentTypeServer
	options.SSHUser = "rotation-user"
	options.SSHPassword = "rotated-on-restart"
	options.InsecureSkipTLSVerify = true

	agent, err := New(options)
	if err != nil {
		t.Fatalf("creating agent for restart: %v", err)
	}

	if !agent.hasIssuedCredential() {
		t.Fatal("restart did not adopt the saved issued credential")
	}
	if agent.shouldRegisterEdge() {
		t.Fatal("restart with an issued credential must skip edge registration")
	}
	if !agent.shouldSetupSSHCredentials() {
		t.Fatal("restart with an issued credential must remain eligible to submit operator SSH credential rotations")
	}
	if agent.hubConfig.BearerToken != issued.Token {
		t.Fatal("restart hub client did not use the saved issued credential")
	}
	if want := apiurl.HubServerURL(hubURL, issued.ClusterID); agent.hubConfig.Host != want {
		t.Fatalf("restart hub client host = %q, want credential workspace host %q", agent.hubConfig.Host, want)
	}
	if agent.hubTLSConfig == nil || !agent.hubTLSConfig.InsecureSkipVerify {
		t.Fatal("restart tunnel TLS config was not rebuilt from the saved credential settings")
	}
	if agent.credentials.TLSConfig != agent.hubTLSConfig {
		t.Fatal("credential refresh client and hub config do not share the restored TLS settings")
	}

	agent.credentials.HTTPClient = server.Client()
	if err := agent.setupSSHCredentials(context.Background(), klog.Background(), nil); err != nil {
		t.Fatalf("submit rotated SSH credentials with the saved agent credential: %v", err)
	}
	var gotRequest observedRequest
	select {
	case gotRequest = <-gotRequests:
	case <-time.After(2 * time.Second):
		t.Fatal("rotated SSH credentials were not uploaded")
	}
	if gotRequest.path != sshCredentialsPath {
		t.Fatalf("SSH credential request path = %q, want %q", gotRequest.path, sshCredentialsPath)
	}
	if gotRequest.authorization != "Bearer "+issued.Token {
		t.Fatalf("SSH credential request authorization = %q, want the saved issued credential", gotRequest.authorization)
	}
	if gotRequest.credentials.Username != options.SSHUser || gotRequest.credentials.Password != options.SSHPassword {
		t.Fatalf("uploaded SSH credentials = %+v, want the operator's rotated username and password", gotRequest.credentials)
	}
}

// A pod restart is the case the in-cluster Secret exists for: the agent's
// filesystem is gone, so the enrolment bundle can only come from the Secret.
// Reading only the file made the agent fall back to its bootstrap join token on
// every pod restart, which the hub refuses once that token has been rotated or
// orphaned -- the edge then stays Disconnected for good.
func TestPodRestartLoadsTheCredentialFromItsSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // a fresh pod: no credential on disk
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")

	const edgeName = "k8s-restart"
	stored := tunnel.Credential{
		Token:     "credential-from-the-secret",
		TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour),
		HubURL:    "https://hub.example.com",
		ClusterID: "tenant-cluster",
		Resource:  "kubernetesclusters",
		Name:      edgeName,
	}
	secretReads := 0
	stubSecretLoader(t, func(name string) (tunnel.Credential, bool, error) {
		secretReads++
		if name != edgeName {
			t.Errorf("secret loader called for %q, want %q", name, edgeName)
		}
		return stored, true, nil
	})

	store := agentForCredentialTest(edgeName, stored.HubURL).newCredentialStore()

	if secretReads == 0 {
		t.Fatal("the agent never looked in its Secret; a pod restart would fall back to the join token")
	}
	adopted, ok := store.Current()
	if !ok {
		t.Fatal("pod restart did not adopt the credential stored in the Secret")
	}
	if adopted.Token != stored.Token {
		t.Fatalf("adopted token = %q, want the credential from the Secret", adopted.Token)
	}
	if store.Token() != stored.Token {
		t.Fatalf("store serves %q, want the credential from the Secret rather than the join token", store.Token())
	}
}

// The file still wins when it is there, and the Secret is not consulted at all:
// outside a pod there is no Secret to read, and inside one the two copies are
// written together.
func TestSavedCredentialOnDiskIsPreferredToTheSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.43.0.1")

	const edgeName = "k8s-disk-wins"
	onDisk := tunnel.Credential{
		Token:     "credential-from-disk",
		TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour),
		HubURL:    "https://hub.example.com",
		ClusterID: "tenant-cluster",
		Resource:  "kubernetesclusters",
		Name:      edgeName,
	}
	if err := SaveAgentCredential(edgeName, onDisk); err != nil {
		t.Fatalf("saving test credential: %v", err)
	}
	stubSecretLoader(t, func(string) (tunnel.Credential, bool, error) {
		t.Error("the Secret was read although a credential was present on disk")
		return tunnel.Credential{}, false, nil
	})

	store := agentForCredentialTest(edgeName, onDisk.HubURL).newCredentialStore()

	if store.Token() != onDisk.Token {
		t.Fatalf("store serves %q, want the credential from disk", store.Token())
	}
}

// agentForCredentialTest builds the minimum Agent newCredentialStore needs.
// Going through New() would also build a downstream cluster config, which is
// unrelated to credential loading and fails wherever no kubeconfig exists.
func agentForCredentialTest(edgeName, hubURL string) *Agent {
	options := NewOptions()
	options.EdgeName = edgeName
	options.HubURL = hubURL
	// Installed service arguments keep the bootstrap token after enrolment; the
	// saved credential must win over it.
	options.Token = "stale-bootstrap-token"
	options.Type = AgentTypeKubernetes
	options.InsecureSkipTLSVerify = true
	return &Agent{opts: options}
}

// stubSecretLoader swaps the in-cluster Secret loader for the duration of a
// test; the real one builds its own in-cluster client.
func stubSecretLoader(t *testing.T, fn func(string) (tunnel.Credential, bool, error)) {
	t.Helper()
	original := loadCredentialFromSecret
	loadCredentialFromSecret = fn
	t.Cleanup(func() { loadCredentialFromSecret = original })
}
