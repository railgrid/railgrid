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

package serve

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// The CatalogEntry manifest is the one declaration of a provider's verbs and
// actions. apiexportgen turns it into the "<resource>/<verb>" entries on the
// APIExport; this turns the same document into the routes serve answers on the
// shard-forwarded path, so the two can never disagree about what exists.

// subresourceName is kcp's rule for a resource entry, applied to the verb
// half: lowercase, digits, hyphens, no leading or trailing hyphen.
var subresourceName = regexp.MustCompile(`^[a-z][-a-z0-9]*[a-z0-9]$`)

// actionVersion strips the "/v<n>" suffix an action id carries; the coordinate
// kcp routes on is the action name alone, and the version goes back onto the
// rewritten path from the table.
var actionVersion = regexp.MustCompile(`/(v[0-9]+)$`)

type catalogEntryDoc struct {
	Spec struct {
		DataPlane struct {
			Verbs []struct {
				Resource string `json:"resource"`
				Verb     string `json:"verb"`
			} `json:"verbs"`
		} `json:"dataPlane"`
		Actions []struct {
			ID            string `json:"id"`
			BoundResource struct {
				Resource string `json:"resource"`
			} `json:"boundResource"`
		} `json:"actions"`
	} `json:"spec"`
}

// SubresourcesFromCatalogEntry derives the Subresources table from a
// CatalogEntry manifest's bytes. A verb or action whose name kcp would refuse
// is an error here too, so a provider fails at startup with the coordinate
// named rather than serving an export that cannot be applied.
func SubresourcesFromCatalogEntry(manifest []byte) (map[string]SubresourceRoute, error) {
	var doc catalogEntryDoc
	if err := yaml.Unmarshal(manifest, &doc); err != nil {
		return nil, fmt.Errorf("serve: parsing the CatalogEntry manifest: %w", err)
	}
	table := map[string]SubresourceRoute{}
	add := func(resource, verb string, route SubresourceRoute) error {
		resource, verb = strings.TrimSpace(resource), strings.TrimSpace(verb)
		if resource == "" || verb == "" {
			return fmt.Errorf("serve: a declared verb has an empty resource or verb (%q/%q)", resource, verb)
		}
		if verb == "status" || verb == "scale" {
			return fmt.Errorf("serve: %s/%s names a subresource kcp reserves for the object's own shape", resource, verb)
		}
		if !subresourceName.MatchString(verb) {
			return fmt.Errorf("serve: %s/%s is not a valid kcp subresource name (lowercase letters, digits and hyphens)", resource, verb)
		}
		key := resource + "/" + verb
		if existing, dup := table[key]; dup && existing != route {
			return fmt.Errorf("serve: %s is declared twice with different routes", key)
		}
		table[key] = route
		return nil
	}
	for _, v := range doc.Spec.DataPlane.Verbs {
		if err := add(v.Resource, v.Verb, SubresourceRoute{}); err != nil {
			return nil, err
		}
	}
	for _, a := range doc.Spec.Actions {
		id := strings.TrimSpace(a.ID)
		m := actionVersion.FindStringSubmatch(id)
		if m == nil {
			return nil, fmt.Errorf("serve: action %q has no /v<n> version suffix", id)
		}
		name := strings.TrimSuffix(id, m[0])
		if err := add(a.BoundResource.Resource, name, SubresourceRoute{Action: true, Version: m[1]}); err != nil {
			return nil, err
		}
	}
	return table, nil
}

// SubresourcesFromCatalogEntryFile is SubresourcesFromCatalogEntry over a
// file, for the ordinary case where the manifest is baked into the image next
// to the objects init applies (RAILGRID_KCP_DIR).
func SubresourcesFromCatalogEntryFile(path string) (map[string]SubresourceRoute, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("serve: reading the CatalogEntry manifest: %w", err)
	}
	return SubresourcesFromCatalogEntry(data)
}
