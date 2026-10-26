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

package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

var (
	builtinOnce sync.Once
	builtinList []Template
	builtinErr  error
)

// Builtin returns the shipped catalog, parsed and validated once, ordered by
// category (Categories order) then display name. It panics on a malformed
// file: the set is compiled in and covered by TestBuiltinCatalog, so a
// failure here is a build defect, not a runtime condition.
func Builtin() []Template {
	builtinOnce.Do(func() { builtinList, builtinErr = loadBuiltin() })
	if builtinErr != nil {
		panic(builtinErr)
	}
	out := make([]Template, len(builtinList))
	copy(out, builtinList)
	return out
}

// Lookup finds a built-in template by name.
func Lookup(name string) (Template, bool) {
	name = strings.TrimSpace(name)
	for _, t := range Builtin() {
		if t.Metadata.Name == name {
			return t, true
		}
	}
	return Template{}, false
}

func loadBuiltin() ([]Template, error) {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil, fmt.Errorf("agent templates: read embedded builtin/: %w", err)
	}
	var out []Template
	seen := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("agent templates: read %s: %w", e.Name(), err)
		}
		t, err := Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("agent templates: %s: %w", e.Name(), err)
		}
		// The file is named after the template so a reader can find one from
		// the other; a mismatch is a copy-paste that will confuse exactly that
		// reader.
		if want := strings.TrimSuffix(e.Name(), ".yaml"); want != t.Metadata.Name {
			return nil, fmt.Errorf("agent templates: %s declares metadata.name %q; the file name and the template name must match", e.Name(), t.Metadata.Name)
		}
		if prev, dup := seen[t.Metadata.Name]; dup {
			return nil, fmt.Errorf("agent templates: %s and %s both declare %q", prev, e.Name(), t.Metadata.Name)
		}
		seen[t.Metadata.Name] = e.Name()
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := categoryRank(out[i].Spec.Category), categoryRank(out[j].Spec.Category)
		if ci != cj {
			return ci < cj
		}
		return out[i].Spec.DisplayName < out[j].Spec.DisplayName
	})
	return out, nil
}

func categoryRank(c string) int {
	for i, known := range Categories {
		if known == c {
			return i
		}
	}
	return len(Categories)
}

// Parse decodes one template manifest (YAML or JSON) and validates it.
func Parse(raw []byte) (Template, error) {
	var t Template
	if err := yaml.UnmarshalStrict(raw, &t); err != nil {
		return Template{}, err
	}
	if err := Validate(t); err != nil {
		return Template{}, err
	}
	return t, nil
}
