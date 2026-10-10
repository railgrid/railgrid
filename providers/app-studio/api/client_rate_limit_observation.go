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
	"sync/atomic"
	"time"

	clientmetrics "k8s.io/client-go/tools/metrics"
	"k8s.io/klog/v2"
)

// Ignore scheduler-scale jitter; a Wait of at least one millisecond is logged
// as delayed so stage summaries expose actual token-bucket pacing.
const projectAssistantRateLimiterDelayedThreshold = time.Millisecond

type projectAssistantRateLimiterWaitContextKey struct{}

type projectAssistantRateLimiterWaitSummary struct {
	calls   int64
	total   time.Duration
	delayed int64
	max     time.Duration
}

func (summary *projectAssistantRateLimiterWaitSummary) add(other projectAssistantRateLimiterWaitSummary) {
	if summary == nil {
		return
	}
	summary.calls += other.calls
	summary.total += other.total
	summary.delayed += other.delayed
	if other.max > summary.max {
		summary.max = other.max
	}
}

type projectAssistantRateLimiterWaitObservation struct {
	calls   atomic.Int64
	total   atomic.Int64
	delayed atomic.Int64
	max     atomic.Int64
}

func projectAssistantObserveRateLimiterWaits(ctx context.Context) (context.Context, *projectAssistantRateLimiterWaitObservation) {
	if ctx == nil {
		ctx = context.Background()
	}
	observation := &projectAssistantRateLimiterWaitObservation{}
	return context.WithValue(ctx, projectAssistantRateLimiterWaitContextKey{}, observation), observation
}

func projectAssistantRateLimiterWaitSummaryFromContext(ctx context.Context) (projectAssistantRateLimiterWaitSummary, bool) {
	if ctx == nil {
		return projectAssistantRateLimiterWaitSummary{}, false
	}
	observation, ok := ctx.Value(projectAssistantRateLimiterWaitContextKey{}).(*projectAssistantRateLimiterWaitObservation)
	if !ok || observation == nil {
		return projectAssistantRateLimiterWaitSummary{}, false
	}
	return observation.Snapshot(), true
}

func (observation *projectAssistantRateLimiterWaitObservation) Snapshot() projectAssistantRateLimiterWaitSummary {
	if observation == nil {
		return projectAssistantRateLimiterWaitSummary{}
	}
	return projectAssistantRateLimiterWaitSummary{
		calls:   observation.calls.Load(),
		total:   time.Duration(observation.total.Load()),
		delayed: observation.delayed.Load(),
		max:     time.Duration(observation.max.Load()),
	}
}

func (observation *projectAssistantRateLimiterWaitObservation) observe(latency time.Duration) {
	if observation == nil {
		return
	}
	if latency < 0 {
		latency = 0
	}
	value := int64(latency)
	observation.calls.Add(1)
	observation.total.Add(value)
	if latency >= projectAssistantRateLimiterDelayedThreshold {
		observation.delayed.Add(1)
	}
	for current := observation.max.Load(); value > current; current = observation.max.Load() {
		if observation.max.CompareAndSwap(current, value) {
			break
		}
	}
}

func projectAssistantRateLimiterWaitLogFields(prefix string, summary projectAssistantRateLimiterWaitSummary) []any {
	if prefix == "" {
		prefix = "rateLimiter"
	}
	return []any{
		prefix + "WaitCalls", summary.calls,
		prefix + "WaitDuration", summary.total,
		prefix + "WaitDelayed", summary.delayed,
		prefix + "WaitDelayedThreshold", projectAssistantRateLimiterDelayedThreshold,
		prefix + "WaitMax", summary.max,
	}
}

func projectAssistantLogWorkerPreparationWithRateLimiterWaits(ctx context.Context, runID, stage string, started time.Time) {
	fields := []any{"run", runID, "stage", stage, "duration", time.Since(started)}
	if summary, ok := projectAssistantRateLimiterWaitSummaryFromContext(ctx); ok {
		fields = append(fields, projectAssistantRateLimiterWaitLogFields("rateLimiter", summary)...)
	}
	klog.FromContext(ctx).Info("App Studio worker preparation", fields...)
}

func projectAssistantLogPreparationWithRateLimiterWaits(ctx context.Context, req projectAssistantRunRequest, stage string, started time.Time) {
	fields := []any{"run", projectAssistantRunID(req), "stage", stage, "duration", time.Since(started)}
	if summary, ok := projectAssistantRateLimiterWaitSummaryFromContext(ctx); ok {
		fields = append(fields, projectAssistantRateLimiterWaitLogFields("rateLimiter", summary)...)
	}
	klog.FromContext(ctx).Info("App Studio engine preparation", fields...)
}

type projectAssistantRateLimiterLatencyObserver struct {
	delegate clientmetrics.LatencyMetric
}

func (observer *projectAssistantRateLimiterLatencyObserver) Observe(ctx context.Context, verb string, requestURL url.URL, latency time.Duration) {
	if observer == nil {
		return
	}
	if observer.delegate != nil {
		observer.delegate.Observe(ctx, verb, requestURL, latency)
	}
	if ctx == nil {
		return
	}
	if observation, ok := ctx.Value(projectAssistantRateLimiterWaitContextKey{}).(*projectAssistantRateLimiterWaitObservation); ok {
		observation.observe(latency)
	}
}

func init() {
	// client-go exposes its actual Wait duration here. The wrapper delegates to
	// the existing observer unchanged and keeps no URL or verb data; stage
	// collectors are opt-in through a private context value.
	clientmetrics.RateLimiterLatency = &projectAssistantRateLimiterLatencyObserver{
		delegate: clientmetrics.RateLimiterLatency,
	}
}
