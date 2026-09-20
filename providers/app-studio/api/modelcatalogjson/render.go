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

// Package modelcatalogjson renders the shared model catalog for the portal.
// Separate from the command so a test can render into memory and compare,
// which is what keeps the committed JSON honest.
package modelcatalogjson

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/railgrid/provider-sdk/modelcatalog"
)

// Render returns the catalog as deterministic, newline-terminated JSON.
// Sorted by id so the committed file changes only when the catalog does, not
// when Go's map iteration order shifts.
func Render() ([]byte, error) {
	entries := append([]modelcatalog.ModelInfo(nil), modelcatalog.Catalog()...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	// The catalog carries no user input, so escaping HTML in it only makes
	// the committed file harder to read in a diff.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(entries); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
