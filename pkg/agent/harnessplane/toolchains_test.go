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
	"encoding/json"
	"slices"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner"
)

// TestRunnerAdvertisesTheToolchainsItDetects: a supervised runner's config is
// written by nobody, so what the machine has must be detected and advertised.
// A runner that clones with git while advertising no toolchains reported a
// machine that could not do what it was doing, and Factory refused to assign
// it a job that needed git.
//
// The resolver is faked: the real one searches absolute directories as well as
// PATH, and the machine running this test must not decide its outcome.
func TestRunnerAdvertisesTheToolchainsItDetects(t *testing.T) {
	present := map[string]string{"git": "/usr/bin/git", "node": "/opt/homebrew/bin/node"}
	resolve := func(name string) string {
		if path, ok := present[name]; ok {
			return path
		}
		return name // unresolved: ResolveBinary hands the bare name back
	}
	got := detect(resolve)
	if !slices.Equal(got, []string{"git", "node"}) {
		t.Fatalf("detected = %v, want exactly what resolved, in table order", got)
	}
	if got := detect(func(name string) string { return name }); got != nil {
		t.Fatalf("detected = %v on a machine with nothing, want none", got)
	}
	// And detection reaches the runner's enrollment, which is what the runner
	// answers capabilities from: the rendered config carries exactly what this
	// machine detects, whatever that is.
	c := newChild(HarnessClaude, 8787, childConfig{EdgeName: "build-01"})
	raw, err := c.renderConfig()
	if err != nil {
		t.Fatal(err)
	}
	var cfg runner.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if want := detectToolchains(); !slices.Equal(cfg.Toolchains, want) {
		t.Fatalf("rendered toolchains = %v, want what the machine detects (%v)", cfg.Toolchains, want)
	}
}
