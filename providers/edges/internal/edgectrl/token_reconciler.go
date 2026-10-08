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

package edgectrl

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgeapi "github.com/railgrid/provider-edges/internal/edgeapi"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// TokenReconciler watches a connectable kind (KubernetesCluster / LinuxServer)
// and generates a random bootstrap join token in status.joinToken when one has
// not yet been assigned. Registered once per kind; newObj yields a fresh typed
// object of the kind this instance watches.
type TokenReconciler struct {
	mgr    mcmanager.Manager
	newObj func() edgeapi.Connectable
}

// SetupTokenWithManager registers the token controller for every connectable
// kind on the multicluster manager.
func SetupTokenWithManager(mgr mcmanager.Manager, gvr schema.GroupVersionResource, newObj func() edgeapi.Connectable) error {
	r := &TokenReconciler{mgr: mgr, newObj: newObj}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("token-" + gvr.Resource).
		For(newObj()).
		Complete(r)
}

// Reconcile generates a join token for any connectable that does not yet have one.
func (r *TokenReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("edge", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	edge := r.newObj()
	if err := c.Get(ctx, req.NamespacedName, edge); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	cs := edge.GetConnectionStatus()

	// A user-set regenerate annotation forces a fresh token even on an
	// already-registered edge. Clear the annotation first so a failure mid-way
	// doesn't loop forever.
	if _, regenerate := edge.GetAnnotations()[edgeapi.AnnotationRegenerateJoinToken]; regenerate {
		anns := edge.GetAnnotations()
		delete(anns, edgeapi.AnnotationRegenerateJoinToken)
		edge.SetAnnotations(anns)
		if err := c.Update(ctx, edge); err != nil {
			return ctrl.Result{}, fmt.Errorf("clearing regenerate annotation: %w", err)
		}
		return r.issueToken(ctx, c, edge, cs, "RegenerateRequested", "Bootstrap join token regenerated on request.", logger)
	}

	if !needsJoinToken(cs) {
		return ctrl.Result{}, nil
	}

	return r.issueToken(ctx, c, edge, cs, "AwaitingAgent", "Waiting for agent to register using the bootstrap join token.", logger)
}

func (r *TokenReconciler) issueToken(ctx context.Context, c client.Client, edge edgeapi.Connectable, cs *edgeapi.ConnectionStatus, reason, message string, logger klog.Logger) (ctrl.Result, error) {
	token, err := generateJoinToken()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("generating join token: %w", err)
	}

	cs.JoinToken = token
	// Kept after the token is cleared on registration, so the agent that joined
	// with this token can re-enroll after its durable credential expires.
	// Rotation replaces it, which is what invalidates the previous token.
	cs.JoinTokenHash = edgeapi.HashJoinToken(token)
	meta.SetStatusCondition(&cs.Conditions, metav1.Condition{
		Type:               edgeapi.ConnectionConditionRegistered,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
	if err := c.Status().Update(ctx, edge); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating edge status with join token: %w", err)
	}

	logger.Info("Join token generated for edge", "edge", edge.GetName(), "reason", reason)
	return ctrl.Result{}, nil
}

// needsJoinToken reports whether this edge still has to have a bootstrap token
// minted for it. Explicit rotation (AnnotationRegenerateJoinToken) does not go
// through here.
func needsJoinToken(cs *edgeapi.ConnectionStatus) bool {
	// Already registered: the agent has a durable credential.
	registered := meta.FindStatusCondition(cs.Conditions, edgeapi.ConnectionConditionRegistered)
	if registered != nil && registered.Status == metav1.ConditionTrue {
		return false
	}
	// A token is already outstanding, waiting for an agent to use it.
	if cs.JoinToken != "" {
		return false
	}
	// A hash with no plaintext means a token was issued AND an agent enrolled
	// with it: registration clears status.joinToken and keeps only the hash, so
	// that agent can re-enroll with the same token later.
	//
	// Minting a replacement here is what broke edges across an agent restart.
	// The hub clears joinToken with a MergePatch sent before it records the rest
	// of the registration, and the Registered condition is written afterwards by
	// the lifecycle reconciler -- so there is a window in which the token looks
	// absent and the edge does not look registered yet. Regenerating inside that
	// window overwrites JoinTokenHash with the digest of a token nobody holds,
	// and the hub's follow-up clear then destroys that token's plaintext too.
	// The agent, whose join token is now orphaned, is refused forever.
	if cs.JoinTokenHash != "" {
		return false
	}
	return true
}

// generateJoinToken returns a cryptographically random 32-byte base64url-encoded token.
func generateJoinToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
