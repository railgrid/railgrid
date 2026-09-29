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
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/agent/discovery"
)

// fakeRunners is a harness plane with one runner on one port.
type fakeRunners struct {
	port    int
	token   string
	service discovery.DiscoveredService
}

func (f *fakeRunners) Services() []discovery.DiscoveredService {
	return []discovery.DiscoveredService{f.service}
}

func (f *fakeRunners) RunnerToken(port int) (string, bool) {
	if port != f.port {
		return "", false
	}
	return f.token, true
}

// runnerProxyFixture stands up a loopback upstream that records the
// Authorization header it was given, and proxies one request to it through the
// /svc handler with a harness plane that claims that port.
func runnerProxyFixture(t *testing.T, claimPort func(actual int) int) (do func(header string) (*http.Response, string)) {
	t.Helper()
	seen := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(upstream.Close)

	port, err := strconv.Atoi(upstreamPort(t, upstream))
	if err != nil {
		t.Fatal(err)
	}
	runners := &RunnerRegistry{}
	runners.Set(&fakeRunners{port: claimPort(port), token: "the-real-runner-bearer"})

	cfg := svcProxyConfig{
		SvcProxyOptions: SvcProxyOptions{Policy: SvcPolicyEnforce, Runners: runners},
		resolve:         fixedResolver(map[string][]string{"localhost": {"127.0.0.1"}}),
	}
	handler := newSvcProxyHandler(cfg)

	return func(header string) (*http.Response, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/svc/runner/v1/capabilities", nil)
		req.Header.Set(svcTargetHeader, "http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		resp := rec.Result()
		return resp, <-seen
	}
}

// TestSvcProxyInjectsTheRunnerBearer: the runner's token stays on the host, so
// the published Service carries no credential and the agent is the only party
// that can authenticate to it.
func TestSvcProxyInjectsTheRunnerBearer(t *testing.T) {
	do := runnerProxyFixture(t, func(actual int) int { return actual })
	resp, got := do("")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got != "Bearer the-real-runner-bearer" {
		t.Fatalf("upstream saw Authorization %q, want the supervised runner's bearer", got)
	}
}

// TestSvcProxyRefusesToSmuggleAnAuthorizationHeader: a caller that puts its own
// Authorization on the request must not have it reach the runner. The header is
// REPLACED, not defaulted, so the tunnel can never be used to relay a guessed or
// stolen bearer to a local code-execution service.
func TestSvcProxyRefusesToSmuggleAnAuthorizationHeader(t *testing.T) {
	do := runnerProxyFixture(t, func(actual int) int { return actual })
	_, got := do("Bearer smuggled-from-the-hub")
	if strings.Contains(got, "smuggled") {
		t.Fatalf("upstream saw the caller's own Authorization %q", got)
	}
	if got != "Bearer the-real-runner-bearer" {
		t.Fatalf("upstream saw Authorization %q, want the supervised runner's bearer", got)
	}
}

// TestSvcProxyLeavesANonRunnerTargetAlone: the only source of a runner token is a
// port lookup that answers for supervised runners, so another local service is
// proxied with exactly the credential the provider gave it — and never with a
// runner's.
func TestSvcProxyLeavesANonRunnerTargetAlone(t *testing.T) {
	// The plane claims a different port than the one the request targets.
	do := runnerProxyFixture(t, func(actual int) int { return actual + 1 })

	if _, got := do(""); got != "" {
		t.Fatalf("a non-runner target received Authorization %q", got)
	}
	if _, got := do("Bearer the-services-own-credential"); got != "Bearer the-services-own-credential" {
		t.Fatalf("a non-runner target's own credential was altered: %q", got)
	}
}

// TestServicesHandlerAdvertisesRunnersAlongsideDetectors: the runner is published
// as an ordinary Service by the provider's existing discovery loop, which only
// works if it appears in this answer.
func TestServicesHandlerAdvertisesRunnersAlongsideDetectors(t *testing.T) {
	runners := &RunnerRegistry{}
	runners.Set(&fakeRunners{port: 8787, token: "t", service: discovery.DiscoveredService{
		Name:    "claude",
		Type:    discovery.ServiceTypeRunner,
		Harness: "claude",
		Scheme:  "http",
		Port:    8787,
		Version: "2.1.273",
		Ready:   true,
	}})

	rec := httptest.NewRecorder()
	newServicesHandler(runners).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body servicesResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, svc := range body.Services {
		if svc.Type == discovery.ServiceTypeRunner {
			found = true
			if svc.Harness != "claude" || svc.Port != 8787 || !svc.Ready || svc.Version != "2.1.273" {
				t.Errorf("runner entry = %+v, want the plane's own view verbatim", svc)
			}
		}
	}
	if !found {
		t.Fatalf("no runner entry in %+v", body.Services)
	}
}

// TestServicesHandlerWithoutAHarnessPlane: an agent that supervises nothing must
// still answer discovery, and must not invent a runner.
func TestServicesHandlerWithoutAHarnessPlane(t *testing.T) {
	rec := httptest.NewRecorder()
	newServicesHandler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body servicesResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, svc := range body.Services {
		if svc.Type == discovery.ServiceTypeRunner {
			t.Fatalf("advertised a runner with no harness plane: %+v", svc)
		}
	}
}
