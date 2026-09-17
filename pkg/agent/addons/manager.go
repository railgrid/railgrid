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

package addons

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const controllerName = "addon-manager"

// Group/version/resource of the edges provider's Addon kind, mirrored here so
// the agent needs no import of the provider module (same rule as
// pkg/agent/reconciler).
const (
	edgesGroup   = "edges.railgrid.ai"
	edgesVersion = "v1alpha1"
)

// AddonGVR addresses Addon objects in the tenant workspace.
var AddonGVR = schema.GroupVersionResource{Group: edgesGroup, Version: edgesVersion, Resource: "addons"}

// DefaultResync is how often every known Addon is reconciled even without a
// watch event. It is short because the Running condition is a live health
// probe of the supervised process, not a cached fact.
const DefaultResync = 60 * time.Second

// maxStatusMessage bounds status.message; the API caps it too.
const maxStatusMessage = 2048

// nameCheck is what an add-on name has to satisfy before it becomes a path
// segment and a Secret name. Kubernetes already guarantees a DNS subdomain,
// but this package builds filesystem paths from the name and will not rely on
// admission having happened.
var nameCheck = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// addonView is the subset of an Addon the agent reads. Only these fields are
// decoded; everything else on the object is the provider's or the user's.
type addonView struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              struct {
		EdgeRef EdgeRef     `json:"edgeRef"`
		Type    string      `json:"type"`
		Paused  bool        `json:"paused,omitempty"`
		Runner  *RunnerSpec `json:"runner,omitempty"`
	} `json:"spec,omitempty"`
	Status struct {
		Phase      string             `json:"phase,omitempty"`
		Conditions []metav1.Condition `json:"conditions,omitempty"`
		Transition *metav1.Time       `json:"lastTransitionTime,omitempty"`
	} `json:"status,omitempty"`
}

// Options configures a Manager.
type Options struct {
	// EdgeKind and EdgeName identify this edge. An Addon whose spec.edgeRef
	// names anything else is ignored entirely — not even its status is touched.
	EdgeKind string
	EdgeName string
	// Allowed are the add-on types the machine owner opted this host into with
	// --allow-addon. Empty (the default) means this edge materializes nothing.
	Allowed []string
	// Resync is how often every Addon is reconciled without a watch event.
	Resync time.Duration
}

// Manager watches the tenant workspace's Addon objects for this edge and keeps
// one Addon implementation per object.
type Manager struct {
	hub      dynamic.Interface
	edgeKind string
	edgeName string
	resync   time.Duration

	// allowed is the local opt-in. It is read-only after construction, which is
	// deliberate: nothing the hub says can widen it at runtime.
	allowed map[string]bool

	factories map[string]Factory

	mu        sync.Mutex
	instances map[string]Addon

	queue workqueue.TypedRateLimitingInterface[string]
}

// NewManager builds a manager. hub is a dynamic client scoped to the edge's
// tenant workspace — the same credential the edge status reporter uses.
func NewManager(hub dynamic.Interface, opts Options) (*Manager, error) {
	if hub == nil {
		return nil, fmt.Errorf("addon manager: a hub client is required")
	}
	if opts.EdgeName == "" {
		return nil, fmt.Errorf("addon manager: edge name is required")
	}
	allowed := map[string]bool{}
	for _, t := range opts.Allowed {
		if t = strings.TrimSpace(t); t != "" {
			allowed[t] = true
		}
	}
	resync := opts.Resync
	if resync <= 0 {
		resync = DefaultResync
	}
	return &Manager{
		hub:       hub,
		edgeKind:  opts.EdgeKind,
		edgeName:  opts.EdgeName,
		resync:    resync,
		allowed:   allowed,
		factories: map[string]Factory{},
		instances: map[string]Addon{},
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: controllerName},
		),
	}, nil
}

// Register wires an implementation for one add-on type. Registering a type the
// machine owner did not allow is harmless: the factory is simply never called.
func (m *Manager) Register(addonType string, factory Factory) {
	m.factories[addonType] = factory
}

// AllowedTypes returns the locally allowed add-on types, sorted. The edge
// status reporter publishes them on the edge object so a portal can show what
// a machine will accept BEFORE anyone creates an Addon for it.
func (m *Manager) AllowedTypes() []string {
	out := make([]string, 0, len(m.allowed))
	for t := range m.allowed {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Run starts the manager and blocks until ctx is cancelled, at which point
// every supervised child is stopped.
func (m *Manager) Run(ctx context.Context) error {
	defer utilruntime.HandleCrash()
	defer m.queue.ShutDown()

	logger := klog.FromContext(ctx).WithName(controllerName)
	logger.Info("Starting addon manager", "edgeKind", m.edgeKind, "edgeName", m.edgeName, "allowed", m.AllowedTypes())

	// Addons are cluster-scoped and carry no per-edge label, and a custom
	// resource has no field selector to filter spec.edgeRef server-side. The
	// filter is therefore applied in reconcile; a tenant's Addon for another
	// edge costs this agent one cached object and one no-op reconcile.
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		m.hub, m.resync, metav1.NamespaceAll, nil,
	)
	informer := factory.ForResource(AddonGVR).Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { m.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { m.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { m.enqueue(obj) },
	}); err != nil {
		return fmt.Errorf("adding event handler: %w", err)
	}

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())

	go wait.UntilWithContext(ctx, m.worker, time.Second)

	<-ctx.Done()
	logger.Info("Shutting down addon manager; stopping supervised add-ons")
	m.stopAll()
	return nil
}

// stopAll tears down every supervised child on shutdown. It uses a fresh
// context because the caller's is already cancelled and a stop still has to be
// able to wait out its grace period.
func (m *Manager) stopAll() {
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m.mu.Lock()
	instances := make(map[string]Addon, len(m.instances))
	for name, inst := range m.instances {
		instances[name] = inst
	}
	m.instances = map[string]Addon{}
	m.mu.Unlock()
	for name, inst := range instances {
		if err := inst.Stop(stopCtx); err != nil {
			klog.Background().Error(err, "stopping add-on", "addon", name)
		}
	}
}

func (m *Manager) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	m.queue.Add(key)
}

func (m *Manager) worker(ctx context.Context) {
	for m.processNextWorkItem(ctx) {
	}
}

func (m *Manager) processNextWorkItem(ctx context.Context) bool {
	key, quit := m.queue.Get()
	if quit {
		return false
	}
	defer m.queue.Done(key)

	if err := m.Reconcile(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("reconciling addon %q: %w", key, err))
		m.queue.AddRateLimited(key)
		return true
	}
	m.queue.Forget(key)
	return true
}

// Reconcile converges one Addon. It is exported so the agent (and tests) can
// drive a single object without the informer machinery.
func (m *Manager) Reconcile(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx).WithValues("addon", name)

	obj, err := m.hub.Resource(AddonGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Deleted: stop the child, keep the state directory and repositories.
		logger.Info("Addon deleted; stopping add-on")
		return m.release(ctx, name)
	} else if err != nil {
		return err
	}

	var view addonView
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &view); err != nil {
		// A malformed object cannot be fixed by retrying; say so on status and
		// stop rather than hot-looping the queue.
		logger.Error(err, "decoding addon")
		return nil
	}

	if !m.targetsThisEdge(view.Spec.EdgeRef) {
		// Another edge's Addon. Never touch its status — that edge's agent owns
		// it — but do release anything we may have been running for it before
		// its edgeRef was repointed.
		return m.release(ctx, name)
	}

	spec := Spec{
		Name:       view.Name,
		UID:        view.UID,
		Generation: view.Generation,
		EdgeRef:    view.Spec.EdgeRef,
		Type:       view.Spec.Type,
		Paused:     view.Spec.Paused,
		Runner:     view.Spec.Runner,
	}

	if err := validateName(spec.Name); err != nil {
		return m.writeStatus(ctx, obj, &view, Status{
			Phase:   PhaseBlocked,
			Message: err.Error(),
			Conditions: []Condition{
				{Type: ConditionAllowed, Status: metav1.ConditionFalse, Reason: ReasonConfigError, Message: err.Error()},
			},
		})
	}

	if obj.GetDeletionTimestamp() != nil {
		logger.Info("Addon is terminating; stopping add-on")
		return m.release(ctx, name)
	}

	// The local opt-in. This is the half of the trust model the machine owner
	// holds: without --allow-addon for this type nothing is created, nothing is
	// started, and no Secret is published — only the refusal is reported.
	if !m.allowed[spec.Type] {
		if err := m.release(ctx, name); err != nil {
			logger.Error(err, "releasing a no-longer-allowed add-on")
		}
		msg := fmt.Sprintf("add-on type %q is not allowed on edge %q; the machine owner must start the agent with --allow-addon=%s",
			spec.Type, m.edgeName, spec.Type)
		return m.writeStatus(ctx, obj, &view, Status{
			Phase:   PhaseBlocked,
			Message: msg,
			Conditions: []Condition{
				{Type: ConditionAllowed, Status: metav1.ConditionFalse, Reason: ReasonNotAllowedOnEdge, Message: msg},
				{Type: ConditionRunning, Status: metav1.ConditionFalse, Reason: ReasonNotAllowedOnEdge, Message: "not materialized"},
			},
		})
	}

	inst, err := m.instance(spec.Type, spec.Name)
	if err != nil {
		return m.writeStatus(ctx, obj, &view, Status{
			Phase:   PhaseBlocked,
			Message: err.Error(),
			Conditions: []Condition{
				{Type: ConditionAllowed, Status: metav1.ConditionTrue, Reason: ReasonAllowedOnEdge, Message: "allowed by --allow-addon"},
				{Type: ConditionRunning, Status: metav1.ConditionFalse, Reason: ReasonConfigError, Message: err.Error()},
			},
		})
	}

	allowedCondition := Condition{
		Type: ConditionAllowed, Status: metav1.ConditionTrue, Reason: ReasonAllowedOnEdge,
		Message: "allowed by --allow-addon on this edge",
	}

	if spec.Paused {
		// Paused keeps the instance (and its state directory, token and
		// repositories) so unpausing resumes the same add-on identity.
		if err := inst.Stop(ctx); err != nil {
			logger.Error(err, "stopping paused add-on")
		}
		return m.writeStatus(ctx, obj, &view, Status{
			Phase:   PhasePaused,
			Message: "spec.paused is true",
			Conditions: []Condition{
				allowedCondition,
				{Type: ConditionRunning, Status: metav1.ConditionFalse, Reason: ReasonPaused, Message: "spec.paused is true"},
			},
		})
	}

	status, reconcileErr := inst.Reconcile(ctx, spec)
	if reconcileErr != nil {
		status.Phase = PhaseDegraded
		status.Message = reconcileErr.Error()
		status.Conditions = append(status.Conditions, Condition{
			Type: ConditionRunning, Status: metav1.ConditionFalse, Reason: ReasonConfigError, Message: reconcileErr.Error(),
		})
		logger.Error(reconcileErr, "reconciling add-on")
	}
	status.Conditions = append([]Condition{allowedCondition}, status.Conditions...)
	return m.writeStatus(ctx, obj, &view, status)
}

// targetsThisEdge reports whether an edgeRef names this agent's edge. An empty
// kind means LinuxServer, matching the API default.
func (m *Manager) targetsThisEdge(ref EdgeRef) bool {
	kind := ref.Kind
	if kind == "" {
		kind = "LinuxServer"
	}
	return kind == m.edgeKind && ref.Name == m.edgeName
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("addon name is empty")
	}
	if len(name) > 63 || !nameCheck.MatchString(name) {
		return fmt.Errorf("addon name %q is not a DNS-1123 label; it is used as a directory and Secret name", name)
	}
	return nil
}

// instance returns the live Addon for name, creating it on first sight.
func (m *Manager) instance(addonType, name string) (Addon, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[name]; ok {
		if inst.Type() != addonType {
			// spec.type changed under us: tear the old one down before the new
			// implementation takes the same state directory.
			delete(m.instances, name)
			go func(old Addon) {
				stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := old.Stop(stopCtx); err != nil {
					klog.Background().Error(err, "stopping add-on after a type change", "addon", name)
				}
			}(inst)
		} else {
			return inst, nil
		}
	}
	factory, ok := m.factories[addonType]
	if !ok {
		return nil, fmt.Errorf("this agent build has no implementation for add-on type %q", addonType)
	}
	inst, err := factory(name)
	if err != nil {
		return nil, err
	}
	m.instances[name] = inst
	return inst, nil
}

// release stops and drops the instance for name. The add-on's state directory
// and enrolled repositories are deliberately left on disk.
func (m *Manager) release(ctx context.Context, name string) error {
	m.mu.Lock()
	inst, ok := m.instances[name]
	delete(m.instances, name)
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return inst.Stop(ctx)
}

// statusPatch is the merge patch the agent applies to status. Conditions are
// sent whole (JSON merge patch replaces arrays), which is why writeStatus
// merges the agent's conditions INTO the ones already on the object — dropping
// the provider's Published condition on every heartbeat would make the provider
// and the agent fight over the array.
type statusPatch struct {
	Status statusPatchBody `json:"status"`
}

type statusPatchBody struct {
	Phase              string             `json:"phase"`
	ObservedGeneration int64              `json:"observedGeneration"`
	Conditions         []metav1.Condition `json:"conditions"`
	Version            string             `json:"version"`
	Message            string             `json:"message"`
	LastTransitionTime metav1.Time        `json:"lastTransitionTime"`
	// Harness is null when the add-on could not probe one, which clears a
	// stale block rather than leaving a harness advertised for a runner that
	// is no longer answering.
	Harness *harnessPatchBody `json:"harness"`
}

type harnessPatchBody struct {
	Name    string   `json:"name,omitempty"`
	Version string   `json:"version,omitempty"`
	Ready   bool     `json:"ready,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

func (m *Manager) writeStatus(ctx context.Context, obj *unstructured.Unstructured, view *addonView, st Status) error {
	conditions := append([]metav1.Condition(nil), view.Status.Conditions...)
	for _, c := range st.Conditions {
		apimeta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               c.Type,
			Status:             c.Status,
			Reason:             defaultReason(c.Reason),
			Message:            truncate(c.Message, maxStatusMessage),
			ObservedGeneration: view.Generation,
		})
	}

	transition := metav1.Now()
	if view.Status.Phase == st.Phase && view.Status.Transition != nil {
		transition = *view.Status.Transition
	}

	var harnessBody *harnessPatchBody
	if st.Harness != nil {
		harnessBody = &harnessPatchBody{
			Name:    st.Harness.Name,
			Version: st.Harness.Version,
			Ready:   st.Harness.Ready,
			Reasons: st.Harness.Reasons,
		}
	}

	body, err := json.Marshal(statusPatch{Status: statusPatchBody{
		Phase:              st.Phase,
		ObservedGeneration: view.Generation,
		Conditions:         conditions,
		Version:            st.Version,
		Message:            truncate(st.Message, maxStatusMessage),
		LastTransitionTime: transition,
		Harness:            harnessBody,
	}})
	if err != nil {
		return fmt.Errorf("marshaling addon status patch: %w", err)
	}
	_, err = m.hub.Resource(AddonGVR).Patch(ctx, obj.GetName(), types.MergePatchType, body, metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("patching addon %q status: %w", obj.GetName(), err)
	}
	return nil
}

// defaultReason keeps the API's "reason is required" contract satisfiable even
// if an implementation forgets one.
func defaultReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "Unknown"
	}
	return reason
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
