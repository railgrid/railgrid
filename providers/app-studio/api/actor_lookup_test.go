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

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/provider-sdk/dataplane"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

// staticActors is an actorLookup over a fixed user→user table, standing in
// for the identity a kcp shard stamps. A stamped user absent from the table
// is the actor verbatim, which is the production rule.
type staticActors map[string]string

func (t staticActors) lookup(_ context.Context, _ string, caller dataplane.ProxiedIdentity) (string, error) {
	if caller.User == "" {
		return "", errors.New("no stamped caller")
	}
	if user, ok := t[caller.User]; ok {
		return user, nil
	}
	return caller.User, nil
}

// defaultTestActors gives the HTTP-level tests the actor the old
// X-Railgrid-User header used to supply.
var defaultTestActors = staticActors{"test-user": "test-user"}

// The actor is the identity kcp stamped after authenticating the caller,
// not a header this provider merely receives. A forged X-Railgrid-User must
// not become anyone.
func TestActorComesFromTheStampedIdentityNotTheLabel(t *testing.T) {
	s := &Server{
		tenantWorkspaces: testWorkspaceLookup("cluster-a", "org-a", "workspace-a"),
	}

	r := httptest.NewRequest(http.MethodGet, testVerbPath("cluster-a", "projects", "demo", "view"), nil)
	r.Header.Set("X-Railgrid-User", "mallory")
	r = stampTestCaller(r, "alice")
	id, ok := s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok {
		t.Fatal("identity should authenticate")
	}
	if id.user != "alice" || id.caller == nil || id.caller.User != "alice" {
		t.Fatalf("actor = %q (%+v), want the stamped subject %q", id.user, id.caller, "alice")
	}
	if id.userLabel != "mallory" {
		t.Fatalf("userLabel = %q, want the header kept as a label", id.userLabel)
	}
}

// Before serve's adapter (the replica-affinity layer) the stamp is read off
// the X-Remote-* headers themselves; a request carrying neither has no actor
// and the reason is recorded — never a silent fallback to the label.
func TestActorIsReadFromHeadersBeforeTheAdapterAndUnresolvedWithout(t *testing.T) {
	s := &Server{tenantWorkspaces: testWorkspaceLookup("cluster-a", "org-a", "workspace-a")}

	r := httptest.NewRequest(http.MethodGet, testVerbPath("cluster-a", "projects", "demo", "view"), nil)
	r.Header.Set(dataplane.HeaderRemoteUser, "alice")
	id, ok := s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok || id.user != "alice" {
		t.Fatalf("identity = %+v/%v, want alice from X-Remote-User", id, ok)
	}

	r = httptest.NewRequest(http.MethodGet, testVerbPath("cluster-a", "projects", "demo", "view"), nil)
	r.Header.Set("X-Railgrid-User", "mallory")
	id, _ = s.identityFromRequest(httptest.NewRecorder(), r)
	if id.user != "" {
		t.Fatalf("actor = %q, want empty without a stamp", id.user)
	}
	if !errors.Is(id.userErr, errNoActorLookup) {
		t.Fatalf("userErr = %v, want errNoActorLookup", id.userErr)
	}
}

func TestIdentityCapturesOnlyOneHubActionProof(t *testing.T) {
	s := &Server{
		tenantWorkspaces: testWorkspaceLookup("cluster-a", "org-a", "workspace-a"),
		tenantActors:     defaultTestActors.lookup,
	}
	r := httptest.NewRequest(http.MethodGet, testVerbPath("cluster-a", "projects", "demo", "publishing-members"), nil)
	r = stampTestCaller(r, "test-user")
	r.Header.Set(dataplane.HeaderActionProof, "signed-short-lived-proof")
	id, ok := s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok || id.actionProof != "signed-short-lived-proof" {
		t.Fatalf("identity proof = %q / ok=%v, want the one inbound action proof", id.actionProof, ok)
	}

	r.Header.Add(dataplane.HeaderActionProof, "second-value")
	id, ok = s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok || id.actionProof != "" {
		t.Fatalf("duplicate action-proof headers produced %q / ok=%v, want an empty proof", id.actionProof, ok)
	}
}

func TestProjectMCPRequestCarriesOnlyTheProjectIdentity(t *testing.T) {
	s := &Server{hubToken: "provider-token"}
	s.projectIdentityTokenFor = func(_ context.Context, _ identity, p *aiv1alpha1.Project) (string, error) {
		if p == nil || p.Name != "demo" {
			t.Fatalf("project identity requested for %#v, want the demo Project", p)
		}
		return "project-token", nil
	}
	r := httptest.NewRequest(http.MethodPost, "https://hub.example/mcp", nil)
	r.Header.Set("Authorization", "Bearer caller-token")
	r.Header.Set(dataplane.HeaderActionProof, "short-lived-proof")
	id := identity{tenant: "cluster-a", clusterID: "cluster-a", user: "alice"}
	out, err := s.projectMCPRequest(context.Background(), r, id, &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "uid-demo"}})
	if err != nil {
		t.Fatalf("projectMCPRequest: %v", err)
	}
	if got := out.Header.Get(dataplane.HeaderActionProof); got != "" {
		t.Fatalf("MCP request forwarded action proof %q", got)
	}
	// The aggregate admits a tenant identity holding `use` on the MCPServer
	// and forwards that bearer to every provider; the provider's own token is
	// neither admitted nor the identity the tools should act as.
	if got := out.Header.Get("Authorization"); got != "Bearer project-token" {
		t.Fatalf("MCP request authorization = %q, want the Project identity", got)
	}
	if got := out.Header.Get(dataplane.HeaderUser); got != "alice" {
		t.Fatalf("X-Railgrid-User = %q, want the caller's name as a label", got)
	}
	if r.Header.Get("Authorization") != "Bearer caller-token" {
		t.Fatal("projectMCPRequest mutated the inbound request")
	}
}

func TestProjectMCPRequestRefusesWithoutAProjectIdentity(t *testing.T) {
	s := &Server{hubToken: "provider-token"}
	s.projectIdentityTokenFor = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		return "", errors.New("identity service down")
	}
	r := httptest.NewRequest(http.MethodPost, "https://hub.example/mcp", nil)
	if _, err := s.projectMCPRequest(context.Background(), r, identity{clusterID: "cluster-a"}, &aiv1alpha1.Project{}); err == nil {
		t.Fatal("projectMCPRequest succeeded without a Project identity; it must not fall back to the provider token")
	}
}
