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
	"slices"
	"strings"
	"testing"
)

// TestTheMachinesCeilingReachesTheHarness.
//
// Reported from a live run: the agent answered "web access is blocked in this
// session (no approval surface to grant it)". The runner was started with no
// permission flags at all, so it took the adapter's default and denied anything
// that would prompt — and the knobs that used to configure this were lost when
// the Addon kind was replaced by spec.harness.
func TestTheMachinesCeilingReachesTheHarness(t *testing.T) {
	c := &child{harness: HarnessClaude}
	c.permissionMode = PermissionBypass
	c.allowedTools = []string{"Bash(git *)", "  ", "WebFetch"}

	args := c.childArgs("/usr/local/bin/claude")

	mode := flagValue(args, "--claude-permission-mode")
	if mode != PermissionBypass {
		t.Errorf("--claude-permission-mode = %q, want %q", mode, PermissionBypass)
	}
	var tools []string
	for i, arg := range args {
		if arg == "--claude-allowed-tool" && i+1 < len(args) {
			tools = append(tools, args[i+1])
		}
	}
	if !slices.Equal(tools, []string{"Bash(git *)", "WebFetch"}) {
		t.Errorf("allowed tools = %v, want the two non-blank patterns", tools)
	}
	// No credential ever goes on a command line.
	for _, arg := range args {
		if strings.Contains(strings.ToLower(arg), "credential") || strings.Contains(strings.ToLower(arg), "token-value") {
			t.Errorf("a credential-shaped flag reached the command line: %q", arg)
		}
	}
}

// TestAnUnsetOrUnknownCeilingIsTheSafeDefault: an agent that refused to start a
// runner over a spec value it did not recognise would turn a newer hub into a
// dead machine, so an unknown value reads as the default rather than a refusal.
func TestAnUnsetOrUnknownCeilingIsTheSafeDefault(t *testing.T) {
	for name, setting := range map[string]Setting{
		"unset":                  {Mode: ModeAuto},
		"empty":                  {Mode: ModeAuto, PermissionMode: ""},
		"unknown":                {Mode: ModeAuto, PermissionMode: "yolo"},
		"explicitly the default": {Mode: ModeAuto, PermissionMode: PermissionAcceptEdits},
	} {
		if got := setting.ResolvePermissionMode(); got != PermissionAcceptEdits {
			t.Errorf("%s: mode = %q, want %q", name, got, PermissionAcceptEdits)
		}
	}
	if got := (Setting{PermissionMode: PermissionBypass}).ResolvePermissionMode(); got != PermissionBypass {
		t.Errorf("an explicit bypass was not honoured: %q", got)
	}
}

// TestTheCeilingIsNotACodexFlag: Codex has its own sandboxing and takes neither
// flag, so passing a Claude flag to it would fail the launch.
func TestTheCeilingIsNotACodexFlag(t *testing.T) {
	c := &child{harness: HarnessCodex}
	c.permissionMode = PermissionBypass
	c.allowedTools = []string{"Bash(git *)"}
	for _, arg := range c.childArgs("/usr/local/bin/codex") {
		if strings.HasPrefix(arg, "--claude-") {
			t.Errorf("a Claude flag reached the Codex child: %q", arg)
		}
	}
}

func flagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
