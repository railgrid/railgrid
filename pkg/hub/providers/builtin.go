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

package providers

import (
	"fmt"
	"io/fs"
	"sort"
	"sync"
)

// BuiltinSpec describes a first-party provider the hub bootstraps into
// root:kedge:providers on startup. Each provider lives in its own package
// under providers/<name>/ and calls RegisterBuiltin in its init() — the
// hub picks them up via blank import in cmd/kedge-hub/main.go.
//
// The split is deliberately declarative: this struct is the contract a
// provider package publishes. The kcp apply, RBAC plumbing, and reconcile
// loop all read from here.
type BuiltinSpec struct {
	// Name is the CatalogEntry resource name. Must be a valid k8s name
	// (lowercase, hyphens, no slashes).
	Name string

	// DisplayName is what the portal shows in nav + catalog cards.
	DisplayName string

	// Description is a short blurb on the catalog card.
	Description string

	// Category groups this entry in the portal nav. Matched against
	// Categories[].Name; unknown categories render with a fallback icon.
	Category string

	// IconURL is a portal-relative path to an icon. Empty means fall back
	// to the registry-category icon or a generic Puzzle glyph.
	IconURL string

	// BuiltinRoute is the Vue Router route name the portal renders when
	// this provider's main entry is clicked.
	BuiltinRoute string

	// Children adds sub-navigation under this provider in the side nav.
	// Used by providers that span multiple portal pages.
	Children []BuiltinChild

	// Requires lists other builtin names this entry depends on. The hub
	// refuses to start if the enabled set contains this entry but not all
	// of its requirements — e.g. mcp.Requires = [kubernetes-edges,
	// server-edges].
	Requires []string

	// NOTE: BuiltinSpec previously carried VirtualWorkspaceMount +
	// VirtualWorkspaceHandler (func(*builder.Deps) http.Handler) so a built-in
	// provider could register an in-process virtual workspace. That mechanism
	// was removed when edge connectivity + MCP were extracted into standalone
	// out-of-process providers; no built-in provider mounts a VW anymore.

	// LocalUIAssets, when non-nil, is the pre-built micro-frontend bundle
	// (Vite dist/) the hub serves under /ui/providers/{Name}/* instead of
	// reverse-proxying to spec.ui.url. Built-in providers `go:embed` their
	// own portal/dist into this FS so the hub binary ships their UI inline;
	// third-party providers leave this nil and run their own HTTP server.
	//
	// When set, the FS root must contain at minimum main.js (the custom
	// element bundle the portal's ProviderFrame loads) and index.html (the
	// fallback page for direct browser visits). icon.svg is recommended.
	LocalUIAssets fs.FS
}

// BuiltinChild is a single sub-navigation item. Renders indented under
// the parent in the portal side nav.
type BuiltinChild struct {
	DisplayName  string
	BuiltinRoute string
}

var (
	builtinMu       sync.RWMutex
	builtinRegistry []BuiltinSpec
	builtinByName   = map[string]int{} // name → index in builtinRegistry
)

// RegisterBuiltin adds a first-party provider to the registry. Called
// from providers/<name>/manifest.go init() functions. Panics on duplicate
// names (which would be a programmer error — providers/<name> uniqueness
// matches the package path).
func RegisterBuiltin(s BuiltinSpec) {
	if s.Name == "" {
		panic("providers.RegisterBuiltin: Name is required")
	}
	builtinMu.Lock()
	defer builtinMu.Unlock()
	if _, dup := builtinByName[s.Name]; dup {
		panic(fmt.Sprintf("providers.RegisterBuiltin: duplicate name %q", s.Name))
	}
	builtinByName[s.Name] = len(builtinRegistry)
	builtinRegistry = append(builtinRegistry, s)
}

// AllBuiltins returns a snapshot of every registered builtin in
// registration order (which == cmd/kedge-hub blank-import order). Stable
// across calls within a process lifetime since init() runs once.
func AllBuiltins() []BuiltinSpec {
	builtinMu.RLock()
	defer builtinMu.RUnlock()
	out := make([]BuiltinSpec, len(builtinRegistry))
	copy(out, builtinRegistry)
	return out
}

// BuiltinNames returns the names of every registered builtin, sorted
// alphabetically. Used as the default for the --providers flag so the
// help text is stable + readable.
func BuiltinNames() []string {
	builtinMu.RLock()
	defer builtinMu.RUnlock()
	out := make([]string, 0, len(builtinRegistry))
	for _, s := range builtinRegistry {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// BuiltinByName fetches a single registered spec. Returns ok=false when
// the name isn't registered.
func BuiltinByName(name string) (BuiltinSpec, bool) {
	builtinMu.RLock()
	defer builtinMu.RUnlock()
	i, ok := builtinByName[name]
	if !ok {
		return BuiltinSpec{}, false
	}
	return builtinRegistry[i], true
}

// ResolveEnabledBuiltins picks the subset of builtins named in `enabled`
// and validates that every Requires dep is also enabled. Returns a
// single error listing every problem (missing name OR missing dep) so
// callers fix the flag in one edit.
//
// Empty `enabled` selects all registered builtins — matches the flag's
// default behavior.
func ResolveEnabledBuiltins(enabled []string) ([]BuiltinSpec, error) {
	all := AllBuiltins()

	var picked []BuiltinSpec
	pickedSet := map[string]struct{}{}
	if len(enabled) == 0 {
		picked = append(picked, all...)
		for _, s := range all {
			pickedSet[s.Name] = struct{}{}
		}
	} else {
		var unknown []string
		for _, n := range enabled {
			s, ok := BuiltinByName(n)
			if !ok {
				unknown = append(unknown, n)
				continue
			}
			if _, dup := pickedSet[n]; dup {
				continue
			}
			pickedSet[n] = struct{}{}
			picked = append(picked, s)
		}
		if len(unknown) > 0 {
			return nil, fmt.Errorf("--providers: unknown name(s) %v; known: %v", unknown, BuiltinNames())
		}
	}

	var problems []string
	for _, s := range picked {
		for _, req := range s.Requires {
			if _, ok := pickedSet[req]; !ok {
				problems = append(problems, fmt.Sprintf("%s requires %s", s.Name, req))
			}
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("--providers dependency violations: %v; add the missing names or drop the dependent ones", problems)
	}
	return picked, nil
}
