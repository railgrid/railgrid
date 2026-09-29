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

package v1alpha1

// Harness names. A harness is a coding agent the edge agent can supervise as a
// local `runner/v1` service. Keep in step with pkg/runner/runnercli.Harnesses
// and with the mirror in pkg/agent/runner.
const (
	// HarnessClaude is headless Claude Code.
	HarnessClaude = "claude"
	// HarnessCodex is the Codex app-server.
	HarnessCodex = "codex"
)

// HarnessNames are every harness a railgrid agent knows how to supervise.
var HarnessNames = []string{HarnessClaude, HarnessCodex} //nolint:gochecknoglobals

// HarnessMode decides which harnesses a machine offers.
type HarnessMode string

const (
	// HarnessModeAuto offers every harness whose executable is installed on
	// the machine. It is the DEFAULT, and it is what makes a joined machine a
	// harness host with no second step: the answer to "can this edge run
	// Claude Code" is "is Claude Code installed on it".
	HarnessModeAuto HarnessMode = "auto"
	// HarnessModeNone runs no harness. This is the opt-out, and the only one:
	// there is no agent-side flag that can override it.
	HarnessModeNone HarnessMode = "none"
	// HarnessModeExplicit offers exactly the harnesses in Enabled, whether or
	// not others are installed. A harness named here but not installed is
	// reported detected=false rather than silently dropped.
	HarnessModeExplicit HarnessMode = "explicit"
)

// EdgeHarnessSpec is the ONE place a machine's harness offering is decided.
//
// It lives on the edge object rather than on the agent's command line because
// it has to be changeable while the machine is running, from the edge UI or
// kubectl, by whoever can update the edge. The agent watches its own edge and
// applies a change without a restart, caching the last value it saw on disk so
// a machine that boots while the hub is unreachable keeps running what it was
// last told rather than what its install flags said.
//
// Install flags only SEED this: `railgrid edge create --harness` writes it
// once, and `railgrid agent join --harness` seeds the agent's cache for the
// window before it has ever seen the object. Neither overrides it afterwards.
type EdgeHarnessSpec struct {
	// Mode decides which harnesses this machine offers. Defaults to auto.
	// +kubebuilder:validation:Enum=auto;none;explicit
	// +kubebuilder:default=auto
	// +optional
	Mode HarnessMode `json:"mode,omitempty"`

	// Enabled names the harnesses to offer. It is required with mode
	// "explicit" and rejected otherwise, so intent is never ambiguous: a list
	// that is silently ignored under "auto" would be the worst outcome.
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:Enum=claude;codex
	// +optional
	Enabled []string `json:"enabled,omitempty"`

	// PermissionMode is the most a harness on this machine may do without
	// asking. Defaults to acceptEdits.
	//
	// It lives on the EDGE because the blast radius is the machine's: whoever
	// installed the agent owns what a turn can reach, and a tenant asking for
	// more cannot grant themselves more. A caller may ask for less.
	// +kubebuilder:validation:Enum=acceptEdits;bypassPermissions
	// +kubebuilder:default=acceptEdits
	// +optional
	PermissionMode HarnessPermissionMode `json:"permissionMode,omitempty"`

	// AllowedTools are harness tool patterns pre-approved on this machine, e.g.
	// "Bash(git *)". They matter under acceptEdits, where a shell command is
	// otherwise denied outright, and they are how a machine owner opens a
	// specific door rather than all of them.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	AllowedTools []string `json:"allowedTools,omitempty"`
}

// HarnessPermissionMode is how much a harness on this machine may do without
// asking. It is the MACHINE's ceiling, not a caller's request.
type HarnessPermissionMode string

const (
	// HarnessPermissionAcceptEdits auto-approves file edits inside the turn's
	// own working directory and denies anything else that would prompt. It is
	// the default, and it is why a harness on a machine nobody has configured
	// cannot reach the network or run arbitrary commands.
	HarnessPermissionAcceptEdits HarnessPermissionMode = "acceptEdits"
	// HarnessPermissionBypass approves every tool call. The runner account and
	// the working directory are then the only isolation left, so it belongs to
	// a machine whose owner has decided it is a sandbox — which is exactly why
	// it is set HERE, by whoever installed the agent, and not by a tenant.
	HarnessPermissionBypass HarnessPermissionMode = "bypassPermissions"
)

// ResolvePermissionMode is the mode a runner on this machine is started with.
// A nil spec, or one that names nothing, is acceptEdits.
func (s *EdgeHarnessSpec) ResolvePermissionMode() HarnessPermissionMode {
	if s == nil || s.PermissionMode == "" {
		return HarnessPermissionAcceptEdits
	}
	return s.PermissionMode
}

// ResolveHarnesses returns the harness names this spec asks for, given what is
// installed on the machine. It is the whole decision, shared by the agent
// (which supervises the result) and by anything that wants to predict it.
//
// A nil spec means the default, which is auto.
func (s *EdgeHarnessSpec) ResolveHarnesses(detected []string) []string {
	mode := HarnessModeAuto
	if s != nil && s.Mode != "" {
		mode = s.Mode
	}
	switch mode {
	case HarnessModeNone:
		return nil
	case HarnessModeExplicit:
		if s == nil {
			return nil
		}
		return append([]string(nil), s.Enabled...)
	default:
		return append([]string(nil), detected...)
	}
}
