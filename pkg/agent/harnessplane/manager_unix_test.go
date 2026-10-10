//go:build unix

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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// stubExecutable writes a script that stands in for `railgrid runner run`: it
// accepts any arguments and stays up, so the supervisor has something real to
// manage without the tests depending on a harness or a listening runner.
func stubExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub-agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

// stubHarness makes name look installed under home, which is what mode auto
// keys on.
func stubHarness(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, "bin")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// testManager builds a manager whose detection and free-port check are under the
// test's control, so the result does not depend on what is installed on the
// machine running the tests or on which ports it happens to have free.
func testManager(t *testing.T, home string, detect func() Detection) *Manager {
	return testManagerBlocked(t, home, func() (Detection, Blocked) { return detect(), nil })
}

// testManagerBlocked is testManager for a detection that also reports installs
// the runner account cannot execute.
func testManagerBlocked(t *testing.T, home string, detect func() (Detection, Blocked)) *Manager {
	t.Helper()
	if os.Geteuid() == 0 {
		// The supervisor refuses to launch a child as the agent's own account
		// when that account is root, which is exactly the property under test
		// elsewhere. These cases need a non-root test process.
		t.Skip("this case runs a supervised child as the test's own account")
	}
	m, err := NewManager(nil, Options{
		EdgeName:   "build-01",
		Executable: stubExecutable(t),
		Account:    RunAsAccount{Home: home, UID: InheritUID, GID: InheritUID},
		CachePath:  filepath.Join(home, "agent-build-01.harness.json"),
		Detect:     detect,
		// Every port is free: the stub child never listens, so binding is not
		// what these cases are about.
		PortFree:       func(int) bool { return true },
		ProbeDeadline:  20 * time.Millisecond,
		ProbeTimeout:   20 * time.Millisecond,
		InitialBackoff: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	return m
}

// TestAutoPicksUpANewlyInstalledHarness: detection re-runs on every reconcile
// precisely so a machine that gains Claude Code after it was onboarded starts
// offering it without anybody touching the hub.
func TestAutoPicksUpANewlyInstalledHarness(t *testing.T) {
	home := tempHome(t)
	installed := Detection{}
	m := testManager(t, home, func() Detection { return installed })
	ctx := context.Background()

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile with nothing installed: %v", err)
	}
	if svcs := m.Services(); len(svcs) != 0 {
		t.Fatalf("advertised %d services with no harness installed", len(svcs))
	}
	for _, status := range m.Statuses() {
		if status.Detected || status.Enabled {
			t.Errorf("%s reported detected=%v enabled=%v with nothing installed", status.Name, status.Detected, status.Enabled)
		}
	}

	installed = Detection{HarnessClaude: stubHarness(t, home, HarnessClaude)}
	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile after the install: %v", err)
	}
	svcs := m.Services()
	if len(svcs) != 1 || svcs[0].Harness != HarnessClaude || svcs[0].Type != "runner" {
		t.Fatalf("services after the install = %+v, want one runner for claude", svcs)
	}
	if svcs[0].Scheme != "http" || svcs[0].Port != DefaultBasePort {
		t.Errorf("advertised %s:%d, want http on %d", svcs[0].Scheme, svcs[0].Port, DefaultBasePort)
	}
	if _, ok := m.RunnerToken(int(svcs[0].Port)); !ok {
		t.Error("no bearer for the advertised runner port; the /svc proxy would have nothing to inject")
	}
}

// TestPortAllocationIsDeterministicForTwoHarnesses: two harnesses on one machine
// must not collide, and nobody should have to assign ports by hand.
func TestPortAllocationIsDeterministicForTwoHarnesses(t *testing.T) {
	home := tempHome(t)
	detection := Detection{
		HarnessCodex:  stubHarness(t, home, HarnessCodex),
		HarnessClaude: stubHarness(t, home, HarnessClaude),
	}
	m := testManager(t, home, func() Detection { return detection })
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	ports := map[string]int32{}
	for _, svc := range m.Services() {
		ports[svc.Harness] = svc.Port
	}
	if ports[HarnessClaude] != DefaultBasePort || ports[HarnessCodex] != DefaultBasePort+1 {
		t.Fatalf("ports = %v, want claude on %d and codex on %d (canonical order)", ports, DefaultBasePort, DefaultBasePort+1)
	}
	// Each runner's bearer answers for its own port only, so the proxy can never
	// hand one harness's token to another.
	claudeToken, ok := m.RunnerToken(DefaultBasePort)
	if !ok {
		t.Fatal("no bearer for the claude runner")
	}
	codexToken, ok := m.RunnerToken(DefaultBasePort + 1)
	if !ok {
		t.Fatal("no bearer for the codex runner")
	}
	if claudeToken == codexToken {
		t.Error("the two runners share a bearer token")
	}
	if _, ok := m.RunnerToken(DefaultBasePort + 7); ok {
		t.Error("a port this agent supervises nothing on was handed a runner token")
	}
}

// TestModeNoneStopsTheChildrenAndKeepsState: the opt-out has to be safe to use
// on a running machine. It stops the processes and leaves every durable file
// alone, so switching back resumes the same sessions instead of losing work.
func TestModeNoneStopsTheChildrenAndKeepsState(t *testing.T) {
	home := tempHome(t)
	detection := Detection{HarnessClaude: stubHarness(t, home, HarnessClaude)}
	m := testManager(t, home, func() Detection { return detection })
	ctx := context.Background()

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stateDir := filepath.Join(home, ".railgrid", "runner", HarnessClaude)
	for _, path := range []string{
		filepath.Join(stateDir, "token"),
		filepath.Join(stateDir, "runner.json"),
		filepath.Join(stateDir, "claude-home"),
		filepath.Join(stateDir, "state"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s after a reconcile: %v", path, err)
		}
	}
	tokenBefore, err := os.ReadFile(filepath.Join(stateDir, "token"))
	if err != nil {
		t.Fatal(err)
	}

	m.Observe(ctx, Setting{Mode: ModeNone})

	if svcs := m.Services(); len(svcs) != 0 {
		t.Errorf("still advertising %+v after mode none", svcs)
	}
	if _, ok := m.RunnerToken(DefaultBasePort); ok {
		t.Error("the /svc proxy would still inject a bearer for a stopped runner")
	}
	for _, status := range m.Statuses() {
		if status.Enabled || status.Ready {
			t.Errorf("%s reported enabled=%v ready=%v under mode none", status.Name, status.Enabled, status.Ready)
		}
		if status.Name == HarnessClaude && !status.Detected {
			t.Error("claude should still be reported as detected: it is installed, just switched off")
		}
	}
	tokenAfter, err := os.ReadFile(filepath.Join(stateDir, "token"))
	if err != nil {
		t.Fatalf("the runner token was destroyed by a stop: %v", err)
	}
	if string(tokenAfter) != string(tokenBefore) {
		t.Error("the runner token was rotated by a stop; an in-flight attempt would have been orphaned")
	}

	// Switching back reuses the same identity and port.
	m.Observe(ctx, Setting{Mode: ModeAuto})
	svcs := m.Services()
	if len(svcs) != 1 || svcs[0].Port != DefaultBasePort {
		t.Fatalf("services after switching back = %+v, want claude on %d again", svcs, DefaultBasePort)
	}
	token, _ := m.RunnerToken(DefaultBasePort)
	if token+"\n" != string(tokenBefore) {
		t.Error("the bearer changed across an off/on cycle")
	}
}

// TestTheFirstObservedSpecOverwritesASeededCache is the precedence rule: an
// install flag may seed the cache, and the hub's first word replaces it for good.
func TestTheFirstObservedSpecOverwritesASeededCache(t *testing.T) {
	home := tempHome(t)
	detection := Detection{HarnessClaude: stubHarness(t, home, HarnessClaude)}
	m := testManager(t, home, func() Detection { return detection })
	ctx := context.Background()

	// --harness=none seeds the cache, and the plane runs that while the hub is
	// out of reach.
	seeded, err := SeedCache(m.opts.CachePath, Setting{Mode: ModeNone})
	if err != nil || !seeded {
		t.Fatalf("SeedCache = %v, %v", seeded, err)
	}
	cached, _, err := LoadCache(m.opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	m.Observe(ctx, cached) // stands in for the cached apply Run does at startup
	if len(m.Services()) != 0 {
		t.Fatal("the seeded 'none' was not applied")
	}

	// The hub then says auto. It wins, and it wins on disk too.
	m.Observe(ctx, Setting{Mode: ModeAuto})
	if svcs := m.Services(); len(svcs) != 1 {
		t.Fatalf("services after the observed spec = %+v, want one runner", svcs)
	}
	got, found, err := LoadCache(m.opts.CachePath)
	if err != nil || !found {
		t.Fatalf("LoadCache = %v, %v", found, err)
	}
	if got.Mode != ModeAuto {
		t.Fatalf("cached mode = %q, want the observed %q to have replaced the seed", got.Mode, ModeAuto)
	}

	// And a later flag cannot take it back: seeding is a no-op once the file
	// exists, which is the only mechanism a flag has.
	seeded, err = SeedCache(m.opts.CachePath, Setting{Mode: ModeNone})
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("a flag overwrote the setting the agent had observed on its edge")
	}
}

// TestObserveRejectsAnUnusableSpecAndKeepsRunning: a spec this build cannot act
// on is not a reason to tear down working harnesses.
func TestObserveRejectsAnUnusableSpecAndKeepsRunning(t *testing.T) {
	home := tempHome(t)
	detection := Detection{HarnessClaude: stubHarness(t, home, HarnessClaude)}
	m := testManager(t, home, func() Detection { return detection })
	ctx := context.Background()
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	m.Observe(ctx, Setting{Mode: Mode("whatever-comes-next")})
	if svcs := m.Services(); len(svcs) != 1 {
		t.Fatalf("services = %+v, want the running runner to have survived an unusable spec", svcs)
	}
}

// TestAnExplicitHarnessThatIsNotInstalledIsReported: dropping it silently would
// leave "I enabled claude and nothing happened" with nowhere to look.
func TestAnExplicitHarnessThatIsNotInstalledIsReported(t *testing.T) {
	home := tempHome(t)
	m := testManager(t, home, func() Detection { return Detection{} })
	m.Observe(context.Background(), Setting{Mode: ModeExplicit, Enabled: []string{HarnessClaude}})

	var claude HarnessStatus
	for _, status := range m.Statuses() {
		if status.Name == HarnessClaude {
			claude = status
		}
	}
	if !claude.Enabled || claude.Detected || claude.Ready {
		t.Fatalf("claude = %+v, want enabled with detected=false and ready=false", claude)
	}
	if len(claude.Reasons) == 0 {
		t.Fatal("no reason given for an enabled harness that is not installed")
	}
	if len(m.Services()) != 0 {
		t.Error("a harness that is not installed was advertised as a service")
	}
}

// TestStatusesReportsEveryKnownHarness keeps the heartbeat's shape stable: the
// field is the answer to "which of my edges can run Claude Code", so a harness
// that is absent has to appear as absent rather than not appear.
func TestStatusesReportsEveryKnownHarness(t *testing.T) {
	home := tempHome(t)
	m := testManager(t, home, func() Detection { return Detection{} })
	var names []string
	for _, status := range m.Statuses() {
		names = append(names, status.Name)
	}
	if !slices.Equal(names, Names) {
		t.Fatalf("statuses = %v, want one entry per known harness %v", names, Names)
	}
}

// TestABlockedInstallIsReportedEvenUnderModeAuto: Claude Code installed as root
// lives under /root, which the runner account cannot enter. Detection skips it,
// so under mode auto nothing is wanted and nothing would be said — leaving an
// operator looking at "detected: false" on a machine that plainly has it. The
// status must carry the reason, and name the path and the account.
func TestABlockedInstallIsReportedEvenUnderModeAuto(t *testing.T) {
	home := tempHome(t)
	m := testManagerBlocked(t, home, func() (Detection, Blocked) {
		return Detection{}, Blocked{HarnessClaude: "/root/.local/bin/claude"}
	})
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var claude HarnessStatus
	for _, status := range m.Statuses() {
		if status.Name == HarnessClaude {
			claude = status
		}
	}
	if claude.Detected || claude.Enabled || claude.Ready {
		t.Errorf("a blocked install reported detected=%v enabled=%v ready=%v; want none", claude.Detected, claude.Enabled, claude.Ready)
	}
	if len(claude.Reasons) != 1 || !strings.Contains(claude.Reasons[0], "/root/.local/bin/claude") || !strings.Contains(claude.Reasons[0], "cannot execute") {
		t.Errorf("reasons = %q; want the blocked path and why", claude.Reasons)
	}
}

// TestAnExplicitHarnessThatIsBlockedSaysSoInsteadOfNotInstalled: "not installed"
// would send the operator to install what is already there.
func TestAnExplicitHarnessThatIsBlockedSaysSoInsteadOfNotInstalled(t *testing.T) {
	home := tempHome(t)
	m := testManagerBlocked(t, home, func() (Detection, Blocked) {
		return Detection{}, Blocked{HarnessClaude: "/root/.local/bin/claude"}
	})
	m.Observe(context.Background(), Setting{Mode: ModeExplicit, Enabled: []string{HarnessClaude}})
	for _, status := range m.Statuses() {
		if status.Name != HarnessClaude {
			continue
		}
		if len(status.Reasons) != 1 || strings.Contains(status.Reasons[0], "not installed") || !strings.Contains(status.Reasons[0], "cannot execute") {
			t.Errorf("reasons = %q; want the blocked explanation, not \"not installed\"", status.Reasons)
		}
	}
}
