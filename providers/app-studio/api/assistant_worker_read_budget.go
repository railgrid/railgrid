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
	"strings"
	"sync"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/railgrid/provider-sdk/dataplane"

	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/tenant"
)

const (
	projectAssistantWorkerReadQPS   = 40
	projectAssistantWorkerReadBurst = 40
)

type projectAssistantWorkerReadClientFactory interface {
	AsProviderWithRateLimiter(string, flowcontrol.RateLimiter) (dynamic.Interface, error)
}

type projectAssistantWorkerReadClientContextFactory interface {
	AsProviderWithRateLimiterContext(context.Context, string, flowcontrol.RateLimiter) (dynamic.Interface, error)
}

type projectAssistantWorkerReadBudget struct {
	once    sync.Once
	limiter flowcontrol.RateLimiter
}

func (budget *projectAssistantWorkerReadBudget) rateLimiter() flowcontrol.RateLimiter {
	if budget == nil {
		return nil
	}
	budget.once.Do(func() {
		budget.limiter = flowcontrol.NewTokenBucketRateLimiter(projectAssistantWorkerReadQPS, projectAssistantWorkerReadBurst)
	})
	return budget.limiter
}

func projectAssistantAsProviderWithReadLimiter(
	ctx context.Context,
	callers any,
	clusterID string,
	limiter flowcontrol.RateLimiter,
) (dynamic.Interface, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if factory, ok := callers.(projectAssistantWorkerReadClientContextFactory); ok {
		provider, err := factory.AsProviderWithRateLimiterContext(ctx, clusterID, limiter)
		return provider, true, err
	}
	if factory, ok := callers.(projectAssistantWorkerReadClientFactory); ok {
		provider, err := factory.AsProviderWithRateLimiter(clusterID, limiter)
		return provider, true, err
	}
	return nil, false, nil
}

func projectAssistantSupportsReadLimiterFactory(callers any) bool {
	if _, ok := callers.(projectAssistantWorkerReadClientContextFactory); ok {
		return true
	}
	_, ok := callers.(projectAssistantWorkerReadClientFactory)
	return ok
}

// projectAssistantWorkerReadClient derives a provider-scoped client for the
// background worker and attaches the Server's shared read budget. The request
// identity (including the Gate's original client and caller proof) is never
// changed; fresh caller reviews continue using that identity through their
// existing path. Test seams and older factories without the optional limiter
// constructor keep using the caller already passed to the worker.
func (s *Server) projectAssistantWorkerReadClient(ctx context.Context, id identity, fallback *asclient.Client) (*asclient.Client, error) {
	if s == nil || fallback == nil || s.projectClientFor != nil {
		return fallback, nil
	}
	if !projectAssistantSupportsReadLimiterFactory(s.callers) {
		return fallback, nil
	}
	clusterID := strings.TrimSpace(id.clusterID)
	if !dataplane.IsClusterID(clusterID) {
		return nil, errors.New("cannot prepare assistant worker reads without a workspace cluster ID")
	}
	provider, supported, err := projectAssistantAsProviderWithReadLimiter(ctx, s.callers, clusterID, s.assistantWorkerReadBudget.rateLimiter())
	if err != nil {
		return nil, err
	}
	if !supported {
		return fallback, nil
	}
	if provider == nil {
		return nil, errors.New("assistant worker read client factory returned no provider client")
	}
	return asclient.NewFromScope(tenant.NewScopeFromDynamic(provider)), nil
}

// projectAssistantCurrentProjectReadClient derives a provider-scoped client
// for one fresh Project metadata GET. It shares the bounded worker read budget
// without changing id.provider, which remains the Gate and fresh-review client.
// Older factories and injected project-client test seams keep the existing
// provider client behavior.
func (s *Server) projectAssistantCurrentProjectReadClient(ctx context.Context, id identity, fallback dynamic.Interface) (dynamic.Interface, error) {
	if s == nil || s.projectClientFor != nil {
		return fallback, nil
	}
	if !projectAssistantSupportsReadLimiterFactory(s.callers) {
		return fallback, nil
	}
	clusterID := strings.TrimSpace(id.clusterID)
	if !dataplane.IsClusterID(clusterID) {
		return nil, errors.New("cannot read the current Project without a workspace cluster ID")
	}
	provider, supported, err := projectAssistantAsProviderWithReadLimiter(ctx, s.callers, clusterID, s.assistantWorkerReadBudget.rateLimiter())
	if err != nil {
		return nil, err
	}
	if !supported {
		return fallback, nil
	}
	if provider == nil {
		return nil, errors.New("current Project read client factory returned no provider client")
	}
	return provider, nil
}
