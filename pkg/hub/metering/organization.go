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

package metering

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

const (
	// Reporter is the hub's name on the MembershipReports it writes. Each
	// reporter owns its own report per account; metering unions them.
	Reporter = "railgrid-hub"
	// FieldManager is the hub's server-side-apply manager in the metering
	// workspace.
	FieldManager = "railgrid-hub"

	// LabelOrganization marks the hub-owned metering objects of an
	// Organization with its name.
	LabelOrganization = "railgrid.ai/organization"
	// LabelPlanFromSpec marks an Entitlement whose plan the hub wrote from
	// Organization.spec.plan. Clearing spec.plan later puts such an
	// Entitlement back on the default plan; an Entitlement without the label
	// carries a plan an admin set directly and is left alone.
	LabelPlanFromSpec = "railgrid.ai/plan-from-spec"

	// membershipRefresh paces the re-list of an organization's workspaces.
	// Child workspaces are created by the hub itself (createWorkspace) and
	// are not watched by this single-cluster manager; until a Workspace watch
	// through the core export feeds this reconciler, the report follows them
	// on this cadence.
	membershipRefresh = 5 * time.Minute
	// notInstalledRetry is how long to wait for metering to appear before
	// looking at an organization again.
	notInstalledRetry = time.Minute
)

var (
	gvrAccounts          = schema.GroupVersionResource{Group: "metering.contrib.kcp.io", Version: "v1alpha1", Resource: "accounts"}
	gvrEntitlements      = schema.GroupVersionResource{Group: "metering.contrib.kcp.io", Version: "v1alpha1", Resource: "entitlements"}
	gvrMembershipReports = schema.GroupVersionResource{Group: "metering.contrib.kcp.io", Version: "v1alpha1", Resource: "membershipreports"}
	gvrWorkspaces        = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
)

// Installed answers whether metering is discovered; Discovery implements it
// and tests substitute a constant.
type Installed interface {
	Current() (Endpoints, bool)
}

// OrganizationOptions configures the reconciler.
type OrganizationOptions struct {
	// Discovery gates the work: no metering, nothing is written.
	Discovery Installed
	// Metering is a client scoped to the metering workspace.
	Metering dynamic.Interface
	// Workspaces returns a client scoped to a kcp workspace path, for reading
	// an organization's Workspace objects.
	Workspaces func(path string) (dynamic.Interface, error)
	// DefaultPlan is the Plan an organization's Entitlement names while
	// Organization.spec.plan is empty.
	DefaultPlan string
	Logger      klog.Logger
}

// OrganizationReconciler gives every Organization its own billing objects in
// the metering workspace: an Account and a MembershipReport the hub owns and
// keeps current, and an Entitlement whose plan follows Organization.spec.plan
// (the default plan while that is empty). The Entitlement is otherwise left
// alone: its overrides, and a plan an admin set on it directly, are never
// reset by a resync. When an Organization is gone, its three objects go too.
//
// One account per organization is the design (docs/organizations.md O-5): the
// organization is the tenancy boundary everywhere else in the hub and so is
// the billing unit too. How many free organizations one person may hold is a
// cap the hub applies when an organization is created, not something metering
// attributes to a user.
type OrganizationReconciler struct {
	client.Client
	opts OrganizationOptions
}

// SetupOrganizationReconciler registers the reconciler with the hub's
// organization manager (the users workspace, where Organizations live).
func SetupOrganizationReconciler(mgr manager.Manager, opts OrganizationOptions) error {
	if opts.Discovery == nil || opts.Metering == nil || opts.Workspaces == nil {
		return fmt.Errorf("metering organization reconciler: Discovery, Metering and Workspaces are required")
	}
	if opts.DefaultPlan == "" {
		opts.DefaultPlan = "free"
	}
	r := &OrganizationReconciler{Client: mgr.GetClient(), opts: opts}
	return ctrl.NewControllerManagedBy(mgr).
		Named("metering-organization").
		For(&tenancyv1alpha1.Organization{}).
		Complete(r)
}

// WorkspaceClients returns a Workspaces func over base, caching one dynamic
// client per path.
func WorkspaceClients(base *rest.Config) func(path string) (dynamic.Interface, error) {
	host := base.Host
	if i := strings.Index(host, "/clusters/"); i != -1 {
		host = host[:i]
	}
	host = strings.TrimRight(host, "/")
	var mu sync.Mutex
	cache := map[string]dynamic.Interface{}
	return func(path string) (dynamic.Interface, error) {
		mu.Lock()
		defer mu.Unlock()
		if c, ok := cache[path]; ok {
			return c, nil
		}
		cfg := rest.CopyConfig(base)
		cfg.Host = host + "/clusters/" + path
		c, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return nil, err
		}
		cache[path] = c
		return c, nil
	}
}

// MeteringClient returns a dynamic client scoped to the metering workspace.
func MeteringClient(base *rest.Config, workspace string) (dynamic.Interface, error) {
	return WorkspaceClients(base)(workspace)
}

// Reconcile is the per-Organization step.
func (r *OrganizationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.opts.Logger.WithValues("organization", req.Name)
	if _, ok := r.opts.Discovery.Current(); !ok {
		return ctrl.Result{RequeueAfter: notInstalledRetry}, nil
	}
	org := &tenancyv1alpha1.Organization{}
	if err := r.Get(ctx, req.NamespacedName, org); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.remove(ctx, req.Name)
		}
		return ctrl.Result{}, err
	}
	if org.Status.WorkspacePath == "" {
		// Bootstrap has not provisioned the workspace yet; its status update
		// re-triggers this reconciler.
		return ctrl.Result{}, nil
	}
	members, err := r.members(ctx, org)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.applyAccount(ctx, org); err != nil {
		return ctrl.Result{}, fmt.Errorf("account: %w", err)
	}
	if err := r.ensureEntitlement(ctx, org, logger); err != nil {
		return ctrl.Result{}, fmt.Errorf("entitlement: %w", err)
	}
	if err := r.applyMembership(ctx, org.Name, members); err != nil {
		return ctrl.Result{}, fmt.Errorf("membership report: %w", err)
	}
	return ctrl.Result{RequeueAfter: membershipRefresh}, nil
}

// members lists the organization's workspace and every child workspace as
// {cluster, path}. Edge workspaces (children of children) are not user
// intent and are left out, matching the design.
func (r *OrganizationReconciler) members(ctx context.Context, org *tenancyv1alpha1.Organization) ([]map[string]any, error) {
	path := org.Status.WorkspacePath
	i := strings.LastIndex(path, ":")
	if i <= 0 {
		return nil, fmt.Errorf("organization workspace path %q has no parent", path)
	}
	parent, name := path[:i], path[i+1:]
	parentClient, err := r.opts.Workspaces(parent)
	if err != nil {
		return nil, err
	}
	orgWS, err := parentClient.Resource(gvrWorkspaces).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get organization workspace %s: %w", path, err)
	}
	cluster, _, _ := unstructured.NestedString(orgWS.Object, "spec", "cluster")
	if cluster == "" {
		return nil, fmt.Errorf("organization workspace %s has no cluster yet", path)
	}
	members := []map[string]any{{"cluster": cluster, "path": path}}
	orgClient, err := r.opts.Workspaces(path)
	if err != nil {
		return nil, err
	}
	children, err := orgClient.Resource(gvrWorkspaces).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list workspaces of %s: %w", path, err)
	}
	for _, ws := range children.Items {
		c, _, _ := unstructured.NestedString(ws.Object, "spec", "cluster")
		if c == "" {
			continue
		}
		members = append(members, map[string]any{"cluster": c, "path": path + ":" + ws.GetName()})
	}
	return members, nil
}

// applyAccount server-side-applies the organization's Account: its display
// name follows the organization's.
func (r *OrganizationReconciler) applyAccount(ctx context.Context, org *tenancyv1alpha1.Organization) error {
	displayName := org.Spec.DisplayName
	if displayName == "" {
		displayName = org.Name
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("Account")
	u.SetName(org.Name)
	u.SetLabels(map[string]string{LabelOrganization: org.Name})
	_ = unstructured.SetNestedMap(u.Object, map[string]any{"displayName": displayName}, "spec")
	return applyObject(ctx, r.opts.Metering.Resource(gvrAccounts), u)
}

// ensureEntitlement creates the organization's Entitlement when there is
// none, on spec.plan or else the default plan, and afterwards keeps its plan
// equal to spec.plan while that is set. Clearing spec.plan returns an
// Entitlement whose plan the hub wrote (LabelPlanFromSpec) to the default
// plan. A plan an admin set directly on the Entitlement while spec.plan is
// empty is never touched, and overrides are preserved throughout.
func (r *OrganizationReconciler) ensureEntitlement(ctx context.Context, org *tenancyv1alpha1.Organization, logger klog.Logger) error {
	res := r.opts.Metering.Resource(gvrEntitlements)
	want := org.Spec.Plan
	if want == "" {
		want = r.opts.DefaultPlan
	}
	ent, err := res.Get(ctx, org.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
		u.SetKind("Entitlement")
		u.SetName(org.Name)
		labels := map[string]string{LabelOrganization: org.Name}
		if org.Spec.Plan != "" {
			labels[LabelPlanFromSpec] = "true"
			labels[PlanSourceLabel] = PlanSourceOperator
		}
		u.SetLabels(labels)
		_ = unstructured.SetNestedMap(u.Object, map[string]any{"account": org.Name, "plan": want}, "spec")
		if _, err := res.Create(ctx, u, metav1.CreateOptions{FieldManager: FieldManager}); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return err
		}
		logger.Info("organization placed on a plan", "plan", want)
		return nil
	}
	current, _, _ := unstructured.NestedString(ent.Object, "spec", "plan")
	labels := ent.GetLabels()
	fromSpec := labels[LabelPlanFromSpec] == "true"
	switch {
	case org.Spec.Plan != "":
		if current == org.Spec.Plan && fromSpec && labels[PlanSourceLabel] == PlanSourceOperator {
			return nil
		}
	case !fromSpec:
		// spec.plan is empty and the hub did not write this plan: it is an
		// admin's own, set on the Entitlement directly. Leave it.
		return nil
	}
	if err := unstructured.SetNestedField(ent.Object, want, "spec", "plan"); err != nil {
		return err
	}
	if labels == nil {
		labels = map[string]string{}
	}
	if org.Spec.Plan != "" {
		labels[LabelPlanFromSpec] = "true"
		// Tell the deployment's billing reconciler the plan is an admin's:
		// a subscription change must not overwrite it.
		labels[PlanSourceLabel] = PlanSourceOperator
	} else {
		delete(labels, LabelPlanFromSpec)
		delete(labels, PlanSourceLabel)
	}
	ent.SetLabels(labels)
	if _, err := res.Update(ctx, ent, metav1.UpdateOptions{FieldManager: FieldManager}); err != nil {
		return err
	}
	logger.Info("organization entitlement follows spec.plan", "plan", want)
	return nil
}

// applyMembership writes the hub's report for the organization into the
// organization's own account, only when the account or the member set
// changed, so a resync is not a write.
func (r *OrganizationReconciler) applyMembership(ctx context.Context, orgName string, members []map[string]any) error {
	res := r.opts.Metering.Resource(gvrMembershipReports)
	name := Reporter + "." + orgName
	if existing, err := res.Get(ctx, name, metav1.GetOptions{}); err == nil {
		current, _, _ := unstructured.NestedSlice(existing.Object, "spec", "members")
		currentAccount, _, _ := unstructured.NestedString(existing.Object, "spec", "account")
		if currentAccount == orgName && reflect.DeepEqual(normalize(current), normalize(toAny(members))) {
			return nil
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("MembershipReport")
	u.SetName(name)
	u.SetLabels(map[string]string{LabelOrganization: orgName})
	_ = unstructured.SetNestedField(u.Object, map[string]any{
		"account":    orgName,
		"reporter":   Reporter,
		"observedAt": time.Now().UTC().Format(time.RFC3339),
		"members":    toAny(members),
	}, "spec")
	return applyObject(ctx, res, u)
}

// remove deletes the organization's objects; absence is success.
func (r *OrganizationReconciler) remove(ctx context.Context, orgName string) error {
	for _, d := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{gvrMembershipReports, Reporter + "." + orgName},
		{gvrEntitlements, orgName},
		{gvrAccounts, orgName},
	} {
		if err := r.opts.Metering.Resource(d.gvr).Delete(ctx, d.name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %s %s: %w", d.gvr.Resource, d.name, err)
		}
	}
	return nil
}

// applyObject is the server-side apply used for the hub-owned objects; a
// variable so tests, whose fake clients do not implement apply patches, can
// substitute create-or-update.
var applyObject = apply

func apply(ctx context.Context, res dynamic.ResourceInterface, u *unstructured.Unstructured) error {
	data, err := u.MarshalJSON()
	if err != nil {
		return err
	}
	force := true
	_, err = res.Patch(ctx, u.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: FieldManager, Force: &force})
	return err
}

func toAny(members []map[string]any) []any {
	out := make([]any, 0, len(members))
	for _, m := range members {
		out = append(out, map[string]any(m))
	}
	return out
}

// normalize reduces a members slice to comparable {cluster path} pairs,
// ignoring order and the labels a report may carry.
func normalize(members []any) map[string]bool {
	out := map[string]bool{}
	for _, m := range members {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		c, _ := mm["cluster"].(string)
		p, _ := mm["path"].(string)
		out[c+" "+p] = true
	}
	return out
}
