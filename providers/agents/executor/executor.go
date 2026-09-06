// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package executor dispatches background agent work (schedule fires, webhook
// events, channel messages) decoupled from how it is executed. The Executor
// interface is deliberately narrow and the Job payload serializable so the
// in-process implementation can later be swapped for a durable-execution
// engine (Temporal, Restate, ...) by registering the same Handler as an
// activity — without touching the scheduling policy that submits jobs.
package executor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

// JobKind identifies what submitted the job.
type JobKind string

const (
	KindSchedule JobKind = "schedule"
	KindTrigger  JobKind = "trigger"
	KindChannel  JobKind = "channel"
)

// Job is one unit of background agent work. It carries only serializable data
// (no closures, no clients) so a durable-execution backend can persist and
// replay it.
type Job struct {
	// ID is unique per submission (used for dedup/idempotency by durable backends).
	ID string `json:"id"`
	// Kind is what fired this job.
	Kind JobKind `json:"kind"`
	// ClusterID is the tenant workspace's logical-cluster ID the job acts in.
	ClusterID string `json:"clusterID"`
	// SourceName is the Schedule / Trigger / Connection name that fired.
	SourceName string `json:"sourceName"`
	// ReplyTarget optionally overrides where a channel reply is delivered — used
	// by the Discord gateway bot, where the reply channel is the one the user
	// typed in (not the connection's configured channel). Empty → the
	// connection's default channel/target.
	ReplyTarget string `json:"replyTarget,omitempty"`
	// NotifyChannel is the logical agent-channel role (a Name in the agent's
	// spec.channels) that schedule/trigger output is delivered to. Empty → the
	// agent's primary channel. Resolved to a Connection at delivery time so a
	// re-pointed channel takes effect without re-enqueuing.
	NotifyChannel string `json:"notifyChannel,omitempty"`
	// AgentRef is the Agent to run.
	AgentRef string `json:"agentRef"`
	// Task is the prompt to execute.
	Task string `json:"task"`
	// Trigger is the Run trigger value (schedule|heartbeat|wakeup|event|channel).
	Trigger string `json:"trigger"`
	// SessionID groups the run's transcript.
	SessionID string `json:"sessionID"`
	// Timeout bounds the run; zero means the executor default.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// Handler executes one job. Implementations must be safe for concurrent calls.
// A future durable backend registers this same function as its activity.
type Handler func(ctx context.Context, job Job) error

// Executor runs jobs in the background.
type Executor interface {
	// Start begins accepting work; returns once the executor is running.
	Start(ctx context.Context) error
	// Submit enqueues a job. It must not block on job execution; it may wait
	// for queue space until ctx is done (or SubmitWait elapses), then returns
	// an error wrapping ErrQueueFull so the caller can ask the sender to retry.
	Submit(ctx context.Context, job Job) error
	// Stop drains and shuts down.
	Stop()
}

// ErrStopped is returned by Submit after Stop (or before Start).
var ErrStopped = errors.New("executor is not running")

// ErrQueueFull is returned (wrapped) by Submit when no queue slot freed up
// before the caller's context ended. Inbound webhook handlers map it to 503 +
// Retry-After so the platform redelivers instead of the event being lost.
var ErrQueueFull = errors.New("executor queue full")

// SubmitWait is the hard upper bound Submit waits for a queue slot when the
// caller's context carries no earlier deadline. Callers with a tighter budget
// (Slack retries a webhook after 3s) pass a shorter deadline on ctx.
const SubmitWait = 5 * time.Second

// submitSlowWait is how long a job must sit waiting for a queue slot before
// Submit says so. Below it the queue drained as fast as it filled and the job
// was not meaningfully delayed; above it the pool is saturated, which is a
// capacity signal an operator can act on.
const submitSlowWait = 250 * time.Millisecond

// InProcess is the small in-house implementation: a bounded worker pool with
// per-job timeout and panic isolation. No durability — a restart drops queued
// jobs (the scheduling policy re-derives them from CR state on the next tick),
// which is exactly the gap a Temporal-backed implementation would close.
type InProcess struct {
	handler Handler
	jobs    chan Job
	timeout time.Duration
	workers int
	cancel  context.CancelFunc
	// stopped is closed by Stop (via the worker context) so a Submit parked
	// on a full queue returns ErrStopped instead of waiting out SubmitWait.
	stopped <-chan struct{}
	running atomic.Bool
}

// NewInProcess builds the in-house executor. workers bounds concurrency;
// defaultTimeout is the per-job watchdog (0 → 10 minutes).
func NewInProcess(h Handler, workers int, defaultTimeout time.Duration) *InProcess {
	if workers <= 0 {
		workers = 4
	}
	if defaultTimeout <= 0 {
		defaultTimeout = 10 * time.Minute
	}
	return &InProcess{
		handler: h,
		jobs:    make(chan Job, 64),
		timeout: defaultTimeout,
		workers: workers,
	}
}

func (e *InProcess) Start(ctx context.Context) error {
	if e.running.Load() {
		return nil
	}
	ctx, e.cancel = context.WithCancel(ctx)
	e.stopped = ctx.Done()
	for i := 0; i < e.workers; i++ {
		go e.worker(ctx)
	}
	e.running.Store(true)
	return nil
}

// Submit enqueues the job, waiting for a free slot until ctx is done or
// SubmitWait elapses, whichever comes first. A full queue used to drop the job
// on the floor; an inbound message that is silently dropped is
// indistinguishable from one that never arrived, so callers now get an
// explicit ErrQueueFull to turn into a retryable response. Jobs are still not
// durable across a restart — a persistent queue is a follow-up.
func (e *InProcess) Submit(ctx context.Context, job Job) error {
	if !e.running.Load() {
		return ErrStopped
	}
	select {
	case e.jobs <- job:
		return nil
	default:
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wait, cancel := context.WithTimeout(ctx, SubmitWait)
	defer cancel()
	// Log the wait, not the attempt. A momentarily full channel is ordinary
	// under burst load — a worker frees a slot microseconds later and nothing
	// was delayed — so a line per overflow is noise that buries the case worth
	// seeing: a queue that stayed full long enough to hold the job up. The
	// timeout path needs no line of its own; it returns ErrQueueFull, which the
	// caller reports.
	start := time.Now()
	select {
	case e.jobs <- job:
		if waited := time.Since(start); waited >= submitSlowWait {
			log.Printf("executor: queue full (%d/%d), job %s/%s waited %s for a slot",
				len(e.jobs), cap(e.jobs), job.Kind, job.SourceName, waited.Round(time.Millisecond))
		}
		return nil
	case <-e.stopped:
		// Stop ran while we waited: the workers are gone, so no slot will
		// free. ErrQueueFull would tell the caller to retry against an
		// executor that no longer exists.
		return fmt.Errorf("%w — job %s/%s not accepted", ErrStopped, job.Kind, job.SourceName)
	case <-wait.Done():
		return fmt.Errorf("%w (%d/%d queued) — job %s/%s not accepted: %v", ErrQueueFull, len(e.jobs), cap(e.jobs), job.Kind, job.SourceName, wait.Err())
	}
}

// Depth reports how many jobs are queued but not yet picked up by a worker.
func (e *InProcess) Depth() int { return len(e.jobs) }

func (e *InProcess) Stop() {
	e.running.Store(false)
	if e.cancel != nil {
		e.cancel()
	}
}

func (e *InProcess) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-e.jobs:
			e.run(ctx, job)
		}
	}
}

func (e *InProcess) run(ctx context.Context, job Job) {
	timeout := job.Timeout
	if timeout <= 0 {
		timeout = e.timeout
	}
	jctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("executor: job %s %s/%s panicked: %v", job.Kind, job.ClusterID, job.SourceName, r)
		}
	}()
	if err := e.handler(jctx, job); err != nil {
		log.Printf("executor: job %s %s/%s failed: %v", job.Kind, job.ClusterID, job.SourceName, err)
	}
}
