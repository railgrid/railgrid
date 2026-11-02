// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package modelcredential

import (
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-sdk/claimscope"
)

func hasCredentialSecretProtectionFinalizer(cred *agentsv1alpha1.ModelCredential) bool {
	return slices.Contains(cred.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer)
}

// protectCredentialSecretOwnership persists the guard before any caller adds
// this credential as a Secret owner. The API server's resourceVersion check
// makes a concurrent spec edit or delete win cleanly; callers must stop on an
// error rather than continuing to adopt or transfer ownership.
func protectCredentialSecretOwnership(ctx context.Context, c client.Client, cred *agentsv1alpha1.ModelCredential) error {
	if hasCredentialSecretProtectionFinalizer(cred) {
		return nil
	}
	cred.Finalizers = append(cred.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer)
	if err := c.Update(ctx, cred); err != nil {
		return fmt.Errorf("installing model credential Secret protection finalizer: %w", err)
	}
	return nil
}

// secretEventOwnsCredential also routes rotated-away keys to their owner.
// Such keys no longer match secretRef but still require cleanup on watch events.
func secretEventOwnsCredential(obj client.Object, cred *agentsv1alpha1.ModelCredential) bool {
	for _, owner := range obj.GetOwnerReferences() {
		if isLegacyCredentialOwner(owner, cred) {
			return true
		}
	}
	return false
}

// The cache is only a work detector. Destructive cleanup still performs its
// existing ordered live reads and dependency checks. A stale cache can postpone
// work until its Secret watch catches up, but cannot authorize a deletion.
func reconcileActiveCredentialSecrets(ctx context.Context, c client.Client, reader client.Reader, cred *agentsv1alpha1.ModelCredential, keepName string) (bool, error) {
	var keys corev1.SecretList
	if err := c.List(ctx, &keys, client.InNamespace(llm.SecretNamespace)); err != nil {
		return false, err
	}
	for i := range keys.Items {
		key := &keys.Items[i]
		if hasLegacyCredentialOwner(key, cred) && key.Name != keepName && key.Name != cred.Annotations[agentsv1alpha1.PendingKeySecretAnnotation] {
			return reconcileCredentialSecretOwnership(ctx, c, reader, cred, keepName)
		}
	}
	return false, nil
}

// recoverInitialCredentialKey completes the durable first-write intent after
// a crash between creating an immutable owned key and activating its parent.
// A missing key cannot be reconstructed: the caller can retry with its key, or
// delete the failed credential. No deadline can safely distinguish a slow writer
// from a dead one, so we never expire a live reservation by time alone.
func recoverInitialCredentialKey(ctx context.Context, c client.Client, reader client.Reader, cred *agentsv1alpha1.ModelCredential) error {
	pending := cred.Annotations[agentsv1alpha1.PendingKeySecretAnnotation]
	if pending == "" || pending != cred.Spec.SecretRef.Name || cred.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != "" {
		return nil
	}
	id := strings.TrimPrefix(pending, agentsv1alpha1.GeneratedModelCredentialSecretName(""))
	if decoded, err := hex.DecodeString(id); err != nil || len(decoded) != 16 {
		return nil
	}
	var current agentsv1alpha1.ModelCredential
	if err := reader.Get(ctx, client.ObjectKeyFromObject(cred), &current); err != nil {
		return err
	}
	if current.UID != cred.UID || !current.DeletionTimestamp.IsZero() ||
		current.Annotations[agentsv1alpha1.PendingKeySecretAnnotation] != pending ||
		current.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != "" || !reflect.DeepEqual(current.Spec, cred.Spec) {
		return apierrors.NewConflict(agentsv1alpha1.Resource("modelcredentials"), cred.Name, fmt.Errorf("initial key reservation changed"))
	}
	var key corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{Namespace: llm.SecretNamespace, Name: pending}, &key); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if key.Immutable == nil || !*key.Immutable || !key.DeletionTimestamp.IsZero() ||
		key.Labels[claimscope.OwnerLabel] != "agents" ||
		key.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != id ||
		!hasLegacyCredentialOwner(&key, &current) || llm.APIKeyFromSecret(&key, current.Spec) == "" {
		return nil
	}
	current.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] = id
	delete(current.Annotations, agentsv1alpha1.PendingKeySecretAnnotation)
	if err := c.Update(ctx, &current); err != nil {
		return err
	}
	*cred = current
	return nil
}

// reconcileCredentialSecretOwnership removes or deletes every Secret still
// owned by this exact ModelCredential UID. SecretRef is intentionally not the
// discovery source: it is mutable, and a stale owner reference still makes
// Kubernetes garbage collection act on the old object.
//
// A Secret stays when another ModelCredential or ModelProviderConnection
// refers to it, or when it has any other owner reference. In that case only
// this credential's exact owner reference is detached. An unshared Secret is
// deleted with UID and resourceVersion preconditions. pending reports a
// deletion that has started but is waiting on the Secret's own finalizers.
func reconcileCredentialSecretOwnership(
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	cred *agentsv1alpha1.ModelCredential,
	keepName string,
) (pending bool, err error) {
	if reader == nil {
		reader = c
	}
	if cred.UID == "" {
		return false, fmt.Errorf("refusing to finalize model credential %q without a UID", cred.Name)
	}

	// List keys before refreshing the owner. Writers persist the pending-key
	// reservation before they create its Secret, so a live owner read after this
	// list observes either the reservation or a Secret that predates it. This
	// ordering keeps a stale informer snapshot from deleting a newly staged key.
	var secrets corev1.SecretList
	if err := reader.List(ctx, &secrets, client.InNamespace(llm.SecretNamespace)); err != nil {
		return false, fmt.Errorf("listing Secrets owned by model credential %q: %w", cred.Name, err)
	}
	if keepName != "" {
		var current agentsv1alpha1.ModelCredential
		if err := reader.Get(ctx, types.NamespacedName{Name: cred.Name}, &current); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("refreshing model credential %q before active key cleanup: %w", cred.Name, err)
		}
		if current.UID != cred.UID {
			return false, fmt.Errorf("model credential %q changed UID before active key cleanup", cred.Name)
		}
		if !current.DeletionTimestamp.IsZero() {
			// Deletion ignores an in-flight reservation. The finalizer must clean
			// every key owned by this exact UID before releasing the resource.
			keepName = ""
		} else {
			cred = &current
			keepName = strings.TrimSpace(current.Spec.SecretRef.Name)
		}
	}
	var connections agentsv1alpha1.ModelProviderConnectionList
	if err := reader.List(ctx, &connections); err != nil {
		return false, fmt.Errorf("listing provider connections for model credential %q: %w", cred.Name, err)
	}
	var models agentsv1alpha1.ModelCredentialList
	if err := reader.List(ctx, &models); err != nil {
		return false, fmt.Errorf("listing model credentials for model credential %q: %w", cred.Name, err)
	}

	for i := range secrets.Items {
		secret := &secrets.Items[i]
		// The old MCP writer could leave a conventional, labelled key without
		// an owner before its first reconcile. Preserve it when shared, but
		// include it in guarded cleanup when its last credential is deleted.
		unadoptedLegacy := keepName == "" && len(secret.OwnerReferences) == 0 &&
			secret.Name == cred.Spec.SecretRef.Name && secret.Name == agentsv1alpha1.ModelCredentialSecretName(cred.Name) && secret.Labels[claimscope.OwnerLabel] == "agents"
		if !hasLegacyCredentialOwner(secret, cred) && !unadoptedLegacy {
			continue
		}
		if secret.Name == keepName || (keepName != "" && secret.Name == cred.Annotations[agentsv1alpha1.PendingKeySecretAnnotation]) {
			continue
		}
		if secret.UID == "" || secret.ResourceVersion == "" {
			return false, fmt.Errorf("refusing to clean up owned Secret %q without UID and resourceVersion", secret.Name)
		}

		shared := hasOtherSecretOwner(secret, cred)
		if !shared {
			for _, connection := range connections.Items {
				if connection.Spec.SecretRef.Name == secret.Name {
					shared = true
					break
				}
			}
		}
		if !shared {
			for _, model := range models.Items {
				if model.UID != cred.UID && model.Spec.SecretRef.Name == secret.Name {
					shared = true
					break
				}
			}
		}

		if shared {
			secret.OwnerReferences = slices.DeleteFunc(secret.OwnerReferences, func(owner metav1.OwnerReference) bool {
				return isLegacyCredentialOwner(owner, cred)
			})
			if err := c.Update(ctx, secret); err != nil {
				return false, fmt.Errorf("detaching model credential %q from shared Secret %q: %w", cred.Name, secret.Name, err)
			}
			continue
		}

		if !secret.DeletionTimestamp.IsZero() {
			pending = true
			continue
		}
		secretUID, secretResourceVersion := secret.UID, secret.ResourceVersion
		if err := c.Delete(ctx, secret, client.Preconditions{UID: &secretUID, ResourceVersion: &secretResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("deleting unshared model credential Secret %q: %w", secret.Name, err)
		}

		var afterDelete corev1.Secret
		err := reader.Get(ctx, types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, &afterDelete)
		if err == nil {
			if afterDelete.DeletionTimestamp.IsZero() {
				return false, fmt.Errorf("model credential Secret %q delete has not started", secret.Name)
			}
			pending = true
		} else if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("confirming model credential Secret %q deletion: %w", secret.Name, err)
		}
	}
	return pending, nil
}

func hasLegacyCredentialOwner(secret *corev1.Secret, cred *agentsv1alpha1.ModelCredential) bool {
	for _, owner := range secret.OwnerReferences {
		if isLegacyCredentialOwner(owner, cred) {
			return true
		}
	}
	return false
}

func isLegacyCredentialOwner(owner metav1.OwnerReference, cred *agentsv1alpha1.ModelCredential) bool {
	return cred.UID != "" &&
		owner.APIVersion == agentsv1alpha1.SchemeGroupVersion.String() &&
		owner.Kind == "ModelCredential" &&
		owner.Name == cred.Name &&
		owner.UID == cred.UID
}

func hasOtherSecretOwner(secret *corev1.Secret, cred *agentsv1alpha1.ModelCredential) bool {
	for _, owner := range secret.OwnerReferences {
		if !isLegacyCredentialOwner(owner, cred) {
			return true
		}
	}
	return false
}

func (r *Reconciler) finalizeCredentialSecretProtection(
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	cred *agentsv1alpha1.ModelCredential,
) (ctrl.Result, error) {
	pending, err := reconcileCredentialSecretOwnership(ctx, c, reader, cred, "")
	if err != nil {
		return ctrl.Result{}, err
	}
	if pending {
		return ctrl.Result{RequeueAfter: backoffInitial}, nil
	}
	cred.Finalizers = removeFinalizer(cred.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer)
	if err := c.Update(ctx, cred); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("removing model credential Secret protection finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// releaseCredentialSecretProtection handles a source object that has already
// converted to a connection binding. Migration transfers its owned Secret to
// the connection first; this final check cleans up any older owned Secret left
// behind before the source's guard is removed.
func (r *Reconciler) releaseCredentialSecretProtection(
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	cred *agentsv1alpha1.ModelCredential,
) (ctrl.Result, error) {
	if !hasCredentialSecretProtectionFinalizer(cred) {
		return ctrl.Result{}, nil
	}
	pending, err := reconcileCredentialSecretOwnership(ctx, c, reader, cred, "")
	if err != nil {
		return ctrl.Result{}, err
	}
	if pending {
		return ctrl.Result{RequeueAfter: backoffInitial}, nil
	}
	cred.Finalizers = removeFinalizer(cred.Finalizers, agentsv1alpha1.ModelCredentialSecretProtectionFinalizer)
	if err := c.Update(ctx, cred); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("releasing converted model credential Secret protection finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}
