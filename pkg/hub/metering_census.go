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

package hub

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	meteringv1alpha1 "github.com/kcp-dev/contrib-metering/sdk/apis/metering/v1alpha1"
	kcptenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// membershipReportGVR addresses the platform-only MembershipReport type, written
// through the metering-platform APIExport VW into the metering :platform workspace.
var membershipReportGVR = schema.GroupVersionResource{
	Group: meteringv1alpha1.GroupName, Version: "v1alpha1", Resource: "membershipreports",
}

// apiExportEndpointSliceGVR addresses APIExportEndpointSlices — read (in the
// metering workspace) to discover the metering-platform VW URL for report writes.
var apiExportEndpointSliceGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices",
}

const (
	censusExportSliceName           = "census.railgrid.ai"
	meteringPlatformExportSliceName = "metering-platform"
)

// startMeteringCensus starts the least-privileged, event-driven membership census.
// railgrid is the platform, so it owns reporting which workspaces belong to which
// billing account; contrib-metering stays topology-agnostic and folds these
// MembershipReports into its membership index.
//
// It is a multicluster-runtime controller over the census APIExport VW: it engages
// every org that bound census.railgrid.ai and watches tenancy.kcp.io/workspaces
// across them (via the permission claim). On any workspace event it recomputes the
// ONE org the event came from (req.ClusterName == the org's logical cluster == the
// account) and upserts that org's MembershipReport through the metering-platform VW.
//
// Credentials: the scoped census ServiceAccount (saConfig), NOT the hub kcp admin.
//
// Org lifecycle (an org being deleted → its report deleted) is deliberately NOT
// handled here: it belongs to the billing termination controller (the
// WorkspaceType terminator that flushes final usage on teardown). The census only
// tracks membership of live orgs.
func startMeteringCensus(ctx context.Context, saConfig *rest.Config, platformClusterID, reporter string, log logr.Logger) error {
	scheme := NewScheme()

	// The census endpointslice lives in the metering workspace; scope the provider
	// config there so apiexport.New can read it and discover the per-shard VW URLs.
	provCfg := rest.CopyConfig(saConfig)
	provCfg.Host = apiurl.KCPClusterURL(provCfg.Host, kcppaths.SystemMetering)

	provider, err := apiexport.New(provCfg, censusExportSliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("metering census: apiexport provider: %w", err)
	}
	mgr, err := mcmanager.New(provCfg, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return fmt.Errorf("metering census: multicluster manager: %w", err)
	}

	r := &censusReconciler{
		mgr:               mgr,
		saConfig:          saConfig,
		platformClusterID: platformClusterID,
		reporter:          reporter,
		log:               log,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("metering census: setup reconciler: %w", err)
	}

	go func() {
		log.Info("starting metering census controller", "export", censusExportSliceName)
		if err := mgr.Start(ctx); err != nil {
			log.Error(err, "metering census manager exited")
		}
	}()
	return nil
}

// censusReconciler recomputes one org's MembershipReport on every workspace event.
type censusReconciler struct {
	mgr               mcmanager.Manager
	saConfig          *rest.Config
	platformClusterID string
	reporter          string
	log               logr.Logger
}

// SetupWithManager watches tenancy.kcp.io/workspaces across every engaged org.
func (r *censusReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	return mcbuilder.ControllerManagedBy(mgr).
		Named("metering-census").
		For(&kcptenancyv1alpha1.Workspace{}).
		Complete(r)
}

// Reconcile rebuilds the report for the org the workspace event came from. The
// triggering object is ignored — we re-list the org's full membership so the
// report is the absolute current set (create AND delete are handled by re-listing).
func (r *censusReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	account := string(req.ClusterName) // the org's logical cluster id == the account
	if account == "" {
		return ctrl.Result{}, nil
	}
	logger := r.log.WithValues("account", account)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get cluster %s: %w", account, err)
	}
	var list kcptenancyv1alpha1.WorkspaceList
	if err := cl.GetClient().List(ctx, &list); err != nil {
		return ctrl.Result{}, fmt.Errorf("list workspaces in %s: %w", account, err)
	}

	members := membersForOrg(account, list.Items)
	if len(members) == 0 {
		// No resolvable workspaces this pass (org has none yet, or all just deleted).
		// Leave any existing report as-is — org teardown is the termination
		// controller's job, not ours.
		logger.V(3).Info("no workspaces resolved; skipping report")
		return ctrl.Result{}, nil
	}

	writer, err := r.platformWriter(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := upsertReport(ctx, writer, r.reporter, account, members); err != nil {
		return ctrl.Result{}, fmt.Errorf("upsert report for %s: %w", account, err)
	}
	logger.V(2).Info("membership report updated", "members", len(members))
	return ctrl.Result{}, nil
}

// membersForOrg builds the member set for one account: the org itself plus every
// child workspace. account is the org's logical cluster id; each child's own
// cluster + canonical path come from spec.cluster / spec.URL.
func membersForOrg(account string, workspaces []kcptenancyv1alpha1.Workspace) []meteringv1alpha1.MemberWorkspace {
	children := make([]meteringv1alpha1.MemberWorkspace, 0, len(workspaces))
	orgPath := ""
	for i := range workspaces {
		w := &workspaces[i]
		if w.Spec.URL == "" || w.Spec.Cluster == "" {
			continue
		}
		_, childPath := apiurl.SplitBaseAndCluster(w.Spec.URL)
		if childPath == "" || childPath == "default" {
			continue
		}
		if orgPath == "" {
			orgPath = parentPath(childPath)
		}
		children = append(children, meteringv1alpha1.MemberWorkspace{Cluster: w.Spec.Cluster, Path: childPath})
	}
	if orgPath == "" {
		return nil
	}
	// Org boundary workspace first, then its children.
	return append([]meteringv1alpha1.MemberWorkspace{{Cluster: account, Path: orgPath}}, children...)
}

// platformWriter returns a dynamic client that writes MembershipReports into the
// :platform workspace through the metering-platform VW (addressed by cluster id).
// Built fresh per call (a cheap endpointslice GET) so a shard/endpoint change is
// always reflected.
func (r *censusReconciler) platformWriter(ctx context.Context) (dynamic.Interface, error) {
	endpoints, err := r.vwEndpoints(ctx, meteringPlatformExportSliceName)
	if err != nil {
		return nil, err
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("metering-platform VW has no endpoints yet")
	}
	cfg := rest.CopyConfig(r.saConfig)
	cfg.Host = strings.TrimRight(endpoints[0], "/") + "/clusters/" + r.platformClusterID
	return dynamic.NewForConfig(cfg)
}

// vwEndpoints reads the named APIExportEndpointSlice in the metering workspace and
// returns its advertised per-shard VW URLs.
func (r *censusReconciler) vwEndpoints(ctx context.Context, sliceName string) ([]string, error) {
	cfg := rest.CopyConfig(r.saConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemMetering)
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metering client: %w", err)
	}
	slice, err := cl.Resource(apiExportEndpointSliceGVR).Get(ctx, sliceName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get endpointslice %s: %w", sliceName, err)
	}
	eps, _, _ := unstructured.NestedSlice(slice.Object, "status", "endpoints")
	urls := make([]string, 0, len(eps))
	for _, e := range eps {
		if m, ok := e.(map[string]interface{}); ok {
			if u, _ := m["url"].(string); u != "" {
				urls = append(urls, u)
			}
		}
	}
	return urls, nil
}

// upsertReport creates or updates the MembershipReport for one account. The name
// is deterministic per (reporter, account) so passes idempotently replace it.
func upsertReport(ctx context.Context, writer dynamic.Interface, reporter, account string, members []meteringv1alpha1.MemberWorkspace) error {
	name := reporter + "-" + account
	desired := &meteringv1alpha1.MembershipReport{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: meteringv1alpha1.MembershipReportSpec{
			Account:    account,
			Reporter:   reporter,
			Members:    members,
			ObservedAt: metav1.Now(),
		},
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desired)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	obj := &unstructured.Unstructured{Object: u}
	obj.SetAPIVersion(meteringv1alpha1.GroupName + "/v1alpha1")
	obj.SetKind("MembershipReport")

	existing, err := writer.Resource(membershipReportGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = writer.Resource(membershipReportGVR).Create(ctx, obj, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = writer.Resource(membershipReportGVR).Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// parentPath strips the last colon-separated segment from a kcp workspace path,
// e.g. root:railgrid:tenants:<org>:<child> -> root:railgrid:tenants:<org>.
func parentPath(path string) string {
	if i := strings.LastIndexByte(path, ':'); i > 0 {
		return path[:i]
	}
	return path
}
