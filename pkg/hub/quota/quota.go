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

// Package quota implements the quota checks pinned by decisions O-5
// (Org quota = soft cap, admin-overridable per User; default 10) and
// O-6 (Workspace quota = soft cap, admin-overridable per Org; default 50)
// in docs/organizations.md. The package is library-only: it exposes
// constants, helpers, and a Counter interface so REST handlers
// (roadmap step 10) and any future controller-side admission can run
// the same check.
//
// Quota counting deliberately ignores Organizations created by the
// hub's personal-Org bootstrap (spec.personal=true). Those are
// auto-provisioned per User and shouldn't burn the user's
// admin-overridable cap.
package quota

import (
	"context"
	"fmt"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

const (
	// DefaultOrgsPerUser is the platform-wide soft cap on the number
	// of Organizations a single User may create. Per O-5; overridable
	// per User via User.spec.orgQuota (0 means use this default).
	DefaultOrgsPerUser int32 = 10

	// DefaultWorkspacesPerOrg is the platform-wide soft cap on the
	// number of child Workspaces an Organization may hold. Per O-6;
	// overridable per Org via Organization.spec.workspaceQuota (0
	// means use this default).
	DefaultWorkspacesPerOrg int32 = 50

	// LabelCreatedBy records which User CR created a given Organization.
	// Set at create time by the hub's Org-create endpoint (roadmap step 10)
	// and by the personal-Org bootstrap controller (roadmap step 1+). Used
	// here to count Orgs against the user's quota.
	LabelCreatedBy = "tenancy.kedge.faros.sh/created-by"
)

// EffectiveOrgsPerUser returns the effective Org quota for the given
// User. spec.orgQuota of 0 (the zero value) defers to the platform
// default; a non-zero value overrides it.
func EffectiveOrgsPerUser(user *tenancyv1alpha1.User) int32 {
	if user == nil || user.Spec.OrgQuota == 0 {
		return DefaultOrgsPerUser
	}
	return user.Spec.OrgQuota
}

// EffectiveWorkspacesPerOrg returns the effective Workspace quota for
// the given Organization. spec.workspaceQuota of 0 defers to the
// platform default; a non-zero value overrides it.
func EffectiveWorkspacesPerOrg(org *tenancyv1alpha1.Organization) int32 {
	if org == nil || org.Spec.WorkspaceQuota == 0 {
		return DefaultWorkspacesPerOrg
	}
	return org.Spec.WorkspaceQuota
}

// Counter is the minimal interface the quota checks consume. The
// caller supplies a Counter whose Count method returns the current
// usage; the quota check compares against the cap and returns
// QuotaExceededError when usage >= cap.
//
// Pulling the count behind an interface keeps the package
// dependency-free of the kedge / kcp clientsets so the helpers can be
// unit-tested with a literal fakeCounter (see quota_test.go) and the
// production wiring picks the appropriate listing strategy at the
// call site.
type Counter interface {
	Count(ctx context.Context) (int32, error)
}

// CounterFunc adapts a function to the Counter interface.
type CounterFunc func(ctx context.Context) (int32, error)

// Count implements Counter.
func (f CounterFunc) Count(ctx context.Context) (int32, error) { return f(ctx) }

// QuotaExceededError is returned by the Check* helpers when the
// supplied Counter reports usage at or above the effective cap. Carries
// structured fields so REST handlers (roadmap step 10) can map it to
// a 409 Conflict (or 4xx of choice) with a machine-readable hint.
//
// The kind field describes what is being capped ("Organization" or
// "Workspace"). Owner identifies the parent (User name for Org quota;
// Organization UUID for Workspace quota).
type QuotaExceededError struct {
	Kind  string
	Owner string
	Count int32
	Cap   int32
}

// Error implements the error interface.
func (e *QuotaExceededError) Error() string {
	return fmt.Sprintf("quota exceeded: %s for %q at %d/%d", e.Kind, e.Owner, e.Count, e.Cap)
}

// CheckOrgQuota verifies the User has not reached their Org-creation
// cap. It calls counter.Count(ctx) to read current usage, then
// compares against EffectiveOrgsPerUser(user).
//
//   - Returns nil when usage < cap.
//   - Returns a *QuotaExceededError when usage >= cap.
//   - Returns the Counter's error verbatim on count failure.
//
// Personal Organizations created by the bootstrap controller are
// expected to be excluded by the Counter implementation (the Counter
// caller filters by spec.personal=false). The package does not enforce
// that filter on its own to keep the abstraction tight; the
// "Organizations counted exclude spec.personal=true" rule is part of
// the Counter contract.
func CheckOrgQuota(ctx context.Context, user *tenancyv1alpha1.User, counter Counter) error {
	if counter == nil {
		return fmt.Errorf("quota: counter is required")
	}
	count, err := counter.Count(ctx)
	if err != nil {
		return fmt.Errorf("quota: counting Organizations: %w", err)
	}
	cap := EffectiveOrgsPerUser(user)
	if count >= cap {
		ownerName := ""
		if user != nil {
			ownerName = user.Name
		}
		return &QuotaExceededError{
			Kind:  "Organization",
			Owner: ownerName,
			Count: count,
			Cap:   cap,
		}
	}
	return nil
}

// CheckWorkspaceQuota verifies the Organization has not reached its
// Workspace-creation cap. Same contract as CheckOrgQuota.
func CheckWorkspaceQuota(ctx context.Context, org *tenancyv1alpha1.Organization, counter Counter) error {
	if counter == nil {
		return fmt.Errorf("quota: counter is required")
	}
	count, err := counter.Count(ctx)
	if err != nil {
		return fmt.Errorf("quota: counting Workspaces: %w", err)
	}
	cap := EffectiveWorkspacesPerOrg(org)
	if count >= cap {
		ownerName := ""
		if org != nil {
			ownerName = org.Name
		}
		return &QuotaExceededError{
			Kind:  "Workspace",
			Owner: ownerName,
			Count: count,
			Cap:   cap,
		}
	}
	return nil
}
