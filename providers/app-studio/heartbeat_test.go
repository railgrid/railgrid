// Copyright 2026 The Faros Authors.
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
)

func TestHeartbeatCanSendFollowsControllerReadiness(t *testing.T) {
	if !heartbeatCanSend(nil) {
		t.Fatal("heartbeat without a health dependency should remain compatible")
	}

	restOnly := newControllerHealth(false)
	if !heartbeatCanSend(restOnly) {
		t.Fatal("REST-only mode should continue heartbeating")
	}

	required := newControllerHealth(true)
	if heartbeatCanSend(required) {
		t.Fatal("starting required controller must not heartbeat")
	}
	required.markFailed(errors.New("manager exited"))
	if heartbeatCanSend(required) {
		t.Fatal("failed required controller must not heartbeat")
	}
	required.markReady()
	if !heartbeatCanSend(required) {
		t.Fatal("running required controller should heartbeat")
	}
	required.markStopped(errors.New("shutdown"))
	if heartbeatCanSend(required) {
		t.Fatal("stopped required controller must not heartbeat")
	}
}
