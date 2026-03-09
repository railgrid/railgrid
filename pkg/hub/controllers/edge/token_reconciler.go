/*
Copyright 2026 The Faros Authors.

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

package edge

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// TokenReconciler watches Edge resources and generates a random bootstrap join
// token in Status.JoinToken when one has not yet been assigned.
type TokenReconciler struct {
	mgr mcmanager.Manager
}

// SetupTokenWithManager registers the edge token controller with the multicluster manager.
func SetupTokenWithManager(mgr mcmanager.Manager) error {
	r := &TokenReconciler{mgr: mgr}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("edge-token").
		For(&kedgev1alpha1.Edge{}).
		Complete(r)
}

// Reconcile generates a join token for any Edge that does not yet have one.
func (r *TokenReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("edge", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var edge kedgev1alpha1.Edge
	if err := c.Get(ctx, req.NamespacedName, &edge); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Nothing to do if the token already exists or the edge is already registered.
	registered := meta.FindStatusCondition(edge.Status.Conditions, kedgev1alpha1.EdgeConditionRegistered)
	if registered != nil && registered.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
	}
	if edge.Status.JoinToken != "" {
		return ctrl.Result{}, nil
	}

	token, err := generateJoinToken()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("generating join token: %w", err)
	}

	edge.Status.JoinToken = token
	meta.SetStatusCondition(&edge.Status.Conditions, metav1.Condition{
		Type:               kedgev1alpha1.EdgeConditionRegistered,
		Status:             metav1.ConditionFalse,
		Reason:             "AwaitingAgent",
		Message:            "Waiting for agent to register using the bootstrap join token.",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
	if err := c.Status().Update(ctx, &edge); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating edge status with join token: %w", err)
	}

	logger.Info("Join token generated for edge", "edge", req.Name)
	return ctrl.Result{}, nil
}

// generateJoinToken returns a cryptographically random 32-byte base64url-encoded token.
func generateJoinToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
