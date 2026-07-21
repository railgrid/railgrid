// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/faroshq/provider-agents/store"
)

// usageBucket is one aggregated row (per agent, per model, or the grand total).
type usageBucket struct {
	Key          string `json:"key"`
	Runs         int64  `json:"runs"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	USDMicros    int64  `json:"usdMicros"`
	// LatencyP50MS/P95MS are computed over completed runs with timing.
	LatencyP50MS int64 `json:"latencyP50MS"`
	LatencyP95MS int64 `json:"latencyP95MS"`
}

// usagePoint is one day in the spend/volume timeseries.
type usagePoint struct {
	Date         string `json:"date"` // YYYY-MM-DD (UTC)
	Runs         int64  `json:"runs"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	USDMicros    int64  `json:"usdMicros"`
}

type usageResponse struct {
	WindowDays int           `json:"windowDays"`
	Total      usageBucket   `json:"total"`
	ByAgent    []usageBucket `json:"byAgent"`
	ByModel    []usageBucket `json:"byModel"`
	Series     []usagePoint  `json:"series"`
}

// usageRollup aggregates the run history into cost/usage/observability rollups
// over a rolling window (default 30 days, ?days= to override, capped at 90).
// Everything is derived from the runs table — no separate telemetry store — so
// it powers both the cost dashboard and the latency/error panel. Per-model
// attribution uses each agent's CURRENT primary model (spec.models.chat), since
// runs are recorded per agent; this is exact unless an agent's model changed
// mid-window.
func (s *Server) usageRollup(w http.ResponseWriter, r *http.Request) {
	c, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	scope := id.scope("")

	days := 30
	if v := strings.TrimSpace(r.URL.Query().Get("days")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = min(n, 90)
		}
	}
	since := time.Now().UTC().AddDate(0, 0, -days)

	// Pull a generous slice of recent runs and filter to the window in-process
	// (ListRuns is limit-based, not time-ranged).
	runs, err := s.store.ListRuns(r.Context(), scope, 5000)
	if err != nil {
		writeResourceError(w, err)
		return
	}

	// agent → primary model, for per-model attribution. Best-effort: an agent
	// with no assigned model is bucketed under "(unassigned)".
	agentModel := map[string]string{}
	if agents, aerr := c.Agents().List(r.Context(), metav1.ListOptions{}); aerr == nil {
		for i := range agents.Items {
			a := &agents.Items[i]
			agentModel[a.Name] = strings.TrimSpace(a.Spec.Models["chat"])
		}
	}

	total := usageBucket{Key: "total"}
	byAgent := map[string]*usageBucket{}
	byModel := map[string]*usageBucket{}
	series := map[string]*usagePoint{}
	latAll := []int64{}
	latByAgent := map[string][]int64{}
	latByModel := map[string][]int64{}

	for i := range runs {
		run := &runs[i]
		if run.CreatedAt.Before(since) {
			continue
		}
		modelKey := agentModel[run.AgentName]
		if modelKey == "" {
			modelKey = "(unassigned)"
		}
		isErr := run.Phase == store.RunPhaseFailed

		acc := func(b *usageBucket) {
			b.Runs++
			if isErr {
				b.Errors++
			}
			b.InputTokens += run.InputTokens
			b.OutputTokens += run.OutputTokens
			b.USDMicros += run.USDMicros
		}
		acc(&total)
		ab := byAgent[run.AgentName]
		if ab == nil {
			ab = &usageBucket{Key: run.AgentName}
			byAgent[run.AgentName] = ab
		}
		acc(ab)
		mb := byModel[modelKey]
		if mb == nil {
			mb = &usageBucket{Key: modelKey}
			byModel[modelKey] = mb
		}
		acc(mb)

		// Latency: completed runs with both timestamps.
		if run.StartedAt != nil && run.FinishedAt != nil {
			ms := run.FinishedAt.Sub(*run.StartedAt).Milliseconds()
			if ms >= 0 {
				latAll = append(latAll, ms)
				latByAgent[run.AgentName] = append(latByAgent[run.AgentName], ms)
				latByModel[modelKey] = append(latByModel[modelKey], ms)
			}
		}

		// Daily timeseries.
		day := run.CreatedAt.UTC().Format("2006-01-02")
		pt := series[day]
		if pt == nil {
			pt = &usagePoint{Date: day}
			series[day] = pt
		}
		pt.Runs++
		pt.InputTokens += run.InputTokens
		pt.OutputTokens += run.OutputTokens
		pt.USDMicros += run.USDMicros
	}

	total.LatencyP50MS, total.LatencyP95MS = percentiles(latAll)
	resp := usageResponse{WindowDays: days, Total: total}
	for k, b := range byAgent {
		b.LatencyP50MS, b.LatencyP95MS = percentiles(latByAgent[k])
		resp.ByAgent = append(resp.ByAgent, *b)
	}
	for k, b := range byModel {
		b.LatencyP50MS, b.LatencyP95MS = percentiles(latByModel[k])
		resp.ByModel = append(resp.ByModel, *b)
	}
	// Highest spend first; ties broken by run count then name for stability.
	rank := func(a, b usageBucket) bool {
		if a.USDMicros != b.USDMicros {
			return a.USDMicros > b.USDMicros
		}
		if a.Runs != b.Runs {
			return a.Runs > b.Runs
		}
		return a.Key < b.Key
	}
	sort.Slice(resp.ByAgent, func(i, j int) bool { return rank(resp.ByAgent[i], resp.ByAgent[j]) })
	sort.Slice(resp.ByModel, func(i, j int) bool { return rank(resp.ByModel[i], resp.ByModel[j]) })
	for _, p := range series {
		resp.Series = append(resp.Series, *p)
	}
	sort.Slice(resp.Series, func(i, j int) bool { return resp.Series[i].Date < resp.Series[j].Date })

	writeJSON(w, http.StatusOK, resp)
}

// percentiles returns the p50 and p95 of xs (nearest-rank). Empty → (0, 0).
func percentiles(xs []int64) (p50, p95 int64) {
	if len(xs) == 0 {
		return 0, 0
	}
	sorted := append([]int64(nil), xs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(q float64) int64 {
		idx := int(q * float64(len(sorted)))
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return at(0.50), at(0.95)
}
