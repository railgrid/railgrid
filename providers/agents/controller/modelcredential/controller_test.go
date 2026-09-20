// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package modelcredential

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/railgrid/provider-sdk/claimscope"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
)

const (
	testCluster = "tenant-a"
	testCred    = "openai"
	testSecret  = "railgrid-agents-model-openai"
)

var now = time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)

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

func credential() *agentsv1alpha1.ModelCredential {
	return &agentsv1alpha1.ModelCredential{
		ObjectMeta: metav1.ObjectMeta{Name: testCred, Generation: 2},
		Spec: agentsv1alpha1.ModelCredentialSpec{
			Provider:  llm.ProviderOpenAICompatible,
			BaseURL:   "https://api.openai.com/v1",
			Model:     "gpt-4o",
			SecretRef: agentsv1alpha1.ModelCredentialSecretRef{Name: testSecret},
		},
	}
}

// secret builds the credential Secret. labelled=false is the mistake the
// SecretResolved condition exists to name.
func secret(labelled bool, data map[string][]byte) *corev1.Secret {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testSecret, Namespace: llm.SecretNamespace},
		Data:       data,
	}
	if labelled {
		sec.Labels = claimscope.OwnerLabels("agents")
	}
	return sec
}

func newReconciler(t *testing.T, probe Prober, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.ModelCredential{}).
		WithObjects(objs...).
		Build()
	return &Reconciler{
		Manager: fakeManager{c: c},
		Probe:   probe,
		Now:     func() time.Time { return now },
	}, c
}

func reconcileOnce(t *testing.T, r *Reconciler) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(t.Context(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: testCred}},
		ClusterName: testCluster,
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return res
}

func read(t *testing.T, c client.Client) *agentsv1alpha1.ModelCredential {
	t.Helper()
	var got agentsv1alpha1.ModelCredential
	if err := c.Get(t.Context(), types.NamespacedName{Name: testCred}, &got); err != nil {
		t.Fatalf("reading credential: %v", err)
	}
	return &got
}

func condition(t *testing.T, cred *agentsv1alpha1.ModelCredential, typ string) metav1.Condition {
	t.Helper()
	cond := meta.FindStatusCondition(cred.Status.Conditions, typ)
	if cond == nil {
		t.Fatalf("condition %s not set: %+v", typ, cred.Status.Conditions)
	}
	return *cond
}

// The happy path: the Secret resolves, the endpoint answers, and what it
// answered lands on the object — which is what the portal's model picker reads
// before any agent exists.
func TestReconcileReadyRecordsModels(t *testing.T) {
	var sawKey string
	probe := func(_ context.Context, baseURL, apiKey string) ([]string, time.Duration, error) {
		sawKey = apiKey
		if baseURL != "https://api.openai.com/v1" {
			t.Errorf("probed %q, want the credential's baseURL", baseURL)
		}
		return []string{"gpt-4o", "gpt-5"}, 12 * time.Millisecond, nil
	}
	r, c := newReconciler(t, probe, credential(), secret(true, map[string][]byte{"apiKey": []byte("k")}))
	res := reconcileOnce(t, r)

	if sawKey != "k" {
		t.Fatalf("probe got key %q, want the one in the Secret", sawKey)
	}
	got := read(t, c)
	for _, typ := range []string{
		agentsv1alpha1.ConditionSecretResolved,
		agentsv1alpha1.ConditionReachable,
		agentsv1alpha1.ConditionReady,
	} {
		if cond := condition(t, got, typ); cond.Status != metav1.ConditionTrue {
			t.Fatalf("%s = %s (%s): %s", typ, cond.Status, cond.Reason, cond.Message)
		}
	}
	if strings.Join(got.Status.Models, ",") != "gpt-4o,gpt-5" {
		t.Fatalf("status.models = %v", got.Status.Models)
	}
	if got.Status.ObservedGeneration != 2 {
		t.Fatalf("observedGeneration = %d, want 2", got.Status.ObservedGeneration)
	}
	if got.Status.LastProbeError != "" {
		t.Fatalf("lastProbeError = %q on a successful probe", got.Status.LastProbeError)
	}
	// A working credential is re-asked on the slow clock: an API key revoked
	// at the provider changes nothing in kcp, so this is the one thing here
	// that legitimately polls.
	if res.RequeueAfter != readyResync {
		t.Fatalf("requeue = %v, want %v", res.RequeueAfter, readyResync)
	}
}

// A Secret with the key but without the owner label is the failure that used
// to be invisible: the portal's own reads work (they run as the user), and
// every unattended run goes blind, because the provider's claim is scoped to
// that label.
func TestReconcileUnlabelledSecretIsNotResolved(t *testing.T) {
	probed := false
	probe := func(context.Context, string, string) ([]string, time.Duration, error) {
		probed = true
		return nil, 0, nil
	}
	r, c := newReconciler(t, probe, credential(), secret(false, map[string][]byte{"apiKey": []byte("k")}))
	reconcileOnce(t, r)

	got := read(t, c)
	cond := condition(t, got, agentsv1alpha1.ConditionSecretResolved)
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonOwnerLabelMissing {
		t.Fatalf("SecretResolved = %s/%s, want False/%s", cond.Status, cond.Reason, agentsv1alpha1.ReasonOwnerLabelMissing)
	}
	if !strings.Contains(cond.Message, claimscope.OwnerLabel) {
		t.Fatalf("message does not name the label to add: %q", cond.Message)
	}
	if probed {
		t.Fatal("the endpoint must not be called without a resolved key")
	}
	if condition(t, got, agentsv1alpha1.ConditionReady).Status != metav1.ConditionFalse {
		t.Fatal("Ready must be False when the Secret did not resolve")
	}
}

func TestReconcileSecretMissingAndIncomplete(t *testing.T) {
	t.Run("no secret", func(t *testing.T) {
		r, c := newReconciler(t, nil, credential())
		reconcileOnce(t, r)
		cond := condition(t, read(t, c), agentsv1alpha1.ConditionSecretResolved)
		if cond.Reason != agentsv1alpha1.ReasonSecretUnreadable {
			t.Fatalf("reason = %s, want %s", cond.Reason, agentsv1alpha1.ReasonSecretUnreadable)
		}
	})
	t.Run("no key", func(t *testing.T) {
		r, c := newReconciler(t, nil, credential(), secret(true, map[string][]byte{"other": []byte("x")}))
		reconcileOnce(t, r)
		cond := condition(t, read(t, c), agentsv1alpha1.ConditionSecretResolved)
		if cond.Reason != agentsv1alpha1.ReasonSecretIncomplete {
			t.Fatalf("reason = %s, want %s", cond.Reason, agentsv1alpha1.ReasonSecretIncomplete)
		}
	})
}

// A refused key is Reachable=False with a bounded error, and the error never
// carries the key — the upstream body can echo the header it travelled in.
func TestReconcileProbeFailureIsBoundedAndKeyFree(t *testing.T) {
	probe := func(context.Context, string, string) ([]string, time.Duration, error) {
		return nil, 40 * time.Millisecond, &llm.ProbeError{Status: 401, Msg: strings.Repeat("secret-key-value ", 200)}
	}
	r, c := newReconciler(t, probe, credential(), secret(true, map[string][]byte{"apiKey": []byte("secret-key-value")}))
	res := reconcileOnce(t, r)

	got := read(t, c)
	cond := condition(t, got, agentsv1alpha1.ConditionReachable)
	if cond.Status != metav1.ConditionFalse || cond.Reason != agentsv1alpha1.ReasonProbeFailed {
		t.Fatalf("Reachable = %s/%s", cond.Status, cond.Reason)
	}
	if len(got.Status.LastProbeError) > maxProbeError {
		t.Fatalf("lastProbeError is %d chars, over the %d bound", len(got.Status.LastProbeError), maxProbeError)
	}
	// SecretResolved stays True: the key is there, the provider refused it.
	// Conflating the two would send the user to the wrong place.
	if condition(t, got, agentsv1alpha1.ConditionSecretResolved).Status != metav1.ConditionTrue {
		t.Fatal("SecretResolved must stay True when only the endpoint refused")
	}
	if res.RequeueAfter != backoffInitial {
		t.Fatalf("first failure requeue = %v, want %v", res.RequeueAfter, backoffInitial)
	}
}

// The failure ladder is read off the Ready condition's own transition time, so
// it survives a restart and is the same on every replica.
func TestBackoffLadder(t *testing.T) {
	status := &agentsv1alpha1.ModelCredentialStatus{}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type: agentsv1alpha1.ConditionReady, Status: metav1.ConditionFalse,
		Reason: agentsv1alpha1.ReasonNotReady, LastTransitionTime: metav1.Time{Time: now},
	})
	for _, tc := range []struct {
		elapsed time.Duration
		want    time.Duration
	}{
		{0, backoffInitial},
		{time.Minute, backoffInitial},
		{5 * time.Minute, backoffSecond},
		{30 * time.Minute, backoffThird},
		{3 * time.Hour, backoffMax},
	} {
		if got := backoffFor(status, now.Add(tc.elapsed)); got != tc.want {
			t.Errorf("after %v: backoff = %v, want %v", tc.elapsed, got, tc.want)
		}
	}
	// No condition yet — the first failure, before anything was written.
	if got := backoffFor(&agentsv1alpha1.ModelCredentialStatus{}, now); got != backoffInitial {
		t.Errorf("unwritten status: backoff = %v, want %v", got, backoffInitial)
	}
}

// status.models is bounded, because an aggregator serves thousands and the
// kind's MaxItems would reject the write.
func TestReconcileBoundsDiscoveredModels(t *testing.T) {
	many := make([]string, llm.MaxStatusModels+50)
	for i := range many {
		many[i] = "m"
	}
	probe := func(context.Context, string, string) ([]string, time.Duration, error) {
		return many, time.Millisecond, nil
	}
	r, c := newReconciler(t, probe, credential(), secret(true, map[string][]byte{"apiKey": []byte("k")}))
	reconcileOnce(t, r)
	if got := len(read(t, c).Status.Models); got != llm.MaxStatusModels {
		t.Fatalf("status.models has %d entries, want %d", got, llm.MaxStatusModels)
	}
}

// A second reconcile that found nothing new writes nothing, so a slow resync
// does not churn resourceVersions across every tenant.
func TestReconcileIsIdempotent(t *testing.T) {
	probe := func(context.Context, string, string) ([]string, time.Duration, error) {
		return []string{"gpt-4o"}, time.Millisecond, nil
	}
	r, c := newReconciler(t, probe, credential(), secret(true, map[string][]byte{"apiKey": []byte("k")}))
	reconcileOnce(t, r)
	first := read(t, c).ResourceVersion
	reconcileOnce(t, r)
	if second := read(t, c).ResourceVersion; second != first {
		t.Fatalf("an unchanged reconcile wrote: %s → %s", first, second)
	}
}

// A failed Secret read says nothing about the Secret, so it must not be
// collapsed into "missing": the conditions are left alone and the error is
// returned so the retry settles it. The alternative flags a working credential
// because the apiserver blinked.
func TestReconcileSecretReadFailureIsNotAVerdict(t *testing.T) {
	base := fake.NewClientBuilder().
		WithScheme(agentsscheme.NewScheme()).
		WithStatusSubresource(&agentsv1alpha1.ModelCredential{}).
		WithObjects(credential(), secret(true, map[string][]byte{"apiKey": []byte("k")})).
		Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isSecret := obj.(*corev1.Secret); isSecret {
				return errors.New("apiserver unavailable")
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
	r := &Reconciler{Manager: fakeManager{c: c}, Now: func() time.Time { return now }}

	_, err := r.Reconcile(t.Context(), mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: testCred}},
		ClusterName: testCluster,
	})
	if err == nil {
		t.Fatal("a failed read must be returned as an error, not turned into a verdict")
	}
	var got agentsv1alpha1.ModelCredential
	if err := base.Get(t.Context(), types.NamespacedName{Name: testCred}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Conditions) != 0 {
		t.Fatalf("conditions were written from a failed read: %+v", got.Status.Conditions)
	}
}

// The upstream body never reaches the object. Providers quote the offending
// request back, and this status is durable and listable — a gateway that
// echoed the Authorization header would otherwise put a live key in it.
func TestReconcileNeverStoresTheUpstreamBody(t *testing.T) {
	probe := func(context.Context, string, string) ([]string, time.Duration, error) {
		return nil, time.Millisecond, &llm.ProbeError{
			Status: 401,
			Msg:    `{"error":{"message":"Incorrect API key provided: sk-live-do-not-store"}}`,
		}
	}
	r, c := newReconciler(t, probe, credential(), secret(true, map[string][]byte{"apiKey": []byte("sk-live-do-not-store")}))
	reconcileOnce(t, r)

	got := read(t, c)
	written := got.Status.LastProbeError + " " + condition(t, got, agentsv1alpha1.ConditionReachable).Message +
		" " + condition(t, got, agentsv1alpha1.ConditionReady).Message
	if strings.Contains(written, "sk-live-do-not-store") || strings.Contains(written, "Incorrect API key") {
		t.Fatalf("the upstream body reached the object: %q", written)
	}
	if !strings.Contains(got.Status.LastProbeError, "401") {
		t.Fatalf("lastProbeError = %q, want the HTTP status", got.Status.LastProbeError)
	}
}
