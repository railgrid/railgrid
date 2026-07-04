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

package v1alpha1

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// componentNameRE constrains development / data-plane component names. They
// become URL path segments (…/components/<name>/<verb>) and, by the backend
// convention, graph resource ids — so they stay strict DNS-label-ish.
var componentNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidateDevelopment checks the structural rules on spec.development and its
// relationship to spec.dataPlane that kubebuilder markers cannot express: map
// key shapes, workspacePath sanity (relative, inside the workspace, no
// duplicates), and data-plane component naming. Returns nil when the spec
// declares no development block AND no data-plane components.
func (s *TemplateSpec) ValidateDevelopment() error {
	if s.DataPlane != nil {
		if len(s.DataPlane.Endpoints) == 0 && len(s.DataPlane.Components) == 0 {
			return fmt.Errorf("spec.dataPlane declares neither endpoints nor components")
		}
		for name := range s.DataPlane.Components {
			if !componentNameRE.MatchString(name) {
				return fmt.Errorf("spec.dataPlane.components key %q must match %s", name, componentNameRE)
			}
		}
	}

	if s.Development == nil {
		return nil
	}
	if len(s.Development.Components) == 0 {
		return fmt.Errorf("spec.development.components must declare at least one component")
	}

	seenPaths := map[string]string{}
	for name, comp := range s.Development.Components {
		if !componentNameRE.MatchString(name) {
			return fmt.Errorf("spec.development.components key %q must match %s", name, componentNameRE)
		}
		wp, err := normalizeWorkspacePath(comp.WorkspacePath)
		if err != nil {
			return fmt.Errorf("spec.development.components[%s].workspacePath: %w", name, err)
		}
		if prev, dup := seenPaths[wp]; dup {
			return fmt.Errorf("spec.development.components[%s].workspacePath %q duplicates component %q", name, comp.WorkspacePath, prev)
		}
		seenPaths[wp] = name
		if strings.TrimSpace(comp.StartCommand) == "" {
			return fmt.Errorf("spec.development.components[%s].startCommand is required", name)
		}
		if comp.Reload != nil {
			for i, rule := range comp.Reload.Rules {
				if len(rule.Paths) == 0 {
					return fmt.Errorf("spec.development.components[%s].reload.rules[%d].paths is required", name, i)
				}
				if strings.TrimSpace(rule.Command) == "" {
					return fmt.Errorf("spec.development.components[%s].reload.rules[%d].command is required", name, i)
				}
			}
		}
	}

	// Sync routing needs no path to be a prefix of another — a file under
	// "web/admin" must belong to exactly one component. "." claims the whole
	// workspace and is only valid for a single-component template.
	for a, an := range seenPaths {
		if a == "." && len(seenPaths) > 1 {
			return fmt.Errorf("spec.development.components[%s].workspacePath %q claims the workspace root but other components are declared", an, a)
		}
		for b, bn := range seenPaths {
			if a == b || a == "." || b == "." {
				continue
			}
			if strings.HasPrefix(b+"/", a+"/") {
				return fmt.Errorf("spec.development.components[%s].workspacePath %q is a prefix of component %q path %q", an, a, bn, b)
			}
		}
	}

	// Data-plane components, when both are declared, must be a subset of the
	// development components (plus the routed-tier instance-level verbs) so
	// the sync router and the verb URLs agree on names.
	if s.DataPlane != nil {
		for name := range s.DataPlane.Components {
			if _, ok := s.Development.Components[name]; !ok {
				return fmt.Errorf("spec.dataPlane.components[%s] has no matching spec.development.components entry", name)
			}
		}
	}

	return nil
}

// normalizeWorkspacePath cleans and validates a component workspace path:
// relative, confined to the workspace, "." allowed for the root.
func normalizeWorkspacePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("must not be empty")
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%q must be relative", p)
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%q escapes the workspace", p)
	}
	return clean, nil
}
