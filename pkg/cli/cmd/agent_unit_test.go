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
	"os"
	"path/filepath"
	"testing"
)

// TestUnitEdgeName covers the guard that keeps `agent join` from replacing
// another edge's unit now that the unit name no longer carries the edge.
func TestUnitEdgeName(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name string
		path string
		want string
	}{
		{"missing unit", filepath.Join(dir, "absent.service"), ""},
		{"template form", write("a.service", "ExecStart=/usr/local/bin/railgrid agent run \\\n  --hub-url https://hub \\\n  --edge-name ubuntu \\\n  --type server\n"), "ubuntu"},
		{"equals form", write("b.service", "ExecStart=/usr/local/bin/railgrid agent run --edge-name=minis --type server\n"), "minis"},
		{"no edge flag", write("c.service", "ExecStart=/usr/local/bin/railgrid agent join --config /etc/railgrid/agent.yaml\n"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := unitEdgeName(tc.path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
