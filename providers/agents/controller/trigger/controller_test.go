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
	testWebhook = "github"
)

func trigger(webhookRef, agentRef string) *agentsv1alpha1.Trigger {
	tr := &agentsv1alpha1.Trigger{ObjectMeta: metav1.ObjectMeta{Name: testTrigger}}
	tr.Spec.WebhookRef = webhookRef
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

func webhook(name string) *agentsv1alpha1.Webhook {
	w := &agentsv1alpha1.Webhook{ObjectMeta: metav1.ObjectMeta{Name: name}}
	w.Spec.Source = agentsv1alpha1.WebhookSourceGitHub
	return w
}

type harness struct {
	c client.Client
	r *Reconciler
}

func newHarness(t *testing.T, objs ...client.Object) *harness {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Trigger{}).
		WithObjects(objs...).
		Build()
	return &harness{c: c, r: &Reconciler{Manager: fakeManager{c: c}}}
}

func (h *harness) reconcileNamed(t *testing.T, name string) agentsv1alpha1.Trigger {
	t.Helper()
	req := mcreconcile.Request{ClusterName: testCluster, Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: name}}}
	if _, err := h.r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Trigger
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func (h *harness) reconcile(t *testing.T) agentsv1alpha1.Trigger {
	t.Helper()
	return h.reconcileNamed(t, testTrigger)
}

func validated(t *testing.T, tr agentsv1alpha1.Trigger) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(tr.Status.Conditions, agentsv1alpha1.ConditionValidated)
	if c == nil {
		t.Fatalf("no %s condition: %+v", agentsv1alpha1.ConditionValidated, tr.Status.Conditions)
	}
	return *c
}

// ---- the Validated condition ------------------------------------------------

func TestValidatedTrueOnASoundSpec(t *testing.T) {
	h := newHarness(t, trigger(testWebhook, testAgent), agent(testAgent), webhook(testWebhook))
	cond := validated(t, h.reconcile(t))
	if cond.Status != metav1.ConditionTrue || cond.Reason != agentsv1alpha1.ReasonValidated {
		t.Fatalf("condition = %s/%s (%q), want True/Validated", cond.Status, cond.Reason, cond.Message)
	}
}

// A settled trigger is not written again: the condition is the only thing
// this reconciler writes, and rewriting an unchanged one would fight the
// delivery path's status stamps for nothing.
func TestSettledTriggerIsNotRewritten(t *testing.T) {
	h := newHarness(t, trigger(testWebhook, testAgent), agent(testAgent), webhook(testWebhook))
	first := h.reconcile(t)
	if second := h.reconcile(t); second.ResourceVersion != first.ResourceVersion {
		t.Fatalf("a settled trigger was written again (rv %s -> %s)", first.ResourceVersion, second.ResourceVersion)
	}
}

func TestValidatedRejectsMissingRequiredFields(t *testing.T) {
	h := newHarness(t, trigger(testWebhook, ""), webhook(testWebhook))
	if cond := validated(t, h.reconcile(t)); cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("empty agentRef: reason = %s, want InvalidSpec", cond.Reason)
	}
}

// A Trigger from before the Webhook kind has no webhookRef and a URL already
// pasted into GitHub. The reconciler gives it a Webhook of its own name — the
// path is keyed by name, so the URL is unchanged — and points the trigger at
// it, instead of leaving the trigger invalid and the URL answering 404 until
// somebody notices.
func TestLegacyTriggerIsMigratedToAWebhookOfItsOwnName(t *testing.T) {
	h := newHarness(t, trigger("", testAgent), agent(testAgent))
	got := h.reconcile(t)
	if got.Spec.WebhookRef != testTrigger {
		t.Fatalf("spec.webhookRef = %q, want the trigger's own name %q", got.Spec.WebhookRef, testTrigger)
	}
	var wh agentsv1alpha1.Webhook
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: testTrigger}, &wh); err != nil {
		t.Fatalf("no Webhook %q was created: %v", testTrigger, err)
	}
	if wh.Spec.Source != agentsv1alpha1.WebhookSourceWebhook {
		t.Errorf("migrated webhook source = %q, want webhook", wh.Spec.Source)
	}
	// The spec write re-delivers the object; the next pass validates it.
	if cond := validated(t, h.reconcile(t)); cond.Status != metav1.ConditionTrue {
		t.Fatalf("after migration Validated = %s/%s, want True", cond.Status, cond.Reason)
	}
}

// A Webhook of the trigger's name may already exist — the tenant made one, or
// an earlier pass created it and the spec write conflicted. It is adopted as
// it stands, not replaced.
func TestLegacyTriggerAdoptsAnExistingWebhookOfItsName(t *testing.T) {
	existing := webhook(testTrigger)
	existing.Spec.DisplayName = "mine"
	h := newHarness(t, trigger("", testAgent), agent(testAgent), existing)
	if got := h.reconcile(t); got.Spec.WebhookRef != testTrigger {
		t.Fatalf("spec.webhookRef = %q, want %q", got.Spec.WebhookRef, testTrigger)
	}
	var wh agentsv1alpha1.Webhook
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: testTrigger}, &wh); err != nil {
		t.Fatal(err)
	}
	if wh.Spec.Source != agentsv1alpha1.WebhookSourceGitHub || wh.Spec.DisplayName != "mine" {
		t.Errorf("the existing webhook was rewritten: %+v", wh.Spec)
	}
}

// A dangling agentRef means the event arrives, finds no agent, and is dropped
// in silence.
func TestValidatedRejectsAnUnknownAgentRef(t *testing.T) {
	h := newHarness(t, trigger(testWebhook, "ghost"), webhook(testWebhook))
	cond := validated(t, h.reconcile(t))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonUnknownAgentRef {
		t.Fatalf("condition = %s/%s, want False/UnknownAgentRef", cond.Status, cond.Reason)
	}
	if !strings.Contains(cond.Message, "ghost") {
		t.Fatalf("message must name the agent: %q", cond.Message)
	}
}

// A dangling webhookRef means nothing is ever routed to the trigger.
func TestValidatedRejectsAnUnknownWebhookRef(t *testing.T) {
	h := newHarness(t, trigger("ghost", testAgent), agent(testAgent))
	cond := validated(t, h.reconcile(t))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonUnknownWebhookRef {
		t.Fatalf("condition = %s/%s, want False/UnknownWebhookRef", cond.Status, cond.Reason)
	}
	if !strings.Contains(cond.Message, "ghost") {
		t.Fatalf("message must name the webhook: %q", cond.Message)
	}
}

// The matcher ignores a key it does not know, so a typo filters nothing. The
// condition is the only place that is said.
func TestValidatedFlagsUnknownFilterKeys(t *testing.T) {
	tr := trigger(testWebhook, testAgent)
	tr.Spec.Filter = map[string]string{"eventType": "push", "evenType": "push", "branch": "main"}
	h := newHarness(t, tr, agent(testAgent), webhook(testWebhook))
	cond := validated(t, h.reconcile(t))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonUnknownFilterKey {
		t.Fatalf("condition = %s/%s, want False/UnknownFilterKey", cond.Status, cond.Reason)
	}
	for _, want := range []string{"branch", "evenType", "ignored"} {
		if !strings.Contains(cond.Message, want) {
			t.Fatalf("message must say %q: %q", want, cond.Message)
		}
	}
	if listed, _, _ := strings.Cut(cond.Message, " are not"); strings.Contains(listed, "eventType") {
		t.Fatalf("a known key must not be listed as unknown: %q", cond.Message)
	}
}

func TestWebhookChangeEnqueuesTriggersThatReferenceIt(t *testing.T) {
	first := trigger(testWebhook, testAgent)
	first.Name = "first"
	second := trigger(" "+testWebhook+" ", testAgent)
	second.Name = "second"
	unrelated := trigger("other", testAgent)
	unrelated.Name = "unrelated"
	h := newHarness(t, first, second, unrelated, agent(testAgent))
	for _, name := range []string{"first", "second"} {
		if cond := validated(t, h.reconcileNamed(t, name)); cond.Reason != agentsv1alpha1.ReasonUnknownWebhookRef {
			t.Fatalf("initial condition for %s = %s, want UnknownWebhookRef", name, cond.Reason)
		}
	}
	if err := h.c.Create(context.Background(), webhook(testWebhook)); err != nil {
		t.Fatal(err)
	}

	got := TriggerRequestsForWebhook(context.Background(), h.c, testWebhook)
	names := map[string]bool{}
	for _, req := range got {
		names[req.Name] = true
	}
	if len(got) != 2 || !names["first"] || !names["second"] || names["unrelated"] {
		t.Fatalf("requests = %+v, want first and second only", got)
	}
	for _, req := range got {
		if cond := validated(t, h.reconcileNamed(t, req.Name)); cond.Status != metav1.ConditionTrue {
			t.Fatalf("condition for %s after webhook creation = %+v, want True", req.Name, cond)
		}
	}
}

// A channelRef naming nothing still fires, but answers somewhere its author
// did not ask for — worth saying, not worth blocking.
func TestValidatedRejectsAChannelRefTheAgentDoesNotHave(t *testing.T) {
	tr := trigger(testWebhook, testAgent)
	tr.Spec.ChannelRef = "incidents"
	h := newHarness(t, tr, agent(testAgent, "primary"), webhook(testWebhook))
	cond := validated(t, h.reconcile(t))
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("condition = %s/%s, want False/InvalidSpec", cond.Status, cond.Reason)
	}

	h = newHarness(t, tr, agent(testAgent, "incidents"), webhook(testWebhook))
	if cond := validated(t, h.reconcile(t)); cond.Status != metav1.ConditionTrue {
		t.Fatalf("a channelRef the agent has must validate: %s/%s (%q)", cond.Status, cond.Reason, cond.Message)
	}
}

// ---- reconcile mechanics ----------------------------------------------------

// A trigger deleted between the event and the reconcile is not an error.
func TestReconcileIgnoresMissingTrigger(t *testing.T) {
	h := newHarness(t)
	req := mcreconcile.Request{ClusterName: testCluster, Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "gone"}}}
	if _, err := h.r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("a missing trigger must not error: %v", err)
	}
}

// A trigger on its way out is left alone: its status is about to go with it.
func TestReconcileIgnoresDeletingTrigger(t *testing.T) {
	tr := trigger(testWebhook, testAgent)
	tr.Finalizers = []string{"test.railgrid.ai/hold"}
	h := newHarness(t, tr, agent(testAgent), webhook(testWebhook))
	if err := h.c.Delete(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	if got := h.reconcile(t); len(got.Status.Conditions) != 0 {
		t.Fatalf("a deleting trigger must not be validated: %+v", got.Status)
	}
}
