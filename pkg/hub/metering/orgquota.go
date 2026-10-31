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
	"math"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

// OrganizationsMeter is the Level meter on a user's Entitlement that caps how
// many Organizations they may create. The metering `organizations`
// MeteringConfig attributes Organization objects to the same account through
// the created-by label, so the webhook enforces the same row at the shard.
const OrganizationsMeter = "organizations"

// OrgQuota resolves a user's organizations-per-user cap from the
// `organizations` limit on their metering Entitlement, falling back to a
// constant when metering is not installed, the user has no Entitlement yet, or
// the plan carries no such row. It satisfies restapi.OrgQuotaPolicy.
type OrgQuota struct {
	discovery Installed
	store     dynamic.Interface
	fallback  int32
	logger    klog.Logger
}

// NewOrgQuota builds the policy. fallback is the hub's --default-orgs-per-user.
func NewOrgQuota(discovery Installed, store dynamic.Interface, fallback int32, logger klog.Logger) *OrgQuota {
	return &OrgQuota{discovery: discovery, store: store, fallback: fallback, logger: logger}
}

// Cap returns the cap for user. A read failure other than NotFound is logged
// and answered with the fallback: this check is the friendly 409 in front of
// the metering webhook, which remains the authoritative gate, so a metering
// outage must not turn organization creation into a 500.
func (q *OrgQuota) Cap(ctx context.Context, user string) (int32, error) {
	if q.discovery == nil || q.store == nil {
		return q.fallback, nil
	}
	if _, ok := q.discovery.Current(); !ok {
		return q.fallback, nil
	}
	ent, err := q.store.Resource(gvrEntitlements).Get(ctx, UserAccountName(user), metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			q.logger.Error(err, "reading user entitlement for the organization quota; using the default", "user", user, "default", q.fallback)
		}
		return q.fallback, nil
	}
	if cap, ok := limitFromEntitlement(ent, OrganizationsMeter); ok {
		return cap, nil
	}
	return q.fallback, nil
}

// limitFromEntitlement reads the max of meter from status.effectiveLimits.
// A row without a max (a Meter-only row) does not cap. A max of "0" is a
// real cap of zero, which createOrg honours as "no organizations".
func limitFromEntitlement(ent *unstructured.Unstructured, meter string) (int32, bool) {
	limits, _, _ := unstructured.NestedSlice(ent.Object, "status", "effectiveLimits")
	for _, raw := range limits {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := row["meter"].(string); name != meter {
			continue
		}
		maxStr, _ := row["max"].(string)
		if maxStr == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(maxStr, 64)
		if err != nil || f < 0 || f > math.MaxInt32 {
			return 0, false
		}
		return int32(f), true
	}
	return 0, false
}
