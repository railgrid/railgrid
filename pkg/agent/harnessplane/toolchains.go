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

import "github.com/railgrid/railgrid/pkg/runner/harness"

// knownToolchains are the executables a runner advertises as `toolchains` when
// they are on the machine, in the order they are reported.
//
// A runner's capabilities say what it HAS, and a caller matching a job to a
// machine reads them: Factory refuses to assign a job that requires `git` to a
// runner that does not advertise it. A hand-written runner.json used to declare
// these; a supervised runner must detect them instead, because nobody writes its
// config — and a runner that clones repositories with git while advertising no
// toolchains at all was reporting a machine that could not do what it was
// doing. The list is short and named: an operator reading a Service's status
// should recognise every entry, and a job that needs something not listed here
// cannot be matched on it, which is a reason to extend this table rather than
// to guess.
var knownToolchains = []string{ //nolint:gochecknoglobals // immutable detection table
	"git",
	"go",
	"node",
	"npm",
	"python3",
	"make",
	"docker",
}

// detectToolchains reports which of the known toolchains this machine has.
//
// Resolution is the runner's own: PATH first, then the well-known install
// directories a service account's PATH does not include (pkg/runner/harness
// ResolveBinary). A toolchain is present when that resolves to something other
// than the bare name.
func detectToolchains() []string { return detect(harness.ResolveBinary) }

// detect is detectToolchains over a resolver, so the rule can be tested
// without the machine running the test deciding the answer: ResolveBinary
// searches absolute directories as well as PATH, and a developer's box has
// all of these installed.
func detect(resolve func(name string) string) []string {
	var out []string
	for _, name := range knownToolchains {
		if resolve(name) != name {
			out = append(out, name)
		}
	}
	return out
}
