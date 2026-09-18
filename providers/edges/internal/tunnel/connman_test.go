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

package tunnel

import (
	"io"
	"net"
	"testing"

	"github.com/railgrid/provider-sdk/revdial"
)

// newTestDialer returns a live *revdial.Dialer over an in-memory pipe. The peer
// end is drained because NewDialer immediately starts a keep-alive write loop
// and net.Pipe is unbuffered — an undrained peer would wedge it.
func newTestDialer(t *testing.T) *revdial.Dialer {
	t.Helper()
	local, peer := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, peer) }()
	d := revdial.NewDialer(local, "/pickup")
	t.Cleanup(func() {
		_ = d.Close()
		_ = peer.Close()
	})
	return d
}

// TestDeleteIfKeepsTunnelThatSupersededUs reproduces the reconnect race that
// left a healthy agent unroutable: the agent restarts, its new tunnel registers
// under the same stable key, and only then does the old handler wake up and run
// its cleanup. That cleanup must not evict the replacement.
func TestDeleteIfKeepsTunnelThatSupersededUs(t *testing.T) {
	c := NewConnManager()
	key := EdgeConnKey("kubernetesclusters", "1ngen6o0so3jwz2h", "minis")

	old := newTestDialer(t)
	c.Store(key, old)

	// Agent reconnects: a second tunnel registers under the same key while the
	// first handler is still parked on <-dialer.Done().
	fresh := newTestDialer(t)
	c.Store(key, fresh)

	if c.DeleteIf(key, old) {
		t.Fatal("DeleteIf reported a delete for a dialer that no longer owns the key")
	}

	got, ok := c.LoadLocal(key)
	if !ok {
		t.Fatal("live tunnel was evicted by the superseded handler's cleanup; the agent is now unroutable")
	}
	if got != Dialer(fresh) {
		t.Fatalf("LoadLocal returned the wrong dialer: got %p, want %p", got, fresh)
	}
}

// TestStoreClosesDisplacedDialer covers the other half: the superseded handler
// has to be woken at all. Without this it stays parked on <-dialer.Done() until
// its half-open socket happens to die.
func TestStoreClosesDisplacedDialer(t *testing.T) {
	c := NewConnManager()
	key := EdgeConnKey("kubernetesclusters", "1ngen6o0so3jwz2h", "minis")

	old := newTestDialer(t)
	c.Store(key, old)
	if old.IsClosed() {
		t.Fatal("dialer closed before it was displaced")
	}

	fresh := newTestDialer(t)
	c.Store(key, fresh)

	select {
	case <-old.Done():
	default:
		t.Fatal("displaced dialer was not closed; its handler stays parked on Done()")
	}
	if fresh.IsClosed() {
		t.Fatal("Store closed the dialer it was asked to store")
	}
}

// TestStoreSameDialerTwiceIsNotSelfClosing guards the degenerate case: a
// re-Store of the identical dialer must not close the entry it is storing.
func TestStoreSameDialerTwiceIsNotSelfClosing(t *testing.T) {
	c := NewConnManager()
	key := EdgeConnKey("linuxservers", "1ngen6o0so3jwz2h", "minis-server")

	d := newTestDialer(t)
	c.Store(key, d)
	c.Store(key, d)

	if d.IsClosed() {
		t.Fatal("re-storing the same dialer closed it")
	}
	if _, ok := c.LoadLocal(key); !ok {
		t.Fatal("re-storing the same dialer dropped the entry")
	}
}

// TestDeleteIfRemovesOwnEntry is the ordinary path: a genuine disconnect with
// no replacement still cleans up, otherwise stale entries would accumulate and
// edgeproxy would dial dead tunnels.
func TestDeleteIfRemovesOwnEntry(t *testing.T) {
	c := NewConnManager()
	key := EdgeConnKey("kubernetesclusters", "1ngen6o0so3jwz2h", "home")

	d := newTestDialer(t)
	c.Store(key, d)

	if !c.DeleteIf(key, d) {
		t.Fatal("owner's DeleteIf did not delete its own entry")
	}
	if _, ok := c.LoadLocal(key); ok {
		t.Fatal("entry survived its owner's DeleteIf")
	}
	if c.DeleteIf(key, d) {
		t.Fatal("second DeleteIf reported a delete for an already-removed key")
	}
}

// TestOnChangeFiresForStoreAndDelete covers the hook the edge lifecycle
// reconciler feeds into its controller source: a local connect and a local
// disconnect must each surface the edge key exactly once, and a superseded
// handler's no-op DeleteIf must stay silent.
func TestOnChangeFiresForStoreAndDelete(t *testing.T) {
	c := NewConnManager()
	key := EdgeConnKey("linuxservers", "cl-1", "edge-1")
	var seen []string
	c.OnChange(func(k string) { seen = append(seen, k) })

	old := newTestDialer(t)
	c.Store(key, old)
	if len(seen) != 1 || seen[0] != key {
		t.Fatalf("after Store: notified %v, want [%s]", seen, key)
	}

	replacement := newTestDialer(t)
	c.Store(key, replacement)
	if c.DeleteIf(key, old) {
		t.Fatal("superseded DeleteIf should not delete")
	}
	if len(seen) != 2 {
		t.Fatalf("superseded DeleteIf notified: %v", seen)
	}

	if !c.DeleteIf(key, replacement) {
		t.Fatal("owner DeleteIf should delete")
	}
	if len(seen) != 3 || seen[2] != key {
		t.Fatalf("after DeleteIf: notified %v, want a third %s", seen, key)
	}
}
