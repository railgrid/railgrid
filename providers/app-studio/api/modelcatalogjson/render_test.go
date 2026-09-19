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

package modelcatalogjson

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the committed model catalog JSON")

// catalogJSONPath is the committed artifact, relative to this package.
const catalogJSONPath = "../../portal/src/generated/model-catalog.json"

// The Go table is the source; the JSON is its build output. Nothing enforces
// that at build time — the portal just imports a file — so this is what
// notices when someone adds a model to the catalog and the portal keeps
// showing the old pricing.
func TestCommittedCatalogJSONMatchesTheGoCatalog(t *testing.T) {
	want, err := Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if *update {
		if err := os.WriteFile(catalogJSONPath, want, 0o644); err != nil {
			t.Fatalf("writing %s: %v", catalogJSONPath, err)
		}
		t.Logf("updated %s", filepath.Clean(catalogJSONPath))
		return
	}

	got, err := os.ReadFile(catalogJSONPath)
	if err != nil {
		t.Fatalf("reading %s: %v — run `go run ./hack/modelcatalog-json` from providers/app-studio", catalogJSONPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: the portal would show pricing the provider no longer uses.\n"+
			"Regenerate with `go run ./hack/modelcatalog-json` (or `go test ./api/modelcatalogjson -update`).",
			filepath.Clean(catalogJSONPath))
	}
}

// The portal indexes the catalog by id and reads pricing off it, so those
// fields have to survive the round trip rather than just be present in Go.
func TestRenderedCatalogIsUsableByThePortal(t *testing.T) {
	data, err := Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var entries []struct {
		ID          string   `json:"id"`
		InputPer1M  *float64 `json:"inputPer1M"`
		OutputPer1M *float64 `json:"outputPer1M"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("rendered catalog is not valid JSON: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("rendered catalog is empty")
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.ID == "" {
			t.Fatal("catalog entry has no id; the portal looks models up by it")
		}
		if _, dup := seen[entry.ID]; dup {
			t.Fatalf("catalog contains duplicate id %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		// Pricing must be present even at zero — omitempty here would make a
		// free model indistinguishable from an unpriced one.
		if entry.InputPer1M == nil || entry.OutputPer1M == nil {
			t.Fatalf("catalog entry %q dropped its pricing fields", entry.ID)
		}
	}
}

// Sorted output keeps the committed file's diff about the catalog rather than
// about map iteration order.
func TestRenderIsDeterministic(t *testing.T) {
	first, err := Render()
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		next, err := Render()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, next) {
			t.Fatal("Render is not deterministic; the committed JSON would churn")
		}
	}
}
