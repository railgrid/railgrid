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

package cmd

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHostUpgradeCommandsStopBeforeRestartOnFailure(t *testing.T) {
	for _, surface := range []struct {
		name  string
		print func() error
	}{
		{"agent-server", func() error { return agentUpgradeServer("mac-mini") }},
		{"agent-macos", func() error { return agentUpgradeMacOS("mac-mini") }},
		{"edge-server", func() error { printServerUpgradeInstructions("mac-mini", ""); return nil }},
		{"edge-macos", func() error { printMacOSUpgradeInstructions("mac-mini"); return nil }},
	} {
		t.Run(surface.name, func(t *testing.T) {
			output, err := captureLaunchdStdout(t, surface.print)
			if err != nil {
				t.Fatal(err)
			}
			// A command block must be contiguous so copying it never includes prose
			// or comments (interactive zsh does not enable comments by default).
			start := strings.Index(output, "  curl ")
			if start < 0 {
				t.Fatal("missing download command")
			}
			block := strings.SplitN(output[start:], "\n\n", 2)[0]
			lines := strings.Split(strings.TrimSpace(block), "\n")
			if len(lines) != 3 {
				t.Fatalf("upgrade must be one three-command block: %q", block)
			}
			for _, shell := range []string{"bash", "zsh"} {
				t.Run(shell, func(t *testing.T) {
					binary, err := exec.LookPath(shell)
					if err != nil {
						t.Skipf("%s is not installed", shell)
					}
					for _, failure := range []string{"none", "extract", "install"} {
						t.Run(failure, func(t *testing.T) {
							logPath := t.TempDir() + "/commands"
							ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
							defer cancel()
							script := `
uname() { case "$1" in -s) printf Darwin ;; -m) printf arm64 ;; esac; }
curl() { printf 'download %s\n' "$2" >> "$UPGRADE_TEST_LOG"; printf archive; }
tar() { cat >/dev/null; test "$UPGRADE_TEST_FAILURE" != extract; }
sudo() {
 case "$1" in
  mv) printf 'install\n' >> "$UPGRADE_TEST_LOG"; test "$UPGRADE_TEST_FAILURE" != install ;;
  *) printf 'restart\n' >> "$UPGRADE_TEST_LOG" ;;
 esac
}
` + block
							args := []string{"-fic", script}
							if shell == "bash" {
								args = []string{"--noprofile", "--norc", "-ic", script}
							}
							command := exec.CommandContext(ctx, binary, args...)
							command.Env = append(os.Environ(), "UPGRADE_TEST_LOG="+logPath, "UPGRADE_TEST_FAILURE="+failure)
							shellOutput, runErr := command.CombinedOutput()
							if (runErr != nil) != (failure != "none") {
								t.Fatalf("shell error=%v output=%s", runErr, shellOutput)
							}
							log, err := os.ReadFile(logPath)
							if err != nil {
								t.Fatal(err)
							}
							want := "download https://github.com/railgrid/railgrid/releases/latest/download/kubectl-railgrid_Darwin_arm64.tar.gz\n"
							if failure != "extract" {
								want += "install\n"
							}
							if failure == "none" {
								want += "restart\n"
							}
							if string(log) != want {
								t.Fatalf("commands=%q, want %q; shell output=%s", log, want, shellOutput)
							}
						})
					}
				})
			}
		})
	}
}
