// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package modelcredential reconciles ModelCredential CRs: it answers, on the
// object, whether a credential actually works.
//
// A credential is a pair — the object and the Secret its spec.secretRef names
// — and the two are written separately by whoever created them, so every
// half-finished shape is reachable: an object whose Secret was never written,
// a Secret without the key, a Secret without the owner label (which the
// provider's label-scoped `secrets` claim hides, so unattended runs go blind
// while the portal's own reads keep working). None of that is visible to the
// CRD schema, and all of it used to surface much later as a run that could not
// build a model.
//
// So it is said here, as three conditions:
//
//   - SecretResolved — the Secret exists, carries spec.secretKey, and is
//     labelled railgrid.ai/owner: agents.
//   - Reachable — GET {spec.baseURL}/models answered with that key.
//   - Ready — both.
//
// status.models records what the endpoint served, which is what the portal's
// model picker offers and what makes the first-run flow work before any agent
// exists. The key is never logged and never written to status; a failed probe
// is recorded as bounded recovery text.
//
// Cadence: a Ready credential is re-probed on a slow clock (an API key can be
// revoked without anything in kcp changing, and nothing here can watch the
// provider's account), a failing one backs off from the moment it started
// failing, and a spec edit re-probes at once because the generation changed.
// Secrets are watched too, so a rotated key re-validates without waiting.
package modelcredential

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/railgrid/provider-sdk/claimscope"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
)

const (
	// readyResync is how long a working credential goes before it is asked
	// again. It is a poll of a system that cannot be watched — an API key
	// revoked at the provider changes nothing in kcp — so it is deliberately
	// slow: this is the sanctioned RequeueAfter, not a stand-in for a watch.
	readyResync = 30 * time.Minute

	// The failure ladder, read off how long Reachable has been False. It needs
	// no durable state of its own beyond the condition's own transition time,
	// so every replica and every retry computes the same answer.
	backoffInitial = 30 * time.Second
	backoffSecond  = 2 * time.Minute
	backoffThird   = 10 * time.Minute
	backoffMax     = 30 * time.Minute
)

// Prober performs one GET {baseURL}/models. Injected so tests never reach the
// network; nil means llm.DiscoverModels.
type Prober func(ctx context.Context, baseURL, apiKey string) ([]string, time.Duration, error)

// Reconciler keeps every ModelCredential's status true.
type Reconciler struct {
	Manager mcmanager.Manager
	// Probe calls the model endpoint. nil means llm.DiscoverModels.
	Probe Prober
	// Now is the clock; nil means time.Now. Tests pin it.
	Now func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) probe(ctx context.Context, baseURL, apiKey string) ([]string, time.Duration, error) {
	if r.Probe != nil {
		return r.Probe(ctx, baseURL, apiKey)
	}
	return llm.DiscoverModels(ctx, baseURL, apiKey)
}

// SetupWithManager wires the reconciler into the multicluster manager.
//
// Secrets are watched as well as the credentials: the key lives in the Secret,
// so a rotation, a first write, or a label finally being added is an event on
// the Secret and nothing on the object. Without this a credential fixed by
// writing its Secret would stay broken until the next resync.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-modelcredential").
		For(&agentsv1alpha1.ModelCredential{}).
		Watches(&corev1.Secret{}, credentialsForSecret).
		Complete(r)
}

// credentialsForSecret enqueues every ModelCredential in the event's cluster
// whose spec.secretRef names the changed Secret.
//
// A list per Secret event rather than an index: Secret events in a tenant
// workspace are rare (this provider's claim shows it only its own labelled
// ones) and the credential list is small.
func credentialsForSecret(clusterName multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		if obj.GetNamespace() != llm.SecretNamespace {
			return nil
		}
		var list agentsv1alpha1.ModelCredentialList
		if err := cl.GetClient().List(ctx, &list); err != nil {
			klog.FromContext(ctx).Error(err, "listing model credentials for a secret event")
			return nil
		}
		var reqs []reconcile.Request
		for i := range list.Items {
			if strings.TrimSpace(list.Items[i].Spec.SecretRef.Name) == obj.GetName() {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
			}
		}
		return reqs
	}))(clusterName, cl)
}

// Reconcile handles one ModelCredential.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("modelcredential", req.Name, "cluster", req.ClusterName)
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var cred agentsv1alpha1.ModelCredential
	if err := c.Get(ctx, req.NamespacedName, &cred); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !cred.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	apiKey, secretReason, secretMessage, err := r.resolveSecret(ctx, c, &cred)
	if err != nil {
		// A read we could not complete says nothing about the Secret. Leave
		// the conditions as they stand and let the retry settle it.
		return ctrl.Result{}, err
	}

	now := r.now()
	status := cred.Status.DeepCopy()
	status.ObservedGeneration = cred.Generation
	setCondition(status, agentsv1alpha1.ConditionSecretResolved, secretReason,
		secretMessage, "the credential's Secret holds an API key this provider can read", cred.Generation, now)

	switch {
	case secretReason != "":
		// No key, no probe. Reachable is not "false because the endpoint
		// refused" — it is unknown — but a condition has to say something, and
		// False with SecretUnresolved is the honest version of "we never
		// asked".
		setCondition(status, agentsv1alpha1.ConditionReachable, agentsv1alpha1.ReasonSecretUnresolved,
			"the endpoint was not called: "+secretMessage, "", cred.Generation, now)
	default:
		models, latency, probeErr := r.probe(ctx, cred.Spec.BaseURL, apiKey)
		status.LastProbeTime = &metav1.Time{Time: now}
		if probeErr != nil {
			// ProbeSummary, not the error: an upstream body is quoted back to
			// whoever asked, never written to the object. Providers echo the
			// offending request, and this status is durable and listable.
			status.LastProbeError = bounded(llm.ProbeSummary(probeErr))
			logger.V(2).Info("model endpoint did not answer", "baseURL", cred.Spec.BaseURL, "latencyMS", latency.Milliseconds())
			setCondition(status, agentsv1alpha1.ConditionReachable, agentsv1alpha1.ReasonProbeFailed,
				"GET "+strings.TrimRight(cred.Spec.BaseURL, "/")+"/models failed: "+status.LastProbeError, "", cred.Generation, now)
		} else {
			status.LastProbeError = ""
			status.Models = boundModels(models)
			setCondition(status, agentsv1alpha1.ConditionReachable, "", "",
				fmt.Sprintf("the endpoint answered with %d model(s) in %dms", len(status.Models), latency.Milliseconds()),
				cred.Generation, now)
		}
	}

	ready := meta.IsStatusConditionTrue(status.Conditions, agentsv1alpha1.ConditionSecretResolved) &&
		meta.IsStatusConditionTrue(status.Conditions, agentsv1alpha1.ConditionReachable)
	readyReason, readyMessage := "", ""
	if !ready {
		readyReason = agentsv1alpha1.ReasonNotReady
		readyMessage = notReadyMessage(status)
	}
	setCondition(status, agentsv1alpha1.ConditionReady, readyReason, readyMessage,
		"this credential resolved and its endpoint answered", cred.Generation, now)

	result := ctrl.Result{RequeueAfter: readyResync}
	if !ready {
		result.RequeueAfter = backoffFor(status, now)
	}
	if equalStatus(&cred.Status, status) {
		return result, nil
	}
	cred.Status = *status
	if err := c.Status().Update(ctx, &cred); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{}, nil // the watch re-delivers the newer object
		}
		return result, err
	}
	logger.V(2).Info("model credential status written", "ready", ready, "models", len(status.Models))
	return result, nil
}

// resolveSecret reads the credential's API key. It returns the key on success,
// or a (reason, message) pair naming what is wrong. An error is a failed read
// that says nothing about the Secret.
func (r *Reconciler) resolveSecret(ctx context.Context, c client.Client, cred *agentsv1alpha1.ModelCredential) (apiKey, reason, message string, err error) {
	name := strings.TrimSpace(cred.Spec.SecretRef.Name)
	if name == "" {
		return "", agentsv1alpha1.ReasonInvalidSpec, "spec.secretRef.name is empty; it must name the Secret holding the API key", nil
	}
	var sec corev1.Secret
	switch err := c.Get(ctx, types.NamespacedName{Namespace: llm.SecretNamespace, Name: name}, &sec); {
	case apierrors.IsNotFound(err):
		// Through the APIExport virtual workspace a Secret without the owner
		// label is indistinguishable from one that does not exist, so both
		// answers are offered — the second is the one people actually hit.
		return "", agentsv1alpha1.ReasonSecretUnreadable, fmt.Sprintf(
			"secret %q in namespace %q is not readable: it does not exist, or it is missing the label %s=%s that this provider's permission claim is scoped to",
			name, llm.SecretNamespace, claimscope.OwnerLabel, agentsclient.ProviderName), nil
	case err != nil:
		return "", "", "", err
	}
	if sec.Labels[claimscope.OwnerLabel] != agentsclient.ProviderName {
		return "", agentsv1alpha1.ReasonOwnerLabelMissing, fmt.Sprintf(
			"secret %q needs the label %s=%s; without it this provider cannot read the key on an unattended run",
			name, claimscope.OwnerLabel, agentsclient.ProviderName), nil
	}
	key := llm.APIKeyFromSecret(&sec, cred.Spec)
	if key == "" {
		return "", agentsv1alpha1.ReasonSecretIncomplete, fmt.Sprintf(
			"secret %q has no %q key; spec.secretKey names the key holding the API key", name, llm.SecretKey(cred.Spec)), nil
	}
	return key, "", "", nil
}

// notReadyMessage names the first unmet condition, so Ready says what to fix
// rather than merely that something is wrong.
func notReadyMessage(status *agentsv1alpha1.ModelCredentialStatus) string {
	for _, t := range []string{agentsv1alpha1.ConditionSecretResolved, agentsv1alpha1.ConditionReachable} {
		if cond := meta.FindStatusCondition(status.Conditions, t); cond != nil && cond.Status != metav1.ConditionTrue {
			return cond.Message
		}
	}
	return "this credential has not been validated yet"
}

// backoffFor spaces retries of a credential that is not Ready, from how long
// Ready has been False. Reading the ladder off the condition's own transition
// time keeps it durable and replica-independent: no queue state, no counter,
// and a restart resumes where it left off.
func backoffFor(status *agentsv1alpha1.ModelCredentialStatus, now time.Time) time.Duration {
	cond := meta.FindStatusCondition(status.Conditions, agentsv1alpha1.ConditionReady)
	if cond == nil || cond.LastTransitionTime.IsZero() {
		return backoffInitial
	}
	switch elapsed := now.Sub(cond.LastTransitionTime.Time); {
	case elapsed < 2*time.Minute:
		return backoffInitial
	case elapsed < 10*time.Minute:
		return backoffSecond
	case elapsed < time.Hour:
		return backoffThird
	default:
		return backoffMax
	}
}

// setCondition records one condition. An empty reason means True, with
// trueMessage as the message; anything else is False with message.
func setCondition(status *agentsv1alpha1.ModelCredentialStatus, condType, reason, message, trueMessage string, generation int64, now time.Time) {
	cond := metav1.Condition{
		Type:               condType,
		Status:             metav1.ConditionTrue,
		Reason:             trueReasonFor(condType),
		Message:            trueMessage,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Time{Time: now},
	}
	if reason != "" {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, reason, message
	}
	meta.SetStatusCondition(&status.Conditions, cond)
}

func trueReasonFor(condType string) string {
	switch condType {
	case agentsv1alpha1.ConditionSecretResolved:
		return agentsv1alpha1.ReasonSecretResolved
	case agentsv1alpha1.ConditionReachable:
		return agentsv1alpha1.ReasonReachable
	default:
		return agentsv1alpha1.ReasonReady
	}
}

// boundModels truncates the discovered ids to what the kind allows. An
// aggregator serves thousands; an API object is not the place to mirror them.
func boundModels(models []string) []string {
	if len(models) > llm.MaxStatusModels {
		return models[:llm.MaxStatusModels]
	}
	return models
}

// maxProbeError bounds status.lastProbeError, matching the kind's MaxLength.
const maxProbeError = 512

func bounded(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > maxProbeError {
		return msg[:maxProbeError]
	}
	return msg
}

// equalStatus reports whether a status write would change anything. Condition
// timestamps are excluded: meta.SetStatusCondition only moves
// LastTransitionTime when the status itself flips, so comparing the rest is
// enough and comparing the probe time alone would make every reconcile a
// write.
func equalStatus(a, b *agentsv1alpha1.ModelCredentialStatus) bool {
	if a.ObservedGeneration != b.ObservedGeneration || a.LastProbeError != b.LastProbeError {
		return false
	}
	if len(a.Models) != len(b.Models) {
		return false
	}
	for i := range a.Models {
		if a.Models[i] != b.Models[i] {
			return false
		}
	}
	if len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		x, y := a.Conditions[i], b.Conditions[i]
		if x.Type != y.Type || x.Status != y.Status || x.Reason != y.Reason ||
			x.Message != y.Message || x.ObservedGeneration != y.ObservedGeneration {
			return false
		}
	}
	// Nothing else changed, so a probe time that only moved forward is not
	// worth a write of its own — the next real change carries it.
	return true
}

// String renders the reconciler for logs.
func (r *Reconciler) String() string {
	return fmt.Sprintf("modelcredential.Reconciler{probe=%t}", r.Probe != nil)
}
