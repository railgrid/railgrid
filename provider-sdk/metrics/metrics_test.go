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

package metrics

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestListenFromEnv(t *testing.T) {
	t.Setenv(ListenEnv, "")
	if got := ListenFromEnv(); got != "" {
		t.Errorf("set-but-empty should disable, got %q", got)
	}
	t.Setenv(ListenEnv, ":1234")
	if got := ListenFromEnv(); got != ":1234" {
		t.Errorf("got %q", got)
	}
}

func TestListenFromEnvDefault(t *testing.T) {
	if _, ok := lookupUnset(t); ok {
		t.Skip("env set by the environment")
	}
	if got := ListenFromEnv(); got != DefaultListen {
		t.Errorf("unset should default to %q, got %q", DefaultListen, got)
	}
}

func lookupUnset(t *testing.T) (string, bool) {
	t.Helper()
	// t.Setenv cannot unset, so only assert the default when the variable is absent.
	return "", false
}

// Serve exposes controller-runtime's registry (Go collectors included) plus a
// provider's own collector, and a hand-rendered exposition merged through
// TextGatherer, on one /metrics.
func TestServeMergesRegistryAndText(t *testing.T) {
	own := prometheus.NewCounter(prometheus.CounterOpts{Name: "provider_test_own_total", Help: "own"})
	if err := Registry.Register(own); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Registry.Unregister(own) })
	own.Inc()

	text := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintln(w, "# TYPE provider_test_hand_total counter")
		_, _ = fmt.Fprintln(w, `provider_test_hand_total{outcome="ok"} 7`)
	})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { ServeWith(ctx, addr, TextGatherer(text)); close(done) }()

	var body string
	for i := 0; i < 50; i++ {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			body = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{"go_goroutines", "provider_test_own_total 1", `provider_test_hand_total{outcome="ok"} 7`} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("ServeWith did not return after cancel")
	}
}

func TestServeDisabledReturns(t *testing.T) {
	done := make(chan struct{})
	go func() { Serve(context.Background(), ""); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("empty addr must return immediately")
	}
}
