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

package servicectrl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
	"github.com/faroshq/provider-edges/internal/haclient"
)

// validationResyncInterval bounds how often a Ready Service is re-validated.
const validationResyncInterval = 10 * time.Minute

// ValidationReconciler validates a Service's credentials against the
// service (Home Assistant: GET /api/config) and stamps status.URL + conditions.
type ValidationReconciler struct {
	mgr                 mcmanager.Manager
	connManager         ConnManager
	edgeProxyPublicPath string
}

// SetupValidationWithManager registers the validation reconciler (For Service).
func SetupValidationWithManager(mgr mcmanager.Manager, connManager ConnManager, edgeProxyPublicPath string) error {
	r := &ValidationReconciler{mgr: mgr, connManager: connManager, edgeProxyPublicPath: edgeProxyPublicPath}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("service-validation").
		For(&edgesv1alpha1.Service{}).
		Complete(r)
}

func (r *ValidationReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("service", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	es := &edgesv1alpha1.Service{}
	if err := c.Get(ctx, req.NamespacedName, es); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	orig := es.DeepCopy()

	// Always keep status.URL current.
	es.Status.URL = r.statusURL(string(req.ClusterName), es.Name)

	// No credentials → nothing to validate.
	if es.Spec.AuthSecretRef == nil {
		setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionUnknown, "NoCredentials", "no authSecretRef configured")
		if es.Status.Phase == "" {
			es.Status.Phase = "Detected"
		}
		return r.commit(ctx, c, orig, es, validationResyncInterval)
	}

	// A kube Service needs a targetRef to have anything to dial. The CRD's CEL
	// rule enforces this, but an object written before the rule (or by a client
	// that bypassed it) would otherwise silently validate against loopback.
	if isKube(es) && (es.Spec.TargetRef == nil || es.Spec.TargetRef.Name == "" || es.Spec.TargetRef.Namespace == "") {
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "MissingTargetRef",
			"spec.targetRef is required when spec.edgeRef.kind is KubernetesCluster")
		setNotProbed(es, "spec.targetRef is missing, so the service was never reached")
		es.Status.Phase = "Unreachable"
		return r.commit(ctx, c, orig, es, validationResyncInterval)
	}

	// Need a live tunnel to the edge to validate.
	key := connKey(connResource(es), string(req.ClusterName), es.Spec.EdgeRef.Name)
	dialer, ok := r.connManager.Load(key)
	if !ok {
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "EdgeDisconnected", "no live tunnel to the edge")
		setNotProbed(es, "the edge is disconnected, so the service was never reached")
		return r.commit(ctx, c, orig, es, 30*time.Second)
	}

	token, err := r.readToken(ctx, c, es)
	if err != nil {
		setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionFalse, "SecretError", err.Error())
		es.Status.Phase = "Unreachable"
		return r.commit(ctx, c, orig, es, validationResyncInterval)
	}

	// Validate. Home Assistant: GET /api/config returns { version, ... }.
	target := haclient.Target{
		Scheme: schemeString(es.Spec.Scheme),
		Host:   targetHost(es),
		Port:   es.Spec.Port,
		Token:  token,
	}
	resp, err := haclient.Do(ctx, dialer, target, http.MethodGet, "/api/config", nil)
	if err != nil {
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "ProbeFailed", err.Error())
		setNotProbed(es, "the service could not be reached, so the token was never checked")
		es.Status.Phase = "Unreachable"
		return r.commit(ctx, c, orig, es, validationResyncInterval)
	}
	defer resp.Body.Close() //nolint:errcheck

	switch {
	case resp.StatusCode == http.StatusOK:
		var cfg struct {
			Version string `json:"version"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cfg)
		if cfg.Version != "" {
			es.Status.Version = cfg.Version
		}
		es.Status.Phase = "Ready"
		setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionTrue, "Validated", "credentials accepted by the service")
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionTrue, "Ready", "service reachable and authenticated")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		es.Status.Phase = "Unreachable"
		setCondition(&es.Status.Conditions, "CredentialsValid", metav1.ConditionFalse, "Unauthorized",
			fmt.Sprintf("service rejected the token (%d)", resp.StatusCode))
	default:
		es.Status.Phase = "Unreachable"
		setCondition(&es.Status.Conditions, "Ready", metav1.ConditionFalse, "ProbeFailed",
			fmt.Sprintf("service returned %d", resp.StatusCode))
		setNotProbed(es, fmt.Sprintf("service returned %d, so the token was never checked", resp.StatusCode))
	}

	logger.V(4).Info("validated service", "phase", es.Status.Phase)
	return r.commit(ctx, c, orig, es, validationResyncInterval)
}

// commit writes status only when it changed, then requeues.
func (r *ValidationReconciler) commit(ctx context.Context, c client.Client, orig, es *edgesv1alpha1.Service, requeue time.Duration) (ctrl.Result, error) {
	if equalStatus(&orig.Status, &es.Status) {
		return ctrl.Result{RequeueAfter: requeue}, nil
	}
	if err := c.Status().Update(ctx, es); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating service status: %w", err)
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// readToken reads the "token" key from the Service's authSecretRef.
func (r *ValidationReconciler) readToken(ctx context.Context, c client.Client, es *edgesv1alpha1.Service) (string, error) {
	ref := es.Spec.AuthSecretRef
	secret := &corev1.Secret{}
	nn := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
	if err := c.Get(ctx, nn, secret); err != nil {
		return "", fmt.Errorf("fetching auth secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	tok, ok := secret.Data["token"]
	if !ok || len(tok) == 0 {
		return "", fmt.Errorf("auth secret %s/%s has no \"token\" key", ref.Namespace, ref.Name)
	}
	return string(tok), nil
}

// statusURL builds the externalized svc-proxy base for a Service.
func (r *ValidationReconciler) statusURL(cluster, name string) string {
	if r.edgeProxyPublicPath == "" {
		return ""
	}
	return fmt.Sprintf("%s/clusters/%s/apis/%s/%s/services/%s/proxy",
		r.edgeProxyPublicPath, cluster,
		edgesv1alpha1.GroupName, edgesv1alpha1.Version, name)
}

func schemeString(s edgesv1alpha1.ServiceScheme) string {
	if s == edgesv1alpha1.ServiceSchemeHTTPS {
		return "https"
	}
	return "http"
}
