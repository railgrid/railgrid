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

package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

// The GC pacing, stated plainly, because §10 asked for it to be stated:
//
// The owner objects are spread across EVERY tenant workspace and belong to
// several different providers' API groups. The hub has no informer over those
// groups — it would need one watch per (workspace × group), established and
// torn down as workspaces are enabled, for kinds whose CRDs arrive with a
// provider's APIExport. That is a cross-workspace owner watch, and it is not
// practical here.
//
// So this reconciler uses the sanctioned alternative from §10: a per-record
// re-check paced at the record's own token TTL. An identity is only useful
// while its holder keeps refreshing, and every refresh re-probes the owner
// synchronously in Ensure; the sweep below is what collects an identity whose
// holder STOPPED refreshing (the provider crashed, the agent was deleted
// while the process was down). The worst-case window between an owner's
// deletion and its identity's collection is therefore one TTL — one hour by
// default, ten minutes for workload identities — versus "never", which is
// what all three minters do today.
//
// A provider that wants immediate revocation does not wait for the sweep: it
// calls DELETE /api/identities/{name} from its own delete reconciler, which is
// what the edges and agents migrations do.

const (
	// DefaultSweepInterval is how often the reconciler walks the records. It
	// is well under the shortest TTL so a record is never missed by more than
	// one interval.
	DefaultSweepInterval = 2 * time.Minute

	// sweepPageSize bounds one list.
	sweepPageSize = 200
)

// Reconciler materializes ScopedIdentity records and garbage-collects the ones
// whose owner is gone.
type Reconciler struct {
	service  *Service
	interval time.Duration
	log      logr.Logger
	now      func() time.Time
}

// ReconcilerOptions configures a Reconciler.
type ReconcilerOptions struct {
	Service  *Service
	Interval time.Duration
	Logger   logr.Logger
}

// NewReconciler builds a Reconciler.
func NewReconciler(opts ReconcilerOptions) *Reconciler {
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultSweepInterval
	}
	now := time.Now
	if opts.Service != nil && opts.Service.now != nil {
		now = opts.Service.now
	}
	return &Reconciler{service: opts.Service, interval: interval, log: opts.Logger, now: now}
}

// Start runs the sweep until ctx is done. It is safe to run on every hub
// replica: every action it takes is idempotent, and two replicas collecting
// the same orphan both succeed.
func (r *Reconciler) Start(ctx context.Context) {
	if r == nil || r.service == nil {
		return
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if _, err := r.Sweep(ctx); err != nil && ctx.Err() == nil {
			r.log.Error(err, "scoped identity sweep failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SweepResult counts what one pass did. Returned for tests and logging.
type SweepResult struct {
	Examined    int
	Collected   int
	Rematerial  int
	Failed      int
	SkippedWait int
}

// Sweep walks every record once: it collects the ones whose owner is gone and
// re-materializes the ones whose rules have drifted or whose token window has
// lapsed.
func (r *Reconciler) Sweep(ctx context.Context) (SweepResult, error) {
	var result SweepResult
	if r == nil || r.service == nil || r.service.records == nil {
		return result, fmt.Errorf("identity reconciler is unavailable")
	}
	continueToken := ""
	for {
		list, err := r.service.records.List(ctx, metav1.ListOptions{Limit: sweepPageSize, Continue: continueToken})
		if err != nil {
			return result, fmt.Errorf("listing scoped identities: %w", err)
		}
		for i := range list.Items {
			record := list.Items[i]
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			r.reconcileOne(ctx, &record, &result)
		}
		continueToken = list.Continue
		if continueToken == "" {
			break
		}
	}
	r.log.V(4).Info("scoped identity sweep", "examined", result.Examined, "collected", result.Collected,
		"rematerialized", result.Rematerial, "failed", result.Failed, "waiting", result.SkippedWait)
	return result, nil
}

func (r *Reconciler) reconcileOne(ctx context.Context, record *tenancyv1alpha1.ScopedIdentity, result *SweepResult) {
	result.Examined++
	owner := Owner{
		Provider: record.Spec.Owner.Provider, Kind: record.Spec.Owner.Kind,
		Group: record.Spec.Owner.Group, Version: record.Spec.Owner.Version,
		Resource: record.Spec.Owner.Resource, Name: record.Spec.Owner.Name,
		UID: record.Spec.Owner.UID, ClusterID: record.Spec.ClusterID,
	}

	found, _, err := r.service.owners.Exists(ctx, record.Spec.ClusterID, owner)
	if err != nil {
		// An unreachable workspace is not a deleted owner. Leaving the record
		// alone is the only safe answer: collecting on a transient error would
		// revoke a live agent's credential because kcp hiccuped.
		result.Failed++
		r.log.V(4).Info("scoped identity owner probe failed; leaving record alone",
			"record", record.Name, "provider", owner.Provider, "err", err.Error())
		return
	}
	if !found {
		if err := r.service.deleteRecord(ctx, record); err != nil {
			result.Failed++
			r.log.Error(err, "collecting orphaned scoped identity", "record", record.Name, "provider", owner.Provider)
			return
		}
		result.Collected++
		r.log.Info("collected scoped identity whose owner is gone",
			"record", record.Name, "provider", owner.Provider, "kind", owner.Kind, "owner", owner.Name)
		return
	}

	// The owner is alive. Re-materialize only when the record's rules have not
	// been applied yet, or when the last token has lapsed — which means the
	// holder stopped refreshing and the RBAC may have drifted unobserved.
	// Re-materializing mints a token nobody collects, which is harmless (it is
	// never written down) and keeps the ClusterRole honest.
	if record.Status.ObservedGeneration == record.Generation &&
		record.Status.Phase == tenancyv1alpha1.ScopedIdentityReady &&
		record.Status.ExpiresAt != nil && r.now().Before(record.Status.ExpiresAt.Time) {
		result.SkippedWait++
		return
	}
	if _, err := r.service.materialize(ctx, record); err != nil {
		r.service.markFailed(ctx, record, err)
		result.Failed++
		r.log.Error(err, "re-materializing scoped identity", "record", record.Name, "provider", owner.Provider)
		return
	}
	result.Rematerial++
}
