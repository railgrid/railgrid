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

package modelcredential

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"k8s.io/klog/v2"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
)

const (
	migratedFrom                = "agents.railgrid.ai/migrated-model-credential-uid"
	conditionConnectionReady    = "ConnectionReady"
	conditionModelConfigured    = "ModelConfigured"
	reasonConnectionUnavailable = "ConnectionUnavailable"
	reasonConnectionNotReady    = "ConnectionNotReady"
)

// ProviderConnectionReconciler owns shared provider health, key lifetime, and
// the restart-safe migration of pre-split ModelCredential objects.
type ProviderConnectionReconciler struct {
	Reconciler
	// MigrateLegacy enables conversion only when the compatible model-management
	// UI ships. Readiness and key cleanup remain active while it is false.
	MigrateLegacy bool
}

func (r *ProviderConnectionReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).Named("agents-modelproviderconnection").For(&agentsv1alpha1.ModelProviderConnection{}).
		Watches(&agentsv1alpha1.ModelCredential{}, connectionsForModel).
		Watches(&corev1.Secret{}, connectionsForSecret).Complete(r)
}

func connectionsForModel(name multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		model, ok := obj.(*agentsv1alpha1.ModelCredential)
		if !ok || agentsv1alpha1.IsHarnessProvider(model.Spec.Provider) {
			return nil
		}
		target := model.Spec.ConnectionRef
		if target == "" {
			target = model.Name
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: target}}}
	}))(name, cl)
}
func connectionsForSecret(clusterName multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return connectionRequestsForSecret(ctx, cl, obj)
	}))(clusterName, cl)
}

func connectionRequestsForSecret(ctx context.Context, cl cluster.Cluster, obj client.Object) []reconcile.Request {
	if obj.GetNamespace() != llm.SecretNamespace {
		return nil
	}
	var list agentsv1alpha1.ModelProviderConnectionList
	if err := cl.GetClient().List(ctx, &list); err != nil {
		klog.FromContext(ctx).Error(err, "listing model provider connections for a secret event")
		return nil
	}
	var requests []reconcile.Request
	for i := range list.Items {
		if strings.TrimSpace(list.Items[i].Spec.SecretRef.Name) == obj.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
	}
	return requests
}

func modelRequestsForConnection(ctx context.Context, cl cluster.Cluster, obj client.Object) []reconcile.Request {
	var list agentsv1alpha1.ModelCredentialList
	if err := cl.GetClient().List(ctx, &list); err != nil {
		klog.FromContext(ctx).Error(err, "listing model bindings for a provider connection event")
		return nil
	}
	var requests []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.ConnectionRef == obj.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
	}
	return requests
}

// migrateCredential transfers only configuration and provider-created ownership;
// it never reads, copies, rotates or rewrites key data. Model names stay stable.
func migrateCredential(ctx context.Context, c client.Client, legacy *agentsv1alpha1.ModelCredential) error {
	return migrateCredentialWithReader(ctx, c, c, legacy)
}

func migrateCredentialWithReader(ctx context.Context, c client.Client, reader client.Reader, legacy *agentsv1alpha1.ModelCredential) error {
	if reader == nil {
		reader = c
	}
	if legacy.Spec.ConnectionRef != "" || agentsv1alpha1.IsHarnessProvider(legacy.Spec.Provider) || !legacy.DeletionTimestamp.IsZero() {
		return nil
	}
	if strings.TrimSpace(legacy.Annotations[agentsv1alpha1.PendingKeySecretAnnotation]) != "" {
		// A direct-key writer has reserved a new immutable Secret but has not
		// activated it yet. Let the key writer finish; its ModelCredential
		// update will enqueue this migration again.
		return nil
	}
	if _, probe := credentialProbeID(legacy); probe {
		return nil
	}
	if legacy.UID == "" {
		return fmt.Errorf("cannot migrate model credential %q without a UID", legacy.Name)
	}
	if err := protectCredentialSecretOwnership(ctx, c, legacy); err != nil {
		return err
	}
	// Keep the local snapshot and its resourceVersion aligned with the finalizer
	// write before creating the destination or transferring Secret ownership.
	var protected agentsv1alpha1.ModelCredential
	if err := reader.Get(ctx, types.NamespacedName{Name: legacy.Name}, &protected); err != nil {
		return fmt.Errorf("confirming model credential %q Secret protection: %w", legacy.Name, err)
	}
	if protected.UID != legacy.UID {
		return fmt.Errorf("model credential %q changed UID before migration", legacy.Name)
	}
	if strings.TrimSpace(protected.Annotations[agentsv1alpha1.PendingKeySecretAnnotation]) != "" {
		return nil
	}
	*legacy = protected
	connection := &agentsv1alpha1.ModelProviderConnection{}
	err := reader.Get(ctx, types.NamespacedName{Name: legacy.Name}, connection)
	desired := agentsv1alpha1.ModelProviderConnectionSpec{Provider: legacy.Spec.Provider, BaseURL: legacy.Spec.BaseURL, SecretRef: legacy.Spec.SecretRef, SecretKey: legacy.Spec.SecretKey, AnthropicWorkspaceID: legacy.Spec.AnthropicWorkspaceID}
	secretID := strings.TrimSpace(legacy.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation])
	if apierrors.IsNotFound(err) {
		annotations := map[string]string{migratedFrom: string(legacy.UID)}
		if secretID != "" {
			annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] = secretID
		}
		connection = &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: legacy.Name, Annotations: annotations, Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}}, Spec: desired}
		if display := legacy.Annotations["agents.railgrid.ai/display-name"]; display != "" {
			connection.Annotations["agents.railgrid.ai/display-name"] = display
		}
		if err := c.Create(ctx, connection); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if connection.Annotations[migratedFrom] != string(legacy.UID) {
		return fmt.Errorf("provider connection %q already exists independently; legacy credential was preserved", legacy.Name)
	} else if !sameConnectionConfiguration(connection.Spec, desired) {
		return fmt.Errorf("provider connection %q and its migration source have different configurations; align their endpoint and secret configuration before retrying; legacy credential was preserved", legacy.Name)
	}
	if secretID != "" && connection.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != secretID {
		if connection.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != "" {
			return fmt.Errorf("provider connection %q has a different generated key identity; legacy credential was preserved", connection.Name)
		}
		connection = connection.DeepCopy()
		if connection.Annotations == nil {
			connection.Annotations = map[string]string{}
		}
		connection.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] = secretID
		if err := c.Update(ctx, connection); err != nil {
			return fmt.Errorf("copying generated key identity to provider connection %q: %w", connection.Name, err)
		}
		if err := reader.Get(ctx, types.NamespacedName{Name: connection.Name}, connection); err != nil {
			return fmt.Errorf("confirming generated key identity on provider connection %q: %w", connection.Name, err)
		}
	}
	if connection.UID == "" {
		return fmt.Errorf("provider connection %q has no UID; legacy Secret ownership cannot be transferred", connection.Name)
	}
	if !connection.DeletionTimestamp.IsZero() && !slices.Contains(connection.Finalizers, agentsv1alpha1.ModelProviderConnectionFinalizer) {
		return fmt.Errorf("provider connection %q is deleting without its Secret protection finalizer; legacy credential migration was preserved", connection.Name)
	}
	if !slices.Contains(connection.Finalizers, agentsv1alpha1.ModelProviderConnectionFinalizer) {
		connection.Finalizers = append(connection.Finalizers, agentsv1alpha1.ModelProviderConnectionFinalizer)
		if err := c.Update(ctx, connection); err != nil {
			return fmt.Errorf("protecting provider connection %q before Secret ownership transfer: %w", connection.Name, err)
		}
		if err := reader.Get(ctx, types.NamespacedName{Name: connection.Name}, connection); err != nil {
			return fmt.Errorf("confirming provider connection %q protection: %w", connection.Name, err)
		}
	}
	// Key ownership is transferred before modifying the source. ResourceVersion
	// protects every step; any failure leaves a source the next reconcile retries.
	var secret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{Namespace: llm.SecretNamespace, Name: legacy.Spec.SecretRef.Name}, &secret); err == nil {
		changed := false
		for i, owner := range secret.OwnerReferences {
			if isLegacyCredentialOwner(owner, legacy) {
				secret.OwnerReferences[i] = metav1.OwnerReference{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection", Name: connection.Name, UID: connection.UID}
				changed = true
			}
		}
		if changed {
			if err := c.Update(ctx, &secret); err != nil {
				return err
			}
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if legacy.Spec.Model != "" {
		legacy.Spec = agentsv1alpha1.ModelCredentialSpec{ConnectionRef: connection.Name, Model: legacy.Spec.Model}
		return c.Update(ctx, legacy)
	}
	// The destination now owns the transferred Secret and has its own guard.
	// Clean any older generated Secret still owned by the source before
	// removing this source's guard; a pending Secret finalizer keeps migration
	// retrying here.
	result, err := (&Reconciler{}).releaseCredentialSecretProtection(ctx, c, reader, legacy)
	if err != nil {
		return err
	}
	if result.RequeueAfter > 0 {
		return fmt.Errorf("waiting for legacy model credential %q Secret cleanup before completing migration", legacy.Name)
	}
	if err := reader.Get(ctx, types.NamespacedName{Name: legacy.Name}, legacy); err != nil {
		return fmt.Errorf("confirming legacy model credential %q before deletion: %w", legacy.Name, err)
	}
	return c.Delete(ctx, legacy, client.Preconditions{UID: &legacy.UID, ResourceVersion: &legacy.ResourceVersion})
}

// Server defaulting must not turn an otherwise identical retry into a conflict.
func sameConnectionConfiguration(a, b agentsv1alpha1.ModelProviderConnectionSpec) bool {
	normalize := func(spec agentsv1alpha1.ModelProviderConnectionSpec) agentsv1alpha1.ModelProviderConnectionSpec {
		if spec.Provider == "" {
			spec.Provider = agentsv1alpha1.ModelProviderOpenAICompatible
		}
		if spec.SecretKey == "" {
			spec.SecretKey = agentsv1alpha1.DefaultModelSecretKey
		}
		return spec
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func (r *ProviderConnectionReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()
	reader := cl.GetAPIReader()
	if reader == nil {
		reader = c
	}
	var connection agentsv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, req.NamespacedName, &connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if !r.MigrateLegacy {
			return ctrl.Result{}, nil
		}
		var legacy agentsv1alpha1.ModelCredential
		if err := reader.Get(ctx, req.NamespacedName, &legacy); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, migrateCredentialWithReader(ctx, c, reader, &legacy)
	}
	// Creation and source conversion are separate writes. Resume from the
	// source UID even when the destination already exists, including deletion
	// requests during an interrupted migration (the finalizer retains the key).
	if sourceUID := connection.Annotations[migratedFrom]; r.MigrateLegacy && sourceUID != "" {
		var legacy agentsv1alpha1.ModelCredential
		if err := reader.Get(ctx, req.NamespacedName, &legacy); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		} else if string(legacy.UID) == sourceUID && legacy.Spec.ConnectionRef == "" && !agentsv1alpha1.IsHarnessProvider(legacy.Spec.Provider) && legacy.DeletionTimestamp.IsZero() {
			return ctrl.Result{}, migrateCredentialWithReader(ctx, c, reader, &legacy)
		}
	}
	if !connection.DeletionTimestamp.IsZero() {
		return r.finalizeConnectionWithReader(ctx, c, reader, &connection)
	}
	if !slices.Contains(connection.Finalizers, agentsv1alpha1.ModelProviderConnectionFinalizer) {
		connection.Finalizers = append(connection.Finalizers, agentsv1alpha1.ModelProviderConnectionFinalizer)
		return ctrl.Result{}, c.Update(ctx, &connection)
	}
	// Reuse secret validation, but bindings never adopt a shared key.
	cred := &agentsv1alpha1.ModelCredential{ObjectMeta: connection.ObjectMeta, Spec: connection.CredentialSpec()}
	cred.Spec.ConnectionRef = connection.Name
	_, reason, message, err := r.resolveSecret(ctx, c, cred)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason == "" {
		if err := adoptConnectionSecret(ctx, c, &connection); err != nil {
			return ctrl.Result{}, err
		}
	}
	cleanupPending, err := reconcileConnectionSecretsWithReader(ctx, c, reader, &connection)
	if err != nil {
		return ctrl.Result{}, err
	}
	status := connection.Status.DeepCopy()
	status.ObservedGeneration = connection.Generation
	now := r.now()
	setCondition(status, agentsv1alpha1.ConditionSecretResolved, reason, message, "The provider API key is available.", connection.Generation, now)
	if reason == "" {
		key, _, _, err := r.resolveSecret(ctx, c, cred)
		if err != nil {
			return ctrl.Result{}, err
		}
		models, _, probeErr := r.probe(ctx, llm.Profile{Provider: connection.Spec.Provider, BaseURL: connection.Spec.BaseURL, APIKey: key, AnthropicWorkspaceID: connection.Spec.AnthropicWorkspaceID})
		status.LastProbeTime = &metav1.Time{Time: now}
		if probeErr != nil {
			status.LastProbeError = bounded(llm.ProbeSummary(probeErr))
			setCondition(status, agentsv1alpha1.ConditionReachable, agentsv1alpha1.ReasonProbeFailed, status.LastProbeError, "", connection.Generation, now)
		} else {
			status.LastProbeError = ""
			status.Models = boundModels(models)
			setCondition(status, agentsv1alpha1.ConditionReachable, "", "", "The provider endpoint answered discovery.", connection.Generation, now)
		}
	} else {
		setCondition(status, agentsv1alpha1.ConditionReachable, agentsv1alpha1.ReasonSecretUnresolved, message, "", connection.Generation, now)
	}
	ready := meta.IsStatusConditionTrue(status.Conditions, agentsv1alpha1.ConditionSecretResolved) && meta.IsStatusConditionTrue(status.Conditions, agentsv1alpha1.ConditionReachable)
	if ready {
		setCondition(status, agentsv1alpha1.ConditionReady, "", "", "The provider connection is ready.", connection.Generation, now)
	} else {
		setCondition(status, agentsv1alpha1.ConditionReady, agentsv1alpha1.ReasonNotReady, notReadyMessage(status), "", connection.Generation, now)
	}
	result := ctrl.Result{RequeueAfter: readyResync}
	if !ready {
		result.RequeueAfter = backoffFor(status, now)
	}
	if cleanupPending {
		result.RequeueAfter = backoffInitial
	}
	if !equalStatus(&connection.Status, status) {
		connection.Status = *status
		err = c.Status().Update(ctx, &connection)
	}
	return result, err
}

func adoptConnectionSecret(ctx context.Context, c client.Client, connection *agentsv1alpha1.ModelProviderConnection) error {
	if connection.UID == "" {
		return nil
	}
	id := strings.TrimSpace(connection.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation])
	legacyGenerated := connection.Spec.SecretRef.Name == agentsv1alpha1.ModelCredentialSecretName(connection.Name)
	randomGenerated := id != "" && connection.Spec.SecretRef.Name == agentsv1alpha1.GeneratedModelCredentialSecretName(id)
	if !legacyGenerated && !randomGenerated {
		return nil
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: llm.SecretNamespace, Name: connection.Spec.SecretRef.Name}, &secret); err != nil {
		return err
	}
	if randomGenerated && secret.Annotations[agentsv1alpha1.GeneratedKeySecretIDAnnotation] != id {
		return nil
	}
	if ownsConnectionSecret(&secret, connection) {
		return nil
	}
	secret.OwnerReferences = append(secret.OwnerReferences, metav1.OwnerReference{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection", Name: connection.Name, UID: connection.UID})
	return c.Update(ctx, &secret)
}

// reconcileConnectionSecretsWithReader keeps ownership only on the current Secret. Older
// generated Secrets are released when another object still uses them, and
// deleted with UID/resourceVersion preconditions when they are unshared.
func reconcileConnectionSecretsWithReader(ctx context.Context, c client.Client, reader client.Reader, connection *agentsv1alpha1.ModelProviderConnection) (bool, error) {
	return cleanupConnectionSecretsWithReader(ctx, c, reader, connection, connection.Spec.SecretRef.Name, true)
}

func ownsConnectionSecret(secret *corev1.Secret, connection *agentsv1alpha1.ModelProviderConnection) bool {
	if connection.UID == "" {
		return false
	}
	for _, owner := range secret.OwnerReferences {
		if owner.APIVersion == agentsv1alpha1.SchemeGroupVersion.String() &&
			owner.Kind == "ModelProviderConnection" &&
			owner.Name == connection.Name &&
			owner.UID == connection.UID {
			return true
		}
	}
	return false
}

func isConnectionSecretOwner(owner metav1.OwnerReference, connection *agentsv1alpha1.ModelProviderConnection) bool {
	return owner.APIVersion == agentsv1alpha1.SchemeGroupVersion.String() &&
		owner.Kind == "ModelProviderConnection" &&
		owner.Name == connection.Name &&
		owner.UID == connection.UID
}

// cleanupConnectionSecretsWithReader discovers every Secret still owned by this exact
// connection UID, rather than trusting its mutable SecretRef. When waitForGone
// is true, the connection finalizer stays until an unshared Secret is gone.
func cleanupConnectionSecretsWithReader(
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	connection *agentsv1alpha1.ModelProviderConnection,
	keepName string,
	waitForGone bool,
) (bool, error) {
	if reader == nil {
		reader = c
	}
	if connection.UID == "" {
		return false, nil
	}
	var secrets corev1.SecretList
	if err := reader.List(ctx, &secrets, client.InNamespace(llm.SecretNamespace)); err != nil {
		return false, err
	}
	if keepName != "" {
		var current agentsv1alpha1.ModelProviderConnection
		if err := reader.Get(ctx, types.NamespacedName{Name: connection.Name}, &current); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("refreshing provider connection %q before active key cleanup: %w", connection.Name, err)
		}
		if current.UID != connection.UID {
			return false, fmt.Errorf("provider connection %q changed UID before active key cleanup", connection.Name)
		}
		if !current.DeletionTimestamp.IsZero() {
			// A deletion finalizer ignores the reservation and removes every key
			// owned by this exact connection UID.
			keepName = ""
		} else {
			connection = &current
			keepName = connection.Spec.SecretRef.Name
		}
	}
	var connections agentsv1alpha1.ModelProviderConnectionList
	if err := reader.List(ctx, &connections); err != nil {
		return false, err
	}
	var models agentsv1alpha1.ModelCredentialList
	if err := reader.List(ctx, &models); err != nil {
		return false, err
	}

	pending := false
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		if !ownsConnectionSecret(secret, connection) || secret.Name == keepName ||
			(keepName != "" && secret.Name == connection.Annotations[agentsv1alpha1.PendingKeySecretAnnotation]) {
			continue
		}
		if secret.UID == "" || secret.ResourceVersion == "" {
			return false, fmt.Errorf("refusing to clean up owned Secret %q without UID and resourceVersion", secret.Name)
		}

		shared := false
		for _, owner := range secret.OwnerReferences {
			if !isConnectionSecretOwner(owner, connection) {
				shared = true
				break
			}
		}
		for _, sibling := range connections.Items {
			if sibling.UID != connection.UID && sibling.Spec.SecretRef.Name == secret.Name {
				shared = true
				break
			}
		}
		if !shared {
			for _, model := range models.Items {
				if model.Spec.SecretRef.Name == secret.Name {
					shared = true
					break
				}
			}
		}

		if shared {
			secret.OwnerReferences = slices.DeleteFunc(secret.OwnerReferences, func(owner metav1.OwnerReference) bool {
				return isConnectionSecretOwner(owner, connection)
			})
			if err := c.Update(ctx, secret); err != nil {
				return false, err
			}
			continue
		}

		if !secret.DeletionTimestamp.IsZero() {
			pending = true
			continue
		}
		secretUID, secretResourceVersion := secret.UID, secret.ResourceVersion
		if err := c.Delete(ctx, secret, client.Preconditions{UID: &secretUID, ResourceVersion: &secretResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		if waitForGone {
			var remaining corev1.Secret
			err := reader.Get(ctx, types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, &remaining)
			if err == nil {
				pending = true
			} else if !apierrors.IsNotFound(err) {
				return false, err
			}
		}
	}
	return pending, nil
}

func (r *ProviderConnectionReconciler) finalizeConnection(ctx context.Context, c client.Client, connection *agentsv1alpha1.ModelProviderConnection) (ctrl.Result, error) {
	return r.finalizeConnectionWithReader(ctx, c, c, connection)
}

func (r *ProviderConnectionReconciler) finalizeConnectionWithReader(ctx context.Context, c client.Client, reader client.Reader, connection *agentsv1alpha1.ModelProviderConnection) (ctrl.Result, error) {
	if !slices.Contains(connection.Finalizers, agentsv1alpha1.ModelProviderConnectionFinalizer) {
		return ctrl.Result{}, nil
	}
	var models agentsv1alpha1.ModelCredentialList
	if err := reader.List(ctx, &models); err != nil {
		return ctrl.Result{}, err
	}
	for _, model := range models.Items {
		if model.Spec.ConnectionRef == connection.Name || (model.Name == connection.Name && string(model.UID) == connection.Annotations[migratedFrom] && model.Spec.ConnectionRef == "" && !agentsv1alpha1.IsHarnessProvider(model.Spec.Provider)) {
			status := connection.Status.DeepCopy()
			setCondition(status, agentsv1alpha1.ConditionReady, "ModelsStillReferenceConnection", "Remove or reassign models before this connection can finish deleting.", "", connection.Generation, r.now())
			if !reflect.DeepEqual(connection.Status, *status) {
				connection.Status = *status
				return ctrl.Result{}, c.Status().Update(ctx, connection)
			}
			return ctrl.Result{}, nil
		}
	}
	pending, err := cleanupConnectionSecretsWithReader(ctx, c, reader, connection, "", true)
	if err != nil {
		return ctrl.Result{}, err
	}
	if pending {
		return ctrl.Result{RequeueAfter: backoffInitial}, nil
	}
	connection.Finalizers = slices.DeleteFunc(connection.Finalizers, func(value string) bool { return value == agentsv1alpha1.ModelProviderConnectionFinalizer })
	return ctrl.Result{}, c.Update(ctx, connection)
}

// A configured model reports its own dependency and model-selection state. Only
// the connection reconciler performs periodic provider discovery and reads keys.
func (r *Reconciler) reconcileModelBinding(ctx context.Context, c client.Client, model *agentsv1alpha1.ModelCredential) (ctrl.Result, error) {
	status := &agentsv1alpha1.ModelCredentialStatus{ObservedGeneration: model.Generation}
	for _, condition := range model.Status.Conditions {
		if condition.Type == conditionConnectionReady || condition.Type == conditionModelConfigured || condition.Type == agentsv1alpha1.ConditionReady {
			status.Conditions = append(status.Conditions, condition)
		}
	}

	var connection agentsv1alpha1.ModelProviderConnection
	err := c.Get(ctx, types.NamespacedName{Name: model.Spec.ConnectionRef}, &connection)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	readyCondition := meta.FindStatusCondition(connection.Status.Conditions, agentsv1alpha1.ConditionReady)
	connectionReason, connectionMessage := "", ""
	switch {
	case apierrors.IsNotFound(err):
		connectionReason, connectionMessage = reasonConnectionUnavailable, "the referenced provider connection does not exist"
	case !connection.DeletionTimestamp.IsZero():
		connectionReason, connectionMessage = reasonConnectionUnavailable, "the referenced provider connection is deleting"
	case connection.Status.ObservedGeneration != connection.Generation || readyCondition == nil || readyCondition.ObservedGeneration != connection.Generation:
		connectionReason, connectionMessage = reasonConnectionNotReady, "the referenced provider connection status is stale"
	case readyCondition.Status != metav1.ConditionTrue:
		connectionReason, connectionMessage = reasonConnectionNotReady, "the referenced provider connection is not ready"
	}
	now := r.now()
	setCondition(status, conditionConnectionReady, connectionReason, connectionMessage,
		"the referenced provider connection is ready", model.Generation, now)

	modelReason, modelMessage := "", ""
	if strings.TrimSpace(model.Spec.Model) == "" {
		modelReason, modelMessage = agentsv1alpha1.ReasonInvalidSpec, "spec.model must name a model ID"
	}
	setCondition(status, conditionModelConfigured, modelReason, modelMessage,
		"spec.model names a model ID", model.Generation, now)

	if connectionReason == "" && modelReason == "" {
		setCondition(status, agentsv1alpha1.ConditionReady, "", "",
			"the model is configured and its provider connection is ready", model.Generation, now)
	} else {
		message := connectionMessage
		if message == "" {
			message = modelMessage
		}
		setCondition(status, agentsv1alpha1.ConditionReady, agentsv1alpha1.ReasonNotReady,
			message, "", model.Generation, now)
	}

	if equalStatus(&model.Status, status) {
		return ctrl.Result{}, nil
	}
	model.Status = *status
	if err := c.Status().Update(ctx, model); apierrors.IsConflict(err) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
