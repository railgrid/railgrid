// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"testing"

	"github.com/railgrid/provider-sdk/vwhealth"
)

// The hub records any received beat as liveness without inspecting it, so the
// provider must go quiet whenever readiness is false and let the TTL mark it
// stale. This is the gate runServe installs as hubclient.Config.CanSend.
func TestHeartbeatCanSendFollowsReadiness(t *testing.T) {
	ready := &vwhealth.Readiness{}
	canSend := func() bool { return ready.Check() == nil }

	if !canSend() {
		t.Fatal("a replica that is not running controllers should keep heartbeating")
	}

	detach := ready.Attach("controllers", checkerFunc(func() error {
		return errors.New("not watching any tenant workspace yet")
	}))
	if canSend() {
		t.Fatal("a leader whose controllers are not watching must not heartbeat")
	}

	detach()
	if !canSend() {
		t.Fatal("heartbeats should resume once the term ends and nothing reports unready")
	}
}
