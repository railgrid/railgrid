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

// Command modelcatalog-json renders provider-sdk/modelcatalog to JSON for the
// portal's pricing and reasoning catalogs.
//
// The model catalog is provider knowledge, not tenant state: token pricing and
// context windows, the same for every workspace. The portal needs it to show
// what a model costs, and the registry it reads is now Studio spec rather than
// a backend response, so there is nothing left to enrich the list on the way
// past.
//
// Exporting beats the two alternatives. A hand-written TypeScript twin drifts
// silently as model data changes. A backend endpoint would put a network
// round-trip, a route and an auth path in front of constants. This makes the Go
// tables the source and each JSON file a build artifact with a test that fails
// when either output diverges.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/railgrid/provider-sdk/modelcatalog"

	"github.com/railgrid/provider-app-studio/api/modelcatalogjson"
)

func main() {
	out := flag.String("out", filepath.Join("portal", "src", "generated", "model-catalog.json"), "model pricing catalog file to write")
	reasoningOut := flag.String("reasoning-out", filepath.Join("portal", "src", "generated", "reasoning-catalog.json"), "reasoning catalog file to write")
	flag.Parse()
	pattern, _ := json.Marshal(modelcatalog.SnapshotSuffixPattern)
	if err := writeCatalog(filepath.Join(filepath.Dir(*out), "model-snapshot-suffix.json"), append(pattern, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	data, err := modelcatalogjson.Render()
	if err != nil {
		fmt.Fprintln(os.Stderr, "modelcatalog-json:", err)
		os.Exit(1)
	}
	if err := writeCatalog(*out, data); err != nil {
		fmt.Fprintln(os.Stderr, "modelcatalog-json:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", *out, len(data))

	reasoningData, err := modelcatalogjson.RenderReasoningProfiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, "modelcatalog-json:", err)
		os.Exit(1)
	}
	if err := writeCatalog(*reasoningOut, reasoningData); err != nil {
		fmt.Fprintln(os.Stderr, "modelcatalog-json:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", *reasoningOut, len(reasoningData))
}

func writeCatalog(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
