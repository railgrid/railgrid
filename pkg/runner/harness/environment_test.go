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

package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorEnvironmentIsExplicitAndPreservesIsolation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "environment.json")
	if err := os.WriteFile(file, []byte(`{"environment":{"pathPrepend":["/opt/tools/bin"],"variables":{"PLAYWRIGHT_BROWSERS_PATH":"/opt/browsers","EXAMPLE":"$(do-not-execute)"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadEnvironmentConfig(file, false)
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"PATH=/usr/bin:/bin", "HOME=/isolated/home", "PLAYWRIGHT_BROWSERS_PATH=/old"}
	got := strings.Join(config.Apply(base), "\n")
	for _, want := range []string{"PATH=/opt/tools/bin:/usr/bin:/bin", "HOME=/isolated/home", "PLAYWRIGHT_BROWSERS_PATH=/opt/browsers", "EXAMPLE=$(do-not-execute)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "/old") || base[0] != "PATH=/usr/bin:/bin" {
		t.Fatal("duplicate override or mutated base env")
	}
}

func TestOperatorEnvironmentRejectsUnsafeOrMalformedConfig(t *testing.T) {
	for _, key := range []string{"HOME", "PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "GOCACHE", "XDG_CONFIG_HOME", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "LD_PRELOAD", "OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		t.Run(key, func(t *testing.T) {
			err := (EnvironmentConfig{Variables: map[string]string{key: "private-value"}}).Validate()
			if err == nil || strings.Contains(err.Error(), "private-value") {
				t.Fatal("reserved setting accepted or value leaked")
			}
		})
	}
	for _, path := range []string{"", ".", "~/bin", "/opt/bin:/tmp", "/opt/../bin"} {
		if err := (EnvironmentConfig{PathPrepend: []string{path}}).Validate(); err == nil {
			t.Fatalf("invalid PATH entry accepted: %q", path)
		}
	}
	file := filepath.Join(t.TempDir(), "environment.json")
	if _, err := LoadEnvironmentConfig(file, true); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnvironmentConfig(file, false); err == nil {
		t.Fatal("missing explicit config accepted")
	}
	for _, data := range []string{`{"environment":{"typo":true}}`, `{} {}`, `{"environment":{"variables":{"BAD=KEY":"private-value"}}}`, strings.Repeat("x", 65<<10)} {
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadEnvironmentConfig(file, false); err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatal("bad config accepted or content leaked")
		}
	}
}
