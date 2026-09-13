// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/faroshq/provider-linear/internal/engine"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

// workspaceGate bounds in-flight requests without retaining idle workspace entries.
type workspaceGate struct {
	mu     sync.Mutex
	active map[string]int
	limit  int
}

func (g *workspaceGate) acquire(key string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[key] >= g.limit {
		return nil, false
	}
	if g.active == nil {
		g.active = make(map[string]int)
	}
	g.active[key]++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.active[key]--
		if g.active[key] == 0 {
			delete(g.active, key)
		}
	}, true
}

type tenantResolver func(context.Context, string, string) (dynamic.Interface, error)

func newWebhookHandler(resolve tenantResolver) http.Handler {
	slots := make(chan struct{}, 16)
	requests := &workspaceGate{limit: 2}
	admission := &workspaceGate{limit: 1}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, name := r.PathValue("cluster"), r.PathValue("connection")
		if !namePattern.MatchString(cluster) || len(validation.IsDNS1123Subdomain(name)) != 0 {
			http.Error(w, "invalid route", http.StatusBadRequest)
			return
		}
		release, ok := requests.acquire(cluster)
		if !ok {
			http.Error(w, "workspace busy", http.StatusServiceUnavailable)
			return
		}
		defer release()
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "receiver busy", http.StatusServiceUnavailable)
			return
		}
		// Leave time for the HTTP response inside Linear's five-second delivery window.
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(4 * time.Second))
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
		if err != nil {
			http.Error(w, "invalid payload", http.StatusRequestEntityTooLarge)
			return
		}
		cl, err := resolve(ctx, cluster, name)
		if err != nil {
			http.Error(w, "subscription unavailable", http.StatusServiceUnavailable)
			return
		}
		e := engine.Engine{Client: cl}
		verified, err := e.PrepareWebhook(ctx, name, r.Header.Get("Linear-Signature"), r.Header.Get("Linear-Delivery"), raw)
		if err != nil {
			http.Error(w, "delivery not accepted", http.StatusServiceUnavailable)
			return
		}
		// Fail fast on contention; Linear retries. Only count-and-create needs exclusion.
		unlock, ok := admission.acquire(cluster)
		if !ok {
			http.Error(w, "workspace admission busy", http.StatusServiceUnavailable)
			return
		}
		defer unlock()
		if err := e.PersistWebhook(ctx, verified); err != nil {
			http.Error(w, "delivery not accepted", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
