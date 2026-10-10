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

package api

import (
	"context"
	"errors"

	"k8s.io/client-go/util/flowcontrol"
)

// Discovery limits are initialized once per Server. Requests and tenants on
// that instance share the bucket and semaphore; separate Server instances,
// including replicas, have independent budgets.
const (
	integrationDiscoveryConcurrency = 4
	integrationDiscoveryQPS         = 150
	integrationDiscoveryBurst       = 10
)

type integrationDiscoveryBudget struct {
	limiter flowcontrol.RateLimiter
	slots   chan struct{}
}

// discoveryBudget returns the Server-scoped transport budget. Sharing this
// limiter paces requests only; it does not share or cache authorization results.
func (s *Server) discoveryBudget() *integrationDiscoveryBudget {
	s.integrationDiscoveryOnce.Do(func() {
		s.integrationDiscoveryBudget = &integrationDiscoveryBudget{
			limiter: flowcontrol.NewTokenBucketRateLimiter(integrationDiscoveryQPS, integrationDiscoveryBurst),
			slots:   make(chan struct{}, integrationDiscoveryConcurrency),
		}
	})
	return s.integrationDiscoveryBudget
}

// integrationDiscoveryIdentity changes only the review client's request budget.
// The authenticated user, groups, extras, tenant, and discovery proof remain
// request-local and unchanged. No permission decision is cached or shared.
func (s *Server) integrationDiscoveryIdentity(ctx context.Context, id identity) (identity, error) {
	if s.integrationAccessReviewer != nil {
		return id, nil // Existing injected review seam owns its transport.
	}
	provider, supported, err := projectAssistantAsProviderWithReadLimiter(ctx, s.callers, id.clusterID, s.discoveryBudget().limiter)
	if err != nil {
		return identity{}, err
	}
	if !supported {
		return identity{}, errors.New("provider discovery review client is unavailable")
	}
	id.provider = provider
	return id, nil
}
