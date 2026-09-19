// Copyright 2026 The Railgrid Authors.
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
//     railgridCluster, credentialsSecretName), exactly the fields the retired
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
// Everything on the far side of the seam is watched, not polled: the
// runtime CRs (one watch per template GVR, registered as templates become
// Ready), the Templates in the provider workspace, and the tenant Secrets
// the bridge reads (see watch.go). Nothing is re-reconciled on a timer.
//
// Cleanup is finalizer-driven: the runtime CR and the bridged Secrets live
// on a different cluster than the Instance, so cross-cluster ownerRefs
// don't apply. status.runtimeRef records where the runtime CR was written
// so deletion still works when the Template has meanwhile been retired.
package instance

import (
	"context"
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
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/railgrid/provider-sdk/apiexportprovider"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	"github.com/railgrid/provider-infrastructure/instancespec"
	"github.com/railgrid/provider-infrastructure/networkpolicy"
)

// instanceGVK is the flattened tenant-facing kind this controller watches.
// Read unstructured: status mirroring carries backend-projected fields the
// typed struct deliberately doesn't model.
var instanceGVK = schema.GroupVersionKind{
	Group:   infrav1alpha1.GroupName,
	Version: infrav1alpha1.Version,
	Kind:    "Instance",
}

const (
	// finalizer guards the cross-cluster state an Instance owns: the runtime
	// kro CR and any bridged Secrets. Added on first reconcile of every
	// Instance — unlike the retired application controller, every instance
	// now owns runtime state.
	finalizer = infrav1alpha1.FinalizerInstanceRuntime
)

// Config wires the Instance controller.
type Config struct {
	// ProviderConfig is the provider kubeconfig's rest.Config (host =
	// provider workspace). Drives the APIExport VW discovery, the per-tenant
	// clients, and the Template catalog watch.
	ProviderConfig *rest.Config
	// APIExportName is the provider's APIExport
	// ("infrastructure.providers.railgrid.ai").
	APIExportName string
	// BaseDomain is the zone apps are exposed under (RAILGRID_APP_BASE_DOMAIN,
	// e.g. "apps.example.com"). Optional: when empty, instances of
	// publishable templates report a Valid=False condition when they ask to
	// be exposed; internal templates work normally.
	BaseDomain string
	// Runtime is a dynamic client for the kro runtime cluster, where the
	// per-template CRs, their namespaces, and the bridged Secrets live.
	Runtime dynamic.Interface
	// RuntimeConfig is the rest.Config Runtime was built from. The
	// controller runs a cache over it to watch the per-template runtime CRs.
	RuntimeConfig *rest.Config
	// CredentialsNamespace is the namespace in the tenant workspace the
	// cloud-credentials Secret lives in (default "default").
	CredentialsNamespace string
	// CodingSandboxEnabled gates the platform-owned universal coding sandbox
	// even when a manually applied Template bypassed catalog seeding.
	CodingSandboxEnabled bool
	// NetworkPolicy configures the ingress NetworkPolicy that isolates each
	// tenant runtime namespace from other workspaces (networkpolicy.go).
	// The zero value is disabled.
	NetworkPolicy networkpolicy.Config
}

// Controller reconciles Instances across tenant workspaces.
type Controller struct {
	cfg Config
	mgr mcmanager.Manager
	// templates reads Templates from the provider-workspace informer cache
	// (the same cache the Template watch feeds).
	templates client.Reader

	// index is the cluster → Instance → Template index the Template and
	// Secret mappers query (watch.go).
	index *instanceIndex
	// runtimeWatches registers the per-GVR runtime-cluster watches.
	runtimeWatches *runtimeWatchRegistrar
	// runtimeCache is the cache the per-GVR watches are registered on; the
	// Template controller shares it for its RGD watch (RuntimeCache).
	runtimeCache cache.Cache

	// networkPolicySynced records, per runtime namespace name, which namespace
	// UID its isolation policy was last converged in and when
	// (networkPolicySync), so the warm reconcile path skips the extra API
	// call. Rebuilt with the controller every leadership term.
	networkPolicySynced sync.Map

	// contracts caches the compiled values contract per Template, keyed by
	// name and invalidated by resourceVersion (and dropped outright on a
	// Template event, see mapTemplate). Compilation (structural schema + CEL
	// programs) is expensive relative to a reconcile.
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
// Instance reconciler with its event sources. Call Start to run it.
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
	if cfg.RuntimeConfig == nil {
		return nil, fmt.Errorf("instance: RuntimeConfig is required")
	}
	if cfg.CredentialsNamespace == "" {
		cfg.CredentialsNamespace = "default"
	}
	if err := cfg.NetworkPolicy.Validate(); err != nil {
		return nil, fmt.Errorf("instance: %w", err)
	}

	c := &Controller{cfg: cfg, index: newInstanceIndex(), contracts: map[string]*cachedContract{}}

	// Instances + Secrets are read unstructured, but the apiexport
	// multicluster provider builds a TYPED cache over APIExportEndpointSlice
	// to discover the virtual-workspace URL — so the kcp apis scheme must be
	// registered or the manager fails with "no kind is registered for the
	// type v1alpha1.APIExportEndpointSlice". Templates are watched typed on
	// the local (provider workspace) cluster, so the infra scheme joins it.
	scheme := runtime.NewScheme()
	utilruntime.Must(apiskcpv1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(scheme))
	utilruntime.Must(infrav1alpha1.AddToScheme(scheme))

	provider, err := apiexportprovider.New(cfg.ProviderConfig, cfg.APIExportName, apiexportprovider.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	skipNameValidation := true
	mgr, err := mcmanager.New(cfg.ProviderConfig, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		// The serve binary rebuilds this controller for every leadership term,
		// and controller names register process-globally.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return nil, fmt.Errorf("creating multicluster manager: %w", err)
	}

	// The runtime cluster is a second cache, started with the manager. Its
	// informers are per-template GVRs registered on demand (runtimeWatches),
	// all unstructured, so it needs no scheme of its own.
	runtimeCluster, err := cluster.New(cfg.RuntimeConfig, func(o *cluster.Options) {
		o.Scheme = runtime.NewScheme()
	})
	if err != nil {
		return nil, fmt.Errorf("creating runtime cluster: %w", err)
	}
	if err := mgr.GetLocalManager().Add(runtimeCluster); err != nil {
		return nil, fmt.Errorf("adding runtime cluster to manager: %w", err)
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(instanceGVK)
	secret := &unstructured.Unstructured{}
	secret.SetGroupVersionKind(secretGVK)
	instanceController, err := mcbuilder.ControllerManagedBy(mgr).
		Named("infra-instance").
		For(obj).
		// Templates live in the provider workspace — the manager's local
		// cluster — not in the tenant clusters the provider engages.
		Watches(&infrav1alpha1.Template{}, c.templateHandler,
			mcbuilder.WithEngageWithLocalCluster(true),
			mcbuilder.WithEngageWithProviderClusters(false)).
		// Tenant Secrets come through the virtual workspace (the APIExport
		// claims get/list/watch on secrets), one watch per engaged cluster.
		Watches(secret, c.secretHandler, mcbuilder.WithPredicates(c.secretPredicate())).
		Build(&reconciler{c: c})
	if err != nil {
		return nil, fmt.Errorf("registering instance reconciler: %w", err)
	}

	c.mgr = mgr
	c.templates = mgr.GetLocalManager().GetClient()
	c.runtimeCache = runtimeCluster.GetCache()
	c.runtimeWatches = newRuntimeWatchRegistrar(instanceController, c.runtimeCache)
	return c, nil
}

// LocalManager is the plain controller-runtime manager for the multicluster
// manager's local cluster — the provider's own workspace, where Templates
// live. Callers register provider-workspace controllers (the Template
// reconciler) on it instead of standing up a second manager and a second
// lease for the same cluster.
func (c *Controller) LocalManager() manager.Manager { return c.mgr.GetLocalManager() }

// RuntimeCache is the cache over the kro runtime cluster this controller runs
// (started with the manager). The Template controller watches the RGDs the kro
// backend authors there through the same cache.
func (c *Controller) RuntimeCache() cache.Cache { return c.runtimeCache }

// templateHandler enqueues every Instance of the Template that changed. The
// cluster argument is the local cluster and irrelevant: the mapper spans all
// engaged tenant clusters through the index.
func (c *Controller) templateHandler(_ multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
	return handler.TypedEnqueueRequestsFromMapFunc[client.Object, mcreconcile.Request](c.mapTemplate)
}

// secretHandler enqueues the Instance(s) of the engaged tenant cluster that
// bridge the Secret that changed.
func (c *Controller) secretHandler(clusterName multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
	return handler.TypedEnqueueRequestsFromMapFunc[client.Object, mcreconcile.Request](func(_ context.Context, obj client.Object) []mcreconcile.Request {
		return c.mapSecret(clusterName, obj)
	})
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
			c.index.remove(req.ClusterName, req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	templateName, _, _ := unstructured.NestedString(inst.Object, "spec", "template")
	c.index.set(req.ClusterName, req.NamespacedName, templateName,
		secretRefName(inst, "imagePullSecretRef"), secretRefName(inst, "oidcBridgeSecretRef"))

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
	if templateName == infrav1alpha1.UniversalCodingSandboxTemplateName && !c.cfg.CodingSandboxEnabled {
		return c.failValidation(ctx, tenantClient, inst, infrav1alpha1.ReasonCodingSandboxDisabled,
			"the universal coding sandbox is disabled by provider configuration")
	}
	if reason, due := lifecycleDue(time.Now(), inst.GetCreationTimestamp(), tmpl.Spec.Development, nil); due {
		if err := tenantClient.Delete(ctx, inst); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete expired sandbox: %w", err)
		}
		log.Info("sandbox lifecycle limit reached", "reason", reason)
		return ctrl.Result{}, nil
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

	// Bridge the Secrets this instance REFERENCES: the registry pull Secret
	// named by spec.imagePullSecretRef, and the BYO OIDC client secret named
	// by spec.oidcBridgeSecretRef when the gate says so. A reference that
	// names nothing is reported on the Instance, never guessed at.
	bridgedCond, err := c.bridgeSecrets(ctx, tenantClient, tenant, inst, proceed.bridgeOIDC)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Materialize / converge the runtime CR and mirror its status.
	stampedValues, _, _ := unstructured.NestedMap(inst.Object, "spec", "values")
	stampedValues = runtime.DeepCopyJSON(stampedValues)
	if stampedValues == nil {
		stampedValues = map[string]any{}
	}
	currentRuntime, _ := c.currentRuntime(ctx, tenant, tmpl, inst)
	if tmpl.Spec.Development != nil {
		stampedValues[infrav1alpha1.RailgridNetworkPhaseField] = desiredNetworkPhase(currentRuntime)
	}
	runtimeObj, err := c.syncRuntime(ctx, tenant, tmpl, inst, stampedValues)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("syncing runtime instance: %w", err)
	}
	if reason, due := lifecycleDue(time.Now(), inst.GetCreationTimestamp(), tmpl.Spec.Development, runtimeObj); due {
		if err := tenantClient.Delete(ctx, inst); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete idle sandbox: %w", err)
		}
		log.Info("sandbox lifecycle limit reached", "reason", reason)
		return ctrl.Result{}, nil
	}

	ready, err := c.mirrorStatus(ctx, tenantClient, inst, tmpl, runtimeObj, validCondition(metav1.ConditionTrue, infrav1alpha1.ReasonReady, ""), oidcCond, bridgedCond)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("mirroring status: %w", err)
	}

	log.V(2).Info("instance reconciled", "template", templateName, "ready", ready)
	return ctrl.Result{RequeueAfter: instanceRequeueAfter(time.Now(), inst.GetCreationTimestamp(), tmpl, runtimeObj)}, nil
}

// desiredNetworkPhase chooses the next controller-owned network phase for a
// development Instance. The setup -> runtime transition is deliberately
// one-way: a runtime object that has already selected the runtime phase keeps
// that phase while its new generation rolls out. Reverting it to setup during
// that window causes the runtime graph to oscillate between egress policies
// and can prevent the data plane from ever becoming ready.
//
// A missing object, or an object still in setup, remains in setup until its
// current generation is fully ready. The stricter readiness predicate is
// important here because a Ready condition from an older generation must not
// authorize the phase transition.
func desiredNetworkPhase(runtimeObj *unstructured.Unstructured) string {
	if runtimeObj == nil {
		return infrav1alpha1.RailgridNetworkPhaseSetup
	}

	phase, found, err := unstructured.NestedString(runtimeObj.Object, "spec", infrav1alpha1.RailgridNetworkPhaseField)
	if err == nil && found && phase == infrav1alpha1.RailgridNetworkPhaseRuntime {
		return infrav1alpha1.RailgridNetworkPhaseRuntime
	}
	// Keep the coarse readiness check as a cheap guard; the generation-aware
	// predicate is the authority for this transition.
	if runtimeReady(runtimeObj) && runtimeReadyForNetwork(runtimeObj) {
		return infrav1alpha1.RailgridNetworkPhaseRuntime
	}
	return infrav1alpha1.RailgridNetworkPhaseSetup
}

// instanceRequeueAfter returns the only RequeueAfter this reconciler asks for:
// the exact lifecycle deadline of a development Instance (idle timeout, max
// lifetime), derived from the Template and the Instance's own timestamps. That
// is the "waking at a computed lifecycle deadline" carve-out of
// docs/provider-connectivity-contract.md § "Pillar 1 carve-outs" — not a
// resync. An Instance with no deadline gets 0: nothing to wake up for, because
// everything else it depends on is watched (the runtime CR through the
// runtime-cluster watch, its Template and bridged Secrets through watch.go).
func instanceRequeueAfter(now time.Time, created metav1.Time, tmpl *infrav1alpha1.Template, runtimeObj *unstructured.Unstructured) time.Duration {
	var development *infrav1alpha1.TemplateDevelopment
	if tmpl != nil {
		development = tmpl.Spec.Development
	}
	return lifecycleRequeueAfter(now, created, development, runtimeObj)
}

// failValidation reports a terminal validation outcome on the Instance.
// Terminal means terminal: nothing changes until the tenant edits the Instance
// or an author fixes the Template, and both arrive as events (the Instance's
// own watch, mapTemplate), so there is nothing to requeue for. The runtime CR
// — if one exists from a previously valid spec — is deliberately left alone:
// last-good keeps running.
func (c *Controller) failValidation(ctx context.Context, tenantClient client.Client, inst *unstructured.Unstructured, reason, message string) (ctrl.Result, error) {
	if _, err := c.mirrorStatus(ctx, tenantClient, inst, nil, nil, validCondition(metav1.ConditionFalse, reason, message), nil); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// resolveTemplate reads the Template from the provider-workspace cache and
// returns it with its compiled values contract, cached by resourceVersion.
// A nil contract with nil error means the Template exists but its schema
// does not compile. A Ready Template also gets its runtime GVR watched.
func (c *Controller) resolveTemplate(ctx context.Context, name string) (*infrav1alpha1.Template, *instancespec.Contract, error) {
	if name == "" {
		return nil, nil, apierrors.NewNotFound(infrav1alpha1.Resource("templates"), name)
	}
	tmpl := &infrav1alpha1.Template{}
	if err := c.templates.Get(ctx, client.ObjectKey{Name: name}, tmpl); err != nil {
		return nil, nil, err
	}
	if c.runtimeWatches != nil && templateReady(tmpl) {
		if _, err := c.runtimeWatches.ensure(ctx, runtimeGVRFor(tmpl), tmpl.Spec.InstanceCRD.Kind); err != nil {
			return nil, nil, err
		}
	}

	c.mu.Lock()
	cached, ok := c.contracts[name]
	c.mu.Unlock()
	if ok && cached.resourceVersion == tmpl.GetResourceVersion() {
		if cached.err != nil {
			return cached.template, nil, nil
		}
		return cached.template, cached.contract, nil
	}

	contract, cerr := instancespec.NewContract(tmpl)
	c.mu.Lock()
	c.contracts[name] = &cachedContract{
		resourceVersion: tmpl.GetResourceVersion(),
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
