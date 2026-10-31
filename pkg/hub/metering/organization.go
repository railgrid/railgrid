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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

const (
	// Reporter is the hub's name on the MembershipReports it writes. Each
	// reporter owns its own report per account; metering unions them.
	Reporter = "railgrid-hub"
	// FieldManager is the hub's server-side-apply manager in the metering
	// workspace.
	FieldManager = "railgrid-hub"

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

// schemaGVR is the GVR type the hub-owned object helpers iterate over.
type schemaGVR = schema.GroupVersionResource

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
	// DefaultPlan is the Plan a new organization's Entitlement names.
	DefaultPlan string
	Logger      klog.Logger
}

// OrganizationReconciler keeps every Organization's workspaces reported to
// the metering Account that pays for them (OwnerAccount): by default the
// Account of the User who created the organization, so one user's
// organizations pool into one set of allowances; an Organization with
// spec.plan set gets an Account and an Entitlement of its own, with the
// Entitlement's plan kept in step with spec.plan while any overrides an admin
// added are preserved. The MembershipReport is hub-owned and kept current.
// When an Organization is gone, its report and any own objects go too.
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
		// A deleted User's organizations must move off the user's account
		// right away, not on the next membership refresh: in between, their
		// reports would name an account the user reconciler is about to
		// remove.
		Watches(&tenancyv1alpha1.User{}, handler.EnqueueRequestsFromMapFunc(r.organizationsCreatedBy),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc:  func(event.CreateEvent) bool { return false },
				UpdateFunc:  func(event.UpdateEvent) bool { return false },
				GenericFunc: func(event.GenericEvent) bool { return false },
			})).
		Complete(r)
}

// organizationsCreatedBy maps a User event to every Organization carrying its
// created-by label.
func (r *OrganizationReconciler) organizationsCreatedBy(ctx context.Context, obj client.Object) []ctrl.Request {
	orgs := &tenancyv1alpha1.OrganizationList{}
	if err := r.List(ctx, orgs, client.MatchingLabels{tenancyv1alpha1.OrganizationCreatorLabel: obj.GetName()}); err != nil {
		r.opts.Logger.Error(err, "listing organizations created by a deleted user", "user", obj.GetName())
		return nil
	}
	out := make([]ctrl.Request, 0, len(orgs.Items))
	for i := range orgs.Items {
		out = append(out, ctrl.Request{NamespacedName: types.NamespacedName{Name: orgs.Items[i].Name}})
	}
	return out
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
	account, own := OwnerAccount(org)
	if !own {
		// The creator's account only exists while the User does. An
		// organization whose creator was deleted (the User cascade removes
		// the personal org, not the organizations they created) would
		// otherwise report into an account with no Entitlement, which the
		// webhook treats as unlimited; keep it on an account of its own.
		exists, err := r.creatorExists(ctx, org)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !exists {
			logger.Info("organization keeps its own account: its creator no longer exists", "creator", org.Labels[tenancyv1alpha1.OrganizationCreatorLabel])
			account, own = org.Name, true
		}
	}
	if !own {
		// A per-org Entitlement an admin has already customized is intent
		// the hub must not discard: keep the org on its own account and ask
		// for spec.plan to make it explicit.
		customized, err := r.customizedOwnEntitlement(ctx, org.Name)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("entitlement: %w", err)
		}
		if customized {
			logger.Info("organization keeps its own account: its entitlement was customized by an admin; set spec.plan to make that explicit")
			account, own = org.Name, true
		}
	}
	if own {
		if err := r.applyAccount(ctx, org); err != nil {
			return ctrl.Result{}, fmt.Errorf("account: %w", err)
		}
		if err := r.ensureOwnEntitlement(ctx, org, logger); err != nil {
			return ctrl.Result{}, fmt.Errorf("entitlement: %w", err)
		}
	} else if err := r.dropOwnObjects(ctx, org.Name, logger); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.applyMembership(ctx, org.Name, account, members); err != nil {
		return ctrl.Result{}, fmt.Errorf("membership report: %w", err)
	}
	return ctrl.Result{RequeueAfter: membershipRefresh}, nil
}

// OwnerAccount names the metering Account an Organization's usage is pooled
// into and whether that account is the organization's own. An Organization
// with spec.plan set owns its account. Otherwise it belongs to the User who
// created it (the created-by label), which is also the account the metering
// `organizations` meter attributes the Organization object to. An
// Organization without a creator label cannot be attributed to anyone and
// keeps an account of its own.
func OwnerAccount(org *tenancyv1alpha1.Organization) (account string, own bool) {
	if org.Spec.Plan != "" {
		return org.Name, true
	}
	creator := org.Labels[tenancyv1alpha1.OrganizationCreatorLabel]
	if creator == "" {
		return org.Name, true
	}
	return UserAccountName(creator), false
}

// creatorExists reports whether the User named by the creator label still
// exists in the users workspace.
func (r *OrganizationReconciler) creatorExists(ctx context.Context, org *tenancyv1alpha1.Organization) (bool, error) {
	creator := org.Labels[tenancyv1alpha1.OrganizationCreatorLabel]
	if err := r.Get(ctx, types.NamespacedName{Name: creator}, &tenancyv1alpha1.User{}); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get creator %q: %w", creator, err)
	}
	return true, nil
}

// LabelPlanFromSpec marks a per-org Entitlement whose plan the hub wrote
// from Organization.spec.plan. Clearing spec.plan later must pool the
// organization back, so such an Entitlement is the hub's own, not an admin
// customization, as long as it carries no overrides.
const LabelPlanFromSpec = "railgrid.ai/plan-from-spec"

// customizedOwnEntitlement reports whether a per-org Entitlement exists that
// carries admin intent the hub must not discard: overrides, or a plan other
// than the default that the hub did not itself write from spec.plan.
func (r *OrganizationReconciler) customizedOwnEntitlement(ctx context.Context, orgName string) (bool, error) {
	ent, err := r.opts.Metering.Resource(gvrEntitlements).Get(ctx, orgName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	overrides, _, _ := unstructured.NestedSlice(ent.Object, "spec", "overrides")
	if len(overrides) > 0 {
		return true, nil
	}
	if ent.GetLabels()[LabelPlanFromSpec] == "true" {
		return false, nil
	}
	plan, _, _ := unstructured.NestedString(ent.Object, "spec", "plan")
	return plan != r.opts.DefaultPlan, nil
}

// ensureOwnEntitlement creates the organization's own Entitlement and, when
// spec.plan names a plan, keeps the Entitlement's plan equal to it (overrides
// are preserved). Without spec.plan the Entitlement is create-only on the
// default plan, as before.
func (r *OrganizationReconciler) ensureOwnEntitlement(ctx context.Context, org *tenancyv1alpha1.Organization, logger klog.Logger) error {
	plan := org.Spec.Plan
	labels := map[string]string{"railgrid.ai/organization": org.Name}
	if plan == "" {
		plan = r.opts.DefaultPlan
	} else {
		labels[LabelPlanFromSpec] = "true"
	}
	created, err := ensureEntitlementObject(ctx, r.opts.Metering, org.Name, plan, labels)
	if err != nil {
		return err
	}
	if created {
		logger.Info("organization placed on its own plan", "plan", plan)
		return nil
	}
	if org.Spec.Plan == "" {
		return nil
	}
	res := r.opts.Metering.Resource(gvrEntitlements)
	ent, err := res.Get(ctx, org.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	current, _, _ := unstructured.NestedString(ent.Object, "spec", "plan")
	entLabels := ent.GetLabels()
	if current == org.Spec.Plan && entLabels[LabelPlanFromSpec] == "true" {
		return nil
	}
	if err := unstructured.SetNestedField(ent.Object, org.Spec.Plan, "spec", "plan"); err != nil {
		return err
	}
	if entLabels == nil {
		entLabels = map[string]string{}
	}
	entLabels[LabelPlanFromSpec] = "true"
	ent.SetLabels(entLabels)
	if _, err := res.Update(ctx, ent, metav1.UpdateOptions{FieldManager: FieldManager}); err != nil {
		return err
	}
	logger.Info("organization entitlement follows spec.plan", "plan", org.Spec.Plan)
	return nil
}

// dropOwnObjects removes the per-org Account and Entitlement the hub created
// for an organization that is now pooled into its creator's account. Only
// uncustomized objects reach here (see customizedOwnEntitlement).
func (r *OrganizationReconciler) dropOwnObjects(ctx context.Context, orgName string, logger klog.Logger) error {
	if _, err := r.opts.Metering.Resource(gvrEntitlements).Get(ctx, orgName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			// Nothing to drop; the common case after the first pass.
			if _, err := r.opts.Metering.Resource(gvrAccounts).Get(ctx, orgName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
				return nil
			}
		} else {
			return fmt.Errorf("entitlement: %w", err)
		}
	}
	if err := deleteAccountObjects(ctx, r.opts.Metering, orgName); err != nil {
		return err
	}
	logger.Info("organization pooled into its creator's account; its own account and entitlement were removed")
	return nil
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

func (r *OrganizationReconciler) applyAccount(ctx context.Context, org *tenancyv1alpha1.Organization) error {
	displayName := org.Spec.DisplayName
	if displayName == "" {
		displayName = org.Name
	}
	return applyAccountObject(ctx, r.opts.Metering, org.Name, displayName, map[string]string{"railgrid.ai/organization": org.Name})
}

// applyMembership writes the hub's report for the organization to account,
// only when the account or the member set changed, so a resync is not a
// write.
func (r *OrganizationReconciler) applyMembership(ctx context.Context, orgName, account string, members []map[string]any) error {
	res := r.opts.Metering.Resource(gvrMembershipReports)
	name := Reporter + "." + orgName
	if existing, err := res.Get(ctx, name, metav1.GetOptions{}); err == nil {
		current, _, _ := unstructured.NestedSlice(existing.Object, "spec", "members")
		currentAccount, _, _ := unstructured.NestedString(existing.Object, "spec", "account")
		if currentAccount == account && reflect.DeepEqual(normalize(current), normalize(toAny(members))) {
			return nil
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("MembershipReport")
	u.SetName(name)
	u.SetLabels(map[string]string{"railgrid.ai/organization": orgName})
	_ = unstructured.SetNestedField(u.Object, map[string]any{
		"account":    account,
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
