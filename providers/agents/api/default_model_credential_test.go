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
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tenant"
)

// credentialFixture is a ModelCredential as the workspace stores one.
type credentialFixture struct {
	name, created, provider, model string
	isDefault                      bool
}

func defaultCredentialTestClient(creds ...credentialFixture) *agentsclient.Client {
	objs := make([]runtime.Object, 0, len(creds))
	for _, cred := range creds {
		spec := map[string]any{"provider": cred.provider, "model": cred.model, "baseURL": "https://models.example/v1"}
		if cred.isDefault {
			spec["default"] = true
		}
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": agentsv1alpha1.SchemeGroupVersion.String(),
			"kind":       "ModelCredential",
			"metadata":   map[string]any{"name": cred.name, "creationTimestamp": cred.created},
			"spec":       spec,
		}})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		agentsclient.AgentGVR:           "AgentList",
		agentsclient.ScheduleGVR:        "ScheduleList",
		agentsclient.ConnectionGVR:      "ConnectionList",
		agentsclient.ModelCredentialGVR: "ModelCredentialList",
	}, objs...)
	return agentsclient.NewFromScope(tenant.NewScopeFromDynamic("workspace-cluster", dyn))
}

func TestCreateAgentWithoutAModelGetsTheWorkspaceDefault(t *testing.T) {
	s := newMCPTestServer(t)
	c := defaultCredentialTestClient(
		credentialFixture{name: "mine", created: "2026-11-01T00:00:00Z", provider: "openai-compatible", model: "gpt-x"},
		credentialFixture{name: "newer-default", created: "2026-11-10T00:00:00Z", provider: "openai-compatible", model: "b", isDefault: true},
		credentialFixture{name: "railgrid", created: "2026-11-05T00:00:00Z", provider: "openai-compatible", model: "default", isDefault: true},
		// Marked, but not something a chat agent can run on.
		credentialFixture{name: "harness", created: "2026-10-01T00:00:00Z", provider: "claude-code", isDefault: true},
		credentialFixture{name: "no-model", created: "2026-10-02T00:00:00Z", provider: "openai-compatible", isDefault: true},
	)

	a, _, err := s.createAgentFromRequest(context.Background(), c, &createAgentRequest{Name: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Spec.ModelCredentialFor("chat"); got != "railgrid" {
		t.Fatalf("chat credential = %q, want the oldest usable default (railgrid)", got)
	}

	// Naming a credential is never overridden.
	a, _, err = s.createAgentFromRequest(context.Background(), c, &createAgentRequest{Name: "explicit", ModelCredential: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Spec.ModelCredentialFor("chat"); got != "mine" {
		t.Fatalf("chat credential = %q, want mine", got)
	}
}

func TestCreateAgentWithoutAModelOrADefaultStaysUnassigned(t *testing.T) {
	s := newMCPTestServer(t)
	c := defaultCredentialTestClient(
		credentialFixture{name: "mine", created: "2026-11-01T00:00:00Z", provider: "openai-compatible", model: "gpt-x"},
	)
	a, _, err := s.createAgentFromRequest(context.Background(), c, &createAgentRequest{Name: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Spec.ModelCredentialFor("chat"); got != "" {
		t.Fatalf("chat credential = %q; an unmarked credential must not be picked for the user", got)
	}
}

func TestTemplateModelSlotIsFilledFromTheDefaultOnlyWhenEmpty(t *testing.T) {
	c := defaultCredentialTestClient(
		credentialFixture{name: "railgrid", created: "2026-11-05T00:00:00Z", provider: "openai-compatible", model: "default", isDefault: true},
	)
	req := &createAgentRequest{Name: "a", Template: "news-reporter", Wiring: map[string]string{"channel": "tg"}}
	if err := applyDefaultModelCredential(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}
	if req.ModelCredential != "railgrid" {
		t.Fatalf("modelCredential = %q, want railgrid", req.ModelCredential)
	}

	req = &createAgentRequest{Name: "b", Template: "news-reporter", Wiring: map[string]string{"modelCredential": "openai"}}
	if err := applyDefaultModelCredential(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}
	if req.ModelCredential != "" {
		t.Fatalf("modelCredential = %q; a wired model slot must be left alone", req.ModelCredential)
	}
}
