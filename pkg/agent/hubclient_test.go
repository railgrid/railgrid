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

package agent

import (
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

// TestApplyHubClientDefaults covers the invariant that every hub client gets a
// request deadline. A zero rest.Config.Timeout means http.Client.Timeout is
// zero, i.e. no deadline: an in-flight request against a half-open connection
// then never returns and wedges its caller.
func TestApplyHubClientDefaults(t *testing.T) {
	cases := []struct {
		name string
		in   *rest.Config
		want time.Duration
	}{
		{
			name: "zero timeout gets the default",
			in:   &rest.Config{Host: "https://hub.example"},
			want: hubClientTimeout,
		},
		{
			name: "explicit timeout is preserved",
			in:   &rest.Config{Host: "https://hub.example", Timeout: 5 * time.Second},
			want: 5 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyHubClientDefaults(tc.in)
			if got.Timeout != tc.want {
				t.Errorf("Timeout = %v, want %v", got.Timeout, tc.want)
			}
		})
	}

	if applyHubClientDefaults(nil) != nil {
		t.Error("applyHubClientDefaults(nil) should return nil rather than panic")
	}
}

// TestHubClientTimeoutBoundsHeartbeats guards the relationship the wedge
// depended on: the client deadline must be short enough that a stalled request
// cannot outlive the hub's staleness threshold for an Edge (90s), otherwise the
// Edge is marked Disconnected before the agent ever notices the request failed.
func TestHubClientTimeoutBoundsHeartbeats(t *testing.T) {
	const staleHeartbeatThreshold = 90 * time.Second
	if hubClientTimeout >= staleHeartbeatThreshold {
		t.Errorf("hubClientTimeout (%v) must be well under the hub staleness threshold (%v)",
			hubClientTimeout, staleHeartbeatThreshold)
	}
}
