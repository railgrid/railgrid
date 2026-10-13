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

package sharedstore

import (
	"context"
	"sync"
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/railgrid/railgrid/pkg/hub/actionproof"
)

func TestActionProofUseIsAtomicAcrossStoreReplicas(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clientset := kubefake.NewClientset()
	replicaA := NewActionProofUseStore(newTestStore(t, clientset, &now))
	replicaB := NewActionProofUseStore(newTestStore(t, clientset, &now))
	expiresAt := now.Add(time.Minute)
	const contenders = 32
	var wg sync.WaitGroup
	results := make(chan bool, contenders)
	errs := make(chan error, contenders)
	for i := range contenders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := replicaA
			if i%2 != 0 {
				store = replicaB
			}
			consumed, err := store.Consume(context.Background(), "nonce-a", "app-access.grants.manage", "POST", "/api/orgs/org/workspaces/ws/app-auth/grants", expiresAt)
			if err != nil {
				errs <- err
				return
			}
			results <- consumed
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("consume: %v", err)
	}
	wins := 0
	for consumed := range results {
		if consumed {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("successful claims=%d, want exactly one across replicas", wins)
	}

	// The same signed proof intentionally authorizes a separate resolver and
	// grant-create operation. A distinct capability, method, or path receives a
	// separate durable use coordinate.
	for _, coordinate := range []struct{ capability, method, path string }{
		{"app-auth.users.resolve", "POST", "/api/orgs/org/workspaces/ws/app-auth/user"},
		{"app-access.grants.manage", "PUT", "/api/orgs/org/workspaces/ws/app-auth/grants"},
		{"app-access.grants.manage", "POST", "/api/orgs/org/workspaces/other/app-auth/grants"},
	} {
		consumed, err := replicaB.Consume(context.Background(), "nonce-a", coordinate.capability, coordinate.method, coordinate.path, expiresAt)
		if err != nil || !consumed {
			t.Fatalf("distinct coordinate %+v consumed=%v err=%v, want independent use", coordinate, consumed, err)
		}
	}
	if consumed, err := replicaA.Consume(context.Background(), "nonce-a", "app-access.grants.manage", "POST", "/api/orgs/org/workspaces/ws/app-auth/grants", expiresAt); err != nil || consumed {
		t.Fatalf("same coordinate second claim consumed=%v err=%v, want replay refusal", consumed, err)
	}
}

func TestActionProofUseKeySeparatesEveryCoordinate(t *testing.T) {
	base := actionproof.UseKey("nonce", "capability", "POST", "/path")
	for _, tc := range []struct{ nonce, capability, method, path string }{
		{"other-nonce", "capability", "POST", "/path"},
		{"nonce", "other-capability", "POST", "/path"},
		{"nonce", "capability", "PUT", "/path"},
		{"nonce", "capability", "POST", "/other"},
	} {
		if got := actionproof.UseKey(tc.nonce, tc.capability, tc.method, tc.path); got == base {
			t.Fatalf("coordinate %+v collided with base use key", tc)
		}
	}
}

func TestCreateIfAbsentRejectsUnavailableStore(t *testing.T) {
	var store *Store
	if created, err := store.CreateIfAbsent(context.Background(), "key", []byte("value"), time.Now().Add(time.Minute)); err == nil || created {
		t.Fatalf("nil store create = %v, %v; want false and an error", created, err)
	}
}
