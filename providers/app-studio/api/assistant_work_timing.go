/*
Copyright 2026 The Railgrid Authors.

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

package api

import (
	"context"
	"sync"
	"time"

	"github.com/railgrid/provider-app-studio/store"
)

type projectAssistantWorkTimingKey struct{}

// The prior measurement comes from the last durable checkpoint. Completed
// operations add to it once, even when tools execute concurrently.
type projectAssistantWorkTiming struct {
	mu       sync.Mutex
	prior    *int64
	measured bool
	duration time.Duration
}

func measureProjectAssistantWork(ctx context.Context) func() {
	timing, _ := ctx.Value(projectAssistantWorkTimingKey{}).(*projectAssistantWorkTiming)
	if timing == nil {
		return func() {}
	}
	start := time.Now()
	return sync.OnceFunc(func() {
		elapsed := time.Since(start)
		timing.mu.Lock()
		defer timing.mu.Unlock()
		timing.measured = true
		timing.duration += elapsed
	})
}

func (t *projectAssistantWorkTiming) updateRun(run *store.AssistantRun) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.measured {
		return
	}
	ms := t.duration.Milliseconds()
	if t.prior != nil {
		ms += *t.prior
	}
	run.WorkedDurationMS = &ms
}
