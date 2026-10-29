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

// Package usage lets a provider report how many of its tenant-owned objects
// exist, the way contrib-metering's usage contract asks for it
// (docs/usage-contract.md in railgrid/contrib-metering).
//
// A provider declares its resource-counter meters, each the sum of one or
// more of its kinds, and calls Setup on the leader-elected multicluster
// manager it already runs over its APIExport. For every meter the package
// registers one watch-driven reconciler: any change to a counted object in a
// tenant workspace re-counts that workspace from the manager's cache and
// upserts one UsageRecord for the current grid window into the provider's
// own workspace, named deterministically so a re-emit is an update. Each
// workspace with objects is re-stated every window even when nothing
// changed, as the contract requires for a level; a workspace that reaches
// zero is stated as zero once and then left alone until something is created.
//
// The metering API is a feature of the installation, not of the provider:
// the hub binds the metering-provider export into the provider workspace
// once metering is installed, and nothing is bound when it is not. The
// emitter therefore probes for the UsageRecord resource before writing and
// pauses, re-checking every minute, while it is absent. A provider needs no
// flag and no metering dependency to carry this code.
package usage

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

const (
	// FieldManager is the server-side-apply manager the emitter writes as.
	FieldManager = "railgrid-usage-emitter"
	// LabelMeter and LabelSubject are stamped on every record the emitter
	// writes, so a provider workspace can be listed by meter or by tenant.
	LabelMeter   = "usage.railgrid.ai/meter"
	LabelSubject = "usage.railgrid.ai/subject"

	// gateRetry paces the probe for the UsageRecord resource while it is not
	// served in the provider workspace.
	gateRetry = time.Minute
	// maxJitter spreads the per-window re-statements of many workspaces so
	// they do not all land on the grid boundary together.
	maxJitter = 10 * time.Second
)

// GVR is the UsageRecord resource the emitter writes.
var GVR = schema.GroupVersionResource{Group: "metering.contrib.kcp.io", Version: "v1alpha1", Resource: "usagerecords"}

// Meter is one resource counter: the number of live objects of Kinds in a
// tenant workspace, reported under Name.
type Meter struct {
	// Name is the meter as a Plan limit names it: a bare resource noun such
	// as "agents" or "edges".
	Name string
	// Kinds are prototypes of the counted kinds: typed objects registered
	// in the manager's scheme, or *unstructured.Unstructured carrying a
	// GroupVersionKind (which shares the informer of a provider that already
	// reconciles the kind unstructured). The meter is their sum.
	Kinds []client.Object
}

// Options configures Setup.
type Options struct {
	// Meters to emit; at least one.
	Meters []Meter
	// Provider is the rest.Config of the provider's own workspace, where the
	// records are written (RAILGRID_PROVIDER_KUBECONFIG).
	Provider *rest.Config
	// Granularity is the window grid; DefaultGranularity when zero.
	Granularity time.Duration
	Logger      logr.Logger
}

// Setup registers one reconciler per meter on mgr. Call it on the
// leader-elected manager only: the emitter writes.
func Setup(mgr mcmanager.Manager, opts Options) error {
	if len(opts.Meters) == 0 {
		return errors.New("usage: at least one meter is required")
	}
	if opts.Provider == nil {
		return errors.New("usage: Options.Provider (the provider workspace config) is required")
	}
	if opts.Granularity <= 0 {
		opts.Granularity = DefaultGranularity
	}
	dyn, err := dynamic.NewForConfig(opts.Provider)
	if err != nil {
		return fmt.Errorf("usage: provider workspace client: %w", err)
	}
	g := &gate{res: dyn.Resource(GVR), logger: opts.Logger, now: time.Now}
	scheme := mgr.GetLocalManager().GetScheme()
	if err := Validate(opts.Meters, scheme); err != nil {
		return err
	}
	for _, m := range opts.Meters {
		e := &emitter{
			meter:       m.Name,
			granularity: opts.Granularity,
			logger:      opts.Logger.WithValues("meter", m.Name),
			gate:        g,
			now:         time.Now,
			list: func(ctx context.Context, cluster multicluster.ClusterName, l client.ObjectList) (bool, error) {
				cl, err := mgr.GetCluster(ctx, cluster)
				if err != nil {
					if errors.Is(err, multicluster.ErrClusterNotFound) {
						return true, nil
					}
					return false, err
				}
				return false, cl.GetClient().List(ctx, l)
			},
			write: applyRecord(dyn.Resource(GVR)),
		}
		b := mcbuilder.ControllerManagedBy(mgr).Named("usage-" + m.Name)
		for _, kind := range m.Kinds {
			newList, err := listFactory(kind, scheme)
			if err != nil {
				return fmt.Errorf("usage: meter %s: %w", m.Name, err)
			}
			e.lists = append(e.lists, newList)
			// Every event in a workspace collapses to one request for that
			// workspace and meter: the count is a property of the workspace,
			// not of the object that changed.
			meter := m.Name
			b = b.Watches(kind, mchandler.TypedEnqueueRequestsFromMapFunc(
				func(context.Context, client.Object) []mcreconcile.Request {
					return []mcreconcile.Request{{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: meter}}}}
				}))
		}
		if err := b.Complete(e); err != nil {
			return fmt.Errorf("usage: meter %s: %w", m.Name, err)
		}
	}
	return nil
}

// Validate checks a meter declaration against the scheme the manager will
// run with: every meter named once, every kind countable. Setup runs it; a
// provider's own test should too, so a kind missing from its scheme fails
// the build rather than the first leadership term.
func Validate(meters []Meter, scheme *runtime.Scheme) error {
	if len(meters) == 0 {
		return errors.New("usage: at least one meter is required")
	}
	seen := map[string]bool{}
	for _, m := range meters {
		if m.Name == "" || len(m.Kinds) == 0 {
			return fmt.Errorf("usage: meter %q needs a name and at least one kind", m.Name)
		}
		if seen[m.Name] {
			return fmt.Errorf("usage: meter %q declared twice", m.Name)
		}
		seen[m.Name] = true
		for _, kind := range m.Kinds {
			if _, err := listFactory(kind, scheme); err != nil {
				return fmt.Errorf("usage: meter %s: %w", m.Name, err)
			}
		}
	}
	return nil
}

// listFactory returns a constructor for the list type that counts kind.
func listFactory(kind client.Object, scheme *runtime.Scheme) (func() client.ObjectList, error) {
	if u, ok := kind.(*unstructured.Unstructured); ok {
		gvk := u.GroupVersionKind()
		if gvk.Empty() {
			return nil, errors.New("unstructured kind has no GroupVersionKind")
		}
		return func() client.ObjectList {
			l := &unstructured.UnstructuredList{}
			l.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
			return l
		}, nil
	}
	gvk, err := apiutil.GVKForObject(kind, scheme)
	if err != nil {
		return nil, err
	}
	listGVK := gvk.GroupVersion().WithKind(gvk.Kind + "List")
	if _, err := scheme.New(listGVK); err != nil {
		return nil, fmt.Errorf("%s has no list type in the scheme: %w", gvk.Kind, err)
	}
	return func() client.ObjectList {
		obj, _ := scheme.New(listGVK)
		return obj.(client.ObjectList)
	}, nil
}

// emitter is the per-meter reconciler.
type emitter struct {
	meter       string
	granularity time.Duration
	logger      logr.Logger
	gate        *gate
	lists       []func() client.ObjectList
	now         func() time.Time
	// list fills l from the cluster's cache; gone reports a workspace the
	// manager no longer serves, which ends the series.
	list  func(ctx context.Context, cluster multicluster.ClusterName, l client.ObjectList) (gone bool, err error)
	write func(ctx context.Context, record *unstructured.Unstructured) error
}

// Reconcile re-counts one workspace and states the current window.
func (e *emitter) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := e.logger.WithValues("subject", req.ClusterName)
	count := 0
	for _, newList := range e.lists {
		l := newList()
		gone, err := e.list(ctx, req.ClusterName, l)
		if gone {
			logger.V(2).Info("workspace no longer served; usage series ends")
			return ctrl.Result{}, nil
		}
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("counting %s in %s: %w", e.meter, req.ClusterName, err)
		}
		items, err := meta.ExtractList(l)
		if err != nil {
			return ctrl.Result{}, err
		}
		count += len(items)
	}
	if !e.gate.ready(ctx) {
		return ctrl.Result{RequeueAfter: gateRetry}, nil
	}
	now := e.now()
	start, end := AlignWindow(now, e.granularity)
	if err := e.write(ctx, Record(string(req.ClusterName), e.meter, start, end, count, now)); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			// The resource type is gone (apply creates, so not-found is never
			// the object) or the permission was withdrawn: back to probing.
			e.gate.lost()
			return ctrl.Result{RequeueAfter: gateRetry}, nil
		}
		return ctrl.Result{}, fmt.Errorf("writing %s usage for %s: %w", e.meter, req.ClusterName, err)
	}
	logger.V(2).Info("usage stated", "quantity", count, "window", start.Format(time.RFC3339))
	if count == 0 {
		// Zero is stated once. The level stays zero without further records,
		// and the next create in this workspace re-opens the series.
		return ctrl.Result{}, nil
	}
	// Re-state at the next window, so a limit always reads a fresh level.
	return ctrl.Result{RequeueAfter: end.Sub(now) + rand.N(maxJitter)}, nil
}

// Record builds the UsageRecord for one series and window.
func Record(subjectCluster, meter string, start, end time.Time, quantity int, observedAt time.Time) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(GVR.GroupVersion().String())
	u.SetKind("UsageRecord")
	u.SetName(RecordName(subjectCluster, meter, start, end))
	u.SetLabels(map[string]string{LabelMeter: meter, LabelSubject: subjectCluster})
	_ = unstructured.SetNestedMap(u.Object, map[string]any{
		"subjectCluster": subjectCluster,
		"meter":          meter,
		"window": map[string]any{
			"start": start.UTC().Format(time.RFC3339),
			"end":   end.UTC().Format(time.RFC3339),
		},
		"quantity":   strconv.Itoa(quantity),
		"observedAt": observedAt.UTC().Format(time.RFC3339),
	}, "spec")
	return u
}

// applyRecord is the server-side-apply upsert into the provider workspace.
func applyRecord(res dynamic.ResourceInterface) func(context.Context, *unstructured.Unstructured) error {
	return func(ctx context.Context, record *unstructured.Unstructured) error {
		data, err := record.MarshalJSON()
		if err != nil {
			return err
		}
		force := true
		_, err = res.Patch(ctx, record.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: FieldManager, Force: &force})
		return err
	}
}

// gate answers whether the UsageRecord resource is served in the provider
// workspace, probing at most once a minute while it is not. One gate is
// shared by every meter of a provider.
type gate struct {
	res    dynamic.ResourceInterface
	logger logr.Logger
	now    func() time.Time

	mu        sync.Mutex
	available bool
	checked   time.Time
	reported  bool
}

func (g *gate) ready(ctx context.Context) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.available {
		return true
	}
	now := g.now()
	if !g.checked.IsZero() && now.Sub(g.checked) < gateRetry {
		return false
	}
	g.checked = now
	_, err := g.res.List(ctx, metav1.ListOptions{Limit: 1})
	switch {
	case err == nil:
		g.available, g.reported = true, false
		g.logger.Info("metering API available in the provider workspace; emitting usage")
		return true
	case apierrors.IsNotFound(err):
		if !g.reported {
			g.logger.Info("metering API not bound in the provider workspace; usage emission paused until it is")
			g.reported = true
		}
		return false
	case apierrors.IsForbidden(err):
		// Bound, but this identity may not write records: the provider runs
		// as something other than the hub-provisioned `provider` account, or
		// the role predates the usagerecords rule. A deployment fact, said once.
		if !g.reported {
			g.logger.Info("metering API bound but not permitted for this identity; usage emission paused until it is", "reason", err.Error())
			g.reported = true
		}
		return false
	default:
		g.logger.Error(err, "probing the metering API")
		return false
	}
}

// lost records that a write found the resource gone; the next reconcile
// probes again.
func (g *gate) lost() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.available, g.checked = false, time.Time{}
}
