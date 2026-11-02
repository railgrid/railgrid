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
	"strings"

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

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/llm"
)

const (
	conditionConnectionReady = "ConnectionReady"
	conditionModelConfigured = "ModelConfigured"

	reasonConnectionUnavailable = "ConnectionUnavailable"
	reasonConnectionNotReady    = "ConnectionNotReady"
)

// ProviderConnectionReconciler publishes health for shared provider
// connections. MigrateLegacy is reserved for the later migration layer; this
// foundation deliberately leaves it false and does not migrate credentials.
type ProviderConnectionReconciler struct {
	Reconciler
	MigrateLegacy bool
}

// SetupWithManager watches connection configuration and the Secret containing
// its key. A key rotation therefore probes again without waiting for the
// connection's periodic health check.
func (r *ProviderConnectionReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("agents-modelproviderconnection").
		For(&agentsv1alpha1.ModelProviderConnection{}).
		Watches(&corev1.Secret{}, connectionsForSecret).
		Complete(r)
}

// connectionsForSecret enqueues connections in this workspace that refer to
// the changed Secret. The provider's label-scoped Secret claim bounds what can
// be observed; no owner references or Secret data are changed here.
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

// modelsForConnection rechecks every binding when its referenced connection
// changes, including a connection becoming stale or entering deletion.
func modelsForConnection(clusterName multicluster.ClusterName, cl cluster.Cluster) mchandler.EventHandler {
	return mchandler.Lift(handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return modelRequestsForConnection(ctx, cl, obj)
	}))(clusterName, cl)
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

// Reconcile validates the connection Secret and probes its model endpoint.
// Connections own shared keys, so this only reads the Secret: it never adds an
// owner reference, finalizer, or other ownership metadata.
func (r *ProviderConnectionReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cl.GetClient()

	var connection agentsv1alpha1.ModelProviderConnection
	if err := c.Get(ctx, req.NamespacedName, &connection); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !connection.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Reuse the established Secret visibility and key-shape checks while marking
	// the temporary credential as connection-backed. resolveSecret then never
	// adopts the shared Secret.
	credential := &agentsv1alpha1.ModelCredential{
		ObjectMeta: connection.ObjectMeta,
		Spec:       connection.CredentialSpec(),
	}
	credential.Spec.ConnectionRef = connection.Name
	apiKey, secretReason, secretMessage, err := r.resolveSecret(ctx, c, credential)
	if err != nil {
		return ctrl.Result{}, err
	}

	now := r.now()
	status := connection.Status.DeepCopy()
	status.ObservedGeneration = connection.Generation
	setCondition(status, agentsv1alpha1.ConditionSecretResolved, secretReason,
		secretMessage, secretResolvedMessage(connection.Spec.Provider), connection.Generation, now)
	if secretReason != "" {
		status.LastProbeError = ""
		status.Models = nil
		setCondition(status, agentsv1alpha1.ConditionReachable, agentsv1alpha1.ReasonSecretUnresolved,
			"the endpoint was not called: "+secretMessage, "", connection.Generation, now)
	} else {
		models, _, probeErr := r.probe(ctx, llm.Profile{
			Provider:             connection.Spec.Provider,
			BaseURL:              connection.Spec.BaseURL,
			APIKey:               apiKey,
			AnthropicWorkspaceID: connection.Spec.AnthropicWorkspaceID,
		})
		status.LastProbeTime = &metav1.Time{Time: now}
		if probeErr != nil {
			status.LastProbeError = bounded(llm.ProbeSummary(probeErr))
			setCondition(status, agentsv1alpha1.ConditionReachable, agentsv1alpha1.ReasonProbeFailed,
				"GET "+strings.TrimRight(connection.Spec.BaseURL, "/")+"/models failed: "+status.LastProbeError,
				"", connection.Generation, now)
		} else {
			status.LastProbeError = ""
			status.Models = boundModels(models)
			setCondition(status, agentsv1alpha1.ConditionReachable, "", "",
				"the provider endpoint answered discovery",
				connection.Generation, now)
		}
	}

	ready := meta.IsStatusConditionTrue(status.Conditions, agentsv1alpha1.ConditionSecretResolved) &&
		meta.IsStatusConditionTrue(status.Conditions, agentsv1alpha1.ConditionReachable)
	if ready {
		setCondition(status, agentsv1alpha1.ConditionReady, "", "", "the provider connection is ready", connection.Generation, now)
	} else {
		setCondition(status, agentsv1alpha1.ConditionReady, agentsv1alpha1.ReasonNotReady,
			notReadyMessage(status), "", connection.Generation, now)
	}

	result := ctrl.Result{RequeueAfter: readyResync}
	if !ready {
		result.RequeueAfter = backoffFor(status, now)
	}
	if equalStatus(&connection.Status, status) {
		return result, nil
	}
	connection.Status = *status
	if err := c.Status().Update(ctx, &connection); apierrors.IsConflict(err) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return result, err
	}
	return result, nil
}

// reconcileModelBinding makes the model object report the state of its two
// dependencies without reading the shared key itself.
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
