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

const migratedFrom = "agents.railgrid.ai/migrated-model-credential-uid"

// ProviderConnectionReconciler owns shared provider health, key lifetime, and
// the restart-safe migration of pre-split ModelCredential objects.
type ProviderConnectionReconciler struct{ Reconciler }

func (r *ProviderConnectionReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).For(&agentsv1alpha1.ModelProviderConnection{}).
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
func connectionsForSecret(name multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		if obj.GetNamespace() != llm.SecretNamespace {
			return nil
		}
		var list agentsv1alpha1.ModelProviderConnectionList
		if err := cl.GetClient().List(ctx, &list); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, item := range list.Items {
			if item.Spec.SecretRef.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: item.Name}})
			}
		}
		return requests
	}))(name, cl)
}

// migrateCredential transfers only configuration and provider-created ownership;
// it never reads, copies, rotates or rewrites key data. Model names stay stable.
func migrateCredential(ctx context.Context, c client.Client, legacy *agentsv1alpha1.ModelCredential) error {
	if legacy.Spec.ConnectionRef != "" || agentsv1alpha1.IsHarnessProvider(legacy.Spec.Provider) || !legacy.DeletionTimestamp.IsZero() {
		return nil
	}
	if _, probe := credentialProbeID(legacy); probe {
		return nil
	}
	connection := &agentsv1alpha1.ModelProviderConnection{}
	err := c.Get(ctx, types.NamespacedName{Name: legacy.Name}, connection)
	desired := agentsv1alpha1.ModelProviderConnectionSpec{Provider: legacy.Spec.Provider, BaseURL: legacy.Spec.BaseURL, SecretRef: legacy.Spec.SecretRef, SecretKey: legacy.Spec.SecretKey, AnthropicWorkspaceID: legacy.Spec.AnthropicWorkspaceID}
	if apierrors.IsNotFound(err) {
		connection = &agentsv1alpha1.ModelProviderConnection{ObjectMeta: metav1.ObjectMeta{Name: legacy.Name, Annotations: map[string]string{migratedFrom: string(legacy.UID)}, Finalizers: []string{agentsv1alpha1.ModelProviderConnectionFinalizer}}, Spec: desired}
		if display := legacy.Annotations["agents.railgrid.ai/display-name"]; display != "" {
			connection.Annotations["agents.railgrid.ai/display-name"] = display
		}
		if err := c.Create(ctx, connection); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if connection.Annotations[migratedFrom] != string(legacy.UID) || !connection.DeletionTimestamp.IsZero() {
		return fmt.Errorf("provider connection %q already exists independently; legacy credential was preserved", legacy.Name)
	}
	// Key ownership is transferred before modifying the source. ResourceVersion
	// protects every step; any failure leaves a source the next reconcile retries.
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: llm.SecretNamespace, Name: legacy.Spec.SecretRef.Name}, &secret); err == nil {
		changed := false
		for i, owner := range secret.OwnerReferences {
			if owner.UID == legacy.UID && owner.Kind == "ModelCredential" {
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
	return c.Delete(ctx, legacy, client.Preconditions{UID: &legacy.UID, ResourceVersion: &legacy.ResourceVersion})
}

func (r *ProviderConnectionReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()
	var connection agentsv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, req.NamespacedName, &connection); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		var legacy agentsv1alpha1.ModelCredential
		if err := c.Get(ctx, req.NamespacedName, &legacy); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, migrateCredential(ctx, c, &legacy)
	}
	if !connection.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalizeConnection(ctx, c, &connection)
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
	if !equalStatus(&connection.Status, status) {
		connection.Status = *status
		err = c.Status().Update(ctx, &connection)
	}
	return result, err
}

func adoptConnectionSecret(ctx context.Context, c client.Client, connection *agentsv1alpha1.ModelProviderConnection) error {
	if connection.UID == "" || connection.Spec.SecretRef.Name != agentsv1alpha1.ModelCredentialSecretName(connection.Name) {
		return nil
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: llm.SecretNamespace, Name: connection.Spec.SecretRef.Name}, &secret); err != nil {
		return err
	}
	for _, owner := range secret.OwnerReferences {
		if owner.UID == connection.UID {
			return nil
		}
	}
	secret.OwnerReferences = append(secret.OwnerReferences, metav1.OwnerReference{APIVersion: agentsv1alpha1.SchemeGroupVersion.String(), Kind: "ModelProviderConnection", Name: connection.Name, UID: connection.UID})
	return c.Update(ctx, &secret)
}
func (r *ProviderConnectionReconciler) finalizeConnection(ctx context.Context, c client.Client, connection *agentsv1alpha1.ModelProviderConnection) error {
	if !slices.Contains(connection.Finalizers, agentsv1alpha1.ModelProviderConnectionFinalizer) {
		return nil
	}
	var models agentsv1alpha1.ModelCredentialList
	if err := c.List(ctx, &models); err != nil {
		return err
	}
	for _, model := range models.Items {
		if model.Spec.ConnectionRef == connection.Name || (model.Name == connection.Name && string(model.UID) == connection.Annotations[migratedFrom] && model.Spec.ConnectionRef == "" && !agentsv1alpha1.IsHarnessProvider(model.Spec.Provider)) {
			status := connection.Status.DeepCopy()
			setCondition(status, agentsv1alpha1.ConditionReady, "ModelsStillReferenceConnection", "Remove or reassign models before this connection can finish deleting.", "", connection.Generation, r.now())
			if !reflect.DeepEqual(connection.Status, *status) {
				connection.Status = *status
				return c.Status().Update(ctx, connection)
			}
			return nil
		}
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: llm.SecretNamespace, Name: connection.Spec.SecretRef.Name}, &secret); err == nil {
		for _, owner := range secret.OwnerReferences {
			if owner.UID == connection.UID && owner.Kind == "ModelProviderConnection" {
				if err := c.Delete(ctx, &secret, client.Preconditions{UID: &secret.UID}); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
				break
			}
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	connection.Finalizers = slices.DeleteFunc(connection.Finalizers, func(value string) bool { return value == agentsv1alpha1.ModelProviderConnectionFinalizer })
	return c.Update(ctx, connection)
}

// A configured model reports its own dependency and model-selection state. Only
// the connection reconciler performs periodic provider discovery and reads keys.
func (r *Reconciler) reconcileModelBinding(ctx context.Context, c client.Client, model *agentsv1alpha1.ModelCredential) (ctrl.Result, error) {
	status := &agentsv1alpha1.ModelCredentialStatus{ObservedGeneration: model.Generation, Conditions: slices.DeleteFunc(slices.Clone(model.Status.Conditions), func(condition metav1.Condition) bool {
		return condition.Type != "ConnectionReady" && condition.Type != "ModelConfigured" && condition.Type != agentsv1alpha1.ConditionReady
	})}
	var connection agentsv1alpha1.ModelProviderConnection
	err := c.Get(ctx, types.NamespacedName{Name: model.Spec.ConnectionRef}, &connection)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	reason, message := "", ""
	if err != nil || !connection.DeletionTimestamp.IsZero() {
		reason = "ConnectionUnavailable"
		message = "The provider connection is missing or deleting."
	} else if connection.Status.ObservedGeneration != connection.Generation || !meta.IsStatusConditionTrue(connection.Status.Conditions, agentsv1alpha1.ConditionReady) {
		reason = "ConnectionNotReady"
		message = "The provider connection is not ready. Review its status."
	}
	setCondition(status, "ConnectionReady", reason, message, "The referenced provider connection is ready.", model.Generation, r.now())
	modelReason := ""
	if model.Spec.Model == "" {
		modelReason = agentsv1alpha1.ReasonInvalidSpec
	}
	setCondition(status, "ModelConfigured", modelReason, "Select a model ID.", "A model ID is configured.", model.Generation, r.now())
	if reason == "" && modelReason == "" {
		setCondition(status, agentsv1alpha1.ConditionReady, "", "", "The model is configured and its provider connection is ready.", model.Generation, r.now())
	} else {
		if message == "" {
			message = "Select a model ID."
		}
		setCondition(status, agentsv1alpha1.ConditionReady, agentsv1alpha1.ReasonNotReady, message, "", model.Generation, r.now())
	}
	if !equalStatus(&model.Status, status) {
		model.Status = *status
		return ctrl.Result{}, c.Status().Update(ctx, model)
	}
	return ctrl.Result{}, nil
}
