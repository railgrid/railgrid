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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

const (
	// userAccountPrefix turns a User name into its metering Account name.
	// The metering `organizations` MeteringConfig prefixes the created-by
	// label with the same string, so the two meet on one account.
	userAccountPrefix = "user-"
	// LabelUser marks the hub-owned metering objects that belong to a User.
	LabelUser = "railgrid.ai/user"
)

// UserAccountName is the metering Account that pools everything a User owns:
// their personal Organization and every Organization they create that has
// not been given a plan of its own.
func UserAccountName(user string) string {
	return userAccountPrefix + user
}

// UserOptions configures the user reconciler.
type UserOptions struct {
	// Discovery gates the work: no metering, nothing is written.
	Discovery Installed
	// Metering is a client scoped to the metering workspace.
	Metering dynamic.Interface
	// DefaultPlan is the Plan a new user's Entitlement names.
	DefaultPlan string
	Logger      klog.Logger
}

// UserReconciler gives every User a metering Account and an Entitlement on
// the default plan. The Account is what the user's organizations pool into
// (see OwnerAccount), so creating more organizations never multiplies the
// free allowances; the `organizations` limit on this Entitlement is what
// caps how many they may create. The Entitlement is create-only: the plan an
// admin or billing sets later is never reset. When a User is gone, both
// objects go too.
type UserReconciler struct {
	client.Client
	opts UserOptions
}

// SetupUserReconciler registers the reconciler with the hub's organization
// manager (the users workspace, where Users live).
func SetupUserReconciler(mgr manager.Manager, opts UserOptions) error {
	if opts.Discovery == nil || opts.Metering == nil {
		return fmt.Errorf("metering user reconciler: Discovery and Metering are required")
	}
	if opts.DefaultPlan == "" {
		opts.DefaultPlan = "free"
	}
	r := &UserReconciler{Client: mgr.GetClient(), opts: opts}
	return ctrl.NewControllerManagedBy(mgr).
		Named("metering-user").
		For(&tenancyv1alpha1.User{}).
		Complete(r)
}

// Reconcile is the per-User step.
func (r *UserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.opts.Logger.WithValues("user", req.Name)
	if _, ok := r.opts.Discovery.Current(); !ok {
		return ctrl.Result{RequeueAfter: notInstalledRetry}, nil
	}
	user := &tenancyv1alpha1.User{}
	if err := r.Get(ctx, req.NamespacedName, user); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.remove(ctx, req.Name)
		}
		return ctrl.Result{}, err
	}
	account := UserAccountName(user.Name)
	displayName := user.Spec.Name
	if displayName == "" {
		displayName = user.Spec.Email
	}
	if displayName == "" {
		displayName = user.Name
	}
	if err := applyAccountObject(ctx, r.opts.Metering, account, displayName, map[string]string{LabelUser: user.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("account: %w", err)
	}
	created, err := ensureEntitlementObject(ctx, r.opts.Metering, account, r.opts.DefaultPlan, map[string]string{LabelUser: user.Name})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("entitlement: %w", err)
	}
	if created {
		logger.Info("user placed on the default plan", "plan", r.opts.DefaultPlan, "account", account)
	}
	return ctrl.Result{}, nil
}

// remove deletes the user's objects; absence is success.
func (r *UserReconciler) remove(ctx context.Context, user string) error {
	return deleteAccountObjects(ctx, r.opts.Metering, UserAccountName(user))
}

// applyAccountObject server-side-applies an Account.
func applyAccountObject(ctx context.Context, store dynamic.Interface, name, displayName string, labels map[string]string) error {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("Account")
	u.SetName(name)
	u.SetLabels(labels)
	_ = unstructured.SetNestedMap(u.Object, map[string]any{"displayName": displayName}, "spec")
	return applyObject(ctx, store.Resource(gvrAccounts), u)
}

// ensureEntitlementObject creates the Entitlement on plan when there is none;
// an existing one is left exactly as it is. Returns whether it created.
func ensureEntitlementObject(ctx context.Context, store dynamic.Interface, account, plan string, labels map[string]string) (bool, error) {
	res := store.Resource(gvrEntitlements)
	if _, err := res.Get(ctx, account, metav1.GetOptions{}); err == nil {
		return false, nil
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("Entitlement")
	u.SetName(account)
	u.SetLabels(labels)
	_ = unstructured.SetNestedMap(u.Object, map[string]any{"account": account, "plan": plan}, "spec")
	if _, err := res.Create(ctx, u, metav1.CreateOptions{FieldManager: FieldManager}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// deleteAccountObjects removes an Account and its Entitlement; absence is
// success.
func deleteAccountObjects(ctx context.Context, store dynamic.Interface, account string) error {
	for _, gvr := range []schemaGVR{gvrEntitlements, gvrAccounts} {
		if err := store.Resource(gvr).Delete(ctx, account, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %s %s: %w", gvr.Resource, account, err)
		}
	}
	return nil
}
