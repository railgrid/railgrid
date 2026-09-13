// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package authority

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroshq/provider-linear/internal/engine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
)

func TestMetadataHistoryScanOnlyEnqueuesChangesAcrossWorkspaces(t *testing.T) {
	version := "1"
	var scans int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scans++
		if !strings.Contains(r.Header.Get("Accept"), "as=PartialObjectMetadataList") {
			t.Error("history list requested full results")
		}
		if r.URL.Query().Get("limit") != "100" {
			t.Error("unbounded metadata page")
		}
		cluster, cursor := "one", "next"
		if r.URL.Query().Get("continue") == "next" {
			cluster, cursor = "two", ""
		}
		page := metav1.PartialObjectMetadataList{TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadataList"}, ListMeta: metav1.ListMeta{Continue: cursor}, Items: []metav1.PartialObjectMetadata{{ObjectMeta: metav1.ObjectMeta{Name: "same-name", ResourceVersion: version, Annotations: map[string]string{"kcp.io/cluster": cluster}}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	cl, err := metadata.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	var keys []objectKey
	enqueue := func(key objectKey) { keys = append(keys, key) }
	seen, err := scanChanges(context.Background(), cl.Resource(engine.Operations), nil, enqueue)
	if err != nil || len(keys) != 2 || keys[0].cluster == keys[1].cluster {
		t.Fatalf("keys=%v error=%v", keys, err)
	}
	keys = nil
	seen, err = scanChanges(context.Background(), cl.Resource(engine.Operations), seen, enqueue)
	if err != nil || len(keys) != 0 {
		t.Fatalf("unchanged history re-enqueued: %v %v", keys, err)
	}
	version = "2"
	_, err = scanChanges(context.Background(), cl.Resource(engine.Operations), seen, enqueue)
	if err != nil || len(keys) != 2 || scans != 6 {
		t.Fatalf("changed history missed: %v %v scans=%d", keys, err, scans)
	}
}

func TestWorkspaceQueueIsFairBoundedAndCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := newWorkspaceQueue()
	for _, name := range []string{"first", "second", "third"} {
		q.add(objectKey{"slow", name})
	}
	q.add(objectKey{"fast", "first"})
	started := make(chan objectKey, 4)
	done := make(chan struct{})
	var mu sync.Mutex
	active := map[string]int{}
	go func() {
		defer close(done)
		q.run(ctx, 2, func(ctx context.Context, key objectKey) (time.Duration, error) {
			mu.Lock()
			active[key.cluster]++
			if active[key.cluster] > 1 {
				t.Error("concurrent jobs in same workspace")
			}
			mu.Unlock()
			defer func() { mu.Lock(); active[key.cluster]--; mu.Unlock() }()
			started <- key
			if key.cluster == "slow" {
				<-ctx.Done()
			}
			return 0, nil
		})
	}()
	got := map[string]bool{}
	for range 2 {
		select {
		case key := <-started:
			got[key.cluster] = true
		case <-time.After(time.Second):
			t.Fatal("slow workspace blocked ready work")
		}
	}
	if !got["fast"] || !got["slow"] {
		t.Fatalf("unfair schedule: %v", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("workers did not stop")
	}
}
