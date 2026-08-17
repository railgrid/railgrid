// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package instance reconciles the flattened Instance kind across every
// tenant workspace that enabled the infrastructure provider, through the
// provider's APIExport virtual workspace.
//
// An Instance is the tenant-facing half of a template instance; the
// per-template kro CR on the runtime cluster is the backend half. This
// controller is the seam between them:
//
//   - validate spec.values against the Template's schema (structural,
//     defaults, CEL) — the work the retired per-template CRDs did at
//     admission — reporting Valid=False instead of syncing anything invalid;
//   - stamp the platform-computed fields into spec.values (expose.fqdn,
//     farosCluster, credentialsSecretName), exactly the fields the retired
//     application controller stamped, with the per-kind treatment now
//     derived from the Template's schema instead of a hardcoded kind table;
//   - bridge cross-cluster Secrets (BYO OIDC client secret, registry pull
//     secret) into the instance's runtime namespace;
//   - materialize the per-template kro CR (Template.spec.instanceCRD kind,
//     Namespaced) in the tenant's runtime namespace and keep its spec
//     converged on the stamped values;
//   - mirror the runtime CR's status (written by kro from the RGD's
//     statusMapping) back onto the Instance, merged with the
//     provider-owned conditions.
//
// Cleanup is finalizer-driven: the runtime CR and the bridged Secrets live
// on a different cluster than the Instance, so cross-cluster ownerRefs
// don't apply. status.runtimeRef records where the runtime CR was written
// so deletion still works when the Template has meanwhile been retired.
package instance

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/instancespec"
)

// instanceGVK is the flattened tenant-facing kind this controller watches.
// Read unstructured: status mirroring carries backend-projected fields the
// typed struct deliberately doesn't model.
var instanceGVK = schema.GroupVersionKind{
	Group:   infrav1alpha1.GroupName,
	Version: infrav1alpha1.Version,
	Kind:    "Instance",
}

// templatesGVR is the catalog resource in the provider's own workspace.
var templatesGVR = schema.GroupVersionResource{
	Group:    infrav1alpha1.GroupName,
	Version:  infrav1alpha1.Version,
	Resource: "templates",
}

const (
	// finalizer guards the cross-cluster state an Instance owns: the runtime
	// kro CR and any bridged Secrets. Added on first reconcile of every
	// Instance — unlike the retired application controller, every instance
	// now owns runtime state.
	finalizer = infrav1alpha1.FinalizerInstanceRuntime

	// requeueNotReady / requeueReady drive the status-mirror poll. The
	// runtime cluster isn't watched (kro's own reconcile latency dominates
	// anyway), so status freshness comes from these requeues.
	requeueNotReady = 10 * time.Second
	requeueReady    = 60 * time.Second
)

// Config wires the Instance controller.
type Config struct {
	// ProviderConfig is the provider kubeconfig's rest.Config (host =
	// provider workspace). Drives the APIExport VW discovery, the per-tenant
	// clients, and the Template catalog reads.
	ProviderConfig *rest.Config
	// APIExportName is the provider's APIExport
	// ("infrastructure.providers.faros.sh").
	APIExportName string
	// BaseDomain is the zone apps are exposed under (FAROS_APP_BASE_DOMAIN,
	// e.g. "apps.example.com"). Optional: when empty, instances of
	// publishable templates report a Valid=False condition when they ask to
	// be exposed; internal templates work normally.
	BaseDomain string
	// Runtime is a dynamic client for the kro runtime cluster, where the
	// per-template CRs, their namespaces, and the bridged Secrets live.
	Runtime dynamic.Interface
	// CredentialsNamespace is the namespace in the tenant workspace the
	// cloud-credentials Secret lives in (default "default").
	CredentialsNamespace string
}

// Controller reconciles Instances across tenant workspaces.
type Controller struct {
	cfg       Config
	mgr       mcmanager.Manager
	templates dynamic.Interface

	// contracts caches the compiled values contract per Template, keyed by
	// name and invalidated by resourceVersion. Compilation (structural
	// schema + CEL programs) is expensive relative to a reconcile.
	mu        sync.Mutex
	contracts map[string]*cachedContract
}

type cachedContract struct {
	resourceVersion string
	template        *infrav1alpha1.Template
	contract        *instancespec.Contract
	err             error
}

// New builds the multicluster manager (APIExport VW) and registers the
// Instance reconciler. Call Start to run it.
func New(cfg Config) (*Controller, error) {
	if cfg.ProviderConfig == nil {
		return nil, fmt.Errorf("instance: ProviderConfig is required")
	}
	if cfg.APIExportName == "" {
		return nil, fmt.Errorf("instance: APIExportName is required")
	}
	if cfg.Runtime == nil {
		return nil, fmt.Errorf("instance: Runtime client is required")
	}
	if cfg.CredentialsNamespace == "" {
		cfg.CredentialsNamespace = "default"
	}

	templates, err := dynamic.NewForConfig(cfg.ProviderConfig)
	if err != nil {
		return nil, fmt.Errorf("instance: templates client: %w", err)
	}

	c := &Controller{cfg: cfg, templates: templates, contracts: map[string]*cachedContract{}}

	// Instances + Secrets are read unstructured, but the apiexport
	// multicluster provider builds a TYPED cache over APIExportEndpointSlice
	// to discover the virtual-workspace URL — so the kcp apis scheme must be
	// registered or the manager fails with "no kind is registered for the
	// type v1alpha1.APIExportEndpointSlice".
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

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(instanceGVK)
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("infra-instance").
		For(obj).
		Complete(&reconciler{c: c}); err != nil {
		return nil, fmt.Errorf("registering instance reconciler: %w", err)
	}

	c.mgr = mgr
	return c, nil
}

// Start runs the multicluster manager (blocking).
func (c *Controller) Start(ctx context.Context) error { return c.mgr.Start(ctx) }

type reconciler struct {
	c *Controller
}

// Reconcile converges one Instance: validate → stamp → bridge → sync
// runtime CR → mirror status.
func (r *reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	c := r.c
	tenant := string(req.ClusterName)
	log := klog.FromContext(ctx).WithValues("cluster", tenant, "instance", req.Name)

	cl, err := c.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting workspace cluster %s: %w", tenant, err)
	}
	tenantClient := cl.GetClient()

	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(instanceGVK)
	if err := tenantClient.Get(ctx, req.NamespacedName, inst); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !inst.GetDeletionTimestamp().IsZero() {
		return c.finalize(ctx, tenantClient, tenant, inst)
	}

	// Every instance owns runtime-cluster state, so every instance carries
	// the finalizer from its first reconcile.
	if !controllerutil.ContainsFinalizer(inst, finalizer) {
		controllerutil.AddFinalizer(inst, finalizer)
		if err := tenantClient.Update(ctx, inst); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{}, nil // our own update re-queues
	}

	templateName, _, _ := unstructured.NestedString(inst.Object, "spec", "template")
	tmpl, contract, err := c.resolveTemplate(ctx, templateName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return c.failValidation(ctx, tenantClient, inst, infrav1alpha1.ReasonTemplateNotFound,
				fmt.Sprintf("template %q is not in the catalog", templateName))
		}
		return ctrl.Result{}, fmt.Errorf("resolving template %q: %w", templateName, err)
	}
	if contract == nil {
		// The Template exists but its schema doesn't compile — the Template
		// controller reports SchemaValid=False on it; instances park here.
		return c.failValidation(ctx, tenantClient, inst, infrav1alpha1.ReasonInvalidValues,
			fmt.Sprintf("template %q has an invalid values schema; see the Template's SchemaValid condition", templateName))
	}

	values, _, _ := unstructured.NestedMap(inst.Object, "spec", "values")
	if _, errs := contract.ValidateAndDefault(ctx, values); len(errs) != 0 {
		return c.failValidation(ctx, tenantClient, inst, infrav1alpha1.ReasonInvalidValues, errs.ToAggregate().Error())
	}

	traits, err := traitsFor(tmpl)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("deriving template traits: %w", err)
	}

	// Exposure gate + platform stamps. A terminal gate outcome (e.g. a
	// gate-required workload asked to publish without OIDC) reports its
	// condition and skips stamping/bridging, but the runtime CR still
	// converges on whatever the values say — same behavior the retired
	// application controller had.
	oidcCond, proceed, err := c.applyExposure(ctx, tenantClient, tenant, inst, traits)
	if err != nil {
		return ctrl.Result{}, err
	}
	if proceed.stampedSpec {
		return ctrl.Result{}, nil // our own spec update re-queues with fresh values
	}

	// Bridge the registry pull Secret (private production images) for any
	// promoted instance, and the BYO OIDC client secret when the gate says so.
	if err := c.bridgeSecrets(ctx, tenantClient, tenant, inst, proceed.bridgeOIDC); err != nil {
		return ctrl.Result{}, err
	}

	// Materialize / converge the runtime CR and mirror its status.
	stampedValues, _, _ := unstructured.NestedMap(inst.Object, "spec", "values")
	runtimeObj, err := c.syncRuntime(ctx, tenant, tmpl, inst, stampedValues)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("syncing runtime instance: %w", err)
	}

	ready, err := c.mirrorStatus(ctx, tenantClient, inst, tmpl, runtimeObj, validCondition(metav1.ConditionTrue, infrav1alpha1.ReasonReady, ""), oidcCond)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("mirroring status: %w", err)
	}

	log.V(2).Info("instance reconciled", "template", templateName, "ready", ready)
	if ready {
		return ctrl.Result{RequeueAfter: requeueReady}, nil
	}
	return ctrl.Result{RequeueAfter: requeueNotReady}, nil
}

// failValidation reports a terminal validation outcome on the Instance and
// schedules a slow retry (a Template fix must be picked up without a spec
// edit). The runtime CR — if one exists from a previously valid spec — is
// deliberately left alone: last-good keeps running.
func (c *Controller) failValidation(ctx context.Context, tenantClient client.Client, inst *unstructured.Unstructured, reason, message string) (ctrl.Result, error) {
	if _, err := c.mirrorStatus(ctx, tenantClient, inst, nil, nil, validCondition(metav1.ConditionFalse, reason, message), nil); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueReady}, nil
}

// resolveTemplate fetches the Template and its compiled values contract,
// cached by resourceVersion. A nil contract with nil error means the
// Template exists but its schema does not compile.
func (c *Controller) resolveTemplate(ctx context.Context, name string) (*infrav1alpha1.Template, *instancespec.Contract, error) {
	if name == "" {
		return nil, nil, apierrors.NewNotFound(infrav1alpha1.Resource("templates"), name)
	}
	obj, err := c.templates.Resource(templatesGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	c.mu.Lock()
	cached, ok := c.contracts[name]
	c.mu.Unlock()
	if ok && cached.resourceVersion == obj.GetResourceVersion() {
		if cached.err != nil {
			return cached.template, nil, nil
		}
		return cached.template, cached.contract, nil
	}

	tmpl := &infrav1alpha1.Template{}
	raw, err := obj.MarshalJSON()
	if err != nil {
		return nil, nil, fmt.Errorf("marshal template: %w", err)
	}
	if err := json.Unmarshal(raw, tmpl); err != nil {
		return nil, nil, fmt.Errorf("decode template: %w", err)
	}

	contract, cerr := instancespec.NewContract(tmpl)
	c.mu.Lock()
	c.contracts[name] = &cachedContract{
		resourceVersion: obj.GetResourceVersion(),
		template:        tmpl,
		contract:        contract,
		err:             cerr,
	}
	c.mu.Unlock()
	if cerr != nil {
		return tmpl, nil, nil
	}
	return tmpl, contract, nil
}
