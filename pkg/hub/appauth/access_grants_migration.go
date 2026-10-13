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

package appauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sync"

	"github.com/kcp-dev/sdk/apis/core"
	"github.com/railgrid/provider-sdk/apiexportprovider"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const (
	appAccessMigrationCatalogController = "appauth-grant-migration-catalog"
	appAccessMigrationEndpointSlice     = "infrastructure"
	appAccessMigrationExportName        = "infrastructure.providers.railgrid.ai"
)

var (
	appAccessMigrationInstanceGVK = schema.GroupVersionKind{
		Group: "infrastructure.railgrid.ai", Version: "v1alpha1", Kind: "Instance",
	}
	appAccessMigrationInstanceGVR = schema.GroupVersionResource{
		Group: "infrastructure.railgrid.ai", Version: "v1alpha1", Resource: "instances",
	}
	appAccessMigrationLogicalClusterGVR = schema.GroupVersionResource{
		Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
	}
	appAccessMigrationAPIExportGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports",
	}
	appAccessMigrationEndpointSliceGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
	}
)

// SetupAppAccessGrantMigrationWithManager registers the leader-scoped source
// controller that discovers platform and organization-owned Infrastructure
// exports from their CatalogEntries. sourceManager must watch the
// providers.railgrid.ai APIExport. The per-export Instance managers are
// derived only from verified provider workspaces and endpoint slices; their
// startup informer lists replay existing Instances after a leader restart.
//
// leaderCtx owns every child manager. The shutdown runnable cancels and drains
// those managers before the source manager returns, so no migration writer
// outlives the hub's leader term.
func SetupAppAccessGrantMigrationWithManager(sourceManager mcmanager.Manager, leaderCtx context.Context, kcpConfig *rest.Config, scheme *runtime.Scheme, handler *AppAccessGrantHandler) error {
	if sourceManager == nil || leaderCtx == nil || kcpConfig == nil || scheme == nil || handler == nil {
		return fmt.Errorf("appauth: grant migration requires the CatalogEntry manager, leader context, kcp config, scheme, and handler")
	}
	migrationSource := &appAccessGrantMigrationSource{
		manager:   sourceManager,
		leaderCtx: leaderCtx,
		kcpConfig: rest.CopyConfig(kcpConfig),
		scheme:    scheme,
		handler:   handler,
		children:  map[appAccessMigrationScopeKey]*appAccessMigrationChild{},
		failures:  &appAccessMigrationFailureSource{pending: map[appAccessMigrationScopeKey]struct{}{}},
	}
	migrationSource.childManagerFactory = func(descriptor appAccessMigrationDescriptor) (migrationManager, error) {
		return migrationSource.newChildManager(descriptor)
	}
	if err := builder.ControllerManagedBy(sourceManager).
		Named(appAccessMigrationCatalogController).
		For(&providersv1alpha1.CatalogEntry{}).
		WatchesRawSource(migrationSource.failures).
		Complete(migrationSource); err != nil {
		return fmt.Errorf("appauth: registering grant migration CatalogEntry controller: %w", err)
	}
	if err := sourceManager.GetLocalManager().Add(&appAccessMigrationDrainRunnable{source: migrationSource}); err != nil {
		return fmt.Errorf("appauth: registering grant migration shutdown drain: %w", err)
	}
	return nil
}

type appAccessGrantMigrationSource struct {
	manager   mcmanager.Manager
	leaderCtx context.Context
	kcpConfig *rest.Config
	scheme    *runtime.Scheme
	handler   *AppAccessGrantHandler

	mu                  sync.Mutex
	children            map[appAccessMigrationScopeKey]*appAccessMigrationChild
	childManagerFactory func(appAccessMigrationDescriptor) (migrationManager, error)
	failures            *appAccessMigrationFailureSource
}

type appAccessMigrationScopeKey struct {
	cluster string
	name    string
}

type appAccessMigrationDescriptor struct {
	key        appAccessMigrationScopeKey
	path       string
	exportName string
}

type appAccessMigrationChild struct {
	descriptor appAccessMigrationDescriptor
	manager    migrationManager
	cancel     context.CancelFunc
	done       chan struct{}
}

type migrationManager interface {
	Start(context.Context) error
}

// appAccessMigrationFailureSource requeues a CatalogEntry after a child
// manager exits unexpectedly. Pending keys bridge a fast child failure before
// controller-runtime starts this raw source.
type appAccessMigrationFailureSource struct {
	mu      sync.Mutex
	queue   workqueue.TypedRateLimitingInterface[mcreconcile.Request]
	pending map[appAccessMigrationScopeKey]struct{}
}

func (s *appAccessMigrationFailureSource) Start(ctx context.Context, queue workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
	s.mu.Lock()
	s.queue = queue
	pending := make([]appAccessMigrationScopeKey, 0, len(s.pending))
	for key := range s.pending {
		pending = append(pending, key)
		delete(s.pending, key)
	}
	s.mu.Unlock()
	for _, key := range pending {
		s.enqueueTo(queue, key)
	}
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if s.queue == queue {
			s.queue = nil
		}
		s.mu.Unlock()
	}()
	return nil
}

func (s *appAccessMigrationFailureSource) enqueue(key appAccessMigrationScopeKey) {
	s.mu.Lock()
	queue := s.queue
	if queue == nil {
		s.pending[key] = struct{}{}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.enqueueTo(queue, key)
}

func (s *appAccessMigrationFailureSource) enqueueTo(queue workqueue.TypedRateLimitingInterface[mcreconcile.Request], key appAccessMigrationScopeKey) {
	queue.AddRateLimited(mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: client.ObjectKey{Name: key.name}},
		ClusterName: multicluster.ClusterName(key.cluster),
	})
}

func (r *appAccessGrantMigrationSource) Reconcile(ctx context.Context, request mcreconcile.Request) (ctrl.Result, error) {
	key := appAccessMigrationScopeKey{cluster: string(request.ClusterName), name: request.Name}
	if !segmentRE.MatchString(key.cluster) || !segmentRE.MatchString(key.name) {
		return ctrl.Result{}, nil
	}

	clusterClient, err := r.manager.GetCluster(ctx, request.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting CatalogEntry cluster %s: %w", request.ClusterName, err)
	}
	var entry providersv1alpha1.CatalogEntry
	if err := clusterClient.GetClient().Get(ctx, request.NamespacedName, &entry); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.stopChild(ctx, key)
		}
		return ctrl.Result{}, err
	}
	if entry.DeletionTimestamp != nil || !appAccessMigrationCatalogEntry(&entry) {
		return ctrl.Result{}, r.stopChild(ctx, key)
	}

	descriptor, eligible, err := r.verifiedDescriptor(ctx, key, &entry)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !eligible {
		return ctrl.Result{}, r.stopChild(ctx, key)
	}
	if err := r.ensureChild(descriptor); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func appAccessMigrationCatalogEntry(entry *providersv1alpha1.CatalogEntry) bool {
	if entry == nil || entry.Name != "infrastructure" || entry.Spec.Export == nil ||
		entry.Spec.Export.Name != appAccessMigrationExportName {
		return false
	}
	for _, resource := range entry.Spec.Export.Resources {
		gv, err := schema.ParseGroupVersion(resource.APIVersion)
		if err == nil && resource.Name == "instances" && resource.Kind == "Instance" && gv.Group == appAccessMigrationInstanceGVK.Group {
			return true
		}
	}
	return false
}

func appAccessMigrationProviderPath(path, providerName string) bool {
	if path == kcppaths.ProviderPath(providerName) {
		return true
	}
	orgUUID, name, ok := kcppaths.SplitOrgProviderPath(path)
	return ok && name == providerName && segmentRE.MatchString(orgUUID)
}

func (r *appAccessGrantMigrationSource) verifiedDescriptor(ctx context.Context, key appAccessMigrationScopeKey, entry *providersv1alpha1.CatalogEntry) (appAccessMigrationDescriptor, bool, error) {
	path, err := r.providerWorkspacePath(ctx, key.cluster)
	if err != nil {
		return appAccessMigrationDescriptor{}, false, err
	}
	if !appAccessMigrationProviderPath(path, entry.Name) {
		return appAccessMigrationDescriptor{}, false, nil
	}

	providerConfig := rest.CopyConfig(r.kcpConfig)
	providerConfig.Host = apiurl.KCPClusterURL(providerConfig.Host, path)
	providerClient, err := dynamic.NewForConfig(providerConfig)
	if err != nil {
		return appAccessMigrationDescriptor{}, false, fmt.Errorf("creating client for Infrastructure export workspace %q: %w", path, err)
	}
	export, err := providerClient.Resource(appAccessMigrationAPIExportGVR).Get(ctx, entry.Spec.Export.Name, metav1.GetOptions{})
	if err != nil {
		return appAccessMigrationDescriptor{}, false, fmt.Errorf("reading Infrastructure APIExport %q in %q: %w", entry.Spec.Export.Name, path, err)
	}
	if !apiExportServesInfrastructureInstances(export) {
		return appAccessMigrationDescriptor{}, false, fmt.Errorf("infrastructure APIExport %q in %q does not serve infrastructure Instances", entry.Spec.Export.Name, path)
	}
	slice, err := providerClient.Resource(appAccessMigrationEndpointSliceGVR).Get(ctx, appAccessMigrationEndpointSlice, metav1.GetOptions{})
	if err != nil {
		return appAccessMigrationDescriptor{}, false, fmt.Errorf("reading Infrastructure APIExportEndpointSlice in %q: %w", path, err)
	}
	exportName, _, _ := unstructured.NestedString(slice.Object, "spec", "export", "name")
	exportPath, _, _ := unstructured.NestedString(slice.Object, "spec", "export", "path")
	if slice.GetName() != appAccessMigrationEndpointSlice || exportName != entry.Spec.Export.Name || exportPath != path {
		return appAccessMigrationDescriptor{}, false, fmt.Errorf("infrastructure APIExportEndpointSlice %q in %q does not reference APIExport %q at path %q", slice.GetName(), path, entry.Spec.Export.Name, path)
	}
	return appAccessMigrationDescriptor{key: key, path: path, exportName: entry.Spec.Export.Name}, true, nil
}

func apiExportServesInfrastructureInstances(export *unstructured.Unstructured) bool {
	if export == nil || export.GetName() != appAccessMigrationExportName {
		return false
	}
	resources, found, err := unstructured.NestedSlice(export.Object, "spec", "resources")
	if err != nil || !found {
		return false
	}
	for _, raw := range resources {
		resource, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if resource["group"] == appAccessMigrationInstanceGVK.Group && resource["name"] == "instances" {
			return true
		}
	}
	return false
}

func (r *appAccessGrantMigrationSource) providerWorkspacePath(ctx context.Context, clusterID string) (string, error) {
	if !segmentRE.MatchString(clusterID) {
		return "", fmt.Errorf("invalid provider CatalogEntry cluster %q", clusterID)
	}
	config := rest.CopyConfig(r.kcpConfig)
	config.Host = apiurl.KCPClusterURL(config.Host, clusterID)
	clusterClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return "", fmt.Errorf("creating client for provider CatalogEntry cluster %q: %w", clusterID, err)
	}
	logicalCluster, err := clusterClient.Resource(appAccessMigrationLogicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading provider CatalogEntry LogicalCluster %q: %w", clusterID, err)
	}
	return logicalCluster.GetAnnotations()[core.LogicalClusterPathAnnotationKey], nil
}

func (r *appAccessGrantMigrationSource) ensureChild(descriptor appAccessMigrationDescriptor) error {
	r.mu.Lock()
	if existing := r.children[descriptor.key]; existing != nil {
		select {
		case <-existing.done:
			delete(r.children, descriptor.key)
		default:
			if existing.descriptor == descriptor {
				r.mu.Unlock()
				return nil
			}
			existing.cancel()
			r.mu.Unlock()
			if err := waitForMigrationChildStop(r.leaderCtx, existing.done); err != nil {
				return err
			}
			r.mu.Lock()
			if r.children[descriptor.key] == existing {
				delete(r.children, descriptor.key)
			}
		}
	}
	if r.leaderCtx.Err() != nil {
		r.mu.Unlock()
		return r.leaderCtx.Err()
	}
	if r.childManagerFactory == nil {
		r.mu.Unlock()
		return fmt.Errorf("appauth: grant migration child manager factory is nil")
	}
	childManager, err := r.childManagerFactory(descriptor)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	childCtx, cancel := context.WithCancel(r.leaderCtx)
	child := &appAccessMigrationChild{descriptor: descriptor, manager: childManager, cancel: cancel, done: make(chan struct{})}
	r.children[descriptor.key] = child
	r.mu.Unlock()
	go r.runChild(childCtx, child)
	return nil
}

func (r *appAccessGrantMigrationSource) stopChild(ctx context.Context, key appAccessMigrationScopeKey) error {
	r.mu.Lock()
	child := r.children[key]
	if child == nil {
		r.mu.Unlock()
		return nil
	}
	child.cancel()
	r.mu.Unlock()
	if err := waitForMigrationChildStop(ctx, child.done); err != nil {
		return err
	}
	r.mu.Lock()
	if r.children[key] == child {
		delete(r.children, key)
	}
	r.mu.Unlock()
	return nil
}

func waitForMigrationChildStop(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *appAccessGrantMigrationSource) stopAll() error {
	r.mu.Lock()
	children := make([]*appAccessMigrationChild, 0, len(r.children))
	for _, child := range r.children {
		child.cancel()
		children = append(children, child)
	}
	r.mu.Unlock()

	for _, child := range children {
		<-child.done
	}
	r.mu.Lock()
	for key, child := range r.children {
		select {
		case <-child.done:
			delete(r.children, key)
		default:
		}
	}
	r.mu.Unlock()
	return nil
}

func (r *appAccessGrantMigrationSource) runChild(ctx context.Context, child *appAccessMigrationChild) {
	defer func() {
		child.cancel()
		close(child.done)
	}()
	err := child.manager.Start(ctx)
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		err = fmt.Errorf("manager stopped while its context was live")
	}
	klog.FromContext(ctx).Error(err, "app-access grant migration manager stopped; scheduling CatalogEntry retry",
		"cluster", child.descriptor.key.cluster, "provider", child.descriptor.key.name)
	r.mu.Lock()
	if r.children[child.descriptor.key] == child {
		delete(r.children, child.descriptor.key)
	}
	r.mu.Unlock()
	if r.failures != nil && r.leaderCtx.Err() == nil {
		r.failures.enqueue(child.descriptor.key)
	}
}

func (r *appAccessGrantMigrationSource) newChildManager(descriptor appAccessMigrationDescriptor) (migrationManager, error) {
	config := rest.CopyConfig(r.kcpConfig)
	config.Timeout = 0
	config.Host = apiurl.KCPClusterURL(config.Host, descriptor.path)
	instance := &unstructured.Unstructured{}
	instance.SetGroupVersionKind(appAccessMigrationInstanceGVK)
	accept := func(client.Object) (bool, error) { return true, nil }
	provider, err := apiexportprovider.New(config, appAccessMigrationEndpointSlice, apiexportprovider.Options{
		Scheme:        r.scheme,
		ObjectToWatch: instance,
		AddFilter:     accept,
		UpdateFilter:  accept,
	})
	if err != nil {
		return nil, fmt.Errorf("creating Infrastructure APIExport provider for %q: %w", descriptor.path, err)
	}
	skipNameValidation := true
	childManager, err := mcmanager.New(config, provider, manager.Options{
		Scheme:     r.scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return nil, fmt.Errorf("creating Infrastructure grant migration manager for %q: %w", descriptor.path, err)
	}
	if err := (&appAccessGrantMigrationReconciler{handler: r.handler}).setupWithManager(childManager, appAccessMigrationControllerName(descriptor.key)); err != nil {
		return nil, err
	}
	return childManager, nil
}

func appAccessMigrationControllerName(key appAccessMigrationScopeKey) string {
	sum := sha256.Sum256([]byte(key.cluster + "\x00" + key.name))
	return "appauth-grant-migration-" + hex.EncodeToString(sum[:])[:12]
}

type appAccessMigrationDrainRunnable struct {
	source *appAccessGrantMigrationSource
}

func (r *appAccessMigrationDrainRunnable) Start(ctx context.Context) error {
	<-ctx.Done()
	return r.source.stopAll()
}

// appAccessGrantMigrationReconciler migrates only an Instance's already
// persisted, structurally recognized approvals. Each reconcile rereads the
// Instance and Project from the exact logical cluster and validates their UIDs,
// Project ownership, and environment binding before the first RBAC write.
type appAccessGrantMigrationReconciler struct {
	handler *AppAccessGrantHandler
}

func appAccessMigrationCandidate(obj client.Object) bool {
	return obj != nil && segmentRE.MatchString(obj.GetName()) && segmentRE.MatchString(obj.GetLabels()[appAccessProject])
}

func appAccessMigrationInstancePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return appAccessMigrationCandidate(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldInstance, oldOK := e.ObjectOld.(*unstructured.Unstructured)
			newInstance, newOK := e.ObjectNew.(*unstructured.Unstructured)
			if !oldOK || !newOK || (!appAccessMigrationCandidate(oldInstance) && !appAccessMigrationCandidate(newInstance)) {
				return false
			}
			return oldInstance.GetGeneration() != newInstance.GetGeneration() ||
				!reflect.DeepEqual(oldInstance.GetLabels(), newInstance.GetLabels()) ||
				!reflect.DeepEqual(oldInstance.GetOwnerReferences(), newInstance.GetOwnerReferences()) ||
				(oldInstance.GetDeletionTimestamp() == nil) != (newInstance.GetDeletionTimestamp() == nil)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return appAccessMigrationCandidate(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return appAccessMigrationCandidate(e.Object)
		},
	}
}

func (r *appAccessGrantMigrationReconciler) setupWithManager(mgr mcmanager.Manager, name string) error {
	instance := &unstructured.Unstructured{}
	instance.SetGroupVersionKind(appAccessMigrationInstanceGVK)
	if err := builder.ControllerManagedBy(mgr).
		Named(name).
		For(instance, builder.WithPredicates(appAccessMigrationInstancePredicate())).
		Complete(r); err != nil {
		return fmt.Errorf("appauth: registering Instance grant migration controller: %w", err)
	}
	return nil
}

func (r *appAccessGrantMigrationReconciler) Reconcile(ctx context.Context, request mcreconcile.Request) (ctrl.Result, error) {
	if r.handler == nil {
		return ctrl.Result{}, fmt.Errorf("appauth: grant migration handler is nil")
	}
	return ctrl.Result{}, r.reconcileInstance(ctx, string(request.ClusterName), request.Name)
}

func (r *appAccessGrantMigrationReconciler) reconcileInstance(ctx context.Context, clusterID, name string) error {
	if r.handler == nil || !segmentRE.MatchString(clusterID) || !segmentRE.MatchString(name) {
		return fmt.Errorf("appauth: invalid Instance grant migration target")
	}
	client, err := r.handler.workspaceClient(clusterID)
	if err != nil {
		return err
	}
	instance, err := client.Resource(appAccessMigrationInstanceGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	projectName := instance.GetLabels()[appAccessProject]
	if !segmentRE.MatchString(projectName) || instance.GetUID() == "" {
		return nil
	}
	project, err := client.Resource(appAccessProjectGVR).Get(ctx, projectName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	req, eligible := appAccessMigrationRequest(project, instance)
	if !eligible {
		return nil
	}
	return r.handler.migrateCurrentAppAccessGrants(ctx, client, project, instance, req)
}

func appAccessMigrationRequest(project, instance *unstructured.Unstructured) (appAccessGrantRequest, bool) {
	if project == nil || instance == nil || project.GetDeletionTimestamp() != nil || instance.GetDeletionTimestamp() != nil ||
		!brokerProjectOwnsInstance(project, instance) || project.GetUID() == "" || instance.GetUID() == "" ||
		!segmentRE.MatchString(string(project.GetUID())) || !segmentRE.MatchString(string(instance.GetUID())) {
		return appAccessGrantRequest{}, false
	}
	gv, err := schema.ParseGroupVersion(instance.GetAPIVersion())
	if err != nil || gv.Group != appAccessMigrationInstanceGVK.Group || instance.GetKind() != appAccessMigrationInstanceGVK.Kind {
		return appAccessGrantRequest{}, false
	}
	for _, target := range []struct {
		environment string
		binding     string
	}{{environment: "production", binding: "prod"}, {environment: "development", binding: "dev"}} {
		name, version, err := brokerProjectTarget(project, target.environment, target.binding)
		if err != nil || name != instance.GetName() || version != gv.Version {
			continue
		}
		return appAccessGrantRequest{
			Group: appAccessMigrationInstanceGVK.Group, Resource: "instances", Name: instance.GetName(),
			UID: string(instance.GetUID()), Project: project.GetName(), ProjectUID: string(project.GetUID()),
		}, true
	}
	return appAccessGrantRequest{}, false
}

var _ source.TypedSource[mcreconcile.Request] = (*appAccessMigrationFailureSource)(nil)
