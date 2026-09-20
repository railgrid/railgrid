// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package trigger

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/internal/webhookpath"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
)

type fakeManager struct {
	mcmanager.Manager
	c client.Client
}

func (m fakeManager) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return fakeCluster{c: m.c}, nil
}

type fakeCluster struct {
	cluster.Cluster
	c client.Client
}

func (c fakeCluster) GetClient() client.Client { return c.c }

const (
	testCluster = "tenant-a"
	testTrigger = "deploys"
	testAgent   = "releaser"
)

var testKey = webhookpath.Key("test-key", "")

func trigger(source, agentRef string) *agentsv1alpha1.Trigger {
	tr := &agentsv1alpha1.Trigger{ObjectMeta: metav1.ObjectMeta{Name: testTrigger}}
	tr.Spec.Source = source
	tr.Spec.AgentRef = agentRef
	tr.Spec.Task = "ship it"
	return tr
}

func agent(name string, channels ...string) *agentsv1alpha1.Agent {
	a := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name}}
	a.Spec.DisplayName = name
	for i, ch := range channels {
		a.Spec.Channels = append(a.Spec.Channels, agentsv1alpha1.AgentChannel{Name: ch, ConnectionRef: "tg", Primary: i == 0})
	}
	return a
}

func conn(name string) *agentsv1alpha1.Connection {
	c := &agentsv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: name}}
	c.Spec.Type = agentsv1alpha1.ConnectionTypeGitHub
	return c
}

type harness struct {
	c client.Client
	r *Reconciler
}

func newHarness(t *testing.T, key []byte, objs ...client.Object) *harness {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Trigger{}).
		WithObjects(objs...).
		Build()
	return &harness{c: c, r: &Reconciler{Manager: fakeManager{c: c}, WebhookKey: key}}
}

func (h *harness) reconcile(t *testing.T) agentsv1alpha1.Trigger {
	t.Helper()
	req := mcreconcile.Request{ClusterName: testCluster, Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: testTrigger}}}
	if _, err := h.r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Trigger
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: testTrigger}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func validated(t *testing.T, tr agentsv1alpha1.Trigger) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(tr.Status.Conditions, agentsv1alpha1.ConditionValidated)
	if c == nil {
		t.Fatalf("no %s condition: %+v", agentsv1alpha1.ConditionValidated, tr.Status.Conditions)
	}
	return *c
}

// ---- status.webhookPath: the load-bearing part -------------------------------

// A Trigger created through the kube client gets its inbound URL from nowhere
// else. This is the whole reason the reconciler exists.
func TestWebhookPathIsMinted(t *testing.T) {
	h := newHarness(t, testKey, trigger(agentsv1alpha1.TriggerSourceWebhook, testAgent), agent(testAgent))
	got := h.reconcile(t)
	want := webhookpath.For(testKey, testCluster, testTrigger)
	if want == "" {
		t.Fatal("test setup: no key")
	}
	if got.Status.WebhookPath != want {
		t.Fatalf("webhookPath = %q, want %q", got.Status.WebhookPath, want)
	}
}

// The path is the credential and it is pasted into somebody else's system the
// day it is minted. Re-deriving it must land on the same string. The api layer
// and this reconciler now share internal/webhookpath, so they cannot disagree;
// what this pins is the SHAPE, spelled out literally rather than built from
// the package that produces it, so a change to the derivation fails here
// instead of quietly invalidating every URL already in the wild.
func TestWebhookPathMatchesTheHTTPLayerDerivation(t *testing.T) {
	h := newHarness(t, testKey, trigger(agentsv1alpha1.TriggerSourceWebhook, testAgent), agent(testAgent))
	got := h.reconcile(t)
	const prefix = "/services/providers/agents/webhooks/triggers/" + testCluster + "/" + testTrigger + "/"
	if !strings.HasPrefix(got.Status.WebhookPath, prefix) {
		t.Fatalf("webhookPath = %q, want prefix %q", got.Status.WebhookPath, prefix)
	}
	if token := strings.TrimPrefix(got.Status.WebhookPath, prefix); len(token) != 32 {
		t.Fatalf("token = %q (%d chars), want a 32-char hmac", token, len(token))
	}
}

// A github source is delivered over the same inbound endpoint, so it gets a
// path too.
func TestWebhookPathIsMintedForGitHubSources(t *testing.T) {
	h := newHarness(t, testKey, trigger(agentsv1alpha1.TriggerSourceGitHub, testAgent), agent(testAgent))
	if got := h.reconcile(t); got.Status.WebhookPath == "" {
		t.Fatal("a github trigger must get an inbound path")
	}
}

// api/triggers.go stamps the same value on the MCP path. Converging on it is
// not a fight: a trigger that already has the right path is not rewritten.
func TestWebhookPathIsNotRewrittenWhenAlreadyCorrect(t *testing.T) {
	tr := trigger(agentsv1alpha1.TriggerSourceWebhook, testAgent)
	tr.Status.WebhookPath = webhookpath.For(testKey, testCluster, testTrigger)
	h := newHarness(t, testKey, tr, agent(testAgent))
	first := h.reconcile(t) // writes the condition
	rv := first.ResourceVersion
	second := h.reconcile(t)
	if second.ResourceVersion != rv {
		t.Fatalf("a settled trigger was written again (rv %s -> %s)", rv, second.ResourceVersion)
	}
	if second.Status.WebhookPath != tr.Status.WebhookPath {
		t.Fatalf("webhookPath changed: %q", second.Status.WebhookPath)
	}
}

// A path left over from a source that no longer wants one would keep an
// endpoint alive that the spec no longer describes.
func TestWebhookPathIsClearedForAnUnsupportedSource(t *testing.T) {
	tr := trigger("email", testAgent)
	tr.Status.WebhookPath = "/services/providers/agents/webhooks/triggers/tenant-a/deploys/stale"
	h := newHarness(t, testKey, tr, agent(testAgent))
	if got := h.reconcile(t); got.Status.WebhookPath != "" {
		t.Fatalf("webhookPath = %q, want cleared", got.Status.WebhookPath)
	}
}

// No key configured means no URL can be minted. Clearing the stored one would
// revoke a URL that still works for whoever holds it — the provider being
// misconfigured is not a reason to break every existing trigger.
func TestWebhookPathIsLeftAloneWithoutAKey(t *testing.T) {
	tr := trigger(agentsv1alpha1.TriggerSourceWebhook, testAgent)
	tr.Status.WebhookPath = "/services/providers/agents/webhooks/triggers/tenant-a/deploys/existing"
	h := newHarness(t, nil, tr, agent(testAgent))
	if got := h.reconcile(t); got.Status.WebhookPath != tr.Status.WebhookPath {
		t.Fatalf("webhookPath = %q, want it left alone", got.Status.WebhookPath)
	}
}

// ---- the Validated condition ------------------------------------------------

func TestValidatedTrueOnASoundSpec(t *testing.T) {
	h := newHarness(t, testKey, trigger(agentsv1alpha1.TriggerSourceWebhook, testAgent), agent(testAgent))
	cond := validated(t, h.reconcile(t))
	if cond.Status != metav1.ConditionTrue || cond.Reason != agentsv1alpha1.ReasonValidated {
		t.Fatalf("condition = %s/%s (%q), want True/Validated", cond.Status, cond.Reason, cond.Message)
	}
}

// applyTriggerCreate: "unsupported source X (use webhook or github)".
func TestValidatedRejectsAnUnsupportedSource(t *testing.T) {
	h := newHarness(t, testKey, trigger("email", testAgent), agent(testAgent))
	cond := validated(t, h.reconcile(t))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonInvalidSource {
		t.Fatalf("condition = %s/%s, want False/InvalidSource", cond.Status, cond.Reason)
	}
	if !strings.Contains(cond.Message, "email") {
		t.Fatalf("message must name the offending value: %q", cond.Message)
	}
}

// applyTriggerCreate: "name, agentRef, and source are required".
func TestValidatedRejectsMissingRequiredFields(t *testing.T) {
	h := newHarness(t, testKey, trigger(agentsv1alpha1.TriggerSourceWebhook, ""))
	if cond := validated(t, h.reconcile(t)); cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("empty agentRef: reason = %s, want InvalidSpec", cond.Reason)
	}
	h = newHarness(t, testKey, trigger("", testAgent), agent(testAgent))
	if cond := validated(t, h.reconcile(t)); cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("empty source: reason = %s, want InvalidSpec", cond.Reason)
	}
}

// A dangling agentRef means the event arrives, finds no agent, and is dropped
// in silence. The REST layer never checked this at all.
func TestValidatedRejectsAnUnknownAgentRef(t *testing.T) {
	h := newHarness(t, testKey, trigger(agentsv1alpha1.TriggerSourceWebhook, "ghost"))
	cond := validated(t, h.reconcile(t))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonUnknownAgentRef {
		t.Fatalf("condition = %s/%s, want False/UnknownAgentRef", cond.Status, cond.Reason)
	}
	if !strings.Contains(cond.Message, "ghost") {
		t.Fatalf("message must name the agent: %q", cond.Message)
	}
}

func TestValidatedRejectsAnUnknownConnectionRef(t *testing.T) {
	tr := trigger(agentsv1alpha1.TriggerSourceGitHub, testAgent)
	tr.Spec.ConnectionRef = "ghost"
	h := newHarness(t, testKey, tr, agent(testAgent))
	if cond := validated(t, h.reconcile(t)); cond.Reason != agentsv1alpha1.ReasonUnknownConnectionRef {
		t.Fatalf("reason = %s, want UnknownConnectionRef", cond.Reason)
	}

	tr2 := trigger(agentsv1alpha1.TriggerSourceGitHub, testAgent)
	tr2.Spec.ConnectionRef = "gh"
	h = newHarness(t, testKey, tr2, agent(testAgent), conn("gh"))
	if cond := validated(t, h.reconcile(t)); cond.Status != metav1.ConditionTrue {
		t.Fatalf("a resolvable connectionRef must validate: %s/%s (%q)", cond.Status, cond.Reason, cond.Message)
	}
}

// A channelRef naming nothing still fires, but answers somewhere its author
// did not ask for — worth saying, not worth blocking.
func TestValidatedRejectsAChannelRefTheAgentDoesNotHave(t *testing.T) {
	tr := trigger(agentsv1alpha1.TriggerSourceWebhook, testAgent)
	tr.Spec.ChannelRef = "incidents"
	h := newHarness(t, testKey, tr, agent(testAgent, "primary"))
	cond := validated(t, h.reconcile(t))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("condition = %s/%s, want False/InvalidSpec", cond.Status, cond.Reason)
	}
	// ...and the trigger is still given its URL: the verdict is a diagnosis,
	// not a refusal to provision.
	got := h.reconcile(t)
	if got.Status.WebhookPath == "" {
		t.Fatal("a trigger with a bad channelRef must still get its inbound URL")
	}

	h = newHarness(t, testKey, tr, agent(testAgent, "incidents"))
	if cond := validated(t, h.reconcile(t)); cond.Status != metav1.ConditionTrue {
		t.Fatalf("a channelRef the agent has must validate: %s/%s (%q)", cond.Status, cond.Reason, cond.Message)
	}
}

// ---- reconcile mechanics ----------------------------------------------------

// A trigger deleted between the event and the reconcile is not an error.
func TestReconcileIgnoresMissingTrigger(t *testing.T) {
	h := newHarness(t, testKey)
	req := mcreconcile.Request{ClusterName: testCluster, Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "gone"}}}
	if _, err := h.r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("a missing trigger must not error: %v", err)
	}
}

// A trigger on its way out is left alone: there is nothing to provision and
// its status is about to go with it.
func TestReconcileIgnoresDeletingTrigger(t *testing.T) {
	tr := trigger(agentsv1alpha1.TriggerSourceWebhook, testAgent)
	tr.Finalizers = []string{"test.railgrid.ai/hold"}
	h := newHarness(t, testKey, tr, agent(testAgent))
	ctx := context.Background()
	if err := h.c.Delete(ctx, tr); err != nil {
		t.Fatal(err)
	}
	got := h.reconcile(t)
	if got.Status.WebhookPath != "" || len(got.Status.Conditions) != 0 {
		t.Fatalf("a deleting trigger must not be provisioned: %+v", got.Status)
	}
}
