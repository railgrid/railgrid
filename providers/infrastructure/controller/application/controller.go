// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package application reconciles Application instances across every tenant
// workspace that enabled the infrastructure provider, through the provider's
// APIExport virtual workspace (the same cross-tenant rail the kuery provider's
// engagement controller uses).
//
// The kro fork materializes an Application's workloads on the runtime cluster
// from the RGD, but two things the RGD can't produce itself need a controller:
//
//   - spec.expose.fqdn — the public hostname, <prefix|name>-<tenantHash>.<base>.
//     kro can't derive a tenant hash in-graph, so the controller stamps it onto
//     spec; the RGD then reads ${schema.spec.expose.fqdn} for the Ingress host
//     and the oauth2-proxy redirect URL.
//   - the OIDC client secret — it must land as a Secret beside the oauth2-proxy
//     pod on the runtime cluster WITHOUT sitting in the CR spec in clear text.
//     The controller bridges it into cloud-credentials-<name> in the per-tenant
//     runtime namespace (BYO: read from the tenant's cloud-credentials Secret;
//     Platform SSO: minted via Dex — wired in a follow-up).
//
// Cleanup is finalizer-driven: the bridged Secret lives on a different cluster
// than the instance, so cross-cluster ownerRefs don't apply.
package application

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/faroshq/provider-infrastructure/apps"
	"github.com/faroshq/provider-infrastructure/kro"
)

// appGVK is read with unstructured so the controller doesn't depend on a
// generated client for the per-template CRD (its schema is authored at runtime
// by the Template controller).
var appGVK = schema.GroupVersionKind{Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Kind: "Application"}

// secretGVK is used to Get/Create Secrets via the controller-runtime client
// (tenant side) and shape the bridged Secret (runtime side).
var secretGVK = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}

var (
	secretGVR    = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	namespaceGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
)

const (
	// finalizer guards the cross-cluster bridged Secret (and, later, the Dex
	// client) — both live outside the instance's cluster, so GC can't reap
	// them via ownerRefs.
	finalizer = "infrastructure.kedge.faros.sh/application-bridge"

	// oidcClientSecretKey is the key the bridged Secret carries and the RGD's
	// oauth2-proxy reads via secretKeyRef. BYO tenants put their client secret
	// under this same key in their cloud-credentials Secret.
	oidcClientSecretKey = "oidc_client_secret"

	// cloudCredentialsSecret is the well-known Secret a tenant maintains in
	// their workspace; BYO mode reads oidcClientSecretKey out of it.
	cloudCredentialsSecret = "cloud-credentials"

	modeNone     = "none"
	modeBYO      = "byo"
	modePlatform = "platform"
)

// Config wires the Application controller.
type Config struct {
	// ProviderConfig is the minted provider kubeconfig's rest.Config (host =
	// provider workspace front-proxy, bearer = provider SA). Drives both the
	// APIExport VW discovery and the per-tenant clients.
	ProviderConfig *rest.Config
	// APIExportName is the provider's APIExport
	// ("infrastructure.providers.kedge.faros.sh").
	APIExportName string
	// BaseDomain is the zone apps are exposed under (KEDGE_APP_BASE_DOMAIN,
	// e.g. "apps.example.com"). Required to compute fqdn.
	BaseDomain string
	// Runtime is a dynamic client for the kro runtime cluster (KRO_KUBECONFIG),
	// where the bridged Secret is written into the per-tenant namespace.
	Runtime dynamic.Interface
	// CredentialsNamespace is the namespace in the tenant workspace the
	// cloud-credentials Secret lives in (default "default").
	CredentialsNamespace string
}

// Controller reconciles Application instances across tenant workspaces.
type Controller struct {
	cfg Config
	mgr mcmanager.Manager
}

// New builds the multicluster manager (APIExport VW) and registers the
// Application reconciler. Call Start to run it.
func New(cfg Config) (*Controller, error) {
	if cfg.ProviderConfig == nil {
		return nil, fmt.Errorf("application: ProviderConfig is required")
	}
	if cfg.APIExportName == "" {
		return nil, fmt.Errorf("application: APIExportName is required")
	}
	if cfg.BaseDomain == "" {
		return nil, fmt.Errorf("application: BaseDomain (KEDGE_APP_BASE_DOMAIN) is required")
	}
	if cfg.Runtime == nil {
		return nil, fmt.Errorf("application: Runtime client (KRO_KUBECONFIG) is required")
	}
	if cfg.CredentialsNamespace == "" {
		cfg.CredentialsNamespace = "default"
	}

	c := &Controller{cfg: cfg}

	// Application + Secret are read unstructured, but the apiexport multicluster
	// provider builds a TYPED cache over APIExportEndpointSlice to discover the
	// virtual-workspace URL — so the kcp apis scheme must be registered or the
	// manager fails with "no kind is registered for the type
	// v1alpha1.APIExportEndpointSlice". Mirrors the kuery engagement controller.
	scheme := runtime.NewScheme()
	utilruntime.Must(apiskcpv1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(scheme))

	provider, err := apiexport.New(cfg.ProviderConfig, cfg.APIExportName, apiexport.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	mgr, err := mcmanager.New(cfg.ProviderConfig, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, fmt.Errorf("creating multicluster manager: %w", err)
	}

	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(appGVK)
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("infra-application").
		For(app).
		Complete(c); err != nil {
		return nil, fmt.Errorf("registering application reconciler: %w", err)
	}

	c.mgr = mgr
	return c, nil
}

// Start runs the multicluster manager (blocking).
func (c *Controller) Start(ctx context.Context) error { return c.mgr.Start(ctx) }

// Reconcile stamps the computed fqdn + credentialsSecretName onto the
// instance and bridges the OIDC client secret onto the runtime cluster.
func (c *Controller) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	tenant := string(req.ClusterName)
	log := klog.FromContext(ctx).WithValues("cluster", tenant, "application", req.Name)

	cl, err := c.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting workspace cluster %s: %w", tenant, err)
	}
	tenantClient := cl.GetClient()

	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(appGVK)
	if err := tenantClient.Get(ctx, req.NamespacedName, app); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Deletion: clean up the cross-cluster bridged Secret, then drop the
	// finalizer so the instance can be removed.
	if !app.GetDeletionTimestamp().IsZero() {
		if controllerutil.ContainsFinalizer(app, finalizer) {
			if err := c.deleteBridgedSecret(ctx, tenant, app.GetName()); err != nil {
				return ctrl.Result{}, fmt.Errorf("cleanup bridged secret: %w", err)
			}
			controllerutil.RemoveFinalizer(app, finalizer)
			if err := tenantClient.Update(ctx, app); err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure our finalizer is present before we create cross-cluster state.
	if !controllerutil.ContainsFinalizer(app, finalizer) {
		controllerutil.AddFinalizer(app, finalizer)
		if err := tenantClient.Update(ctx, app); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{}, nil // our own update re-queues
	}

	// 1. Stamp spec.expose.fqdn + spec.credentialsSecretName (idempotent).
	if err := c.stampSpec(ctx, tenantClient, tenant, app); err != nil {
		return ctrl.Result{}, err
	}

	// 2. Bridge the OIDC client secret onto the runtime cluster.
	mode := nestedString(app, "spec", "oidc", "mode")
	if mode == "" {
		// Matches the template schema default. An instance authored without an
		// oidc block gets the no-gate demo behavior rather than a hard error.
		mode = modeNone
	}
	switch mode {
	case modeNone:
		// No auth gate: the Ingress routes straight to the frontend and the
		// oauth2-proxy resources are excluded from the RGD (includeWhen), so
		// there is no client secret to bridge. Surface the unauthenticated
		// posture on the instance so it's not mistaken for a misconfiguration.
		if err := c.setOIDCCondition(ctx, tenantClient, app, "True", "GateDisabled",
			"oidc.mode=none — no auth gate (demo/dev only); anyone with the URL can reach the app"); err != nil {
			return ctrl.Result{}, err
		}
	case modeBYO:
		if err := c.bridgeBYOSecret(ctx, tenantClient, tenant, app.GetName()); err != nil {
			log.Error(err, "bridging BYO OIDC client secret")
			return ctrl.Result{}, err
		}
	case modePlatform:
		// Platform SSO needs the hub Dex gRPC client-management API, which
		// isn't provisioned yet. Surface that clearly on the instance rather
		// than silently leaving the oauth2-proxy pod stuck on a missing
		// secret. Tracked as a separate Dex-infra epic; use oidc.mode=byo.
		log.Info("oidc.mode=platform is not yet supported; set oidc.mode=byo")
		if err := c.setOIDCCondition(ctx, tenantClient, app, "False", "PlatformSSOUnsupported",
			"oidc.mode=platform is not yet supported (needs the hub Dex gRPC API); use oidc.mode=byo"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil // terminal: nothing to retry until BYO is chosen
	default:
		return ctrl.Result{}, fmt.Errorf("unknown oidc.mode %q", mode)
	}

	return ctrl.Result{}, nil
}

// stampSpec computes the fqdn + bridged-Secret name and writes them onto the
// instance spec if not already set. Idempotent: a no-op once both are stamped.
func (c *Controller) stampSpec(ctx context.Context, tenantClient client.Client, tenant string, app *unstructured.Unstructured) error {
	prefix := nestedString(app, "spec", "expose", "hostnamePrefix")
	curFQDN := nestedString(app, "spec", "expose", "fqdn")
	curSecret := nestedString(app, "spec", "credentialsSecretName")

	fqdn, err := apps.Host(prefix, app.GetName(), tenant, c.cfg.BaseDomain)
	if err != nil {
		return fmt.Errorf("computing fqdn: %w", err)
	}
	wantSecret := kro.CredentialsSecretName(app.GetName())

	if curFQDN == fqdn && curSecret == wantSecret {
		return nil
	}
	if err := unstructured.SetNestedField(app.Object, fqdn, "spec", "expose", "fqdn"); err != nil {
		return fmt.Errorf("set spec.expose.fqdn: %w", err)
	}
	if err := unstructured.SetNestedField(app.Object, wantSecret, "spec", "credentialsSecretName"); err != nil {
		return fmt.Errorf("set spec.credentialsSecretName: %w", err)
	}
	if err := tenantClient.Update(ctx, app); err != nil {
		return fmt.Errorf("stamping spec: %w", err)
	}
	return nil
}

// bridgeBYOSecret reads oidc_client_secret out of the tenant's
// cloud-credentials Secret and writes it into the runtime per-tenant namespace
// as cloud-credentials-<name>, the name the RGD references.
func (c *Controller) bridgeBYOSecret(ctx context.Context, tenantClient client.Client, tenant, name string) error {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(secretGVK)
	key := types.NamespacedName{Namespace: c.cfg.CredentialsNamespace, Name: cloudCredentialsSecret}
	if err := tenantClient.Get(ctx, key, src); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("tenant Secret %s/%s not found — create it with key %q before provisioning a BYO application",
				c.cfg.CredentialsNamespace, cloudCredentialsSecret, oidcClientSecretKey)
		}
		return fmt.Errorf("reading tenant cloud-credentials: %w", err)
	}

	// Secret.data values are base64 strings over the wire; pass them through
	// verbatim into the bridged Secret's data so we never decode the secret
	// into memory as plaintext.
	data, _, _ := unstructured.NestedStringMap(src.Object, "data")
	encoded, ok := data[oidcClientSecretKey]
	if !ok || encoded == "" {
		return fmt.Errorf("tenant Secret %s/%s has no key %q", c.cfg.CredentialsNamespace, cloudCredentialsSecret, oidcClientSecretKey)
	}
	return c.writeBridgedSecret(ctx, tenant, name, map[string]string{oidcClientSecretKey: encoded})
}

// writeBridgedSecret upserts the per-instance Secret in the runtime per-tenant
// namespace. data values are base64-encoded strings (Secret .data wire form).
// Ensures the namespace exists first (the kro fork also creates it, but the
// Secret may race ahead of the first workload reconcile).
func (c *Controller) writeBridgedSecret(ctx context.Context, tenant, name string, data map[string]string) error {
	ns := kro.TenantNamespace(tenant)
	if err := c.ensureNamespace(ctx, ns); err != nil {
		return err
	}

	secretName := kro.CredentialsSecretName(name)
	dataAny := make(map[string]any, len(data))
	for k, v := range data {
		dataAny[k] = v
	}
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      secretName,
			"namespace": ns,
			"labels": map[string]any{
				kro.LabelTenant:    kro.LabelTenantValue(tenant),
				kro.LabelManagedBy: kro.ManagedByValue,
			},
		},
		"type": "Opaque",
		"data": dataAny,
	}}

	existing, err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create bridged secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get bridged secret: %w", err)
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	if _, err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update bridged secret: %w", err)
	}
	return nil
}

// ensureNamespace creates the runtime per-tenant namespace if absent.
func (c *Controller) ensureNamespace(ctx context.Context, ns string) error {
	_, err := c.cfg.Runtime.Resource(namespaceGVR).Get(ctx, ns, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get namespace %s: %w", ns, err)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":   ns,
			"labels": map[string]any{kro.LabelManagedBy: kro.ManagedByValue},
		},
	}}
	if _, err := c.cfg.Runtime.Resource(namespaceGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", ns, err)
	}
	return nil
}

// deleteBridgedSecret removes the per-instance bridged Secret from the runtime
// per-tenant namespace. NotFound is success.
func (c *Controller) deleteBridgedSecret(ctx context.Context, tenant, name string) error {
	ns := kro.TenantNamespace(tenant)
	err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).
		Delete(ctx, kro.CredentialsSecretName(name), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func nestedString(u *unstructured.Unstructured, fields ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, fields...)
	return s
}

// oidcConditionType is the condition the controller owns on an Application to
// report OIDC-gate readiness. Distinct from kro's own conditions (kro owns
// Ready/ResourcesReady), so the two writers don't clash.
const oidcConditionType = "OIDCConfigured"

// setOIDCCondition upserts the OIDCConfigured condition on the instance's
// status (by type), leaving any kro-written conditions intact. Idempotent: a
// no-op when the condition already matches, so it doesn't churn the object.
func (c *Controller) setOIDCCondition(ctx context.Context, tenantClient client.Client, app *unstructured.Unstructured, status, reason, message string) error {
	conds, _, _ := unstructured.NestedSlice(app.Object, "status", "conditions")

	for _, raw := range conds {
		if m, ok := raw.(map[string]any); ok && m["type"] == oidcConditionType {
			if m["status"] == status && m["reason"] == reason && m["message"] == message {
				return nil // already current
			}
		}
	}

	next := make([]any, 0, len(conds)+1)
	for _, raw := range conds {
		if m, ok := raw.(map[string]any); ok && m["type"] == oidcConditionType {
			continue // drop the stale one; re-added below
		}
		next = append(next, raw)
	}
	next = append(next, map[string]any{
		"type":               oidcConditionType,
		"status":             status,
		"reason":             reason,
		"message":            message,
		"lastTransitionTime": metav1.Now().UTC().Format(time.RFC3339),
		"observedGeneration": app.GetGeneration(),
	})
	if err := unstructured.SetNestedSlice(app.Object, next, "status", "conditions"); err != nil {
		return fmt.Errorf("set status.conditions: %w", err)
	}
	if err := tenantClient.Status().Update(ctx, app); err != nil {
		return fmt.Errorf("updating OIDC condition: %w", err)
	}
	return nil
}
