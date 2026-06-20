/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/install"
)

// APIExportName is the provider's APIExport (manifest.yaml spec.apiExport.name).
const APIExportName = "infrastructure.providers.kedge.faros.sh"

// requeueInterval re-runs each CR's reconcile periodically so the bootstrap +
// kro release + serve Deployment self-heal even without a spec change.
const requeueInterval = 2 * time.Minute

// Reconciler reconciles InfrastructureProvider CRs.
type Reconciler struct {
	// Client reads CRs + referenced Secrets from the cluster the operator runs
	// in (where the CRs live).
	Client client.Client
}

// Reconcile drives one CR to its desired state.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := klog.FromContext(ctx)

	var cr v1alpha1.InfrastructureProvider
	if err := r.Client.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	applyDefaults(&cr)

	providerKC, err := r.secretValue(ctx, cr.Namespace, cr.Spec.ProviderKubeconfigSecret)
	if err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionBootstrapped, "ProviderKubeconfigMissing", err)
	}
	runtimeKC, err := r.secretValue(ctx, cr.Namespace, cr.Spec.RuntimeKubeconfigSecret)
	if err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionBootstrapped, "RuntimeKubeconfigMissing", err)
	}
	var hubToken []byte
	if cr.Spec.Hub.TokenSecret != nil {
		if hubToken, err = r.secretValue(ctx, cr.Namespace, *cr.Spec.Hub.TokenSecret); err != nil {
			return r.fail(ctx, &cr, v1alpha1.ConditionProviderDeployed, "HubTokenMissing", err)
		}
	}

	providerCfg, err := restConfigForWorkspace(providerKC, cr.Spec.ProviderWorkspace)
	if err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionBootstrapped, "ProviderKubeconfigInvalid", err)
	}
	runtimeCfg, err := clientcmd.RESTConfigFromKubeConfig(runtimeKC)
	if err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionKroReleased, "RuntimeKubeconfigInvalid", err)
	}

	// 1. Bootstrap the provider workspace.
	if err := Bootstrap(ctx, providerCfg, BootstrapOptions{
		WorkspacePath: cr.Spec.ProviderWorkspace,
		APIExportName: APIExportName,
	}); err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionBootstrapped, "BootstrapFailed", err)
	}
	setCond(&cr, v1alpha1.ConditionBootstrapped, metav1.ConditionTrue, "Bootstrapped", "provider workspace reconciled")

	// 2. kro: ensure namespace, seed its kcp-kubeconfig, then helm release.
	cs, err := runtimeClientset(runtimeCfg)
	if err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionKroReleased, "RuntimeClientFailed", err)
	}
	if err := ensureNamespace(ctx, cs, cr.Spec.Kro.Namespace); err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionKroReleased, "KroNamespaceFailed", err)
	}
	if err := install.SeedKroClusterFromKubeconfig(ctx, runtimeCfg, providerKC, cr.Spec.ProviderWorkspace); err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionKroReleased, "KroSeedFailed", err)
	}
	tmp, cleanup, err := writeTempKubeconfig(runtimeKC)
	if err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionKroReleased, "RuntimeKubeconfigWriteFailed", err)
	}
	defer cleanup()
	if err := EnsureKroRelease(ctx, tmp, cr.Spec.Kro); err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionKroReleased, "HelmFailed", err)
	}
	// Dev-only kind networking patches (no-op in prod; gated by env). Lets the
	// operator fully own kro even in Tilt's kind cluster.
	if err := ApplyKroDevPatches(ctx, cs, cr.Spec.Kro.Namespace, cr.Spec.Kro.ReleaseName); err != nil {
		return r.fail(ctx, &cr, v1alpha1.ConditionKroReleased, "KroDevPatchFailed", err)
	}
	setCond(&cr, v1alpha1.ConditionKroReleased, metav1.ConditionTrue, "Released", "kro helm release reconciled")

	// 3. Provider serve Deployment. Skippable in dev (INFRASTRUCTURE_OPERATOR_SKIP_SERVE)
	// where serve runs as a host binary for fast iteration.
	if os.Getenv("INFRASTRUCTURE_OPERATOR_SKIP_SERVE") == "true" {
		setCond(&cr, v1alpha1.ConditionProviderDeployed, metav1.ConditionTrue, "Skipped", "serve managed out-of-band (INFRASTRUCTURE_OPERATOR_SKIP_SERVE)")
	} else {
		if err := EnsureProviderServe(ctx, cs, &cr, providerKC, runtimeKC, hubToken); err != nil {
			return r.fail(ctx, &cr, v1alpha1.ConditionProviderDeployed, "ServeDeployFailed", err)
		}
		setCond(&cr, v1alpha1.ConditionProviderDeployed, metav1.ConditionTrue, "Deployed", "provider serve Deployment reconciled")
	}

	cr.Status.Phase = "Ready"
	cr.Status.ObservedGeneration = cr.Generation
	if err := r.Client.Status().Update(ctx, &cr); err != nil {
		log.Info("status update failed", "err", err.Error())
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// fail records a failure condition + Error phase and requeues.
func (r *Reconciler) fail(ctx context.Context, cr *v1alpha1.InfrastructureProvider, condType, reason string, cause error) (ctrl.Result, error) {
	klog.FromContext(ctx).Error(cause, "reconcile step failed", "condition", condType, "reason", reason)
	setCond(cr, condType, metav1.ConditionFalse, reason, cause.Error())
	cr.Status.Phase = "Error"
	cr.Status.ObservedGeneration = cr.Generation
	if err := r.Client.Status().Update(ctx, cr); err != nil {
		klog.FromContext(ctx).Info("status update failed", "err", err.Error())
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

func (r *Reconciler) secretValue(ctx context.Context, ns string, ref v1alpha1.SecretKeyRef) ([]byte, error) {
	key := ref.Key
	if key == "" {
		key = "kubeconfig"
	}
	var s corev1.Secret
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &s); err != nil {
		return nil, fmt.Errorf("get secret %s/%s: %w", ns, ref.Name, err)
	}
	v, ok := s.Data[key]
	if !ok || len(v) == 0 {
		return nil, fmt.Errorf("secret %s/%s missing key %q", ns, ref.Name, key)
	}
	return v, nil
}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.InfrastructureProvider{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// Run builds a manager on the supplied config (the cluster the CRs live in) and
// runs the InfrastructureProvider reconciler until ctx is cancelled.
func Run(ctx context.Context, cfg *rest.Config) error {
	ctrl.SetLogger(klog.NewKlogr())

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	mgr, err := manager.New(cfg, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return fmt.Errorf("manager.New: %w", err)
	}
	if err := (&Reconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup reconciler: %w", err)
	}
	klog.FromContext(ctx).Info("infrastructure operator manager starting")
	return mgr.Start(ctx)
}

// restConfigForWorkspace builds a rest.Config from kubeconfig bytes and
// retargets its host at the provider workspace path.
func restConfigForWorkspace(kubeconfig []byte, workspacePath string) (*rest.Config, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	if workspacePath != "" {
		host, err := install.RetargetHostToWorkspace(cfg.Host, workspacePath)
		if err != nil {
			return nil, err
		}
		cfg.Host = host
	}
	return cfg, nil
}

// applyDefaults fills spec defaults defensively (in case the CR was created
// against an apiserver without the CRD defaults, or via a raw client).
func applyDefaults(cr *v1alpha1.InfrastructureProvider) {
	k := &cr.Spec.Kro
	if k.Chart == "" {
		k.Chart = "oci://ghcr.io/faroshq/kro-multicluster/charts/kro/kro"
	}
	// Default to the multicluster fork image — the chart otherwise pulls the
	// upstream kro image, which lacks the kcp-apiexport build.
	if k.Image.Repository == "" {
		k.Image.Repository = "ghcr.io/faroshq/kro-multicluster/kro"
	}
	if k.Namespace == "" {
		k.Namespace = "kro-system"
	}
	if k.ReleaseName == "" {
		k.ReleaseName = "kro"
	}
	if k.APIExportEndpointSlice == "" {
		k.APIExportEndpointSlice = "infrastructure"
	}
	if cr.Spec.Provider.Port == 0 {
		cr.Spec.Provider.Port = 8081
	}
	if cr.Spec.Provider.Replicas == 0 {
		cr.Spec.Provider.Replicas = 1
	}
}

func setCond(cr *v1alpha1.InfrastructureProvider, condType string, status metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: cr.Generation,
	})
}
