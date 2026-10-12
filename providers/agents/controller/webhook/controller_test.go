// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package webhook

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
	testWebhook = "deploys"
)

var testKey = webhookpath.Key("test-key", "")

func webhookObj(source string) *agentsv1alpha1.Webhook {
	w := &agentsv1alpha1.Webhook{ObjectMeta: metav1.ObjectMeta{Name: testWebhook}}
	w.Spec.Source = source
	return w
}

func triggerObj(name, webhookRef string) *agentsv1alpha1.Trigger {
	tr := &agentsv1alpha1.Trigger{ObjectMeta: metav1.ObjectMeta{Name: name}}
	tr.Spec.AgentRef = "releaser"
	tr.Spec.WebhookRef = webhookRef
	return tr
}

type harness struct {
	c client.Client
	r *Reconciler
}

func newHarness(t *testing.T, key []byte, objs ...client.Object) *harness {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.Webhook{}, &agentsv1alpha1.Trigger{}).
		WithObjects(objs...).
		Build()
	return &harness{c: c, r: &Reconciler{Manager: fakeManager{c: c}, WebhookKey: key}}
}

func (h *harness) reconcile(t *testing.T) agentsv1alpha1.Webhook {
	t.Helper()
	req := mcreconcile.Request{ClusterName: testCluster, Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: testWebhook}}}
	if _, err := h.r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var got agentsv1alpha1.Webhook
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: testWebhook}, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func validated(t *testing.T, w agentsv1alpha1.Webhook) metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(w.Status.Conditions, agentsv1alpha1.ConditionValidated)
	if c == nil {
		t.Fatalf("no %s condition: %+v", agentsv1alpha1.ConditionValidated, w.Status.Conditions)
	}
	return *c
}

// ---- status.webhookPath: the load-bearing part -------------------------------

// A Webhook created through the kube client gets its inbound URL from nowhere
// else.
func TestWebhookPathIsMinted(t *testing.T) {
	h := newHarness(t, testKey, webhookObj(agentsv1alpha1.WebhookSourceGitHub))
	got := h.reconcile(t)
	want := webhookpath.For(testKey, testCluster, testWebhook)
	if want == "" {
		t.Fatal("test setup: no key")
	}
	if got.Status.WebhookPath != want {
		t.Fatalf("webhookPath = %q, want %q", got.Status.WebhookPath, want)
	}
}

// The path is the credential and it is pasted into somebody else's system the
// day it is minted — for a webhook created under an old trigger's name, it was
// pasted before the Webhook kind existed. The SHAPE is spelled out literally,
// prefix included, so a change to the derivation fails here instead of quietly
// invalidating every URL already in the wild.
func TestWebhookPathKeepsTheTriggerURLShape(t *testing.T) {
	h := newHarness(t, testKey, webhookObj(agentsv1alpha1.WebhookSourceWebhook))
	got := h.reconcile(t)
	const prefix = "/services/providers/agents/webhooks/triggers/" + testCluster + "/" + testWebhook + "/"
	if !strings.HasPrefix(got.Status.WebhookPath, prefix) {
		t.Fatalf("webhookPath = %q, want prefix %q", got.Status.WebhookPath, prefix)
	}
	if token := strings.TrimPrefix(got.Status.WebhookPath, prefix); len(token) != 32 {
		t.Fatalf("token = %q (%d chars), want a 32-char hmac", token, len(token))
	}
}

// A settled webhook is not written again.
func TestSettledWebhookIsNotRewritten(t *testing.T) {
	h := newHarness(t, testKey, webhookObj(agentsv1alpha1.WebhookSourceWebhook))
	first := h.reconcile(t)
	second := h.reconcile(t)
	if second.ResourceVersion != first.ResourceVersion {
		t.Fatalf("a settled webhook was written again (rv %s -> %s)", first.ResourceVersion, second.ResourceVersion)
	}
}

// No key configured means no URL can be minted. Clearing the stored one would
// revoke a URL that still works for whoever holds it.
func TestWebhookPathIsLeftAloneWithoutAKey(t *testing.T) {
	w := webhookObj(agentsv1alpha1.WebhookSourceWebhook)
	w.Status.WebhookPath = "/services/providers/agents/webhooks/triggers/tenant-a/deploys/existing"
	h := newHarness(t, nil, w)
	if got := h.reconcile(t); got.Status.WebhookPath != w.Status.WebhookPath {
		t.Fatalf("webhookPath = %q, want it left alone", got.Status.WebhookPath)
	}
}

// ---- status.routes ----------------------------------------------------------

// The route list is every trigger naming the webhook, sorted, and a trigger
// moved elsewhere maps back to both webhooks.
func TestRoutesListTheTriggersNamingTheWebhook(t *testing.T) {
	h := newHarness(t, testKey, webhookObj(agentsv1alpha1.WebhookSourceGitHub),
		triggerObj("zeta", testWebhook), triggerObj("alpha", " "+testWebhook+" "), triggerObj("other", "elsewhere"))
	got := h.reconcile(t)
	if len(got.Status.Routes) != 2 || got.Status.Routes[0].Name != "alpha" || got.Status.Routes[1].Name != "zeta" {
		t.Fatalf("routes = %+v, want alpha, zeta", got.Status.Routes)
	}

	// The trigger watch maps each trigger to the webhook it names.
	if reqs := WebhookRequestForTrigger(triggerObj("zeta", testWebhook)); len(reqs) != 1 || reqs[0].Name != testWebhook {
		t.Fatalf("map(zeta) = %+v", reqs)
	}
	if reqs := WebhookRequestForTrigger(triggerObj("none", "")); len(reqs) != 0 {
		t.Fatalf("a trigger naming no webhook maps to nothing, got %+v", reqs)
	}

	var zeta agentsv1alpha1.Trigger
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: "zeta"}, &zeta); err != nil {
		t.Fatal(err)
	}
	zeta.Spec.WebhookRef = "elsewhere"
	if err := h.c.Update(context.Background(), &zeta); err != nil {
		t.Fatal(err)
	}
	if got := h.reconcile(t); len(got.Status.Routes) != 1 || got.Status.Routes[0].Name != "alpha" {
		t.Fatalf("routes after zeta moved = %+v, want alpha only", got.Status.Routes)
	}
}

// ---- the Validated condition ------------------------------------------------

func TestValidatedReportsTheSource(t *testing.T) {
	h := newHarness(t, testKey, webhookObj(agentsv1alpha1.WebhookSourceGitHub))
	if cond := validated(t, h.reconcile(t)); cond.Status != metav1.ConditionTrue || cond.Reason != agentsv1alpha1.ReasonValidated {
		t.Fatalf("condition = %s/%s, want True/Validated", cond.Status, cond.Reason)
	}
	h = newHarness(t, testKey, webhookObj("email"))
	cond := validated(t, h.reconcile(t))
	if cond.Reason != agentsv1alpha1.ReasonInvalidSource || !strings.Contains(cond.Message, "email") {
		t.Fatalf("condition = %s (%q), want InvalidSource naming the value", cond.Reason, cond.Message)
	}
	h = newHarness(t, testKey, webhookObj(""))
	if cond := validated(t, h.reconcile(t)); cond.Reason != agentsv1alpha1.ReasonInvalidSpec {
		t.Fatalf("empty source: reason = %s, want InvalidSpec", cond.Reason)
	}
}

// ---- the sink's lifetime ----------------------------------------------------

// With a purge wired, the webhook gets a finalizer, and deleting it purges
// the sink by UID before the object goes.
func TestDeletionPurgesTheSink(t *testing.T) {
	w := webhookObj(agentsv1alpha1.WebhookSourceWebhook)
	w.UID = "uid-1"
	h := newHarness(t, testKey, w)
	var purged []string
	h.r.PurgeDeliveries = func(_ context.Context, clusterID, uid string) error {
		purged = append(purged, clusterID+"/"+uid)
		return nil
	}
	got := h.reconcile(t)
	if !controllerutil.ContainsFinalizer(&got, agentsv1alpha1.WebhookDeliveriesFinalizer) {
		t.Fatalf("finalizers = %v, want %s", got.Finalizers, agentsv1alpha1.WebhookDeliveriesFinalizer)
	}
	if err := h.c.Delete(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	req := mcreconcile.Request{ClusterName: testCluster, Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: testWebhook}}}
	if _, err := h.r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(purged) != 1 || purged[0] != testCluster+"/uid-1" {
		t.Fatalf("purged = %v, want the webhook's UID in its cluster", purged)
	}
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: testWebhook}, &got); err == nil {
		t.Fatalf("the webhook must be gone once the finalizer is released: %+v", got.Finalizers)
	}
}

// A failing purge holds the deletion and retries — until the deadline, past
// which the object goes and the rows are left for an operator.
func TestFailedPurgeRetriesThenGivesUp(t *testing.T) {
	w := webhookObj(agentsv1alpha1.WebhookSourceWebhook)
	w.Finalizers = []string{agentsv1alpha1.WebhookDeliveriesFinalizer}
	h := newHarness(t, testKey, w)
	h.r.PurgeDeliveries = func(context.Context, string, string) error { return errors.New("store down") }
	if err := h.c.Delete(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	req := mcreconcile.Request{ClusterName: testCluster, Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: testWebhook}}}
	res, err := h.r.Reconcile(context.Background(), req)
	if err != nil || res.RequeueAfter != purgeRetryInterval {
		t.Fatalf("result = %+v, %v; want a retry", res, err)
	}
	h.r.now = func() time.Time { return time.Now().Add(purgeGiveUp + time.Minute) }
	if _, err := h.r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var got agentsv1alpha1.Webhook
	if err := h.c.Get(context.Background(), client.ObjectKey{Name: testWebhook}, &got); err == nil {
		t.Fatal("past the deadline the finalizer must be released")
	}
}

// Without a purge nothing could clear the finalizer, so none is added.
func TestNoPurgeNoFinalizer(t *testing.T) {
	h := newHarness(t, testKey, webhookObj(agentsv1alpha1.WebhookSourceWebhook))
	if got := h.reconcile(t); len(got.Finalizers) != 0 {
		t.Fatalf("finalizers = %v, want none without a purge", got.Finalizers)
	}
}
