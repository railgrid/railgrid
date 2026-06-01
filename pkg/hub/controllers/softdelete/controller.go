/*
Copyright 2026 The Faros Authors.

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

// Package softdelete implements roadmap step 8: the soft-delete
// reconciler that honours the 30-day grace window from
// docs/organizations.md decisions O-8 (User delete) and O-13 (Org +
// Workspace delete).
//
// One controller, three reconcilers:
//
//   - User: watches User CRs; when status.deletionRequestedAt is set
//     and the grace window has elapsed, drives the User cascade
//     (personal Org → UMI → User CR).
//
//   - Organization: watches Organization CRs; when
//     status.deletionRequestedAt is set and the grace window has
//     elapsed, drives the Org cascade (child Workspaces → in-Org
//     Memberships → Org workspace → Org CR), stripping the org-scope
//     UMI entries of every member as it goes.
//
//   - Workspace: kcp Workspaces aren't in our scheme, so this branch
//     polls (default every minute) — lists Org workspaces, lists each
//     Org's child Workspaces, reads the annotation
//     tenancy.kedge.faros.sh/deletion-requested-at and, once the grace
//     window expires, drives the Workspace cascade (kcp Workspace +
//     workspace-scope UMI rows).
//
// Inside the grace window the reconciler does NOT delete anything —
// it surfaces a DeletionInProgress condition on the parent CR and
// marks the corresponding UserMembershipIndex rows with
// softDeletedAt so the portal switcher (step 10) hides them. Undelete
// (the deletionRequestedAt / annotation being cleared back to nil)
// clears the marker and the condition; nothing brings back resources
// that the post-window cascade has already removed.
package softdelete

import (
	"context"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

const (
	// GracePeriod is the soft-delete window per O-8 / O-13. Hardcoded
	// (not flag-tunable) so the contract surfaced to users is a single
	// number; matches the wording in docs/organizations.md.
	GracePeriod = 30 * 24 * time.Hour

	// workspacePollInterval is how often the Workspace branch lists
	// Org workspaces, then their child Workspaces, looking for the
	// soft-delete annotation. kcp Workspaces aren't in our scheme so we
	// can't watch them via the cached client; a coarse poll is fine
	// because the cascade clock runs in days.
	workspacePollInterval = 1 * time.Minute

	userControllerName      = "softdelete-user"
	orgControllerName       = "softdelete-organization"
	workspaceControllerName = "softdelete-workspace"
)

// Condition reason constants shared across the three reconcilers.
const (
	// reasonWithinGracePeriod marks the parent CR's DeletionInProgress
	// condition while the soft-delete timestamp is set but the grace
	// window has not elapsed. The portal hides the entry via the UMI
	// softDeletedAt marker that this reconcile also writes.
	reasonWithinGracePeriod = "WithinGracePeriod"

	// reasonCascadeRunning marks the condition once the grace window
	// has elapsed and the controller is actively tearing the resource
	// down. Stays set until the parent CR itself is gone (the User and
	// Org reconciles are then NotFound and exit).
	reasonCascadeRunning = "CascadeRunning"

	// reasonAwaitingDependents marks the condition while the reconciler
	// is waiting on a dependent kind to finish its cascade — most
	// commonly the User reconciler waiting on its personal Org's own
	// cascade to remove the Organization CR.
	reasonAwaitingDependents = "AwaitingDependents"

	// reasonSoleAdminBlocked marks a User cascade that cannot proceed
	// because the user is the sole admin of a non-personal Org. The
	// auto-promote question (open question in docs/organizations.md)
	// is deferred; for now the cascade halts and surfaces the reason
	// so an operator can intervene.
	reasonSoleAdminBlocked = "SoleAdminBlocked"
)

// Reconciler is the shared receiver for the three soft-delete
// reconcile entry points. The User and Organization branches are
// watch-driven via controller-runtime; the Workspace branch is
// poll-driven via runWorkspacePoll(ctx).
type Reconciler struct {
	client      client.Client
	provisioner Provisioner
	// now returns the current time; swappable for tests.
	now func() time.Time
}

// SetupWithManager registers the User and Organization watches with
// mgr and kicks off the Workspace-branch poll goroutine. The Workspace
// branch needs the manager's lifecycle (it stops when the manager's
// context cancels) which is why we run it as a Runnable instead of an
// independent goroutine in server.go.
func SetupWithManager(mgr manager.Manager, provisioner Provisioner) error {
	r := &Reconciler{
		client:      mgr.GetClient(),
		provisioner: provisioner,
		now:         time.Now,
	}
	klog.Info("Registering soft-delete reconciler")

	if err := builder.ControllerManagedBy(mgr).
		Named(userControllerName).
		For(&tenancyv1alpha1.User{}).
		Complete(reconcile.Func(r.reconcileUser)); err != nil {
		return fmt.Errorf("setting up user soft-delete controller: %w", err)
	}

	if err := builder.ControllerManagedBy(mgr).
		Named(orgControllerName).
		For(&tenancyv1alpha1.Organization{}).
		Complete(reconcile.Func(r.reconcileOrganization)); err != nil {
		return fmt.Errorf("setting up organization soft-delete controller: %w", err)
	}

	return mgr.Add(manager.RunnableFunc(r.runWorkspacePoll))
}

// NewManager constructs a controller-runtime manager bound to the
// users workspace's rest.Config (typically the same one the
// organization bootstrap controller uses). Separated from the
// bootstrap controller's manager so soft-delete restarts don't take
// the bootstrap workqueue down.
func NewManager(cfg *rest.Config, scheme *runtime.Scheme) (manager.Manager, error) {
	return manager.New(cfg, manager.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		HealthProbeBindAddress: "0",
	})
}

// runWorkspacePoll is the manager Runnable that drives the
// Workspace-branch reconcile loop. Exits cleanly when ctx is
// cancelled.
func (r *Reconciler) runWorkspacePoll(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName(workspaceControllerName)
	logger.Info("Starting soft-delete workspace poll", "interval", workspacePollInterval.String())

	ticker := time.NewTicker(workspacePollInterval)
	defer ticker.Stop()

	// Run one iteration immediately so cascade timers don't have to
	// wait for the first tick after a hub restart.
	if err := r.reconcileAllWorkspaces(ctx); err != nil {
		logger.Error(err, "Initial workspace soft-delete sweep failed; will retry on next tick")
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.reconcileAllWorkspaces(ctx); err != nil {
				logger.Error(err, "Workspace soft-delete sweep failed; will retry on next tick")
			}
		}
	}
}

// setCondition is the local upsert used by all three reconcilers. A
// copy of the helper in pkg/hub/controllers/organization to keep the
// soft-delete package self-contained; if a third caller appears we'll
// factor a shared pkg/hub/conditions.
func setCondition(conds *[]metav1.Condition, c metav1.Condition, observedGeneration int64) bool {
	c.LastTransitionTime = metav1.Now()
	c.ObservedGeneration = observedGeneration
	for i, existing := range *conds {
		if existing.Type != c.Type {
			continue
		}
		if existing.Status == c.Status &&
			existing.Reason == c.Reason &&
			existing.Message == c.Message &&
			existing.ObservedGeneration == c.ObservedGeneration {
			return false
		}
		if existing.Status == c.Status {
			c.LastTransitionTime = existing.LastTransitionTime
		}
		(*conds)[i] = c
		return true
	}
	*conds = append(*conds, c)
	return true
}

// removeCondition drops a condition by type if present. Used on
// undelete to scrub DeletionInProgress entirely rather than leave a
// Cancelled marker on long-lived CRs.
func removeCondition(conds *[]metav1.Condition, condType string) bool {
	for i, c := range *conds {
		if c.Type != condType {
			continue
		}
		*conds = append((*conds)[:i], (*conds)[i+1:]...)
		return true
	}
	return false
}

// gracePeriodFor returns the absolute time at which a soft-delete
// initiated at t enters cascade. Centralised so tests can override
// GracePeriod if we ever go that direction; today there's only one
// call site.
func gracePeriodFor(t time.Time) time.Time {
	return t.Add(GracePeriod)
}

// fetchOrganization is a small wrapper around client.Get returning
// (nil, nil) on NotFound so callers can branch cleanly.
func (r *Reconciler) fetchOrganization(ctx context.Context, name string) (*tenancyv1alpha1.Organization, error) {
	var org tenancyv1alpha1.Organization
	if err := r.client.Get(ctx, types.NamespacedName{Name: name}, &org); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting Organization %q: %w", name, err)
	}
	return &org, nil
}

// ===== test seam: per-iteration workspace lookups =====

// workspaceCascadeState memoises the per-iteration lookups in the
// Workspace-branch sweep so we don't re-list Memberships once per
// child workspace. Stored on the receiver via runWorkspacePoll's local
// scope; not used outside that goroutine, so no synchronisation.
type workspaceCascadeState struct {
	orgMembersOnce sync.Once
	orgMembers     map[string][]string // orgUUID → []userName
}

// orgMembers returns the cached list of members for orgUUID, computing
// it once per sweep. Used so a single sweep over an Org with multiple
// soft-deleted child workspaces only hits the API once.
func (s *workspaceCascadeState) members(ctx context.Context, r *Reconciler, orgUUID string) ([]string, error) {
	s.orgMembersOnce.Do(func() {
		s.orgMembers = make(map[string][]string)
	})
	if cached, ok := s.orgMembers[orgUUID]; ok {
		return cached, nil
	}
	members, err := r.provisioner.ListOrgMemberships(ctx, orgUUID)
	if err != nil {
		return nil, err
	}
	s.orgMembers[orgUUID] = members
	return members, nil
}
