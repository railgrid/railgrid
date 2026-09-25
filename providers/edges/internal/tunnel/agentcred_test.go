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

package tunnel

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// The enrolment bundle is the agent's ONLY source of addressing: the hub URL
// it calls, the CA it trusts, and the exact routes it refreshes and posts SSH
// credentials at. If any of that went missing on the wire the agent would have
// to guess, which is the format-string coupling contract 3 rule 5 exists to
// prevent — so the round trip is pinned here, on the provider's side, against
// the same JSON the agent decodes.
func TestAgentCredentialRoundTrip(t *testing.T) {
	want := AgentCredential{
		Token:              "tok-1",
		TokenType:          "Bearer",
		ExpiresAt:          time.Now().Add(time.Hour).UTC().Truncate(time.Second),
		HubURL:             "https://hub.example.com",
		CACertData:         []byte("-----BEGIN CERTIFICATE-----"),
		Provider:           "edges",
		ClusterID:          "2hx82dl9ncmepp5l",
		Resource:           "linuxservers",
		Name:               "edge-1",
		RefreshPath:        "/clusters/2hx82dl9ncmepp5l/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/agent-token",
		SSHCredentialsPath: "/clusters/2hx82dl9ncmepp5l/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/ssh-credentials",
	}

	encoded, err := want.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeAgentCredential(encoded)
	if err != nil {
		t.Fatalf("DecodeAgentCredential: %v", err)
	}
	if got.Token != want.Token || got.HubURL != want.HubURL || got.ClusterID != want.ClusterID ||
		got.Resource != want.Resource || got.Name != want.Name ||
		got.RefreshPath != want.RefreshPath || got.SSHCredentialsPath != want.SSHCredentialsPath ||
		string(got.CACertData) != string(want.CACertData) {
		t.Fatalf("round-trip lost addressing:\n got  %+v\n want %+v", got, want)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("expiry did not survive: got %s want %s", got.ExpiresAt, want.ExpiresAt)
	}

	if _, err := DecodeAgentCredential("not base64 !!"); err == nil {
		t.Error("a malformed bundle must be an error")
	}
	tokenless, _ := json.Marshal(AgentCredential{HubURL: "https://hub.example.com"})
	if _, err := DecodeAgentCredential(base64.StdEncoding.EncodeToString(tokenless)); err == nil {
		t.Error("a tokenless bundle must be an error, not an enrolment")
	}
}

// The routes in the bundle are the SAME hub-relative kube paths an edge's
// status.URL is stamped with — the verbs as kcp custom subresources on this
// provider's export — so an agent and a CLI client never disagree about where
// a verb is. The agent composes hubURL + path and kcp authenticates its
// ServiceAccount token on the front door.
func TestAgentCredentialRoutesAreKubePaths(t *testing.T) {
	s := testServer()
	const base = "/clusters/2hx82dl9ncmepp5l/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1"

	if got, want := s.publicVerbPath("2hx82dl9ncmepp5l", linuxServerResource, "edge-1", VerbAgentToken),
		base+"/agent-token"; got != want {
		t.Fatalf("refresh path = %q, want %q", got, want)
	}
	if got, want := s.publicVerbPath("2hx82dl9ncmepp5l", linuxServerResource, "edge-1", VerbSSHCredentials),
		base+"/ssh-credentials"; got != want {
		t.Fatalf("ssh-credentials path = %q, want %q", got, want)
	}
	if got, want := s.publicVerbPath("2hx82dl9ncmepp5l", linuxServerResource, "edge-1", VerbAddonCredentials),
		base+"/addon-credentials"; got != want {
		t.Fatalf("addon-credentials path = %q, want %q", got, want)
	}

	// A coordinate that would not parse back — a workspace path for the
	// cluster — yields no route rather than one the agent would 400 on.
	if got := s.publicVerbPath("root:railgrid:tenants:acme", linuxServerResource, "edge-1", VerbAgentToken); got != "" {
		t.Fatalf("an unrenderable coordinate produced %q, want empty", got)
	}
}

// Both credential verbs must be served on every kind whose agents exist, and
// ssh-credentials only where there is an SSH data plane to hold credentials
// for.
func TestCredentialVerbsAreServed(t *testing.T) {
	for _, resource := range []string{kubernetesClusterResource, linuxServerResource, macOSServerResource} {
		if !verbServed(resource, VerbAgentToken) {
			t.Errorf("%s does not serve agent-token; its agents could never rotate their credential", resource)
		}
	}
	if !verbServed(linuxServerResource, VerbSSHCredentials) {
		t.Error("linuxservers must serve ssh-credentials")
	}
	for _, resource := range []string{kubernetesClusterResource, macOSServerResource, serviceResource} {
		if verbServed(resource, VerbSSHCredentials) {
			t.Errorf("%s serves ssh-credentials but has no SSH data plane", resource)
		}
	}
}
