// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package telemetryreceiver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	store   Store
	config  Config
	metrics receiverMetrics
}

type receiverMetrics struct {
	batches   *prometheus.CounterVec
	events    *prometheus.CounterVec
	erasure   *prometheus.CounterVec
	retention prometheus.Counter
	ready     prometheus.Gauge
	registry  *prometheus.Registry
}

func NewServer(store Store, config Config) (*Server, error) {
	if store == nil {
		return nil, ErrInvalidConfig
	}
	config = config.withDefaults()
	if err := config.validate(); err != nil {
		return nil, err
	}
	server := &Server{store: store, config: config, metrics: newReceiverMetrics()}
	server.metrics.ready.Set(0)
	return server, nil
}

func newReceiverMetrics() receiverMetrics {
	registry := prometheus.NewRegistry()
	batches := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "faros_telemetry_ingest_batches_total", Help: "CloudEvents batches received."}, []string{"outcome"})
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "faros_telemetry_events_total", Help: "CloudEvents by persistence outcome."}, []string{"outcome"})
	erasure := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "faros_telemetry_erasure_total", Help: "Erasure requests by outcome."}, []string{"outcome"})
	retention := prometheus.NewCounter(prometheus.CounterOpts{Name: "faros_telemetry_retention_errors_total", Help: "Retention sweep failures."})
	ready := prometheus.NewGauge(prometheus.GaugeOpts{Name: "faros_telemetry_ready", Help: "Whether the most recent readiness check succeeded."})
	for _, collector := range []prometheus.Collector{batches, events, erasure, retention, ready} {
		registry.MustRegister(collector)
	}
	return receiverMetrics{batches: batches, events: events, erasure: erasure, retention: retention, ready: ready, registry: registry}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", s.handleEvents)
	mux.HandleFunc("POST /v1/erasure", s.handleErasure)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	return mux
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r, s.config.IngestToken) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !isBatchContentType(r.Header.Get("Content-Type")) {
		s.metrics.events.WithLabelValues("rejected").Inc()
		s.metrics.batches.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/cloudevents-batch+json")
		return
	}
	maxBody := int64(s.config.MaxBatchEvents) * int64(s.config.MaxEventBytes+512)
	if maxBody < 64*1024 {
		maxBody = 64 * 1024
	}
	if maxBody > 64*1024*1024 {
		maxBody = 64 * 1024 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		s.metrics.events.WithLabelValues("rejected").Inc()
		s.metrics.batches.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds limit")
		return
	}
	events, err := ParseBatch(r, payload, s.config.MaxBatchEvents, s.config.MaxEventBytes)
	if err != nil {
		s.metrics.events.WithLabelValues("rejected").Inc()
		s.metrics.batches.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	for i := range events {
		events[i].ReceivedAt = now
	}
	stats, err := s.store.Insert(r.Context(), events)
	if err != nil {
		s.metrics.events.WithLabelValues("rejected").Add(float64(len(events)))
		s.metrics.batches.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusServiceUnavailable, "telemetry persistence unavailable")
		return
	}
	s.metrics.batches.WithLabelValues("accepted").Inc()
	s.metrics.events.WithLabelValues("accepted").Add(float64(stats.Accepted))
	s.metrics.events.WithLabelValues("duplicate").Add(float64(stats.Duplicates))
	writeJSON(w, http.StatusAccepted, stats)
}

func (s *Server) handleErasure(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r, s.config.AdminToken) {
		s.metrics.erasure.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var request ErasureRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		s.metrics.erasure.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusBadRequest, "invalid erasure request")
		return
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		s.metrics.erasure.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusBadRequest, "invalid erasure request")
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	tenantID, tenantOK := normalizeTenantID(request.TenantID)
	request.TenantID = tenantID
	if request.RequestID == "" || !tenantOK || len(request.RequestID) > 128 {
		s.metrics.erasure.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusBadRequest, "request_id and tenant_id are required")
		return
	}
	result, err := s.store.EraseTenant(r.Context(), request)
	if err != nil {
		if errors.Is(err, ErrErasureConflict) {
			s.metrics.erasure.WithLabelValues("rejected").Inc()
			writeError(w, http.StatusConflict, "request_id is already associated with another tenant")
			return
		}
		s.metrics.erasure.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusServiceUnavailable, "telemetry persistence unavailable")
		return
	}
	if result.Existing {
		s.metrics.erasure.WithLabelValues("duplicate").Inc()
	} else {
		s.metrics.erasure.WithLabelValues("accepted").Inc()
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.metrics.ready.Set(0)
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	s.metrics.ready.Set(1)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

func (s *Server) RunJanitor(ctx context.Context, interval, rawRetention, aggregateRetention time.Duration) error {
	if err := validateRetention(rawRetention, aggregateRetention, interval); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if _, err := s.store.PurgeExpired(ctx, now.UTC(), rawRetention, aggregateRetention); err != nil {
				s.metrics.retention.Inc()
				s.config.Logger.Error("telemetry retention sweep failed", "error", err)
			}
		}
	}
}

func (s *Server) authorized(r *http.Request, expected string) bool {
	const prefix = "Bearer "
	provided := r.Header.Get("Authorization")
	if !strings.HasPrefix(provided, prefix) || expected == "" {
		return false
	}
	provided = strings.TrimPrefix(provided, prefix)
	return len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func isBatchContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == cloudevents.ApplicationCloudEventsBatchJSON
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
