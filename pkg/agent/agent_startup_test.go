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
