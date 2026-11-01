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

// Package quota implements the organizations-per-user caps pinned by
// decision O-5 in docs/organizations.md: how many Organizations one User may
// create, and how many of the Organizations they administer may be on the
// free plan at once. It is library-only: the REST createOrg handler resolves
// the caps (the hub flags, overridden per user by User.spec.quota), supplies
// a Counter for each, and this package compares the two.
//
// Workspace counts are not a hub quota (the former O-6): they are the
// `workspaces` meter on the organization's metering Account, enforced by the
// metering webhook at the kcp shard.
//
// Counting ignores personal Organizations (spec.personal=true): those are
// auto-provisioned per User, always on the default plan, and must not burn
// either cap.
package quota

import (
	"context"
	"fmt"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

const (
	// DefaultOrgsPerUser is the hub-wide cap on the Organizations one User
	// may create when neither the --default-orgs-per-user flag nor
	// User.spec.quota says otherwise.
	DefaultOrgsPerUser int32 = 10
	// DefaultFreeOrgsPerUser is the hub-wide cap on the Organizations on the
	// free plan one User may administer at once when neither the
	// --free-orgs-per-user flag nor User.spec.quota says otherwise. One: the
	// personal organization is free on top of it, so a user gets one team
	// organization to try for free, and the next needs a plan on one of them.
	DefaultFreeOrgsPerUser int32 = 1

	// KindOrganizations is QuotaExceededError.Kind for the organizations cap.
	KindOrganizations = "Organization"
	// KindFreeOrganizations is QuotaExceededError.Kind for the free
	// organizations cap.
	KindFreeOrganizations = "FreeOrganization"
)

// Caps are the two per-user organization caps.
type Caps struct {
	// Organizations is how many non-personal Organizations a User may create.
	Organizations int32
	// FreeOrganizations is how many non-personal Organizations on the free
	// plan a User may administer at once.
	FreeOrganizations int32
}

// Defaults returns the binary defaults.
func Defaults() Caps {
	return Caps{Organizations: DefaultOrgsPerUser, FreeOrganizations: DefaultFreeOrgsPerUser}
}

// ForUser applies the User's spec.quota overrides to c. A nil user, or one
// without overrides, keeps c; a set field replaces that cap, 0 included.
func (c Caps) ForUser(user *tenancyv1alpha1.User) Caps {
	if user == nil || user.Spec.Quota == nil {
		return c
	}
	if q := user.Spec.Quota; q.Organizations != nil {
		c.Organizations = *q.Organizations
	}
	if q := user.Spec.Quota; q.FreeOrganizations != nil {
		c.FreeOrganizations = *q.FreeOrganizations
	}
	return c
}

// IsFree reports whether org is on the free plan: spec.plan is empty or
// names defaultPlan. The hub's metering reconciler keeps the organization's
// Entitlement in step with spec.plan, so this is the organization's plan
// without a metering read.
func IsFree(org *tenancyv1alpha1.Organization, defaultPlan string) bool {
	return IsFreePlan(org.Spec.Plan, defaultPlan)
}

// IsFreePlan reports whether plan is the free one: empty, or defaultPlan.
func IsFreePlan(plan, defaultPlan string) bool {
	return plan == "" || plan == defaultPlan
}

// Counter is the minimal interface the quota checks consume. The caller
// supplies a Counter whose Count method returns the current usage; the check
// compares against the cap and returns QuotaExceededError when usage >= cap.
type Counter interface {
	Count(ctx context.Context) (int32, error)
}

// CounterFunc adapts a function to the Counter interface.
type CounterFunc func(ctx context.Context) (int32, error)

// Count implements Counter.
func (f CounterFunc) Count(ctx context.Context) (int32, error) { return f(ctx) }

// QuotaExceededError is returned by the checks when the supplied Counter
// reports usage at or above the cap. Carries structured fields so the REST
// handler can answer 409 with a machine-readable hint.
type QuotaExceededError struct {
	// Kind is KindOrganizations or KindFreeOrganizations.
	Kind  string
	Owner string
	Count int32
	Cap   int32
}

// Error implements the error interface.
func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("quota exceeded: %s for %q at %d/%d", e.Kind, e.Owner, e.Count, e.Cap)
}

// CheckOrgQuota verifies the User named owner has created fewer than cap
// Organizations, counter supplying the count of their live, non-personal
// ones.
//
//   - Returns nil when usage < cap.
//   - Returns a *QuotaExceededError when usage >= cap.
//   - Returns the Counter's error verbatim on count failure.
//
// A cap of 0 denies: an admin may legitimately forbid a user from creating
// organizations.
func CheckOrgQuota(ctx context.Context, owner string, cap int32, counter Counter) error {
	return check(ctx, KindOrganizations, owner, cap, counter)
}

// CheckFreeOrgQuota verifies the User named owner administers fewer than cap
// non-personal Organizations on the free plan, counter supplying that count.
// Same contract as CheckOrgQuota.
func CheckFreeOrgQuota(ctx context.Context, owner string, cap int32, counter Counter) error {
	return check(ctx, KindFreeOrganizations, owner, cap, counter)
}

func check(ctx context.Context, kind, owner string, cap int32, counter Counter) error {
	if counter == nil {
		return fmt.Errorf("quota: counter is required")
	}
	count, err := counter.Count(ctx)
	if err != nil {
		return fmt.Errorf("quota: counting %ss: %w", kind, err)
	}
	if count >= cap {
		return &QuotaExceededError{Kind: kind, Owner: owner, Count: count, Cap: cap}
	}
	return nil
}
