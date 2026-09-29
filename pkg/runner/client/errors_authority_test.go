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

package client

import (
	"errors"
	"net/http"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner"
)

// TestAnAuthorizationVerdictIsNeverAProtocolAnswer is the exactly-once dispatch
// property, at the one place it can be lost.
//
// A coordinator may start an attempt for the first time only when the runner,
// having authenticated it, reports that it has no such attempt. Every hop
// between the two — kcp, the Service proxy, the agent — can answer 401 or 403
// with a body of its own, and the runner's own bearer check answers before it
// has looked at any attempt. A caller that branched on the protocol code alone
// would read "not found" out of a request that was never authorized, and
// dispatch a second execution of work that is already running.
func TestAnAuthorizationVerdictIsNeverAProtocolAnswer(t *testing.T) {
	// The worst case: an authorization refusal whose body is a verbatim
	// "attempt not found" protocol error.
	absence := []byte(`{"code":"unavailable","retryable":false,"message":"attempt not found"}`)

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		err := responseError(status, absence)

		var protocolErr *runner.Error
		if errors.As(err, &protocolErr) {
			t.Fatalf("HTTP %d was reported as protocol %q: a caller would read absence from a request it was never authorized to make",
				status, protocolErr.Code)
		}
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) {
			t.Fatalf("HTTP %d produced %T, want *HTTPError so the status stays visible", status, err)
		}
		if httpErr.StatusCode != status {
			t.Errorf("HTTPError.StatusCode = %d, want %d", httpErr.StatusCode, status)
		}
	}

	// Every other status still yields the typed protocol error, because that is
	// what callers branch on for stale attempts, expired cursors and snapshots.
	err := responseError(http.StatusConflict, []byte(`{"code":"stale_attempt","retryable":false,"message":"obsolete epoch"}`))
	var protocolErr *runner.Error
	if !errors.As(err, &protocolErr) {
		t.Fatalf("a protocol body on HTTP 409 produced %T, want *runner.Error", err)
	}
	if protocolErr.Code != runner.ErrorStaleAttempt {
		t.Errorf("Code = %q, want %q", protocolErr.Code, runner.ErrorStaleAttempt)
	}
}
