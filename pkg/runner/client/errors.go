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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/railgrid/railgrid/pkg/runner"
)

// errorBodyLimit bounds what is kept from a failed response. It is small on
// purpose: an error body is for a human reading a log line, not a payload.
const errorBodyLimit = 64 << 10

// HTTPError is a failure the runner protocol does not describe: a proxy, an
// authenticator, or kcp itself answered instead of the runner. The bounded body
// is preserved because it is usually the only diagnosis available.
type HTTPError struct {
	StatusCode int
	Body       []byte
}

func (e *HTTPError) Error() string {
	body := strings.TrimSpace(string(e.Body))
	if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
		// The credential is minted per workspace and TTL'd, and the grant is
		// name-scoped to one Service, so these two statuses have one cause
		// worth naming rather than a body worth reading.
		return fmt.Sprintf("runner access denied (HTTP %d); refresh the tenant credential or its named Service grants: %s", e.StatusCode, body)
	}
	return fmt.Sprintf("runner HTTP %d: %s", e.StatusCode, body)
}

// IdentityError reports a runner that answers but is not the runner this caller
// was enrolled with, or one that does not speak this protocol. It is a
// configuration mismatch rather than an outage, so it carries both identities:
// the two producers of a runner ID do not disagree by accident, and the
// difference is the whole diagnosis.
type IdentityError struct {
	// Reported is the runnerID the runner claims for itself.
	Reported string
	// Enrolled is the runnerID the caller was enrolled with.
	Enrolled string
	// Protocol is the protocol version the runner reported.
	Protocol string
}

func (e *IdentityError) Error() string {
	if e.Protocol != runner.ProtocolVersion {
		return fmt.Sprintf("the runner speaks protocol %q, not %s", e.Protocol, runner.ProtocolVersion)
	}
	return fmt.Sprintf("the runner calls itself %q but this caller was enrolled as %q", e.Reported, e.Enrolled)
}

// responseError turns a failed response into the most specific error the body
// supports.
//
// The runner answers every failure it originates with the protocol Error shape,
// and callers must branch on Error.Code — "is this attempt stale", "must I
// snapshot", "is the cursor gone" are decisions, not log lines. So a body that
// carries a protocol code is returned as *runner.Error and everything else
// (proxy HTML, a kcp Status, an empty body) stays an HTTPError.
//
// An AUTHORIZATION verdict is the exception, and it is not a stylistic one. A
// coordinator's exactly-once dispatch turns on "the runner, having authenticated
// me, says it has never heard of this attempt" — that answer, and only that
// answer, permits a first start. Between the caller and the runner sit kcp, the
// edges Service proxy and the agent, any of which can answer 401 or 403 with a
// body of its own choosing; and the runner's own bearer check answers before it
// has looked at any attempt. If such a body carried `{"code":"unavailable",
// "message":"attempt not found"}` — by malice or by coincidence — a caller
// branching on Code alone would read "not found" from a request that was never
// authorized, and dispatch a second execution of work already running.
//
// So 401 and 403 are always an HTTPError, whatever the body says. The status
// line is the authoritative fact about authorization, and it is the one thing a
// protocol Error cannot carry.
func responseError(status int, body []byte) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return &HTTPError{StatusCode: status, Body: body}
	}
	var protocolErr runner.Error
	if err := json.Unmarshal(body, &protocolErr); err == nil && protocolErr.Code != "" {
		return &protocolErr
	}
	return &HTTPError{StatusCode: status, Body: body}
}
