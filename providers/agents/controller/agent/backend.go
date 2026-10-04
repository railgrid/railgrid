// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package agent

// The BackendReady condition: can this agent's turns actually execute?
//
// It is a third condition rather than another Validated reason because it
// answers a different question. Validated is about the spec — is what the author
// wrote coherent — and it is stable until somebody edits the object.
// BackendReady is about the WORLD: a laptop that went to sleep, a harness
// somebody uninstalled, a credential whose Secret was deleted. Those change with
// no edit at all, and reporting them as "your agent is malformed" would send the
// reader to fix a file that is already right.
//
// It is also deliberately NOT a run failure. A run dispatched at an unready
// runner fails with whatever the protocol says, three hops away from the person
// who pressed the button; the same fact published on the object lets a portal
// grey out the button instead. So the reconciler resolves the whole chain — edge
// → discovered Service → its harness — and says what it found.

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/edgeref"
	"github.com/railgrid/provider-agents/llm"
)

// setBackendReady records the backend verdict. reason=="" is ready.
func setBackendReady(agent *agentsv1alpha1.Agent, reason, message string) bool {
	cond := metav1.Condition{
		Type:               agentsv1alpha1.ConditionBackendReady,
		Status:             metav1.ConditionTrue,
		Reason:             agentsv1alpha1.ReasonBackendReady,
		Message:            "this agent's backend can execute a turn",
		ObservedGeneration: agent.Generation,
	}
	if reason != "" {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reason, message
	}
	return meta.SetStatusCondition(&agent.Status.Conditions, cond)
}

// validateBackend resolves the agent's backend and reports whether it is ready,
// stamping status.backend with what a turn will get.
//
// An error is a read that could not be completed and says nothing about the
// backend; the caller leaves the condition alone and lets the retry settle it.
func (r *Reconciler) validateBackend(ctx context.Context, c client.Client, agent *agentsv1alpha1.Agent, creds credentialVerdict) (reason, message string, status *agentsv1alpha1.AgentBackendStatus, err error) {
	out := &agentsv1alpha1.AgentBackendStatus{Type: agent.Spec.BackendType()}
	if !agent.Spec.HarnessBacked() {
		// A model backend's readiness IS its credentials' readiness, which
		// ModelCredentialsReady already computed this reconcile. Re-deriving it
		// would be two reads and two chances to disagree.
		return creds.reason, creds.message, out, nil
	}

	cfg := agent.Spec.Harness()
	if cfg == nil {
		return agentsv1alpha1.ReasonInvalidSpec,
			`spec.backend.type is "harness" but spec.backend.harness is missing`, out, nil
	}

	// Which harness, from the credential. There is no harness field on the agent
	// precisely so this cannot disagree with the credential.
	cred, cerr := readModelCredential(ctx, c, cfg.CredentialRef)
	switch {
	case apierrors.IsNotFound(cerr):
		return agentsv1alpha1.ReasonUnknownModelCredential,
			fmt.Sprintf("spec.backend.harness.credentialRef names model credential %q, which does not exist in this workspace", cfg.CredentialRef), out, nil
	case cerr != nil:
		return "", "", out, cerr
	}
	selector := llm.HarnessSelector(cred.Spec.Provider)
	if selector == "" {
		return agentsv1alpha1.ReasonUnsupportedHarnessCredential, fmt.Sprintf(
			"model credential %q has provider %q, which is a chat endpoint; a harness-backed agent needs a credential whose provider is %q or %q",
			cfg.CredentialRef, cred.Spec.Provider,
			agentsv1alpha1.ModelProviderClaudeCode, agentsv1alpha1.ModelProviderCodex), out, nil
	}
	if !meta.IsStatusConditionTrue(cred.Status.Conditions, agentsv1alpha1.ConditionReady) {
		return agentsv1alpha1.ReasonModelCredentialNotReady, fmt.Sprintf(
			"model credential %q is not Ready; check its status for what to fix", cfg.CredentialRef), out, nil
	}

	// The edge.
	gvk, ok := edgeref.EdgeGVK(cfg.EdgeRef.Kind)
	if !ok {
		return agentsv1alpha1.ReasonInvalidSpec, fmt.Sprintf(
			"spec.backend.harness.edgeRef.kind %q is not a host edge; a runner is a process on a machine, so it is %s or %s",
			cfg.EdgeRef.Kind, edgeref.KindLinuxServer, edgeref.KindMacOSServer), out, nil
	}
	edge := &unstructured.Unstructured{}
	edge.SetGroupVersionKind(gvk)
	switch err := c.Get(ctx, types.NamespacedName{Name: cfg.EdgeRef.Name}, edge); {
	case unreadableKind(err):
		// The edges API is not bound in this workspace at all, so nothing here can
		// see the machine. False with BackendUnknown, because a reader has to be
		// able to tell "not ready" from "nobody could look".
		return agentsv1alpha1.ReasonBackendUnknown, fmt.Sprintf(
			"%s is not readable in this workspace: enable the edges provider and accept the agents provider's %s requirement",
			gvk.Kind, edgeref.GroupName), out, nil
	case apierrors.IsNotFound(err):
		return agentsv1alpha1.ReasonUnknownEdgeRef, fmt.Sprintf(
			"spec.backend.harness.edgeRef names %s %q, which does not exist in this workspace",
			cfg.EdgeRef.Kind, cfg.EdgeRef.Name), out, nil
	case err != nil:
		return "", "", out, err
	}

	// The discovered runner Service on it, named after the edge and the harness
	// SELECTOR — which is not the name the harness advertises.
	serviceName := edgeref.RunnerServiceName(cfg.EdgeRef.Name, selector)
	out.Service = serviceName
	service := &unstructured.Unstructured{}
	service.SetGroupVersionKind(edgeref.ServiceGVK())
	switch err := c.Get(ctx, types.NamespacedName{Name: serviceName}, service); {
	case unreadableKind(err):
		return agentsv1alpha1.ReasonBackendUnknown, fmt.Sprintf(
			"edges Services are not readable in this workspace: enable the edges provider and accept the agents provider's %s requirement",
			edgeref.GroupName), out, nil
	case apierrors.IsNotFound(err):
		return agentsv1alpha1.ReasonHarnessServiceMissing, fmt.Sprintf(
			"%s %q publishes no runner Service %q, so the %s harness is not installed or not enabled on that machine",
			cfg.EdgeRef.Kind, cfg.EdgeRef.Name, serviceName, selector), out, nil
	case err != nil:
		return "", "", out, err
	}

	harness, found := edgeref.HarnessStatusOf(service)
	if !found {
		return agentsv1alpha1.ReasonHarnessServiceMissing, fmt.Sprintf(
			"Service %q reports no harness, so it is not a runner", serviceName), out, nil
	}
	// What a run will get, published whether or not it is ready: a portal showing
	// "claude-code 2.1.4, not ready" is more use than an empty box.
	out.Harness = &agentsv1alpha1.AgentHarnessStatus{Name: harness.Name, Version: harness.Version}
	// Discovery survives a disconnected tunnel. Its last harness probe cannot
	// establish that the machine or the Service is reachable now.
	connected, _, _ := unstructured.NestedBool(edge.Object, "status", "connected")
	if !connected {
		return agentsv1alpha1.ReasonHarnessNotReady, fmt.Sprintf(
			"%s %q is disconnected", cfg.EdgeRef.Kind, cfg.EdgeRef.Name), out, nil
	}
	conditions, _, _ := unstructured.NestedSlice(service.Object, "status", "conditions")
	serviceReady := false
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if ok && condition["type"] == "Ready" {
			serviceReady = condition["status"] == string(metav1.ConditionTrue)
			break
		}
	}
	if !serviceReady {
		return agentsv1alpha1.ReasonHarnessNotReady, fmt.Sprintf("runner Service %q is not Ready", serviceName), out, nil
	}
	if !harness.Ready {
		detail := ""
		if len(harness.Reasons) > 0 {
			detail = ": " + strings.Join(harness.Reasons, "; ")
		}
		return agentsv1alpha1.ReasonHarnessNotReady, fmt.Sprintf(
			"the %s harness on %s %q is not ready%s", selector, cfg.EdgeRef.Kind, cfg.EdgeRef.Name, detail), out, nil
	}
	return "", "", out, nil
}

// credentialVerdict is the ModelCredentialsReady answer, passed into the backend
// check so a model-backed agent's readiness is computed once.
type credentialVerdict struct {
	reason  string
	message string
}

// readModelCredential reads one ModelCredential by name.
func readModelCredential(ctx context.Context, c client.Client, name string) (*agentsv1alpha1.ModelCredential, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, apierrors.NewNotFound(agentsv1alpha1.SchemeGroupVersion.WithResource("modelcredentials").GroupResource(), name)
	}
	var cred agentsv1alpha1.ModelCredential
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &cred); err != nil {
		return nil, err
	}
	return &cred, nil
}

// unreadableKind reports a kind this workspace cannot serve at all — the edges
// APIBinding is absent, or the claim was never accepted. It is a configuration
// state to report, not a transient error to retry forever.
func unreadableKind(err error) bool {
	if err == nil {
		return false
	}
	return meta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err) ||
		apierrors.IsMethodNotSupported(err) || apierrors.IsForbidden(err)
}

// harnessMeaninglessFields reports the first spec field that cannot mean anything
// for a harness-backed agent.
//
// Each one is REJECTED rather than ignored, and that is the whole point. An
// ignored spec.tools grant reads as a granted one: the author believes they
// scoped what the agent may do, and the harness — which brings its own Bash and
// its own Edit — is bounded by none of it. Same for a per-purpose model: there is
// no second model to route to, so "background: cheap" is a cost saving that is
// not happening. Silence would make each of these a wrong belief rather than a
// wrong file.
func harnessMeaninglessFields(agent *agentsv1alpha1.Agent) (reason, message string) {
	if len(agent.Spec.Tools.Interactive.Families) > 0 || len(agent.Spec.Tools.Interactive.Connections) > 0 ||
		len(agent.Spec.Tools.Interactive.Toolsets) > 0 || len(agent.Spec.Tools.Interactive.RequireApproval) > 0 ||
		len(agent.Spec.Tools.Background.Families) > 0 || len(agent.Spec.Tools.Background.Connections) > 0 ||
		len(agent.Spec.Tools.Background.Toolsets) > 0 || len(agent.Spec.Tools.Background.RequireApproval) > 0 {
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			"spec.tools has no effect on a harness-backed agent: the harness brings its own tools, and a grant here would read as a restriction that is not enforced anywhere. Remove it."
	}
	for purpose := range agent.Spec.ModelCredentials() {
		if purpose != llm.PurposeChat {
			return agentsv1alpha1.ReasonMeaninglessForHarness, fmt.Sprintf(
				"spec.backend.model.credentials[%q] has no effect on a harness-backed agent: there is one harness session and no purpose to route. Use spec.backend.harness instead.", purpose)
		}
	}
	if len(agent.Spec.ModelFallbacks()) > 0 {
		return agentsv1alpha1.ReasonMeaninglessForHarness,
			"spec.backend.model.fallbacks has no effect on a harness-backed agent: a turn runs on one machine's harness, and there is no second endpoint to fail over to."
	}
	return "", ""
}
