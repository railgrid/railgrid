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

package status

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	farosclient "github.com/faroshq/faros/pkg/client"
)

var testGVR = schema.GroupVersionResource{
	Group:    "edges.faros.sh",
	Version:  "v1alpha1",
	Resource: "linuxservers",
}

// TestSendHeartbeatUnblocksWhenHubHangs pins the failure that left agents stuck
// in Disconnected: a hub that accepts the connection and then never answers.
//
// The reporter loop calls sendHeartbeat synchronously, so before the per-request
// deadline existed this call blocked forever. Heartbeats stopped, the hub flipped
// the Edge to Disconnected on staleness, and the agent logged nothing at all —
// the request it was parked in never returned an error to log. Without the
// deadline this test hangs until the go test timeout rather than failing fast.
func TestSendHeartbeatUnblocksWhenHubHangs(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-released // accept the request, then never respond
	}))
	// Release the handler before shutting the server down, otherwise Close
	// blocks on the in-flight request we deliberately stalled.
	defer srv.Close()
	defer close(released)

	prev := heartbeatTimeout
	heartbeatTimeout = 500 * time.Millisecond
	defer func() { heartbeatTimeout = prev }()

	// No client-side timeout: this must be survivable on the strength of the
	// per-request deadline alone.
	dyn, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("building dynamic client: %v", err)
	}
	r := NewEdgeReporter("edge", testGVR, farosclient.NewFromDynamic(dyn), nil, 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.sendHeartbeat(context.Background(), klog.Background())
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sendHeartbeat blocked on an unresponsive hub; the reporter loop would never heartbeat again")
	}
}

// TestSendHeartbeatHonoursParentCancellation ensures shutdown is not delayed by
// the per-request deadline: cancelling the reporter's context must return
// immediately rather than waiting out heartbeatTimeout.
func TestSendHeartbeatHonoursParentCancellation(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-released
	}))
	defer srv.Close()
	defer close(released)

	prev := heartbeatTimeout
	heartbeatTimeout = 30 * time.Second
	defer func() { heartbeatTimeout = prev }()

	dyn, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("building dynamic client: %v", err)
	}
	r := NewEdgeReporter("edge", testGVR, farosclient.NewFromDynamic(dyn), nil, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.sendHeartbeat(ctx, klog.Background())
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sendHeartbeat ignored parent cancellation; agent shutdown would stall")
	}
}
