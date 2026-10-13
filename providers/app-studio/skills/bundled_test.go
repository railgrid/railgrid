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

package skills

import (
	"context"
	"strings"
	"testing"
)

func TestBundledRailgridAuthSkillIsDiscoverableAndReadOnly(t *testing.T) {
	snapshot, err := LoadBuiltinSnapshot(context.Background(), Limits{})
	if err != nil {
		t.Fatalf("LoadBuiltinSnapshot: %v", err)
	}
	entry, err := snapshot.Get("system:railgrid-auth")
	if err != nil {
		t.Fatalf("system auth skill missing: %v", err)
	}
	if !entry.Enabled || entry.Editable || entry.Scope != ScopeSystem || entry.PackagePath != "railgrid-auth" {
		t.Fatalf("system auth skill metadata = %#v", entry)
	}
	wantResources := map[string]bool{"express.md": false, "react.md": false, "ownership.md": false, "migration.md": false}
	for _, resource := range entry.Resources {
		if _, ok := wantResources[resource.Path]; ok {
			wantResources[resource.Path] = true
		}
	}
	for name, found := range wantResources {
		if !found {
			t.Errorf("supporting resource %q is not discoverable", name)
		}
		if _, err := snapshot.ReadResource(context.Background(), entry.QualifiedName, name, ResourceReadOptions{}); err != nil {
			t.Errorf("read %s: %v", name, err)
		}
	}

	checks := map[string][]string{
		"SKILL.md":   {"get_project_authentication", "configure_project_authentication", "only after the project owner explicitly asks", "supportReason", "do not switch or update that Project's Template", "first Railgrid user", "third-party"},
		"express.md": {"@railgrid/auth-node", "X-Railgrid-App-Identity", "RAILGRID_AUTH_AUDIENCE"},
		"react.md":   {"/__railgrid/auth/login?returnTo=/", "/__railgrid/auth/logout", "401"},
		"ownership.md": {
			"railgrid_user_id TEXT PRIMARY KEY", "owner_railgrid_user_id", "WHERE owner_railgrid_user_id = $1", "Two users",
		},
		"migration.md": {"Do not assign all existing rows", "explicit mapping", "Organization membership", "third-party"},
	}
	for resource, fragments := range checks {
		var content string
		if resource == "SKILL.md" {
			content = entry.Content
		} else {
			result, readErr := snapshot.ReadResource(context.Background(), entry.QualifiedName, resource, ResourceReadOptions{})
			if readErr != nil {
				t.Errorf("read %s for content checks: %v", resource, readErr)
				continue
			}
			content = string(result.Content)
		}
		content = strings.Join(strings.Fields(content), " ")
		for _, fragment := range fragments {
			if !strings.Contains(content, fragment) {
				t.Errorf("%s does not contain required guidance %q", resource, fragment)
			}
		}
	}
}
