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
	// Submit enqueues a job. It must not block on job execution.
	Submit(ctx context.Context, job Job) error
	// Stop drains and shuts down.
	Stop()
}

// ErrStopped is returned by Submit after Stop (or before Start).
var ErrStopped = errors.New("executor is not running")

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
	running bool
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
	if e.running {
		return nil
	}
	ctx, e.cancel = context.WithCancel(ctx)
	for i := 0; i < e.workers; i++ {
		go e.worker(ctx)
	}
	e.running = true
	return nil
}

func (e *InProcess) Submit(_ context.Context, job Job) error {
	if !e.running {
		return ErrStopped
	}
	select {
	case e.jobs <- job:
		return nil
	default:
		return fmt.Errorf("executor queue full — job %s/%s dropped", job.Kind, job.SourceName)
	}
}

func (e *InProcess) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.running = false
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
