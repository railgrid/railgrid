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

package harnessplane

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"
)

// probeFixture serves one capabilities document on loopback and returns a child
// pointed at it.
func probeFixture(t *testing.T, body string) (*child, *string) {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/runner/v1/capabilities" {
			t.Errorf("probed %s, want /runner/v1/capabilities", r.URL.Path)
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	_, rawPort, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	c := newChild(HarnessClaude, port, childConfig{
		EdgeName:      "build-01",
		Executable:    "/nonexistent",
		Account:       RunAsAccount{Home: "/tmp", UID: InheritUID, GID: InheritUID},
		ProbeDeadline: 50 * time.Millisecond,
		ProbeTimeout:  50 * time.Millisecond,
		HTTPClient:    srv.Client(),
	})
	c.bearer = "the-runner-bearer"
	return c, &auth
}

// TestProbeReadsTheHarnessVersionAndReadiness: the harness advertises its OWN
// name ("claude-code") while spec.harness selects it as "claude". Matching the
// two would report every Claude Code runner as never ready, so the single entry
// of a single-harness runner is read as-is.
func TestProbeReadsTheHarnessVersionAndReadiness(t *testing.T) {
	c, auth := probeFixture(t, `{"protocolVersion":"`+runner.ProtocolVersion+
		`","version":"v0.2.7","runnerID":"build-01-claude","harnesses":[{"name":"claude-code","version":"2.1.273","ready":true}]}`)

	if err := c.probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	observed := c.snapshot()
	if !observed.Ready || observed.Version != "2.1.273" {
		t.Fatalf("observed = %+v, want ready with version 2.1.273", observed)
	}
	if *auth != "Bearer the-runner-bearer" {
		t.Errorf("probe sent Authorization %q, want the runner's own bearer", *auth)
	}
}

// TestProbeCarriesTheHarnessReasonsThrough: "not ready, and here is why" is what
// an operator acts on, so the reasons reach the heartbeat instead of being
// flattened into a bare false.
func TestProbeCarriesTheHarnessReasonsThrough(t *testing.T) {
	c, _ := probeFixture(t, `{"protocolVersion":"`+runner.ProtocolVersion+
		`","harnesses":[{"name":"claude-code","ready":false,"reasons":["claude executable not found"]}]}`)

	if err := c.probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	observed := c.snapshot()
	if observed.Ready || len(observed.Reasons) != 1 || !strings.Contains(observed.Reasons[0], "not found") {
		t.Fatalf("observed = %+v, want not-ready with the harness's reason", observed)
	}
}

// TestProbeRefusesAForeignProtocol: something else listening on the port is not
// this agent's runner, and reporting it ready would advertise a service the
// agent neither built nor configures.
func TestProbeRefusesAForeignProtocol(t *testing.T) {
	c, _ := probeFixture(t, `{"protocolVersion":"runner/v99","harnesses":[{"name":"claude-code","ready":true}]}`)

	if err := c.probe(context.Background()); err == nil {
		t.Fatal("a foreign protocol version was accepted")
	}
	if observed := c.snapshot(); observed.Ready {
		t.Error("a failed probe left readiness set")
	}
}

// TestRenderConfigPinsTheListenerToLoopback: the enrollment is the only place the
// runner's address is decided, and a runner bound anywhere else would be
// reachable without the tunnel's authorization.
func TestRenderConfigPinsTheListenerToLoopback(t *testing.T) {
	c := newChild(HarnessCodex, 8788, childConfig{EdgeName: "build-01"})
	body, err := c.renderConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"listen": "127.0.0.1:8788"`, `"runnerID": "build-01-codex"`, `"maximumCapacity": 1`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("runner.json lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), "repositories") {
		t.Errorf("runner.json enrolls a repository; the caller names one per attempt:\n%s", body)
	}
}

// TestChildArgsCarryNoCredential: the caller sends its own harness credential
// with each attempt, so nothing about authentication may appear on the command
// line of a code-execution child (where every local account can read it).
func TestChildArgsCarryNoCredential(t *testing.T) {
	c := newChild(HarnessClaude, 8787, childConfig{EdgeName: "build-01",
		Account: RunAsAccount{Home: "/home/railgrid-runner", UID: InheritUID, GID: InheritUID}})
	args := strings.Join(c.childArgs("/opt/bin/claude"), " ")
	// --token-file is fine and expected: it is the runner's OWN bearer, which the
	// agent generated and which never leaves the host. What must not be here is
	// anything about a harness identity.
	for _, forbidden := range []string{"credential", "apiKey", "oauth"} {
		if strings.Contains(args, forbidden) {
			t.Errorf("child args mention %q: %s", forbidden, args)
		}
	}
	for _, want := range []string{"--harness claude", "--claude-home /home/railgrid-runner/.railgrid/runner/claude/claude-home", "--claude-binary /opt/bin/claude"} {
		if !strings.Contains(args, want) {
			t.Errorf("child args lack %q: %s", want, args)
		}
	}
}
