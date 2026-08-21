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

package telemetry

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Runtime struct {
	enabled   bool
	cfg       Config
	sink      Sink
	normalize normalizer
	queue     chan Record
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	stop      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	acceptMu  sync.RWMutex
	metrics   runtimeMetrics
}

type runtimeMetrics struct {
	enqueued, dropped, sent, failed prometheus.Counter
	depth                           prometheus.Gauge
}

func newRuntimeMetrics(registerer prometheus.Registerer) runtimeMetrics {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	m := runtimeMetrics{
		enqueued: prometheus.NewCounter(prometheus.CounterOpts{Name: "faros_telemetry_enqueued_total", Help: "Validated product events accepted by the hub queue."}),
		dropped:  prometheus.NewCounter(prometheus.CounterOpts{Name: "faros_telemetry_dropped_total", Help: "Product events dropped because the bounded queue was unavailable."}),
		sent:     prometheus.NewCounter(prometheus.CounterOpts{Name: "faros_telemetry_sent_total", Help: "Product events accepted by the telemetry receiver."}),
		failed:   prometheus.NewCounter(prometheus.CounterOpts{Name: "faros_telemetry_send_failures_total", Help: "Product event batches exhausted retry attempts."}),
		depth:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "faros_telemetry_queue_depth", Help: "Current number of queued product events."}),
	}
	registerer.MustRegister(m.enqueued, m.dropped, m.sent, m.failed, m.depth)
	return m
}

func NewRuntime(cfg Config, registerer prometheus.Registerer) (*Runtime, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Mode == ModeOff {
		return &Runtime{cfg: cfg}, nil
	}
	return NewRuntimeWithSink(cfg, NewHTTPSink(cfg.Endpoint, cfg.SinkToken, cfg.HTTPClient), registerer)
}

func NewRuntimeWithSink(cfg Config, sink Sink, registerer prometheus.Registerer) (*Runtime, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Mode == ModeOff {
		return &Runtime{cfg: cfg}, nil
	}
	if sink == nil {
		return nil, fmt.Errorf("sink is required: %w", ErrInvalidConfig)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{enabled: true, cfg: cfg, sink: sink, normalize: normalizer{key: []byte(cfg.HMACSecret), installationID: cfg.InstallationID}, queue: make(chan Record, cfg.QueueSize), ctx: ctx, cancel: cancel, done: make(chan struct{}), stop: make(chan struct{}), metrics: newRuntimeMetrics(registerer)}
	go r.run()
	return r, nil
}

func (r *Runtime) Enabled() bool { return r != nil && r.enabled }
func (r *Runtime) MaxRequestBytes() int64 {
	if r == nil {
		return 0
	}
	return r.cfg.MaxRequestBytes
}

func (r *Runtime) Track(ctx context.Context, provider string, event Event) error {
	if !r.Enabled() {
		return ErrDisabled
	}
	r.acceptMu.RLock()
	defer r.acceptMu.RUnlock()
	if r.closed.Load() {
		return ErrClosed
	}
	event.Action = strings.TrimSpace(event.Action)
	if err := validateProviderEvent(provider, event); err != nil {
		return err
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	record, err := r.normalize.record(provider, event)
	if err != nil {
		return fmt.Errorf("prepare telemetry record: %w", err)
	}
	timer := time.NewTimer(r.cfg.EnqueueTimeout)
	defer timer.Stop()
	select {
	case r.queue <- record:
		r.metrics.enqueued.Inc()
		r.metrics.depth.Set(float64(len(r.queue)))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		r.metrics.dropped.Inc()
		return ErrQueueFull
	case <-r.ctx.Done():
		return ErrClosed
	}
}

// TrackPlatform is the internal hub call-site boundary for catalog events
// owned by platform. Provider HTTP ingestion can never select this owner.
func (r *Runtime) TrackPlatform(ctx context.Context, event Event) error {
	return r.Track(ctx, "platform", event)
}

func (r *Runtime) run() {
	defer close(r.done)
	ticker := time.NewTicker(r.cfg.FlushInterval)
	defer ticker.Stop()
	batch := make([]Record, 0, r.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		r.send(batch)
		batch = batch[:0]
		r.metrics.depth.Set(float64(len(r.queue)))
	}
	for {
		select {
		case rec := <-r.queue:
			batch = append(batch, rec)
			if len(batch) >= r.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-r.stop:
			for len(batch) < r.cfg.BatchSize {
				select {
				case rec := <-r.queue:
					batch = append(batch, rec)
				default:
					flush()
					return
				}
			}
			flush()
		}
	}
}

func (r *Runtime) send(batch []Record) {
	backoff := r.cfg.InitialBackoff
	for attempt := 0; attempt < r.cfg.MaxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(r.ctx, r.cfg.SendTimeout)
		err := r.sink.Send(ctx, batch)
		cancel()
		if err == nil {
			r.metrics.sent.Add(float64(len(batch)))
			return
		}
		if attempt+1 < r.cfg.MaxRetries {
			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
			case <-r.ctx.Done():
				timer.Stop()
				r.metrics.failed.Inc()
				return
			}
			backoff *= 2
		}
	}
	r.metrics.failed.Inc()
}

func (r *Runtime) Close() error {
	if !r.Enabled() {
		return nil
	}
	r.closeOnce.Do(func() {
		r.acceptMu.Lock()
		r.closed.Store(true)
		close(r.stop)
		r.acceptMu.Unlock()
	})
	t := time.NewTimer(r.cfg.ShutdownTimeout)
	defer t.Stop()
	select {
	case <-r.done:
		r.cancel()
		return nil
	case <-t.C:
		r.cancel()
		return context.DeadlineExceeded
	}
}
