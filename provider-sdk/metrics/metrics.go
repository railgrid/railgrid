/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package serve builds the whole HTTP surface a railgrid provider is allowed
// to expose, from the closed list of route classes in
// docs/provider-connectivity-contract.md §"Pillar 2 route classes".
//
// It exists for the same reason provider-sdk/dataplane does: every provider

// Package metrics is the one way a provider (or the hub) exposes Prometheus
// metrics. Every process already produces them: controller-runtime registers
// reconcile, workqueue and client-go request metrics on its process registry
// for each manager, and the Go and process collectors sit there too. Nothing
// served that registry, because each manager runs with its metrics server
// disabled (the provider owns its HTTP surface) and provider-sdk/serve has no
// route class for /metrics, on purpose: the public port is reachable through
// the hub proxy, and operational counters are not a tenant-facing surface.
//
// Serve answers that with a separate plain-HTTP listener, cluster-internal,
// that a scraper finds through the pod annotations the charts set
// (prometheus.io/* and k8s.grafana.com/*). One line in main:
//
//	go metrics.Serve(ctx, metrics.ListenFromEnv())
//
// A provider with its own instrumentation registers it on Registry; one that
// already renders Prometheus text by hand merges it with TextGatherer.
package metrics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"k8s.io/klog/v2"

	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// ListenEnv is the environment variable naming the metrics listener address.
// Unset means DefaultListen; set but empty disables the listener. The charts
// set it from their metrics.enabled / metrics.port values.
const ListenEnv = "RAILGRID_METRICS_LISTEN"

// DefaultListen is the address used when ListenEnv is unset.
const DefaultListen = ":9090"

// Registry is the process registry everything is served from: controller-
// runtime's, so a provider's own collectors sit next to the reconcile metrics.
var Registry prometheus.Registerer = ctrlmetrics.Registry

// ListenFromEnv returns the listener address ListenEnv selects (see its doc).
func ListenFromEnv() string {
	if v, ok := os.LookupEnv(ListenEnv); ok {
		return v
	}
	return DefaultListen
}

var processCollectorsOnce sync.Once

// ensureProcessCollectors adds the Go runtime and process collectors to the
// registry. controller-runtime only registers them when its own metrics
// server runs, which it never does here (every manager is built with
// BindAddress "0"), so without this a provider would expose reconcile
// metrics but no go_goroutines or process_resident_memory_bytes.
func ensureProcessCollectors() {
	processCollectorsOnce.Do(func() {
		for _, c := range []prometheus.Collector{collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})} {
			if err := ctrlmetrics.Registry.Register(c); err != nil {
				var already prometheus.AlreadyRegisteredError
				if !errors.As(err, &already) {
					klog.ErrorS(err, "metrics: register process collector")
				}
			}
		}
	})
}

// Handler serves the process registry, optionally merged with extra gatherers.
func Handler(extra ...prometheus.Gatherer) http.Handler {
	ensureProcessCollectors()
	g := prometheus.Gatherers{ctrlmetrics.Registry}
	g = append(g, extra...)
	return promhttp.HandlerFor(g, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})
}

// Serve runs a plain-HTTP server exposing /metrics on addr until ctx is
// cancelled, and returns when it stops. An empty addr disables it and returns
// at once. Failures are logged, never fatal: a provider must not fall over
// because its metrics port is taken.
func Serve(ctx context.Context, addr string) {
	ServeWith(ctx, addr)
}

// ServeWith is Serve with extra gatherers merged into /metrics (see
// TextGatherer for a hand-rendered exposition).
func ServeWith(ctx context.Context, addr string, extra ...prometheus.Gatherer) {
	if addr == "" {
		klog.InfoS("metrics listener disabled", "env", ListenEnv)
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", Handler(extra...))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	klog.InfoS("serving metrics", "addr", addr, "path", "/metrics")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		klog.ErrorS(err, "metrics listener failed; the process keeps running without metrics", "addr", addr)
	}
}

// TextGatherer adapts a handler that writes Prometheus text exposition (the
// hand-rolled kind: "# TYPE x counter" followed by samples) into a Gatherer,
// so its families merge into the same /metrics as the registry. A handler
// that fails or writes something unparsable yields an error for that scrape,
// which promhttp reports while still serving the registry's families.
func TextGatherer(h http.Handler) prometheus.Gatherer {
	return prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			return nil, fmt.Errorf("text gatherer: handler returned %d", rec.Code)
		}
		parser := expfmt.NewTextParser(model.UTF8Validation)
		families, err := parser.TextToMetricFamilies(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			return nil, fmt.Errorf("text gatherer: parse: %w", err)
		}
		names := make([]string, 0, len(families))
		for n := range families {
			names = append(names, n)
		}
		sort.Strings(names) // Gatherers expects sorted families
		out := make([]*dto.MetricFamily, 0, len(names))
		for _, n := range names {
			out = append(out, families[n])
		}
		return out, nil
	})
}
