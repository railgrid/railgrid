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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// TestAHarnessBackedAgentIsNotToldToFixAChatCredential.
//
// Reported from a live workspace: the editor showed "Not ready —
// NoModelCredential. This agent cannot run a turn yet", advising the reader to
// point a field at a ModelCredential. Both halves were wrong for a
// harness-backed agent — it can run, and its editor deliberately offers no such
// field — so the message sent somebody looking for a control that does not
// exist.
func TestAHarnessBackedAgentIsNotToldToFixAChatCredential(t *testing.T) {
	agent := &agentsv1alpha1.Agent{}
	agent.Name = "coder"
	agent.Spec.Backend = agentsv1alpha1.AgentBackendSpec{
		Type:    agentsv1alpha1.AgentBackendHarness,
		Harness: &agentsv1alpha1.AgentHarnessBackend{CredentialRef: "my-claude"},
	}

	setModelCredentialsReady(agent, "", "")

	cond := meta.FindStatusCondition(agent.Status.Conditions, agentsv1alpha1.ConditionModelCredentialsReady)
	if cond == nil {
		t.Fatal("the condition disappeared; anything watching reads that as 'not evaluated yet'")
	}
	if cond.Status != "True" {
		t.Errorf("ModelCredentialsReady = %s, want True: a harness-backed agent needs no chat credential", cond.Status)
	}
	if cond.Reason != agentsv1alpha1.ReasonModelCredentialsNotApplicable {
		t.Errorf("reason = %q, want %q", cond.Reason, agentsv1alpha1.ReasonModelCredentialsNotApplicable)
	}
	if !strings.Contains(cond.Message, "spec.backend.harness.credentialRef") {
		t.Errorf("the message should name the field that DOES decide this: %q", cond.Message)
	}
}

// TestTheMissingCredentialMessageNamesAFieldThatExists: the advice used to name
// spec.models, which no longer exists on the CRD, so following it was
// impossible and the portal had no control for it.
func TestTheMissingCredentialMessageNamesAFieldThatExists(t *testing.T) {
	agent := &agentsv1alpha1.Agent{}
	agent.Name = "chatty"

	reason, message, err := (&Reconciler{}).validateModelCredentials(t.Context(), nil, agent)
	if err != nil {
		t.Fatalf("validateModelCredentials: %v", err)
	}
	if reason != agentsv1alpha1.ReasonNoModelCredential {
		t.Fatalf("reason = %q, want %q", reason, agentsv1alpha1.ReasonNoModelCredential)
	}
	if strings.Contains(message, "spec.models") {
		t.Errorf("the message names a removed field: %q", message)
	}
	if !strings.Contains(message, "spec.backend.model.credentials") {
		t.Errorf("the message should name the field a person actually edits: %q", message)
	}
}
