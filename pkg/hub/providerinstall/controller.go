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

package providerinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const cleanupControllerName = "provider-install-cleanup"

const legacyRouteLookupTimeout = 20 * time.Second

var providerInstallPathSegment = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

var cleanupWorkspaceGVR = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}

// SetupCleanupController registers the watch-driven installer cleanup source
// on the organization manager. The manager's Organization cache discovers one
// provider-parent Workspace informer per known organization. Those informer
// handles are transient and owned by this manager's leader term; all cleanup
// decisions are re-read from the provider Workspace by Installer.
func SetupCleanupController(mgr manager.Manager, installer *Installer) error {
	if mgr == nil || installer == nil || installer.admin == nil || installer.workspaces == nil {
		return fmt.Errorf("providerinstall: cleanup controller requires the organization manager and installer")
	}
	source := newCleanupWatchSource(installer)
	reconciler := &cleanupReconciler{client: mgr.GetClient(), installer: installer, source: source}
	if err := builder.ControllerManagedBy(mgr).
		Named(cleanupControllerName).
		For(&tenancyv1alpha1.Organization{}).
		WatchesRawSource(source).
		Complete(reconciler); err != nil {
		return fmt.Errorf("providerinstall: registering cleanup controller: %w", err)
	}
	if err := mgr.Add(&cleanupSourceDrain{source: source}); err != nil {
		return fmt.Errorf("providerinstall: registering cleanup watch shutdown drain: %w", err)
	}
	return nil
}

// cleanupSourceDrain keeps the manager alive until every term-owned informer
// has stopped. controller-runtime's raw Source contract is non-blocking, so
// the drain must be a manager Runnable to join those child informers.
type cleanupSourceDrain struct {
	source *cleanupWatchSource
}

func (r *cleanupSourceDrain) Start(ctx context.Context) error {
	<-ctx.Done()
	r.source.stopAll()
	return nil
}

type cleanupReconciler struct {
	client    client.Client
	installer *Installer
	source    *cleanupWatchSource
}

func (r *cleanupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Namespace == "" {
		org := req.Name
		if !providerInstallPathSegment.MatchString(org) {
			return ctrl.Result{}, nil
		}
		var object tenancyv1alpha1.Organization
		if err := r.client.Get(ctx, types.NamespacedName{Name: org}, &object); err != nil {
			if apierrors.IsNotFound(err) {
				r.source.stopOrganization(org)
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("reading Organization %q for installer cleanup: %w", org, err)
		}
		if err := r.source.ensureOrganization(org); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	org, provider := req.Namespace, req.Name
	if !providerInstallPathSegment.MatchString(org) || !providerInstallPathSegment.MatchString(provider) {
		return ctrl.Result{}, nil
	}
	if err := r.installer.ReconcileCleanup(ctx, org, provider); err != nil {
		if apierrors.IsNotFound(err) {
			r.source.stopAttempt(org, provider)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// ReconcileCleanup may have persisted the terminal/cleaned marker. Read the
	// provider-parent informer cache (not a second API list) so the per-attempt
	// edge watches follow the durable plan after every event and retry.
	preparationDelay, err := r.source.refreshAttemptFromCache(ctx, org, provider)
	if err != nil {
		return ctrl.Result{}, err
	}
	// A crash after persisting Preparing but before creating the remote Job
	// produces no edge event. Revisit once at the plan's durable lease deadline
	// so the next reconcile can close that attempt; this is a per-object
	// deadline, not a timer sweep.
	return ctrl.Result{RequeueAfter: preparationDelay}, nil
}

type cleanupWatchSource struct {
	installer *Installer

	mu      sync.Mutex
	ctx     context.Context
	queue   workqueue.TypedRateLimitingInterface[reconcile.Request]
	pending map[reconcile.Request]struct{}
	orgs    map[string]*organizationCleanupWatch

	dynamicClient func(*rest.Config) (dynamic.Interface, error)
	kubeClient    func(*rest.Config) (kubernetes.Interface, error)
}

type organizationCleanupWatch struct {
	org        string
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	doneOnce   sync.Once
	workspaces cache.SharedIndexInformer
	attempts   map[string]*attemptCleanupWatch
}

type attemptCleanupWatch struct {
	job       string
	attemptID string
	owner     string
	cancel    context.CancelFunc
	done      chan struct{}
	doneOnce  sync.Once
}

func newCleanupWatchSource(installer *Installer) *cleanupWatchSource {
	return &cleanupWatchSource{
		installer: installer,
		pending:   map[reconcile.Request]struct{}{},
		orgs:      map[string]*organizationCleanupWatch{},
		dynamicClient: func(config *rest.Config) (dynamic.Interface, error) {
			return dynamic.NewForConfig(config)
		},
		kubeClient: func(config *rest.Config) (kubernetes.Interface, error) {
			return kubernetes.NewForConfig(config)
		},
	}
}

// Start implements controller-runtime's raw source contract. Informers are
// created lazily from Organization reconcile requests, never from HTTP reads.
func (s *cleanupWatchSource) Start(ctx context.Context, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	s.mu.Lock()
	s.ctx = ctx
	s.queue = queue
	pending := make([]reconcile.Request, 0, len(s.pending))
	for request := range s.pending {
		pending = append(pending, request)
		delete(s.pending, request)
	}
	s.mu.Unlock()
	for _, request := range pending {
		queue.Add(request)
	}
	return nil
}

func (s *cleanupWatchSource) enqueue(org, provider string) {
	if !providerInstallPathSegment.MatchString(org) || !providerInstallPathSegment.MatchString(provider) {
		return
	}
	request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: org, Name: provider}}
	s.mu.Lock()
	queue := s.queue
	if queue == nil {
		s.pending[request] = struct{}{}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	queue.Add(request)
}

func (s *cleanupWatchSource) ensureOrganization(org string) error {
	if !providerInstallPathSegment.MatchString(org) {
		return fmt.Errorf("providerinstall: invalid organization identifier %q", org)
	}
	s.mu.Lock()
	if _, ok := s.orgs[org]; ok {
		s.mu.Unlock()
		return nil
	}
	if s.ctx == nil || s.ctx.Err() != nil {
		s.mu.Unlock()
		return fmt.Errorf("providerinstall: cleanup source is not running")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	watch := &organizationCleanupWatch{org: org, ctx: ctx, cancel: cancel, done: make(chan struct{}), attempts: map[string]*attemptCleanupWatch{}}
	s.orgs[org] = watch
	s.mu.Unlock()

	config := rest.CopyConfig(s.installer.admin)
	config.Host = apiurl.KCPClusterURL(config.Host, kcppaths.OrgProvidersParent(org))
	config.Timeout = 0 // Streaming informer watches must not inherit a request timeout.
	dynamicClient, err := s.dynamicClient(config)
	if err != nil {
		s.forgetOrganization(org, watch)
		return fmt.Errorf("providerinstall: creating Workspace watch for organization %s: %w", org, err)
	}
	// This standard informer is deliberately pointed at one Org's provider
	// parent. The kcp dynamic cluster informer always lists logicalcluster/*,
	// which this hub identity is not allowed to enumerate.
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynamicClient, 0, "", nil)
	informer := factory.ForResource(cleanupWorkspaceGVR).Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { s.onWorkspace(org, obj) },
		UpdateFunc: func(_, obj any) { s.onWorkspace(org, obj) },
		DeleteFunc: func(obj any) { s.onWorkspaceDeleted(org, obj) },
	}); err != nil {
		s.forgetOrganization(org, watch)
		return fmt.Errorf("providerinstall: registering Workspace watch for organization %s: %w", org, err)
	}
	s.mu.Lock()
	if s.orgs[org] != watch {
		s.mu.Unlock()
		watch.cancel()
		watch.doneOnce.Do(func() { close(watch.done) })
		return nil
	}
	watch.workspaces = informer
	s.mu.Unlock()
	go func() {
		defer watch.doneOnce.Do(func() { close(watch.done) })
		informer.Run(ctx.Done())
	}()
	return nil
}

func (s *cleanupWatchSource) onWorkspace(org string, raw any) {
	workspace := cleanupWorkspaceObject(raw)
	if workspace == nil || workspace.GetName() == "" {
		return
	}
	s.enqueue(org, workspace.GetName())
}

func (s *cleanupWatchSource) onWorkspaceDeleted(org string, raw any) {
	workspace := cleanupWorkspaceObject(raw)
	if workspace == nil || workspace.GetName() == "" {
		return
	}
	s.stopAttempt(org, workspace.GetName())
	s.enqueue(org, workspace.GetName())
}

func cleanupWorkspaceObject(raw any) *unstructured.Unstructured {
	switch object := raw.(type) {
	case *unstructured.Unstructured:
		return object
	case cache.DeletedFinalStateUnknown:
		workspace, _ := object.Obj.(*unstructured.Unstructured)
		return workspace
	case *cache.DeletedFinalStateUnknown:
		if object == nil {
			return nil
		}
		workspace, _ := object.Obj.(*unstructured.Unstructured)
		return workspace
	default:
		return nil
	}
}

func (s *cleanupWatchSource) refreshAttemptFromCache(ctx context.Context, org, provider string) (time.Duration, error) {
	s.mu.Lock()
	organization := s.orgs[org]
	s.mu.Unlock()
	if organization == nil || organization.workspaces == nil {
		return 0, nil
	}
	object, exists, err := organization.workspaces.GetStore().GetByKey(provider)
	if err != nil {
		return 0, fmt.Errorf("providerinstall: reading Workspace %s/%s from informer cache: %w", org, provider, err)
	}
	if !exists {
		s.stopAttempt(org, provider)
		return 0, nil
	}
	workspace, ok := object.(*unstructured.Unstructured)
	if !ok {
		s.stopAttempt(org, provider)
		return 0, fmt.Errorf("providerinstall: Workspace %s/%s cache returned %T", org, provider, object)
	}
	plan, valid := cleanupPlanForWorkspace(org, provider, workspace)
	if !valid || plan.CleanedUp {
		s.stopAttempt(org, provider)
		return 0, nil
	}
	if validAttemptWatchPlan(plan) {
		if err := s.ensureAttemptWatch(ctx, org, provider, plan); err != nil {
			return 0, err
		}
	} else {
		s.stopAttempt(org, provider)
	}
	if preparationLeaseActive(plan) {
		return time.Until(plan.PreparationExpiresAt), nil
	}
	return 0, nil
}

func cleanupPlanForWorkspace(org, provider string, workspace *unstructured.Unstructured) (*Plan, bool) {
	if workspace == nil || workspace.GetUID() == "" {
		return nil, false
	}
	raw := workspace.GetAnnotations()[planAnnotation]
	if raw == "" {
		return nil, false
	}
	plan := &Plan{}
	if err := json.Unmarshal([]byte(raw), plan); err != nil || plan.Owner == "" || plan.Owner != string(workspace.GetUID()) {
		return nil, false
	}
	// New plans carry these fields explicitly. Empty values are legacy plans;
	// the parent informer itself is already scoped to this exact organization
	// and provider Workspace name.
	if plan.Org != "" && plan.Org != org {
		return nil, false
	}
	if plan.Provider != "" && plan.Provider != provider {
		return nil, false
	}
	return plan, true
}

func validAttemptWatchPlan(plan *Plan) bool {
	return plan != nil && providerInstallPathSegment.MatchString(plan.Namespace) &&
		providerInstallPathSegment.MatchString(plan.Job) && plan.Owner != "" && plan.AttemptID != ""
}

func (s *cleanupWatchSource) ensureAttemptWatch(ctx context.Context, org, provider string, plan *Plan) error {
	s.mu.Lock()
	organization := s.orgs[org]
	if organization == nil {
		s.mu.Unlock()
		return nil
	}
	old := organization.attempts[provider]
	if old != nil && old.job == plan.Job && old.attemptID == plan.AttemptID && old.owner == plan.Owner {
		s.mu.Unlock()
		return nil
	}
	if old != nil {
		old.cancel()
		delete(organization.attempts, provider)
	}
	if ctx.Err() != nil {
		s.mu.Unlock()
		return ctx.Err()
	}
	watchCtx, cancel := context.WithCancel(organization.ctx)
	attempt := &attemptCleanupWatch{job: plan.Job, attemptID: plan.AttemptID, owner: plan.Owner, cancel: cancel, done: make(chan struct{})}
	organization.attempts[provider] = attempt
	s.mu.Unlock()
	if old != nil {
		<-old.done
	}

	config, err := s.edgeWatchConfig(ctx, org, provider, plan)
	if err != nil {
		s.forgetAttempt(org, provider, attempt)
		return err
	}
	edge, err := s.kubeClient(config)
	if err != nil {
		s.forgetAttempt(org, provider, attempt)
		return fmt.Errorf("providerinstall: creating Edge watch for %s/%s: %w", org, provider, err)
	}
	if err := s.startAttemptInformers(watchCtx, org, provider, plan, edge, attempt); err != nil {
		s.forgetAttempt(org, provider, attempt)
		return err
	}
	return nil
}

func (s *cleanupWatchSource) edgeWatchConfig(ctx context.Context, org, provider string, plan *Plan) (*rest.Config, error) {
	cluster, edgeName := plan.Cluster, plan.EdgeName
	if cluster == "" || edgeName == "" {
		// Legacy plans predate pinned routing fields. Resolve only through the
		// registered provider Workspace's edge route, never a request header.
		lookupCtx, cancel := context.WithTimeout(ctx, legacyRouteLookupTimeout)
		defer cancel()
		route, err := s.installer.workspaces.GetProviderEdgeRoute(lookupCtx, org, provider)
		if err != nil {
			return nil, fmt.Errorf("providerinstall: resolving legacy Edge target for %s/%s: %w", org, provider, err)
		}
		if route == nil || route.WorkspaceUUID == "" || route.EdgeName == "" {
			return nil, fmt.Errorf("providerinstall: legacy installation %s/%s has no registered Edge target", org, provider)
		}
		cluster, err = s.installer.workspaces.GetChildWorkspaceClusterName(lookupCtx, org, route.WorkspaceUUID)
		if err != nil {
			return nil, fmt.Errorf("providerinstall: resolving legacy Edge cluster for %s/%s: %w", org, provider, err)
		}
		edgeName = route.EdgeName
	}
	if !providerInstallPathSegment.MatchString(cluster) || !providerInstallPathSegment.MatchString(edgeName) {
		return nil, fmt.Errorf("providerinstall: invalid pinned Edge target for %s/%s", org, provider)
	}
	config := rest.CopyConfig(s.installer.admin)
	config.Host = apiurl.KCPClusterURL(config.Host, cluster) + "/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/" + edgeName + "/k8s"
	config.Timeout = 0
	return config, nil
}

func (s *cleanupWatchSource) startAttemptInformers(ctx context.Context, org, provider string, plan *Plan, edge kubernetes.Interface, attempt *attemptCleanupWatch) error {
	selector := labels.Set{ownerLabel: plan.Owner, attemptLabel: plan.Job}.AsSelector().String()
	jobFieldSelector := fields.OneTermEqualSelector("metadata.name", plan.Job).String()
	jobOptions := func(options metav1.ListOptions) metav1.ListOptions {
		options.LabelSelector = selector
		options.FieldSelector = jobFieldSelector
		return options
	}
	podOptions := func(options metav1.ListOptions) metav1.ListOptions {
		options.LabelSelector = selector
		return options
	}
	jobInformer := cache.NewSharedIndexInformer(&cache.ListWatch{
		ListWithContextFunc: func(listCtx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			return edge.BatchV1().Jobs(plan.Namespace).List(listCtx, jobOptions(options))
		},
		WatchFuncWithContext: func(watchCtx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			return edge.BatchV1().Jobs(plan.Namespace).Watch(watchCtx, jobOptions(options))
		},
	}, &batchv1.Job{}, 0, cache.Indexers{})
	podInformer := cache.NewSharedIndexInformer(&cache.ListWatch{
		ListWithContextFunc: func(listCtx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			return edge.CoreV1().Pods(plan.Namespace).List(listCtx, podOptions(options))
		},
		WatchFuncWithContext: func(watchCtx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			return edge.CoreV1().Pods(plan.Namespace).Watch(watchCtx, podOptions(options))
		},
	}, &corev1.Pod{}, 0, cache.Indexers{})
	jobHandler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(raw any) { s.onAttemptObject(org, provider, plan, raw) },
		UpdateFunc: func(_, raw any) { s.onAttemptObject(org, provider, plan, raw) },
		DeleteFunc: func(raw any) { s.onAttemptDeletedObject(org, provider, plan, raw) },
	}
	if _, err := jobInformer.AddEventHandler(jobHandler); err != nil {
		return fmt.Errorf("providerinstall: registering installer Job watch for %s/%s: %w", org, provider, err)
	}
	if _, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(raw any) { s.onAttemptObject(org, provider, plan, raw) },
		UpdateFunc: func(_, raw any) { s.onAttemptObject(org, provider, plan, raw) },
		DeleteFunc: func(raw any) { s.onAttemptDeletedObject(org, provider, plan, raw) },
	}); err != nil {
		return fmt.Errorf("providerinstall: registering installer Pod watch for %s/%s: %w", org, provider, err)
	}
	go func() {
		defer attempt.doneOnce.Do(func() { close(attempt.done) })
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); jobInformer.Run(ctx.Done()) }()
		go func() { defer wg.Done(); podInformer.Run(ctx.Done()) }()
		wg.Wait()
	}()
	return nil
}

func (s *cleanupWatchSource) onAttemptObject(org, provider string, plan *Plan, raw any) {
	object, metaObject := attemptMeta(raw)
	if !attemptObjectMatches(plan, metaObject) {
		return
	}
	switch object := object.(type) {
	case *batchv1.Job:
		if object.Name != plan.Job || !terminalInstallerJob(object) {
			return
		}
	case *corev1.Pod:
		if object.Status.Phase != corev1.PodSucceeded && object.Status.Phase != corev1.PodFailed {
			return
		}
	default:
		return
	}
	s.enqueue(org, provider)
}

func (s *cleanupWatchSource) onAttemptDeletedObject(org, provider string, plan *Plan, raw any) {
	object, metaObject := attemptMeta(raw)
	if !attemptObjectMatches(plan, metaObject) {
		return
	}
	switch object := object.(type) {
	case *batchv1.Job:
		if object.Name != plan.Job {
			return
		}
	case *corev1.Pod:
	default:
		return
	}
	s.enqueue(org, provider)
}

func attemptMeta(raw any) (any, metav1.Object) {
	switch tombstone := raw.(type) {
	case cache.DeletedFinalStateUnknown:
		raw = tombstone.Obj
	case *cache.DeletedFinalStateUnknown:
		if tombstone == nil {
			return nil, nil
		}
		raw = tombstone.Obj
	}
	metaObject, err := meta.Accessor(raw)
	if err != nil {
		return raw, nil
	}
	return raw, metaObject
}

func attemptObjectMatches(plan *Plan, object metav1.Object) bool {
	return plan != nil && object != nil &&
		object.GetLabels()[ownerLabel] == plan.Owner &&
		object.GetLabels()[attemptLabel] == plan.Job &&
		object.GetAnnotations()[attemptIDAnnotation] == plan.AttemptID
}

func terminalInstallerJob(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	for _, condition := range job.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (s *cleanupWatchSource) stopAttempt(org, provider string) {
	s.mu.Lock()
	organization := s.orgs[org]
	var attempt *attemptCleanupWatch
	if organization != nil {
		attempt = organization.attempts[provider]
		delete(organization.attempts, provider)
	}
	s.mu.Unlock()
	if attempt != nil {
		attempt.cancel()
		<-attempt.done
	}
}

func (s *cleanupWatchSource) forgetAttempt(org, provider string, attempt *attemptCleanupWatch) {
	s.mu.Lock()
	if organization := s.orgs[org]; organization != nil && organization.attempts[provider] == attempt {
		delete(organization.attempts, provider)
	}
	s.mu.Unlock()
	attempt.cancel()
	// Setup errors happen before informer goroutines are started.
	attempt.doneOnce.Do(func() { close(attempt.done) })
}

func (s *cleanupWatchSource) forgetOrganization(org string, watch *organizationCleanupWatch) {
	s.mu.Lock()
	if s.orgs[org] == watch {
		delete(s.orgs, org)
	}
	s.mu.Unlock()
	watch.cancel()
	watch.doneOnce.Do(func() { close(watch.done) })
}

func (s *cleanupWatchSource) stopOrganization(org string) {
	s.mu.Lock()
	watch := s.orgs[org]
	delete(s.orgs, org)
	var attempts []*attemptCleanupWatch
	if watch != nil {
		for name, attempt := range watch.attempts {
			attempts = append(attempts, attempt)
			delete(watch.attempts, name)
		}
	}
	s.mu.Unlock()
	if watch == nil {
		return
	}
	watch.cancel()
	for _, attempt := range attempts {
		attempt.cancel()
	}
	<-watch.done
	for _, attempt := range attempts {
		<-attempt.done
	}
}

func (s *cleanupWatchSource) stopAll() {
	s.mu.Lock()
	orgs := s.orgs
	s.orgs = map[string]*organizationCleanupWatch{}
	attempts := make(map[*organizationCleanupWatch][]*attemptCleanupWatch, len(orgs))
	for _, organization := range orgs {
		for provider, attempt := range organization.attempts {
			attempts[organization] = append(attempts[organization], attempt)
			delete(organization.attempts, provider)
		}
	}
	s.mu.Unlock()
	for _, organization := range orgs {
		organization.cancel()
		for _, attempt := range attempts[organization] {
			attempt.cancel()
		}
	}
	for _, organization := range orgs {
		<-organization.done
		for _, attempt := range attempts[organization] {
			<-attempt.done
		}
	}
}

var _ source.Source = (*cleanupWatchSource)(nil)
var _ reconcile.Reconciler = (*cleanupReconciler)(nil)
