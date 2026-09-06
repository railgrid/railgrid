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

package organization

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

// AnnotationCatalogEntryCreationMigrated marks an Organization the one-time
// catalogEntryCreation backfill has visited. Its absence is what identifies
// an Organization that predates the admin default.
const AnnotationCatalogEntryCreationMigrated = "tenants.faros.sh/catalog-entry-creation-migrated"

// BackfillCatalogEntryCreation pins the pre-flip behaviour onto every
// Organization created before spec.catalogEntryCreation defaulted to admin.
//
// The hub used to read an empty field as "members"; it now reads it as
// "admin". Organizations that never set the field relied on the old reading,
// so each one without the migrated annotation gets an explicit "members"
// written — an Organization that already carries a value keeps it — and the
// annotation stamped, after which the backfill never touches it again. New
// Organizations are created with an explicit value and are simply stamped on
// first sight.
//
// reader supplies the Organization list and MUST NOT be cache-backed: an
// empty list from a cold cache is indistinguishable from an empty cluster, and
// this backfill treats "nothing to do" as final. writer applies the patches.
//
// Returns the number of Organizations updated. Errors are returned after the
// first failed write; the caller retries, and every write is idempotent.
func BackfillCatalogEntryCreation(ctx context.Context, reader client.Reader, writer client.Client) (int, error) {
	var orgs tenancyv1alpha1.OrganizationList
	if err := reader.List(ctx, &orgs); err != nil {
		return 0, fmt.Errorf("listing Organizations: %w", err)
	}
	updated := 0
	for i := range orgs.Items {
		org := &orgs.Items[i]
		if org.Annotations[AnnotationCatalogEntryCreationMigrated] != "" {
			continue
		}
		patch := client.MergeFrom(org.DeepCopy())
		if org.Spec.CatalogEntryCreation == "" {
			org.Spec.CatalogEntryCreation = tenancyv1alpha1.CatalogEntryCreationMembers
		}
		if org.Annotations == nil {
			org.Annotations = map[string]string{}
		}
		org.Annotations[AnnotationCatalogEntryCreationMigrated] = time.Now().UTC().Format(time.RFC3339)
		if err := writer.Patch(ctx, org, patch); err != nil {
			return updated, fmt.Errorf("backfilling catalogEntryCreation on Organization %s: %w", org.Name, err)
		}
		updated++
	}
	return updated, nil
}

// catalogEntryCreationBackfill runs BackfillCatalogEntryCreation once the
// manager's caches are synced, retrying until it succeeds or the manager
// stops. It is registered from SetupWithManager so the hub needs no extra
// wiring, and it never fails the manager: a transient apiserver error must
// not take the whole controller down.
//
// The backfill stops after one successful pass, so that pass has to see every
// Organization that exists. Two things make that true, and both are needed:
//
//   - The list goes through mgr.GetAPIReader(), which reads the apiserver
//     directly. mgr.GetClient() would read the informer cache, and a cache
//     that has not been populated yet answers with an empty list — a result
//     this runnable cannot tell apart from "no Organizations exist", after
//     which it would exit having silently migrated nothing. Every remaining
//     Organization would keep an empty spec.catalogEntryCreation, which now
//     reads as "admin", quietly taking provider registration away from
//     members.
//   - It blocks on cache sync first anyway. mgr.Add puts a plain
//     RunnableFunc in the leader-election group, which controller-runtime
//     starts only after the cache group has synced — but that is a property
//     of how this runnable happens to be classified, not something the code
//     states, and it would change the moment someone gave it a
//     NeedLeaderElection method. The explicit wait keeps the guarantee local.
func catalogEntryCreationBackfill(mgr manager.Manager) manager.RunnableFunc {
	return func(ctx context.Context) error {
		logger := klog.FromContext(ctx).WithName("catalog-entry-creation-backfill")
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			// Only returns false when ctx is done — the manager is stopping.
			logger.V(2).Info("cache sync interrupted; skipping backfill")
			return nil
		}
		return wait.PollUntilContextCancel(ctx, 10*time.Second, true, func(ctx context.Context) (bool, error) {
			updated, err := BackfillCatalogEntryCreation(ctx, mgr.GetAPIReader(), mgr.GetClient())
			if err != nil {
				logger.Error(err, "backfill failed; retrying")
				return false, nil
			}
			if updated > 0 {
				logger.Info("pinned catalogEntryCreation on pre-existing Organizations", "updated", updated)
			}
			return true, nil
		})
	}
}
