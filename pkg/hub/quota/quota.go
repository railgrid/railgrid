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

// Package quota implements the organizations-per-user check pinned by
// decision O-5 in docs/organizations.md. It is library-only: the REST
// createOrg handler supplies the effective cap (from the user's metering
// Entitlement when metering is installed, else the hub's
// --default-orgs-per-user) and a Counter over the user's existing
// Organizations, and this package compares the two.
//
// Workspace counts are no longer a hub quota (the former O-6): they are the
// `workspaces` meter on the owning metering Account, enforced by the
// metering webhook at the kcp shard.
//
// Counting ignores Organizations created by the hub's personal-Org
// bootstrap (spec.personal=true): those are auto-provisioned per User and
// must not burn the user's cap.
package quota

import (
	"context"
	"fmt"
)

// DefaultOrgsPerUser is the platform-wide cap on the number of
// Organizations a single User may create when no plan says otherwise. Per
// O-5; the hub's --default-orgs-per-user flag overrides it.
const DefaultOrgsPerUser int32 = 10

// Counter is the minimal interface the quota check consumes. The caller
// supplies a Counter whose Count method returns the current usage; the check
// compares against the cap and returns QuotaExceededError when usage >= cap.
type Counter interface {
	Count(ctx context.Context) (int32, error)
}

// CounterFunc adapts a function to the Counter interface.
type CounterFunc func(ctx context.Context) (int32, error)

// Count implements Counter.
func (f CounterFunc) Count(ctx context.Context) (int32, error) { return f(ctx) }

// QuotaExceededError is returned by CheckOrgQuota when the supplied Counter
// reports usage at or above the effective cap. Carries structured fields so
// the REST handler can answer 409 with a machine-readable hint.
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

// CheckOrgQuota verifies the User named owner has not reached cap. It calls
// counter.Count(ctx) to read current usage.
//
//   - Returns nil when usage < cap.
//   - Returns a *QuotaExceededError when usage >= cap.
//   - Returns the Counter's error verbatim on count failure.
//
// A cap of 0 denies: a plan or override may legitimately forbid creating
// organizations, and the metering webhook enforces the same 0. A negative
// cap means the caller could not resolve one and selects DefaultOrgsPerUser.
// Personal Organizations are expected to be excluded by the Counter
// implementation.
func CheckOrgQuota(ctx context.Context, owner string, cap int32, counter Counter) error {
	if counter == nil {
		return fmt.Errorf("quota: counter is required")
	}
	if cap < 0 {
		cap = DefaultOrgsPerUser
	}
	count, err := counter.Count(ctx)
	if err != nil {
		return fmt.Errorf("quota: counting Organizations: %w", err)
	}
	if count >= cap {
		return &QuotaExceededError{Kind: "Organization", Owner: owner, Count: count, Cap: cap}
	}
	return nil
}
