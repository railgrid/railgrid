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
	"net/url"
	"sync"
	"testing"
	"time"
)

type projectAssistantTestLatencyMetric struct {
	mu      sync.Mutex
	ctx     context.Context
	verb    string
	url     url.URL
	latency time.Duration
	calls   int
}

func (metric *projectAssistantTestLatencyMetric) Observe(ctx context.Context, verb string, requestURL url.URL, latency time.Duration) {
	metric.mu.Lock()
	defer metric.mu.Unlock()
	metric.ctx = ctx
	metric.verb = verb
	metric.url = requestURL
	metric.latency = latency
	metric.calls++
}

func TestProjectAssistantRateLimiterObserverDelegatesAndCollectsOnlyOptedInWaits(t *testing.T) {
	delegate := &projectAssistantTestLatencyMetric{}
	observer := &projectAssistantRateLimiterLatencyObserver{delegate: delegate}
	requestURL := url.URL{Scheme: "https", Host: "private.example", Path: "/clusters/tenant/secrets"}
	ctx, waits := projectAssistantObserveRateLimiterWaits(context.Background())
	latency := 12 * time.Millisecond
	observer.Observe(ctx, "GET", requestURL, latency)

	delegate.mu.Lock()
	if delegate.calls != 1 || delegate.ctx != ctx || delegate.verb != "GET" || delegate.url != requestURL || delegate.latency != latency {
		t.Fatalf("delegate received ctx=%v verb=%q url=%v latency=%s calls=%d", delegate.ctx, delegate.verb, delegate.url, delegate.latency, delegate.calls)
	}
	delegate.mu.Unlock()

	if got, want := waits.Snapshot(), (projectAssistantRateLimiterWaitSummary{calls: 1, total: latency, delayed: 1, max: latency}); got != want {
		t.Fatalf("wait summary = %#v, want %#v", got, want)
	}
	if _, ok := projectAssistantRateLimiterWaitSummaryFromContext(context.Background()); ok {
		t.Fatal("ordinary contexts must not carry or collect wait observations")
	}
	fields := projectAssistantRateLimiterWaitLogFields("rateLimiter", waits.Snapshot())
	for _, field := range fields {
		if field == requestURL.String() || field == requestURL.Path || field == "GET" {
			t.Fatalf("wait log fields retained request data: %#v", fields)
		}
	}
}

func TestProjectAssistantRateLimiterWaitObservationIsConcurrentSafe(t *testing.T) {
	const calls = 64
	ctx, waits := projectAssistantObserveRateLimiterWaits(context.Background())
	observer := &projectAssistantRateLimiterLatencyObserver{}
	var workers sync.WaitGroup
	for i := 0; i < calls; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			observer.Observe(ctx, "GET", url.URL{}, 2*time.Millisecond)
		}()
	}
	workers.Wait()
	if got, want := waits.Snapshot(), (projectAssistantRateLimiterWaitSummary{
		calls: calls, total: calls * 2 * time.Millisecond, delayed: calls, max: 2 * time.Millisecond,
	}); got != want {
		t.Fatalf("concurrent wait summary = %#v, want %#v", got, want)
	}
}
