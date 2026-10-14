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

package commitbundle

import (
	"context"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// notifyBuffer is how many arrivals a watcher may fall behind by. A drop costs
// the waiting consumer its fallback wait, never correctness, so the announce
// path never blocks a Put.
const notifyBuffer = 64

// notifier is the in-process arrival fan-out every store shares. It only
// knows about bundles this process published: with several replicas on a
// shared backend, a bundle another replica wrote is found by the consumer's
// bounded fallback read instead (commit creation already orders the bundle
// write before the CR, so that read normally succeeds first time).
type notifier struct {
	mu       sync.Mutex
	watchers []chan Arrival
}

// Notify returns a channel of arrivals that lives until ctx is done. A send
// that would block is dropped rather than queued, so a watcher that stopped
// draining can never stall a Put.
func (n *notifier) Notify(ctx context.Context) <-chan Arrival {
	ch := make(chan Arrival, notifyBuffer)
	n.mu.Lock()
	n.watchers = append(n.watchers, ch)
	n.mu.Unlock()
	if ctx != nil {
		context.AfterFunc(ctx, func() { n.unwatch(ch) })
	}
	return ch
}

func (n *notifier) unwatch(ch chan Arrival) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, watcher := range n.watchers {
		if watcher == ch {
			n.watchers = append(n.watchers[:i], n.watchers[i+1:]...)
			return
		}
	}
}

func (n *notifier) announce(scope, name string) {
	n.mu.Lock()
	watchers := make([]chan Arrival, len(n.watchers))
	copy(watchers, n.watchers)
	n.mu.Unlock()
	for _, ch := range watchers {
		select {
		case ch <- Arrival{Scope: scope, Name: name}:
		default:
		}
	}
}

// Sweeper is implemented by a store that can reclaim orphaned bundles.
type Sweeper interface {
	// Sweep removes bundles last written more than maxAge before now and
	// returns how many it removed.
	Sweep(ctx context.Context, now time.Time, maxAge time.Duration) (int, error)
	// String names the store for logs.
	String() string
}

// RunSweeper sweeps once at startup and then every interval until ctx is
// done. With a shared backend it belongs on the leader replica: every replica
// sweeping the same bucket is harmless (deletes are idempotent) but wasteful.
func RunSweeper(ctx context.Context, store Sweeper, interval, maxAge time.Duration) {
	logger := klog.FromContext(ctx).WithName("commitbundle-sweeper")
	sweep := func() {
		removed, err := store.Sweep(ctx, time.Now(), maxAge)
		if err != nil {
			logger.Error(err, "sweep orphaned bundles", "store", store.String())
		}
		if removed > 0 {
			logger.Info("removed orphaned bundles", "count", removed, "maxAge", maxAge)
		}
	}
	sweep()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
