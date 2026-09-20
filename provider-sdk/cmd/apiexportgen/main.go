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

// Command apiexportgen writes a provider's APIExport from its CatalogEntry
// manifest plus kcp apigen's output. It runs from every
// codegen-<name>-provider Makefile target, right after hack/apigen.sh.
//
//	go run ./cmd/apiexportgen \
//	  --manifest      providers/code/manifest.yaml \
//	  --apigen-export providers/code/config/kcp/apiexport-code.railgrid.ai.yaml \
//	  --schemas-dir   providers/code/deploy/chart/files/schemas \
//	  --out           providers/code/config/kcp/apiexport-code.providers.railgrid.ai.yaml
//
// --apigen-export is optional: a provider that mints its schemas at runtime
// (infrastructure) passes none and gets an export with an empty resource list,
// which install merges with whatever its runtime writers have added.
//
// apigen names its file after the API GROUP, which is not the export name, so
// the group-named file would otherwise linger in config/kcp/ as a second,
// wrong APIExport. This command deletes it after writing --out (unless the two
// are the same path, which happens whenever a provider's group and export name
// coincide). Nothing in the Makefile has to remember to clean up.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/railgrid/provider-sdk/apiexportgen"
)

func main() {
	var (
		manifest     = flag.String("manifest", "", "path to the provider's CatalogEntry manifest.yaml (required)")
		apigenExport = flag.String("apigen-export", "", "path to the APIExport kcp apigen wrote (optional; empty means no apigen resources)")
		schemasDir   = flag.String("schemas-dir", "", "directory of the APIResourceSchemas the provider ships; resources are filtered to exactly these (optional)")
		out          = flag.String("out", "", "path of the generated APIExport (required)")
	)
	flag.Parse()

	if err := run(*manifest, *apigenExport, *schemasDir, *out); err != nil {
		fmt.Fprintf(os.Stderr, "apiexportgen: %v\n", err)
		os.Exit(1)
	}
}

func run(manifest, apigenExport, schemasDir, out string) error {
	if manifest == "" {
		return fmt.Errorf("--manifest is required")
	}
	if out == "" {
		return fmt.Errorf("--out is required")
	}
	content, err := apiexportgen.Generate(apiexportgen.Options{
		ManifestPath:     manifest,
		APIGenExportPath: apigenExport,
		SchemasDir:       schemasDir,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(out), err)
	}
	if err := os.WriteFile(out, content, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	if apigenExport == "" {
		return nil
	}
	same, err := samePath(apigenExport, out)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	if err := os.Remove(apigenExport); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing apigen's group-named APIExport %s: %w", apigenExport, err)
	}
	return nil
}

// samePath reports whether two paths name the same file. A provider whose API
// group equals its export name (quickstart, kuery, agents, app-studio) has
// apigen writing straight to --out; deleting it would delete the output.
func samePath(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", a, err)
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", b, err)
	}
	return absA == absB, nil
}
