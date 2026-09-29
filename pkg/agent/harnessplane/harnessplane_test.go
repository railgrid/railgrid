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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// tempHome resolves the temporary directory's symlinks. On macOS t.TempDir()
// lives under /var, which is a symlink to /private/var, and the symlink-hardened
// writer deliberately refuses to write through a symlinked path component. A
// real account's home is not behind a symlink, so resolving here tests the code
// rather than the platform's /var.
func tempHome(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestParseSettingRefusesATypo: --harness is the machine owner's half of the
// setting. A value that normalizes to "nothing" would leave an operator
// believing they configured a harness while the machine offers none, so a typo
// has to be an error.
func TestParseSettingRefusesATypo(t *testing.T) {
	if _, err := ParseSetting("cluade"); err == nil {
		t.Fatal("an unknown harness name was accepted")
	} else if !strings.Contains(err.Error(), "cluade") || !strings.Contains(err.Error(), "claude") {
		t.Errorf("the error should name the bad value and the known set: %v", err)
	}
	if _, err := ParseSetting("explicit"); err == nil {
		t.Fatal("--harness=explicit without a list was accepted")
	}
}

func TestParseSettingModesAndLists(t *testing.T) {
	for raw, want := range map[string]Setting{
		"":                 {Mode: ModeAuto},
		"auto":             {Mode: ModeAuto},
		"AUTO":             {Mode: ModeAuto},
		"none":             {Mode: ModeNone},
		"claude":           {Mode: ModeExplicit, Enabled: []string{"claude"}},
		" codex , claude ": {Mode: ModeExplicit, Enabled: []string{"claude", "codex"}},
		"codex,codex":      {Mode: ModeExplicit, Enabled: []string{"codex"}},
	} {
		got, err := ParseSetting(raw)
		if err != nil {
			t.Fatalf("ParseSetting(%q): %v", raw, err)
		}
		if got.Mode != want.Mode || !slices.Equal(got.Enabled, want.Enabled) {
			t.Errorf("ParseSetting(%q) = %+v, want %+v", raw, got, want)
		}
	}
}

// TestResolveKeepsAnEnabledButMissingHarness: dropping it would turn "install
// Claude Code on that box" into silence. The plane reports it instead.
func TestResolveKeepsAnEnabledButMissingHarness(t *testing.T) {
	explicit := Setting{Mode: ModeExplicit, Enabled: []string{HarnessClaude}}
	if got := explicit.Resolve(nil); !slices.Equal(got, []string{HarnessClaude}) {
		t.Errorf("explicit.Resolve(nothing installed) = %v, want [claude]", got)
	}
	auto := Setting{Mode: ModeAuto}
	if got := auto.Resolve([]string{HarnessCodex, HarnessClaude}); !slices.Equal(got, []string{HarnessClaude, HarnessCodex}) {
		t.Errorf("auto.Resolve = %v, want the canonical order [claude codex]", got)
	}
	if got := (Setting{Mode: ModeNone}).Resolve([]string{HarnessClaude}); len(got) != 0 {
		t.Errorf("none.Resolve = %v, want nothing", got)
	}
}

// TestSeedCacheNeverOverwrites is the flag-versus-hub rule at its narrowest: the
// only write a flag may perform is the first one.
func TestSeedCacheNeverOverwrites(t *testing.T) {
	path := filepath.Join(tempHome(t), "agent-build-01.harness.json")

	seeded, err := SeedCache(path, Setting{Mode: ModeNone})
	if err != nil || !seeded {
		t.Fatalf("first SeedCache = %v, %v; want it to write", seeded, err)
	}
	seeded, err = SeedCache(path, Setting{Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Fatal("a second seed overwrote a cache that already existed")
	}
	got, found, err := LoadCache(path)
	if err != nil || !found {
		t.Fatalf("LoadCache = %v, %v", found, err)
	}
	if got.Mode != ModeNone {
		t.Errorf("cached mode = %q, want the first seed (%q) to have survived", got.Mode, ModeNone)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("cache mode = %v, want 0600: it records a hub decision under the agent's home", perm)
	}
}

// TestWriteCacheRoundTripsAnExplicitList keeps the on-disk shape honest: it is
// what a machine that boots without the hub will act on.
func TestWriteCacheRoundTripsAnExplicitList(t *testing.T) {
	path := filepath.Join(tempHome(t), "agent-build-01.harness.json")
	want := Setting{Mode: ModeExplicit, Enabled: []string{HarnessCodex}}
	if err := WriteCache(path, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := LoadCache(path)
	if err != nil || !found {
		t.Fatalf("LoadCache = %v, %v", found, err)
	}
	if got.Mode != want.Mode || !slices.Equal(got.Enabled, want.Enabled) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"mode"`, `"enabled"`, `"updatedAt"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("cache file is missing %s: %s", key, raw)
		}
	}
}

// TestLoadCacheReportsAMissingFileWithoutError: a machine that has never seen
// its edge must fall back to the default, not fail to start.
func TestLoadCacheReportsAMissingFileWithoutError(t *testing.T) {
	_, found, err := LoadCache(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || found {
		t.Fatalf("LoadCache(absent) = %v, %v; want (false, nil)", found, err)
	}
}

// TestDetectFindsAHarnessInTheRunnerAccountsHome: harness.ResolveBinary expands
// "~/" against the CALLING process's home, which for a root systemd agent is
// /root. A per-user install under the runner account's home must still count, or
// mode auto would report "nothing installed" on a machine that has Claude Code.
func TestDetectFindsAHarnessInTheRunnerAccountsHome(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, HarnessClaude), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	// A file without an execute bit is not a harness.
	if err := os.WriteFile(filepath.Join(bin, HarnessCodex), []byte("notes"), 0644); err != nil {
		t.Fatal(err)
	}

	detection := Detect(home)
	if detection[HarnessClaude] == "" {
		t.Errorf("Detect did not find the per-user claude install under %s", bin)
	}
	if got := detection[HarnessCodex]; got != "" && !strings.HasPrefix(got, bin) {
		// Tolerate a real codex on the test machine's PATH; only the
		// non-executable file under the fake home must be ignored.
		t.Logf("codex resolved from the environment: %s", got)
	} else if strings.HasPrefix(got, bin) {
		t.Errorf("Detect accepted a non-executable file as a harness: %s", got)
	}
}

// TestSettingFromObjectDecodesSpecHarness pins the mirror struct to the edge
// API's shape. The edges provider is a separate module this package must not
// import, so the only thing keeping the two in step is this decode — and an edge
// created before spec.harness existed has to read as the API default (auto),
// never as "no harness".
func TestSettingFromObjectDecodesSpecHarness(t *testing.T) {
	edge := func(spec map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "edges.railgrid.ai/v1alpha1",
			"kind":       "LinuxServer",
			"metadata":   map[string]interface{}{"name": "build-01"},
			"spec":       spec,
		}}
	}

	got, ok := settingFromObject(edge(map[string]interface{}{}), "build-01")
	if !ok || got.Mode != ModeAuto {
		t.Fatalf("an edge without spec.harness decoded to %+v (ok=%v), want the API default auto", got, ok)
	}

	got, ok = settingFromObject(edge(map[string]interface{}{
		"harness": map[string]interface{}{"mode": "explicit", "enabled": []interface{}{"codex"}},
	}), "build-01")
	if !ok || got.Mode != ModeExplicit || !slices.Equal(got.Enabled, []string{HarnessCodex}) {
		t.Fatalf("decoded %+v (ok=%v), want explicit [codex]", got, ok)
	}

	got, ok = settingFromObject(edge(map[string]interface{}{
		"harness": map[string]interface{}{"mode": "none"},
	}), "build-01")
	if !ok || got.Mode != ModeNone {
		t.Fatalf("decoded %+v (ok=%v), want none", got, ok)
	}

	// Another edge's object is not this machine's instruction.
	if _, ok := settingFromObject(edge(map[string]interface{}{}), "other-edge"); ok {
		t.Error("accepted a spec.harness from an object that is not this agent's edge")
	}
}
