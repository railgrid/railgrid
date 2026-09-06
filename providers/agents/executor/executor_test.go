// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package executor

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockedExecutor returns a started executor whose single worker is parked on
// a job and whose queue is filled to capacity, plus the release func that
// unblocks the worker.
func blockedExecutor(t *testing.T) (*InProcess, func()) {
	t.Helper()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	e := NewInProcess(func(ctx context.Context, _ Job) error {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}, 1, time.Minute)
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	// First job occupies the worker …
	if err := e.Submit(context.Background(), Job{ID: "busy"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never picked up the first job")
	}
	// … then fill every queue slot.
	for i := 0; i < cap(e.jobs); i++ {
		if err := e.Submit(context.Background(), Job{ID: "fill"}); err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
	}
	if e.Depth() != cap(e.jobs) {
		t.Fatalf("queue depth %d, want %d", e.Depth(), cap(e.jobs))
	}
	return e, func() { close(release) }
}

func TestSubmitBlocksThenFailsWhenContextEnds(t *testing.T) {
	e, release := blockedExecutor(t)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	begin := time.Now()
	err := e.Submit(ctx, Job{ID: "overflow", Kind: KindChannel, SourceName: "slack-1"})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	// It waited for the context rather than failing immediately.
	if waited := time.Since(begin); waited < 100*time.Millisecond {
		t.Fatalf("Submit returned after %s; it should have waited on the context", waited)
	}
	if e.Depth() != cap(e.jobs) {
		t.Fatalf("a refused job must not occupy a slot: depth %d", e.Depth())
	}
}

func TestSubmitSucceedsWhenASlotFrees(t *testing.T) {
	e, release := blockedExecutor(t)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		done <- e.Submit(ctx, Job{ID: "late"})
	}()
	// Nothing frees up for a moment; the submitter must still be waiting.
	select {
	case err := <-done:
		t.Fatalf("Submit returned early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release() // the worker drains the queue, freeing slots
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Submit should succeed once a slot frees: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Submit never returned after the queue drained")
	}
}

func TestSubmitCancelledContextFailsFast(t *testing.T) {
	e, release := blockedExecutor(t)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	begin := time.Now()
	err := e.Submit(ctx, Job{ID: "cancelled"})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	if time.Since(begin) > time.Second {
		t.Fatal("a cancelled context must not wait for SubmitWait")
	}
}

func TestSubmitBeforeStart(t *testing.T) {
	e := NewInProcess(func(context.Context, Job) error { return nil }, 1, time.Minute)
	if err := e.Submit(context.Background(), Job{}); !errors.Is(err, ErrStopped) {
		t.Fatalf("want ErrStopped, got %v", err)
	}
}

// syncBuf collects log output written from the submitting goroutine.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog redirects the standard logger for the duration of a test.
func captureLog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	flags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	})
	return buf
}

// submitAfterRelease fills the queue, submits one more job, and frees a slot
// after freeAfter. It returns whatever Submit logged.
func submitAfterRelease(t *testing.T, freeAfter time.Duration) string {
	t.Helper()
	e, release := blockedExecutor(t)
	buf := captureLog(t)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done <- e.Submit(ctx, Job{ID: "overflow", Kind: KindChannel, SourceName: "slack-1"})
	}()
	time.Sleep(freeAfter)
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Submit should have succeeded once a slot freed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Submit never returned")
	}
	return buf.String()
}

// A queue that is momentarily full is ordinary under burst load: a worker frees
// a slot moments later and nothing was held up. Logging every overflow attempt
// turns a normal burst into a wall of lines and buries the case worth seeing.
func TestSubmitDoesNotLogWhenASlotFreesPromptly(t *testing.T) {
	if logged := submitAfterRelease(t, 20*time.Millisecond); logged != "" {
		t.Fatalf("a slot that frees well inside %s must not be logged, got: %s", submitSlowWait, logged)
	}
}

// A wait past the threshold means the pool is saturated — a capacity signal an
// operator can act on — so that one still gets a line.
func TestSubmitLogsAWaitPastTheThreshold(t *testing.T) {
	logged := submitAfterRelease(t, submitSlowWait+150*time.Millisecond)
	if !strings.Contains(logged, "queue full") {
		t.Fatalf("a wait longer than %s should be logged, got: %q", submitSlowWait, logged)
	}
	if !strings.Contains(logged, "slack-1") {
		t.Fatalf("the log line should identify the job, got: %q", logged)
	}
}
