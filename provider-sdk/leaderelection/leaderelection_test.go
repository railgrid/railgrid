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

package leaderelection

import (
	"context"
	"sync"
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"
)

const (
	testNamespace = "default"
	testLease     = "test-provider-controllers"
)

// fastOptions keeps the election responsive enough for a unit test while
// preserving the real ordering constraints.
func fastOptions(identity string) Options {
	return Options{
		Namespace:     testNamespace,
		Name:          testLease,
		Identity:      identity,
		LeaseDuration: time.Second,
		RenewDeadline: 500 * time.Millisecond,
		RetryPeriod:   50 * time.Millisecond,
	}
}

func TestRunInvokesCallbackWhenElected(t *testing.T) {
	client := kubefake.NewClientset()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	elected := make(chan struct{})
	released := make(chan struct{})
	go func() {
		defer close(released)
		_ = runWithClient(ctx, fastOptions("replica-a"), client, func(termCtx context.Context) {
			close(elected)
			<-termCtx.Done()
		})
	}()

	select {
	case <-elected:
	case <-ctx.Done():
		t.Fatal("never elected")
	}

	cancel()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// Exactly one replica may run the singleton controllers at a time — that is the
// whole point of the lease.
func TestOnlyOneReplicaLeadsAtATime(t *testing.T) {
	client := kubefake.NewClientset()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var (
		mu      sync.Mutex
		running int
		maxSeen int
	)
	leading := make(chan string, 2)

	var wg sync.WaitGroup
	for _, identity := range []string{"replica-a", "replica-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = runWithClient(ctx, fastOptions(identity), client, func(termCtx context.Context) {
				mu.Lock()
				running++
				if running > maxSeen {
					maxSeen = running
				}
				mu.Unlock()
				leading <- identity
				<-termCtx.Done()
				mu.Lock()
				running--
				mu.Unlock()
			})
		}()
	}

	select {
	case <-leading:
	case <-ctx.Done():
		t.Fatal("neither replica was elected")
	}
	// Give the loser ample opportunity to wrongly acquire as well.
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	got := maxSeen
	mu.Unlock()
	if got != 1 {
		t.Fatalf("concurrent leaders = %d, want 1", got)
	}

	cancel()
	wg.Wait()
}

// A leader whose controllers fail to start must not sit on the lease: no peer
// could take over the work it is no longer doing.
func TestLeaderStepsDownWhenCallbackReturnsEarly(t *testing.T) {
	client := kubefake.NewClientset()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	terms := make(chan struct{}, 8)
	go func() {
		_ = runWithClient(ctx, fastOptions("replica-a"), client, func(context.Context) {
			// Simulates a controller that failed to start: return immediately
			// while the lease is still held.
			terms <- struct{}{}
		})
	}()

	// Stepping down and re-campaigning must produce repeated terms rather than
	// one term that holds the lease forever.
	for i := range 2 {
		select {
		case <-terms:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d term(s) observed; leader kept the lease after failing", i)
		}
	}
}

func TestRunRequiresLeaseCoordinates(t *testing.T) {
	client := kubefake.NewClientset()
	err := runWithClient(context.Background(), Options{Identity: "replica-a"}, client, func(context.Context) {})
	if err == nil {
		t.Fatal("expected an error when Namespace and Name are unset")
	}
}
