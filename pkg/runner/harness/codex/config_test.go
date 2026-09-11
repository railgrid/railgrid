/*
Copyright 2026 The Faros Authors.

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

package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/faroshq/faros/pkg/runner/harness"
)

func TestManagedTrustConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, extra, level string
		invalid            bool
	}{
		{name: "trusted", level: "trusted"}, {name: "untrusted", level: "untrusted"},
		{name: "extra-project-key", level: "trusted", extra: "model = \"secret-marker\"", invalid: true},
		{name: "extra-table", level: "trusted", extra: "[mcp_servers.secret]\ncommand = \"secret-marker\"", invalid: true},
		{name: "empty-extra-table", level: "trusted", extra: "[hooks]", invalid: true},
		{name: "invalid-level", level: "secret-marker", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, root, work := trustFixture(t)
			writeTrustConfig(t, home, fmt.Sprintf("[projects.%q]\ntrust_level = %q\n%s\n", work, tc.level, tc.extra))
			a := &Adapter{cfg: Config{Home: home, WorktreeRoot: root}}
			err := a.ensureHome()
			if (err != nil) != tc.invalid {
				t.Fatalf("ensureHome error=%v invalid=%v", err, tc.invalid)
			}
			if err != nil && strings.Contains(err.Error(), "secret-marker") {
				t.Fatal("configuration contents leaked")
			}
			if !tc.invalid {
				if err := a.validateLaunchConfiguration(work); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestManagedTrustRejectsUnsafeScope(t *testing.T) {
	for _, kind := range []string{"parent", "root", "outside", "relative", "unclean", "missing", "symlink", "no-root", "malformed", "oversize", "file-symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home, root, work := trustFixture(t)
			target := work
			switch kind {
			case "parent":
				target = filepath.Dir(work)
			case "root":
				target = root
			case "outside":
				target = t.TempDir()
			case "relative":
				target = "task/attempt"
			case "unclean":
				target = work + "/../attempt"
			case "missing":
				target = filepath.Join(root, "absent", "attempt")
			case "symlink":
				target = filepath.Join(root, "task", "alias")
				if err := os.Symlink(work, target); err != nil {
					t.Fatal(err)
				}
			}
			content := fmt.Sprintf("[projects.%q]\ntrust_level = \"trusted\"\n", target)
			if kind == "malformed" {
				content = "invalid = secret-marker"
			}
			if kind == "oversize" {
				content = strings.Repeat(" ", maxCodexConfigBytes+1)
			}
			writeTrustConfig(t, home, content)
			path := filepath.Join(home, codexConfigName)
			if kind == "file-symlink" || kind == "directory" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if kind == "directory" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					other := filepath.Join(t.TempDir(), "config")
					if err := os.WriteFile(other, []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(other, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "no-root" {
				root = ""
			}
			a := &Adapter{cfg: Config{Home: home, WorktreeRoot: root}}
			if err := a.ensureHome(); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func TestManagedTrustProbeAndLaunchIsolation(t *testing.T) {
	home, root, work := trustFixture(t)
	second := filepath.Join(root, "next", "attempt")
	if err := os.MkdirAll(second, 0700); err != nil {
		t.Fatal(err)
	}
	writeTrustConfig(t, home, fmt.Sprintf("[projects.%q]\ntrust_level = \"trusted\"\n[projects.%q]\ntrust_level = \"trusted\"\n", work, second))
	binary := fakeCodexBinary(t, "probe")
	a := &Adapter{cfg: Config{Home: home, WorktreeRoot: root, Binary: binary, ExpectedVersion: "0.147.0"}}
	for range 2 {
		info, err := a.Probe(context.Background())
		if err != nil || !info.Ready {
			t.Fatalf("probe=%+v err=%v", info, err)
		}
	}
	for _, method := range readMethods(t, filepath.Join(filepath.Dir(binary), "methods")) {
		if method == "thread/start" || method == "thread/resume" || method == "turn/start" {
			t.Fatal("probe started model")
		}
	}
	for _, path := range []string{work, second} {
		if err := a.validateLaunchConfiguration(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.validateLaunchConfiguration(t.TempDir()); err == nil {
		t.Fatal("foreign launch allowed")
	}
	for _, base := range []string{root, filepath.Dir(work), work} {
		local := filepath.Join(base, ".codex")
		if err := os.Mkdir(local, 0700); err != nil {
			t.Fatal(err)
		}
		_, err := a.Run(context.Background(), harness.Launch{Workdir: work, Instructions: "approved", SessionID: "original"}, nil)
		if err == nil {
			t.Fatal("trusted local config reached harness")
		}
		if err := os.Remove(local); err != nil {
			t.Fatal(err)
		}
	}
}

func trustFixture(t *testing.T) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "worktrees")
	work := filepath.Join(root, "task", "attempt")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	return home, root, work
}
func writeTrustConfig(t *testing.T, home, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, codexConfigName), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
