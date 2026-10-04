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
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/agent/tunnel"
	"github.com/railgrid/railgrid/pkg/apiurl"
)

func TestRestartWithSavedCredentialUsesItBeforeEdgeRegistration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const edgeName = "linux-restart"
	const hubURL = "https://console.127.0.0.1.sslip.io:9443"
	issued := tunnel.Credential{
		Token:     "synthetic-issued-test-token",
		TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour),
		HubURL:    hubURL,
		Provider:  "edges-connectivity",
		ClusterID: "tenant-cluster",
		Resource:  "linuxservers",
		Name:      edgeName,
	}
	if err := SaveAgentCredential(edgeName, issued); err != nil {
		t.Fatalf("saving test credential: %v", err)
	}

	options := NewOptions()
	options.EdgeName = edgeName
	options.HubURL = hubURL
	options.Type = AgentTypeServer
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
}
