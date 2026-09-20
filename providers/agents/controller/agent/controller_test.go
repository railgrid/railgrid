// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

func reconcileAgent(t *testing.T, agent *agentsv1alpha1.Agent) agentsv1alpha1.Agent {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Agent{}).
		WithObjects(agent).
		Build()
	r := &Reconciler{Manager: fakeManager{c: c}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Agent
	if err := c.Get(context.Background(), client.ObjectKey{Name: agent.Name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// An agent with no phase — created before the create handler stamped one, or
// whose stamp failed — reads as Ready after one reconcile.
func TestReconcileStampsReadyOnEmptyPhase(t *testing.T) {
	got := reconcileAgent(t, &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "helper"}})
	if got.Status.Phase != agentsv1alpha1.AgentPhaseReady {
		t.Fatalf("phase = %q, want Ready", got.Status.Phase)
	}
}

// Any other phase is somebody else's decision (budget suspension, a manual
// pause) and must be left alone.
func TestReconcileLeavesNonEmptyPhaseAlone(t *testing.T) {
	a := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "paused"}}
	a.Status.Phase = "Suspended"
	a.Status.SuspendedReason = "budget exceeded"
	got := reconcileAgent(t, a)
	if got.Status.Phase != "Suspended" || got.Status.SuspendedReason != "budget exceeded" {
		t.Fatalf("status changed: %+v", got.Status)
	}
}

// A missing agent (deleted between the event and the reconcile) is not an error.
func TestReconcileIgnoresMissingAgent(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(agentsscheme.NewScheme()).Build()
	r := &Reconciler{Manager: fakeManager{c: c}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "gone"}}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("a missing agent must not error: %v", err)
	}
}

// ---- the Validated condition ------------------------------------------------

// validAgent is the smallest spec the reconciler has nothing to say about.
func validAgent(name string) *agentsv1alpha1.Agent {
	a := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name}}
	a.Spec.DisplayName = name
	return a
}

func conn(name string) *agentsv1alpha1.Connection {
	c := &agentsv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: name}}
	c.Spec.Type = agentsv1alpha1.ConnectionTypeTelegram
	return c
}

// validated returns the Validated condition, failing the test when absent —
// every reconciled agent must carry a verdict, one way or the other.
func validated(t *testing.T, a agentsv1alpha1.Agent) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(a.Status.Conditions, agentsv1alpha1.ConditionValidated)
	if c == nil {
		t.Fatalf("no %s condition on %s: %+v", agentsv1alpha1.ConditionValidated, a.Name, a.Status.Conditions)
	}
	return *c
}

// reconcileAgents reconciles the first object with the rest of the workspace
// present, and returns the first object as stored.
func reconcileAgents(t *testing.T, subject *agentsv1alpha1.Agent, rest ...client.Object) agentsv1alpha1.Agent {
	t.Helper()
	objs := append([]client.Object{subject}, rest...)
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Agent{}).
		WithObjects(objs...).
		Build()
	r := &Reconciler{Manager: fakeManager{c: c}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: subject.Name}}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Agent
	if err := c.Get(context.Background(), client.ObjectKey{Name: subject.Name}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// assertInvalid reconciles and asserts Validated=False with the given reason.
func assertInvalid(t *testing.T, reason string, subject *agentsv1alpha1.Agent, rest ...client.Object) metav1.Condition {
	t.Helper()
	cond := validated(t, reconcileAgents(t, subject, rest...))
	if cond.Status != metav1.ConditionFalse || cond.Reason != reason {
		t.Fatalf("condition = %s/%s (%q), want False/%s", cond.Status, cond.Reason, cond.Message, reason)
	}
	return cond
}

// A spec with nothing wrong with it says so, rather than saying nothing.
func TestValidatedTrueOnASoundSpec(t *testing.T) {
	cond := validated(t, reconcileAgents(t, validAgent("helper")))
	if cond.Status != metav1.ConditionTrue || cond.Reason != agentsv1alpha1.ReasonValidated {
		t.Fatalf("condition = %s/%s (%q), want True/Validated", cond.Status, cond.Reason, cond.Message)
	}
}

// agentFromCreateRequest defaulted an empty displayName to the agent's name;
// a kube-client writer gets no such favour, so the gap is named.
func TestValidatedRejectsEmptyDisplayName(t *testing.T) {
	a := &agentsv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "nameless"}}
	cond := assertInvalid(t, agentsv1alpha1.ReasonInvalidSpec, a)
	if !strings.Contains(cond.Message, "displayName") {
		t.Fatalf("message must name the field: %q", cond.Message)
	}
}

// normalizeAgentBudget's checks: usdLimit is a decimal string and nothing but
// this has ever parsed it.
func TestValidatedRejectsUnusableBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget agentsv1alpha1.AgentBudget
	}{
		{"negative tokens", agentsv1alpha1.AgentBudget{Window: "month", TokenLimit: -1}},
		{"not a number", agentsv1alpha1.AgentBudget{Window: "month", USDLimit: "ten dollars"}},
		{"negative usd", agentsv1alpha1.AgentBudget{Window: "month", USDLimit: "-5"}},
		{"infinite usd", agentsv1alpha1.AgentBudget{Window: "month", USDLimit: "1e999"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := validAgent("spender")
			a.Spec.Budget = &tc.budget
			assertInvalid(t, agentsv1alpha1.ReasonInvalidBudget, a)
		})
	}
}

// ...and accepts the shapes it always accepted, so a working budget is not
// suddenly flagged.
func TestValidatedAcceptsAUsableBudget(t *testing.T) {
	for _, usd := range []string{"", "0", "12.50", ".5", "1e2", "+3"} {
		a := validAgent("spender")
		a.Spec.Budget = &agentsv1alpha1.AgentBudget{Window: "month", USDLimit: usd, TokenLimit: 100}
		if cond := validated(t, reconcileAgents(t, a)); cond.Status != metav1.ConditionTrue {
			t.Fatalf("usdLimit %q: %s/%s (%q), want True", usd, cond.Status, cond.Reason, cond.Message)
		}
	}
}

func TestValidatedRejectsNegativeLimits(t *testing.T) {
	a := validAgent("bounded")
	a.Spec.Limits.MaxToolTurns = -1
	assertInvalid(t, agentsv1alpha1.ReasonInvalidSpec, a)

	b := validAgent("bounded")
	b.Spec.Limits.TimeoutSeconds = -1
	assertInvalid(t, agentsv1alpha1.ReasonInvalidSpec, b)
}

// applyAgentUpdate dropped a self-delegation silently; it is named now.
func TestValidatedRejectsSelfDelegation(t *testing.T) {
	a := validAgent("solo")
	a.Spec.Delegates = []string{"solo"}
	assertInvalid(t, agentsv1alpha1.ReasonInvalidSpec, a)
}

// normalizeFamilies dropped an unrecognized family on the floor. Dropping it
// here would mean an agent whose grant reads right and does nothing.
func TestValidatedRejectsUnknownFamily(t *testing.T) {
	a := validAgent("tooled")
	a.Spec.Tools.Interactive.Families = []string{"core", "wheb"}
	cond := assertInvalid(t, agentsv1alpha1.ReasonUnknownToolFamily, a)
	if !strings.Contains(cond.Message, "wheb") {
		t.Fatalf("message must name the offending value: %q", cond.Message)
	}
}

// normalizeFamilies always prepended "core". Without that rewrite, a grant
// that omits it is the one shape that silently costs an agent its notify,
// memory and delegate tools — so it is called out on both run classes.
func TestValidatedRejectsFamiliesWithoutCore(t *testing.T) {
	a := validAgent("mute")
	a.Spec.Tools.Background.Families = []string{"web"}
	assertInvalid(t, agentsv1alpha1.ReasonMissingCoreFamily, a)
}

// An empty grant is not a broken one: the run-time default applies.
func TestValidatedAllowsAnEmptyFamiliesGrant(t *testing.T) {
	if cond := validated(t, reconcileAgents(t, validAgent("default"))); cond.Status != metav1.ConditionTrue {
		t.Fatalf("condition = %s/%s (%q), want True", cond.Status, cond.Reason, cond.Message)
	}
}

// normalizeChannels: a half-filled row was an error rather than a silent drop,
// because dropping it made a save look successful while binding nothing.
func TestValidatedRejectsHalfFilledChannel(t *testing.T) {
	a := validAgent("chatty")
	a.Spec.Channels = []agentsv1alpha1.AgentChannel{{Name: "primary"}}
	cond := assertInvalid(t, agentsv1alpha1.ReasonInvalidSpec, a)
	if !strings.Contains(cond.Message, "connectionRef") {
		t.Fatalf("message must name the missing field: %q", cond.Message)
	}

	b := validAgent("chatty")
	b.Spec.Channels = []agentsv1alpha1.AgentChannel{{ConnectionRef: "tg"}}
	assertInvalid(t, agentsv1alpha1.ReasonInvalidSpec, b, conn("tg"))
}

func TestValidatedRejectsDuplicateChannelNames(t *testing.T) {
	a := validAgent("chatty")
	a.Spec.Channels = []agentsv1alpha1.AgentChannel{
		{Name: "primary", ConnectionRef: "tg"},
		{Name: "primary", ConnectionRef: "slack"},
	}
	cond := assertInvalid(t, agentsv1alpha1.ReasonDuplicateChannel, a, conn("tg"), conn("slack"))
	if !strings.Contains(cond.Message, "primary") {
		t.Fatalf("message must name the duplicate: %q", cond.Message)
	}
}

// normalizeChannels guaranteed exactly one primary by rewriting the rest; the
// reconciler says so instead.
func TestValidatedRejectsTwoPrimaryChannels(t *testing.T) {
	a := validAgent("chatty")
	a.Spec.Channels = []agentsv1alpha1.AgentChannel{
		{Name: "a", ConnectionRef: "tg", Primary: true},
		{Name: "b", ConnectionRef: "slack", Primary: true},
	}
	assertInvalid(t, agentsv1alpha1.ReasonInvalidSpec, a, conn("tg"), conn("slack"))
}

// A channel bound to a Connection that does not exist binds nothing.
func TestValidatedRejectsUnknownChannelConnection(t *testing.T) {
	a := validAgent("chatty")
	a.Spec.Channels = []agentsv1alpha1.AgentChannel{{Name: "primary", ConnectionRef: "ghost", Primary: true}}
	cond := assertInvalid(t, agentsv1alpha1.ReasonUnknownConnectionRef, a)
	if !strings.Contains(cond.Message, "ghost") {
		t.Fatalf("message must name the connection: %q", cond.Message)
	}
}

// validateChannelUniqueness: inbound routing maps a Connection to one agent,
// so the second agent to bind it is the one told.
func TestValidatedRejectsAChannelAnotherAgentHolds(t *testing.T) {
	first := validAgent("first")
	first.CreationTimestamp = metav1.NewTime(time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC))
	first.Spec.Channels = []agentsv1alpha1.AgentChannel{{Name: "primary", ConnectionRef: "tg", Primary: true}}

	second := validAgent("second")
	second.CreationTimestamp = metav1.NewTime(time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC))
	second.Spec.Channels = []agentsv1alpha1.AgentChannel{{Name: "primary", ConnectionRef: "tg", Primary: true}}

	cond := assertInvalid(t, agentsv1alpha1.ReasonChannelConflict, second, first, conn("tg"))
	if !strings.Contains(cond.Message, "first") {
		t.Fatalf("message must name the holder: %q", cond.Message)
	}
	// ...and the agent that had it first keeps it, so the pair does not end up
	// flagging each other into a stalemate.
	if cond := validated(t, reconcileAgents(t, first, second, conn("tg"))); cond.Status != metav1.ConditionTrue {
		t.Fatalf("the older agent must stay valid, got %s/%s (%q)", cond.Status, cond.Reason, cond.Message)
	}
}

// Same connection, same agent: not a conflict with itself.
func TestValidatedAllowsAnAgentsOwnChannels(t *testing.T) {
	a := validAgent("chatty")
	a.Spec.Channels = []agentsv1alpha1.AgentChannel{
		{Name: "primary", ConnectionRef: "tg", Primary: true},
		{Name: "alerts", ConnectionRef: "slack"},
	}
	if cond := validated(t, reconcileAgents(t, a, conn("tg"), conn("slack"))); cond.Status != metav1.ConditionTrue {
		t.Fatalf("condition = %s/%s (%q), want True", cond.Status, cond.Reason, cond.Message)
	}
}

// A second reconcile of an unchanged agent must write nothing: the condition's
// LastTransitionTime is what a status watch keys on, and a reconciler that
// rewrote it every pass would wake every watcher in the workspace forever.
func TestReconcileIsIdempotent(t *testing.T) {
	a := validAgent("steady")
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Agent{}).
		WithObjects(a).
		Build()
	r := &Reconciler{Manager: fakeManager{c: c}}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "steady"}}}
	for i := range 2 {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	var first agentsv1alpha1.Agent
	if err := c.Get(context.Background(), client.ObjectKey{Name: "steady"}, &first); err != nil {
		t.Fatal(err)
	}
	rv := first.ResourceVersion
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var second agentsv1alpha1.Agent
	if err := c.Get(context.Background(), client.ObjectKey{Name: "steady"}, &second); err != nil {
		t.Fatal(err)
	}
	if second.ResourceVersion != rv {
		t.Fatalf("a settled agent was written again (rv %s -> %s)", rv, second.ResourceVersion)
	}
}

// ---- deletion: purging the agent's store data --------------------------------

// purgeRecorder is the store teardown: what it was asked to remove, and
// whether it refuses.
type purgeRecorder struct {
	calls []string // "cluster/agent"
	err   error
}

func (p *purgeRecorder) purge(_ context.Context, clusterID, agentName string) error {
	p.calls = append(p.calls, clusterID+"/"+agentName)
	return p.err
}

// finalizerHarness reconciles one agent with a pinned clock and a recording
// purge.
type finalizerHarness struct {
	c     client.Client
	r     *Reconciler
	purge *purgeRecorder
}

func newFinalizerHarness(t *testing.T, a *agentsv1alpha1.Agent, clock time.Time) *finalizerHarness {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Agent{}).
		WithObjects(a).
		Build()
	p := &purgeRecorder{}
	return &finalizerHarness{c: c, purge: p, r: &Reconciler{
		Manager:   fakeManager{c: c},
		PurgeData: p.purge,
		Now:       func() time.Time { return clock },
	}}
}

func (h *finalizerHarness) reconcile(t *testing.T, name string) (ctrl.Result, *agentsv1alpha1.Agent) {
	t.Helper()
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: name}}}
	res, err := h.r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Agent
	switch err := h.c.Get(context.Background(), client.ObjectKey{Name: name}, &got); {
	case apierrors.IsNotFound(err):
		return res, nil // the finalizer went and the object with it
	case err != nil:
		t.Fatal(err)
	}
	return res, &got
}

// The CR is the agent's configuration; its transcripts, runs, memories and
// usage are rows in the provider store that only DELETE /api/agents/{name}
// ever removed. The finalizer is what keeps a kube-client delete from
// orphaning them.
func TestFinalizerIsAddedWhenThereIsAStoreToPurge(t *testing.T) {
	h := newFinalizerHarness(t, validAgent("helper"), time.Now())
	_, got := h.reconcile(t, "helper")
	if !controllerutil.ContainsFinalizer(got, DataFinalizer) {
		t.Fatalf("no finalizer: %v", got.Finalizers)
	}
}

// Without a store there is nothing to purge, and a finalizer nothing can clear
// would make every agent undeletable on the dev path.
func TestNoFinalizerWithoutAStore(t *testing.T) {
	got := reconcileAgents(t, validAgent("helper"))
	if controllerutil.ContainsFinalizer(&got, DataFinalizer) {
		t.Fatalf("finalizer added with no purge wired: %v", got.Finalizers)
	}
}

// The purge runs with the tenant cluster the reconcile came from — the
// provider maps that to the store's org/workspace scope on its side.
func TestDeletePurgesAndReleases(t *testing.T) {
	a := validAgent("helper")
	a.Finalizers = []string{DataFinalizer}
	h := newFinalizerHarness(t, a, time.Now())
	if err := h.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	_, got := h.reconcile(t, "helper")
	if len(h.purge.calls) != 1 || h.purge.calls[0] != "tenant-a/helper" {
		t.Fatalf("purge calls = %v, want [tenant-a/helper]", h.purge.calls)
	}
	if got != nil {
		t.Fatalf("the finalizer must be released: %v", got.Finalizers)
	}
}

// A failing purge is retried while the deadline holds — the delete already
// looks done to the user, so this is about finishing quietly.
func TestDeleteRetriesAFailingPurge(t *testing.T) {
	deleted := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	a := validAgent("helper")
	a.Finalizers = []string{DataFinalizer}
	h := newFinalizerHarness(t, a, deleted.Add(time.Minute))
	h.purge.err = errors.New("store unreachable")
	if err := h.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	res, got := h.reconcile(t, "helper")
	if got == nil || !controllerutil.ContainsFinalizer(got, DataFinalizer) {
		t.Fatal("the finalizer must be held while the purge is retried")
	}
	if res.RequeueAfter != purgeRetryInterval {
		t.Fatalf("requeueAfter = %s, want %s", res.RequeueAfter, purgeRetryInterval)
	}
}

// ...but never indefinitely. An object the user cannot remove and cannot
// explain is worse than rows an operator can go and delete.
func TestDeleteReleasesAfterTheDeadline(t *testing.T) {
	a := validAgent("helper")
	a.Finalizers = []string{DataFinalizer}
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Agent{}).
		WithObjects(a).
		Build()
	ctx := context.Background()
	if err := c.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	var deleting agentsv1alpha1.Agent
	if err := c.Get(ctx, client.ObjectKey{Name: "helper"}, &deleting); err != nil {
		t.Fatal(err)
	}
	p := &purgeRecorder{err: errors.New("store unreachable")}
	r := &Reconciler{
		Manager:   fakeManager{c: c},
		PurgeData: p.purge,
		// Past purgeGiveUp from the deletion timestamp the object goes,
		// whatever the store says.
		Now: func() time.Time { return deleting.DeletionTimestamp.Add(purgeGiveUp + time.Second) },
	}
	req := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "helper"}}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Agent
	if err := c.Get(ctx, client.ObjectKey{Name: "helper"}, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("the agent must be gone past the deadline; got %v (%v)", got.Finalizers, err)
	}
}

// A delete that arrives with no finalizer of ours (the store was wired after
// the agent was created) purges nothing and blocks nothing.
func TestDeleteWithoutOurFinalizerIsANoOp(t *testing.T) {
	a := validAgent("helper")
	a.Finalizers = []string{"other.example/hold"}
	h := newFinalizerHarness(t, a, time.Now())
	if err := h.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	_, got := h.reconcile(t, "helper")
	if len(h.purge.calls) != 0 {
		t.Fatalf("purge ran for an agent we do not hold: %v", h.purge.calls)
	}
	if got == nil || len(got.Finalizers) != 1 {
		t.Fatalf("another owner's finalizer must be left alone: %+v", got)
	}
}

// ---- the ModelCredentialsReady condition ------------------------------------

// readyCredential is a ModelCredential its own reconciler has already blessed.
func readyCredential(name string) *agentsv1alpha1.ModelCredential {
	cred := &agentsv1alpha1.ModelCredential{ObjectMeta: metav1.ObjectMeta{Name: name}}
	cred.Spec.BaseURL = "https://api.openai.com/v1"
	cred.Spec.SecretRef.Name = "railgrid-agents-model-" + name
	meta.SetStatusCondition(&cred.Status.Conditions, metav1.Condition{
		Type: agentsv1alpha1.ConditionReady, Status: metav1.ConditionTrue,
		Reason: agentsv1alpha1.ReasonReady, Message: "ready",
	})
	return cred
}

func unreadyCredential(name string) *agentsv1alpha1.ModelCredential {
	cred := readyCredential(name)
	meta.SetStatusCondition(&cred.Status.Conditions, metav1.Condition{
		Type: agentsv1alpha1.ConditionReady, Status: metav1.ConditionFalse,
		Reason: agentsv1alpha1.ReasonNotReady, Message: "the endpoint refused the key",
	})
	return cred
}

func modelCredentialsCondition(t *testing.T, got agentsv1alpha1.Agent) metav1.Condition {
	t.Helper()
	cond := meta.FindStatusCondition(got.Status.Conditions, agentsv1alpha1.ConditionModelCredentialsReady)
	if cond == nil {
		t.Fatalf("ModelCredentialsReady not set: %+v", got.Status.Conditions)
	}
	return *cond
}

// An agent's credentials are checked across the workspace, by name, and the
// verdict names the offending ones — that is what a person edits. It is a
// condition of its own rather than another Validated reason: a rotated key
// leaves the agent's spec perfectly correct.
func TestAgentModelCredentialsReady(t *testing.T) {
	for _, tc := range []struct {
		name        string
		models      map[string]string
		fallbacks   []string
		objects     []client.Object
		wantReason  string
		wantMessage string
	}{
		{
			name:       "no credential named",
			wantReason: agentsv1alpha1.ReasonNoModelCredential,
		},
		{
			name:        "unknown credential",
			models:      map[string]string{"chat": "ghost"},
			wantReason:  agentsv1alpha1.ReasonUnknownModelCredential,
			wantMessage: "ghost",
		},
		{
			name:        "credential not ready",
			models:      map[string]string{"chat": "openai"},
			objects:     []client.Object{unreadyCredential("openai")},
			wantReason:  agentsv1alpha1.ReasonModelCredentialNotReady,
			wantMessage: "openai",
		},
		{
			name:       "primary and fallbacks all ready",
			models:     map[string]string{"chat": "openai", "background": "cheap"},
			fallbacks:  []string{"backup"},
			objects:    []client.Object{readyCredential("openai"), readyCredential("cheap"), readyCredential("backup")},
			wantReason: "",
		},
		{
			name:        "one fallback missing",
			models:      map[string]string{"chat": "openai"},
			fallbacks:   []string{"backup"},
			objects:     []client.Object{readyCredential("openai")},
			wantReason:  agentsv1alpha1.ReasonUnknownModelCredential,
			wantMessage: "backup",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := validAgent("scout")
			a.Spec.Models = tc.models
			a.Spec.ModelFallbacks = tc.fallbacks
			got := reconcileAgents(t, a, tc.objects...)
			cond := modelCredentialsCondition(t, got)
			if tc.wantReason == "" {
				if cond.Status != metav1.ConditionTrue {
					t.Fatalf("ModelCredentialsReady = %s/%s: %s", cond.Status, cond.Reason, cond.Message)
				}
				return
			}
			if cond.Status != metav1.ConditionFalse || cond.Reason != tc.wantReason {
				t.Fatalf("ModelCredentialsReady = %s/%s, want False/%s", cond.Status, cond.Reason, tc.wantReason)
			}
			if tc.wantMessage != "" && !strings.Contains(cond.Message, tc.wantMessage) {
				t.Fatalf("message %q does not name %q", cond.Message, tc.wantMessage)
			}
			// The spec itself is fine in every one of these cases, so Validated
			// must not be dragged down with it.
			if v := meta.FindStatusCondition(got.Status.Conditions, agentsv1alpha1.ConditionValidated); v == nil || v.Status != metav1.ConditionTrue {
				t.Fatalf("Validated = %+v, want True — a credential problem is not a spec problem", v)
			}
		})
	}
}

// referencedCredentials is what the message is built from, so its order has to
// be stable: a map iterated raw would reorder the message between reconciles
// and write status on every pass.
func TestReferencedCredentialsIsStableAndDeduped(t *testing.T) {
	a := validAgent("scout")
	a.Spec.Models = map[string]string{"chat": "main", "background": "cheap", "compaction": "main"}
	a.Spec.ModelFallbacks = []string{"cheap", " backup ", ""}
	want := []string{"cheap", "main", "backup"} // background, chat, compaction, then fallbacks
	for range 5 {
		if got := referencedCredentials(a); !slices.Equal(got, want) {
			t.Fatalf("referencedCredentials = %v, want %v", got, want)
		}
	}
}
