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

// Package harnessplane makes a machine a harness host for its workspace.
//
// A harness is a coding agent (Claude Code, Codex) the agent supervises as a
// local `runner/v1` service on loopback. There is exactly ONE setting:
// `spec.harness` on the edge object. The agent watches its own edge, resolves
// that setting against what is actually installed on the machine, supervises
// one runner child per enabled harness, and advertises each running child over
// the discovery channel the edges provider already pulls.
//
// Three rules shape everything here:
//
//   - The hub wins over flags, always. `--harness` only SEEDS the on-disk cache
//     and only when no cache exists; the first observed spec overwrites it, and
//     no code path feeds a flag value back in afterwards. See Manager.Run.
//   - No harness credential ever lands on this host. The caller sends its own
//     credential with each runner/v1 attempt (see pkg/runner/harness.Launch), so
//     this package writes a bearer token, an enrollment and harness homes — and
//     nothing else.
//   - A stop keeps state. Disabling a harness, or shutting the agent down,
//     signals the child's process group and lets it drain; the token, the
//     enrollment, the harness home and the runner's own clones stay on disk so
//     re-enabling resumes the same sessions.
//
// The edge API lives in the standalone edges provider module, so — exactly like
// pkg/agent/reconciler — this package decodes the object as unstructured into
// local mirror structs rather than importing the provider.
//
// The package is deliberately NOT called `runner`: it would then read
// `runner.Config` for pkg/runner's types inside a package of the same name.
package harnessplane

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Harness names. Mirrors v1alpha1.Harness* in the edges provider and
// runnercli.Harnesses in pkg/runner.
const (
	// HarnessClaude is headless Claude Code.
	HarnessClaude = "claude"
	// HarnessCodex is the Codex app-server.
	HarnessCodex = "codex"
)

// Names are every harness this agent build can supervise, in the order ports
// are allocated. The order is fixed rather than sorted-on-the-fly so two
// machines with the same harnesses land on the same ports, and so a reconcile
// never renumbers a harness that is already running.
var Names = []string{HarnessClaude, HarnessCodex} //nolint:gochecknoglobals

// Mode mirrors v1alpha1.HarnessMode.
type Mode string

const (
	// ModeAuto offers every harness installed on the machine. It is the
	// default, and it is what makes a joined machine a harness host with no
	// second step.
	ModeAuto Mode = "auto"
	// ModeNone offers none. This is the opt-out.
	ModeNone Mode = "none"
	// ModeExplicit offers exactly Setting.Enabled, installed or not.
	ModeExplicit Mode = "explicit"
)

// Setting is the effective harness selection: the mirror of the edge object's
// spec.harness, and the shape written to the on-disk cache.
type Setting struct {
	Mode    Mode     `json:"mode,omitempty"`
	Enabled []string `json:"enabled,omitempty"`
	// PermissionMode and AllowedTools are the MACHINE's ceiling on what a turn
	// may do without asking. They are the machine owner's to set, which is why
	// they live on the edge: a tenant asking for more cannot grant themselves
	// more. Mirrors v1alpha1.EdgeHarnessSpec.
	PermissionMode string   `json:"permissionMode,omitempty"`
	AllowedTools   []string `json:"allowedTools,omitempty"`
}

// Permission modes, mirroring v1alpha1.HarnessPermissionMode.
const (
	// PermissionAcceptEdits is the default: file edits inside the turn's own
	// working directory, and anything else that would prompt is denied.
	PermissionAcceptEdits = "acceptEdits"
	// PermissionBypass approves every tool call. The runner account and the
	// working directory are then the only isolation left.
	PermissionBypass = "bypassPermissions"
)

// ResolvePermissionMode is the mode this machine's runners are started with.
// Anything unrecognised is the default rather than a refusal: an agent that
// refused to start a runner over a spec value it did not know would turn a
// newer hub into a dead machine.
func (s Setting) ResolvePermissionMode() string {
	if s.PermissionMode == PermissionBypass {
		return PermissionBypass
	}
	return PermissionAcceptEdits
}

// DefaultSetting is what an edge with no spec.harness means, and what a machine
// with no cache and no flag runs: every harness it has installed.
func DefaultSetting() Setting { return Setting{Mode: ModeAuto} }

// Resolve returns the harnesses this setting asks for given what is installed.
// It is the same decision as v1alpha1.EdgeHarnessSpec.ResolveHarnesses; the two
// are deliberately identical so the hub can predict what the agent will do.
//
// A name that is enabled but not detected is RETURNED anyway: the caller
// reports it as detected=false with a reason instead of silently dropping it,
// because "I asked for claude and nothing happened" is the worst outcome.
func (s Setting) Resolve(detected []string) []string {
	switch s.mode() {
	case ModeNone:
		return nil
	case ModeExplicit:
		out := make([]string, 0, len(s.Enabled))
		for _, name := range Names {
			if slices.Contains(s.Enabled, name) {
				out = append(out, name)
			}
		}
		return out
	default:
		out := make([]string, 0, len(detected))
		for _, name := range Names {
			if slices.Contains(detected, name) {
				out = append(out, name)
			}
		}
		return out
	}
}

// mode defaults an empty mode to auto, matching the CRD default.
func (s Setting) mode() Mode {
	if s.Mode == "" {
		return ModeAuto
	}
	return s.Mode
}

// Validate rejects a setting this agent cannot act on. An unknown mode is an
// error rather than a silent fallback to auto: the CRD enum means the agent
// should never see one, and guessing would start harnesses nobody asked for.
func (s Setting) Validate() error {
	switch s.mode() {
	case ModeNone, ModeAuto:
		if len(s.Enabled) > 0 {
			// Not fatal on its own, but it is always a mistake worth naming:
			// the list is ignored in these modes.
			return fmt.Errorf("harness mode %q does not take an enabled list (%s)", s.mode(), strings.Join(s.Enabled, ","))
		}
	case ModeExplicit:
		if len(s.Enabled) == 0 {
			return fmt.Errorf("harness mode %q requires at least one harness in enabled", ModeExplicit)
		}
		for _, name := range s.Enabled {
			if !slices.Contains(Names, name) {
				return fmt.Errorf("unknown harness %q; known harnesses: %s", name, strings.Join(Names, ", "))
			}
		}
	default:
		return fmt.Errorf("unknown harness mode %q; must be %s, %s or %s", s.Mode, ModeAuto, ModeNone, ModeExplicit)
	}
	return nil
}

// String renders the setting the way the --harness flag accepts it, so a log
// line and a command line read the same.
func (s Setting) String() string {
	if s.mode() == ModeExplicit {
		return strings.Join(s.Enabled, ",")
	}
	return string(s.mode())
}

// ParseSetting parses a --harness value: "auto", "none", or a comma-separated
// list of harness names (which becomes mode explicit). A typo is an error, not
// an empty list: an operator who typed --harness=cluade must find out at
// startup rather than wonder why no runner ever appears.
func ParseSetting(raw string) (Setting, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return DefaultSetting(), nil
	}
	switch Mode(strings.ToLower(value)) {
	case ModeAuto:
		return Setting{Mode: ModeAuto}, nil
	case ModeNone:
		return Setting{Mode: ModeNone}, nil
	case ModeExplicit:
		return Setting{}, fmt.Errorf("--harness=%s needs the harnesses too; pass a comma-separated list such as %q",
			ModeExplicit, strings.Join(Names, ","))
	}
	seen := map[string]bool{}
	var enabled []string
	for _, part := range strings.Split(value, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if !slices.Contains(Names, name) {
			return Setting{}, fmt.Errorf("unknown harness %q; use %s, %s, or a comma-separated list of %s",
				name, ModeAuto, ModeNone, strings.Join(Names, ","))
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		enabled = append(enabled, name)
	}
	if len(enabled) == 0 {
		return Setting{}, fmt.Errorf("--harness %q names no harness", raw)
	}
	sort.Slice(enabled, func(i, j int) bool {
		return slices.Index(Names, enabled[i]) < slices.Index(Names, enabled[j])
	})
	return Setting{Mode: ModeExplicit, Enabled: enabled}, nil
}

// HarnessStatus is one harness as this machine sees it. The JSON tags match
// edgeapi.HarnessStatus in the edges provider, which is what the heartbeat
// publishes on the edge's status.harnesses.
//
// Detected and Enabled are separate facts on purpose: detected says the
// executable is installed, enabled says spec.harness asks for it.
type HarnessStatus struct {
	Name     string   `json:"name"`
	Detected bool     `json:"detected"`
	Enabled  bool     `json:"enabled"`
	Ready    bool     `json:"ready"`
	Version  string   `json:"version,omitempty"`
	Port     int32    `json:"port,omitempty"`
	Reasons  []string `json:"reasons,omitempty"`
}
