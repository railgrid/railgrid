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
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/railgrid/provider-sdk/claimscope"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
)

const (
	credentialProbeLabel                   = "agents.railgrid.ai/credential-probe"
	credentialProbeIDAnnotation            = "agents.railgrid.ai/credential-probe-id"
	credentialProbeExpiresAnnotation       = "agents.railgrid.ai/credential-probe-expires-at"
	credentialProbeCleanupIDAnnotation     = "agents.railgrid.ai/credential-probe-cleanup-id"
	credentialProbeCleanupSecretAnnotation = "agents.railgrid.ai/credential-probe-cleanup-secret"
	credentialProbeCleanupFinalizer        = "agents.railgrid.ai/credential-probe-cleanup"
	credentialProbeMaxAge                  = 10 * time.Minute
	credentialProbeCleanupBlocked          = "ProbeCleanupOwnershipMismatch"
)

// credentialProbeID recognizes only the complete, explicit temporary-probe
// marker. A saved ModelCredential is never eligible based on its name or age.
func credentialProbeID(cred *agentsv1alpha1.ModelCredential) (string, bool) {
	if cred.Labels[claimscope.OwnerLabel] != agentsclient.ProviderName || cred.Labels[credentialProbeLabel] != "true" {
		return "", false
	}
	id := strings.TrimSpace(cred.Annotations[credentialProbeIDAnnotation])
	return id, id != ""
}

// credentialProbeDeadline derives the cleanup deadline from persisted server
// metadata so any leader can resume the same decision after a restart. An
// annotation may shorten the ten-minute maximum but cannot extend it. Missing
// or malformed annotations keep legacy marked probes within that maximum.
func credentialProbeDeadline(cred *agentsv1alpha1.ModelCredential, now time.Time) time.Time {
	if cred.CreationTimestamp.IsZero() {
		// A server-persisted object normally always has this. If a partial test
		// fixture or non-conforming API omits it, fail closed and clean at once
		// rather than grant an unbounded lifetime.
		return now
	}
	deadline := cred.CreationTimestamp.Add(credentialProbeMaxAge)
	if raw := strings.TrimSpace(cred.Annotations[credentialProbeExpiresAnnotation]); raw != "" {
		if annotated, err := time.Parse(time.RFC3339, raw); err == nil && annotated.Before(deadline) {
			deadline = annotated.UTC()
		}
	}
	return deadline
}

func hasCredentialProbeCleanupFinalizer(cred *agentsv1alpha1.ModelCredential) bool {
	for _, finalizer := range cred.Finalizers {
		if finalizer == credentialProbeCleanupFinalizer {
			return true
		}
	}
	return false
}

func (r *Reconciler) beginCredentialProbeCleanup(
	ctx context.Context,
	c client.Client,
	cred *agentsv1alpha1.ModelCredential,
	probeID string,
) (ctrl.Result, error) {
	if cred.UID == "" || cred.ResourceVersion == "" {
		return r.blockCredentialProbeCleanup(ctx, c, cred)
	}
	if !credentialProbeIDStillMatches(cred, probeID) {
		return ctrl.Result{}, fmt.Errorf("temporary model credential probe marker changed before cleanup reservation")
	}
	secretName := strings.TrimSpace(cred.Spec.SecretRef.Name)
	if hasCredentialProbeCleanupFinalizer(cred) {
		reservedID, hasReservedID := cred.Annotations[credentialProbeCleanupIDAnnotation]
		reservedSecret, hasReservedSecret := cred.Annotations[credentialProbeCleanupSecretAnnotation]
		if !hasReservedID || !hasReservedSecret || reservedID != probeID || reservedSecret != secretName {
			// The delete CAS has not succeeded, so a concurrent marker/spec edit
			// can still cancel this reservation. Never redirect an existing
			// reservation to a new Secret based on a retry's mutable spec.
			return ctrl.Result{}, r.releaseCredentialProbeReservation(ctx, c, cred)
		}
	}
	if cred.Annotations == nil {
		cred.Annotations = map[string]string{}
	}
	changed := false
	if !hasCredentialProbeCleanupFinalizer(cred) {
		cred.Finalizers = append(cred.Finalizers, credentialProbeCleanupFinalizer)
		changed = true
	}
	if cred.Annotations[credentialProbeCleanupIDAnnotation] != probeID {
		cred.Annotations[credentialProbeCleanupIDAnnotation] = probeID
		changed = true
	}
	// Store an empty string too: its presence distinguishes an intentional
	// partial create with no referenced Secret from a lost reservation record.
	if reserved, exists := cred.Annotations[credentialProbeCleanupSecretAnnotation]; !exists || reserved != secretName {
		cred.Annotations[credentialProbeCleanupSecretAnnotation] = secretName
		changed = true
	}
	if changed {
		// This resource-version CAS reserves the exact probe identity and
		// referenced Secret. If a concurrent edit wins, no Secret is touched.
		if err := c.Update(ctx, cred); err != nil {
			return ctrl.Result{}, fmt.Errorf("reserving temporary model credential cleanup: %w", err)
		}
	}

	credentialUID, credentialResourceVersion := cred.UID, cred.ResourceVersion
	if err := c.Delete(ctx, cred, client.Preconditions(metav1.Preconditions{
		UID:             &credentialUID,
		ResourceVersion: &credentialResourceVersion,
	})); err != nil && !apierrors.IsNotFound(err) {
		// The finalizer and snapshot remain as a durable retry anchor. A
		// resource-version conflict means an edit raced the delete; retry will
		// revalidate the live marker before making cleanup irreversible.
		return ctrl.Result{}, fmt.Errorf("starting deletion of expired temporary model credential: %w", err)
	}
	var terminating agentsv1alpha1.ModelCredential
	if err := c.Get(ctx, types.NamespacedName{Name: cred.Name}, &terminating); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("confirming temporary model credential deletion: %w", err)
	}
	if terminating.DeletionTimestamp.IsZero() || !hasCredentialProbeCleanupFinalizer(&terminating) {
		return ctrl.Result{}, fmt.Errorf("temporary model credential deletion did not retain its cleanup finalizer")
	}
	return r.finishCredentialProbeCleanup(ctx, c, &terminating)
}

// finishCredentialProbeCleanup runs only after deletionTimestamp makes the
// reservation irreversible. It uses the captured id/name, not mutable spec or
// portal labels, and removes the ModelCredential finalizer only after a fresh
// GET confirms the Secret is absent.
func (r *Reconciler) finishCredentialProbeCleanup(
	ctx context.Context,
	c client.Client,
	cred *agentsv1alpha1.ModelCredential,
) (ctrl.Result, error) {
	probeID, hasID := cred.Annotations[credentialProbeCleanupIDAnnotation]
	secretName, hasSecretName := cred.Annotations[credentialProbeCleanupSecretAnnotation]
	if !hasID || strings.TrimSpace(probeID) == "" || !hasSecretName || cred.UID == "" || cred.ResourceVersion == "" {
		return r.blockCredentialProbeCleanup(ctx, c, cred)
	}
	if secretName != "" {
		secretKey := types.NamespacedName{Namespace: llm.SecretNamespace, Name: secretName}
		var secret corev1.Secret
		err := c.Get(ctx, secretKey, &secret)
		switch {
		case apierrors.IsNotFound(err):
			// A failed/abandoned second create is a valid partial probe. The
			// referenced Secret is already absent, so finalization may continue.
		case err != nil:
			return ctrl.Result{}, fmt.Errorf("reading temporary model credential Secret: %w", err)
		default:
			if !credentialProbeSecretOwnedBy(&secret, cred, probeID) || secret.UID == "" || secret.ResourceVersion == "" {
				return r.blockCredentialProbeCleanup(ctx, c, cred)
			}
			if secret.DeletionTimestamp.IsZero() {
				secretUID, secretResourceVersion := secret.UID, secret.ResourceVersion
				if err := c.Delete(ctx, &secret, client.Preconditions(metav1.Preconditions{
					UID:             &secretUID,
					ResourceVersion: &secretResourceVersion,
				})); err != nil && !apierrors.IsNotFound(err) {
					return ctrl.Result{}, fmt.Errorf("deleting temporary model credential Secret: %w", err)
				}
			}
			// A successful Delete can leave an object with finalizers. Keep the
			// ModelCredential finalizer until the Secret is really gone; the
			// Secret watch maps this saved name back to the terminating owner.
			var afterDelete corev1.Secret
			err = c.Get(ctx, secretKey, &afterDelete)
			if err == nil {
				if afterDelete.DeletionTimestamp.IsZero() {
					return ctrl.Result{}, fmt.Errorf("temporary model credential Secret delete has not started")
				}
				return ctrl.Result{}, nil
			}
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("confirming temporary model credential Secret deletion: %w", err)
			}
		}
	}

	return ctrl.Result{}, r.removeCredentialProbeCleanupFinalizer(ctx, c, cred)
}

func (r *Reconciler) removeCredentialProbeCleanupFinalizer(
	ctx context.Context,
	c client.Client,
	cred *agentsv1alpha1.ModelCredential,
) error {
	cred.Finalizers = removeFinalizer(cred.Finalizers, credentialProbeCleanupFinalizer)
	if cred.Annotations != nil {
		delete(cred.Annotations, credentialProbeCleanupIDAnnotation)
		delete(cred.Annotations, credentialProbeCleanupSecretAnnotation)
		if len(cred.Annotations) == 0 {
			cred.Annotations = nil
		}
	}
	if err := c.Update(ctx, cred); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("removing temporary model credential cleanup finalizer: %w", err)
	}
	return nil
}

func (r *Reconciler) releaseCredentialProbeReservation(ctx context.Context, c client.Client, cred *agentsv1alpha1.ModelCredential) error {
	return r.removeCredentialProbeCleanupFinalizer(ctx, c, cred)
}

func credentialProbeIDStillMatches(cred *agentsv1alpha1.ModelCredential, probeID string) bool {
	id, marked := credentialProbeID(cred)
	return marked && id == probeID
}

func removeFinalizer(finalizers []string, target string) []string {
	remaining := finalizers[:0]
	for _, finalizer := range finalizers {
		if finalizer != target {
			remaining = append(remaining, finalizer)
		}
	}
	return remaining
}

func credentialProbeSecretOwnedBy(secret *corev1.Secret, cred *agentsv1alpha1.ModelCredential, probeID string) bool {
	if secret.Namespace != llm.SecretNamespace ||
		secret.Labels[claimscope.OwnerLabel] != agentsclient.ProviderName ||
		secret.Labels[credentialProbeLabel] != "true" ||
		secret.Annotations[credentialProbeIDAnnotation] != probeID || cred.UID == "" {
		return false
	}
	for _, owner := range secret.OwnerReferences {
		if owner.APIVersion == agentsv1alpha1.SchemeGroupVersion.String() &&
			owner.Kind == "ModelCredential" &&
			owner.Name == cred.Name &&
			owner.UID == cred.UID {
			return true
		}
	}
	return false
}

func (r *Reconciler) blockCredentialProbeCleanup(
	ctx context.Context,
	c client.Client,
	cred *agentsv1alpha1.ModelCredential,
) (ctrl.Result, error) {
	const message = "temporary credential cleanup was refused because Secret ownership could not be confirmed; no Secret or credential was deleted"
	status := cred.Status.DeepCopy()
	status.ObservedGeneration = cred.Generation
	setCondition(status, agentsv1alpha1.ConditionReady, credentialProbeCleanupBlocked, message, "", cred.Generation, r.now())
	if !equalStatus(&cred.Status, status) {
		cred.Status = *status
		if err := c.Status().Update(ctx, cred); err != nil {
			return ctrl.Result{}, fmt.Errorf("recording temporary credential cleanup status: %w", err)
		}
	}
	// Returning an error leaves the marked ModelCredential present and lets the
	// controller's failure backoff retry. A Secret watch also re-enqueues it if
	// its labels or owner reference are repaired.
	return ctrl.Result{}, fmt.Errorf("temporary model credential cleanup blocked: %s", message)
}
