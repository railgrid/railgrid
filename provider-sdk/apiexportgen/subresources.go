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

package apiexportgen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/railgrid/provider-sdk/dataplaneendpoints"
	"sigs.k8s.io/yaml"
)

// A provider's data plane is declared twice in manifest.yaml and served once.
//
//   - spec.dataPlane.verbs[] — {resource, verb}: unversioned, often streaming
//     (exec, log, proxy);
//   - spec.actions[] — a versioned, schema'd request/response call, whose id is
//     "<verb>/v<n>" and whose boundResource names the resource it hangs off.
//
// Both land on ONE coordinate, `{resource}/{verb}`, which is already how the
// hub's scoped-identity policy grants them (pkg/hub/identity/policy.go, clause
// C) and how each provider authorizes them with an SSAR. That coordinate is
// exactly a kcp CUSTOM SUBRESOURCE, so this file turns each declaration into
// the APIExport entry that makes kcp route it — the declarations are unchanged
// and the hub-proxied routes keep working; this only publishes them.

// Subresource is one {resource, verb} coordinate a provider declares.
type Subresource struct {
	// Resource is the plural parent resource, e.g. "projects".
	Resource string
	// Verb is the bare verb, e.g. "promote". For an action it is the id with
	// its "/v<n>" version suffix removed, which is the same string the hub
	// registry keeps as ProviderAction.Name.
	Verb string
	// Source says which declaration this came from, for error messages only.
	Source string
}

// Name is the RBAC-style entry name kcp wants: "<resource>/<verb>".
func (s Subresource) Name() string { return s.Resource + "/" + s.Verb }

// DataPlaneVerb mirrors CatalogEntry spec.dataPlane.verbs[]. Only the two
// fields that make a coordinate are parsed; description, stream and readOnly
// are the portal's business and have no counterpart on an APIExport.
type DataPlaneVerb struct {
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
}

// ActionBoundResource mirrors CatalogEntry spec.actions[].boundResource.
type ActionBoundResource struct {
	Resource string `json:"resource"`
}

// Action mirrors the part of CatalogEntry spec.actions[] that makes a
// coordinate: the id, whose leading segment is the verb, and the resource it is
// bound to.
type Action struct {
	ID            string              `json:"id"`
	BoundResource ActionBoundResource `json:"boundResource"`
}

// schemaOwnedSubresources are the two names kcp refuses as custom
// subresources. They describe the OBJECT'S SHAPE: they are declared on the
// APIResourceSchema and served wherever the resource itself is served, so a
// second declaration would mean two different things answering for one path.
// kcp's APIExport admission rejects them by name.
var schemaOwnedSubresources = map[string]bool{"status": true, "scale": true}

// kcpResourceName is the pattern kcp puts on APIExport spec.resources[].name
// (kubebuilder Pattern on apisv1alpha2.ResourceSchema.Name). A name it rejects
// makes the WHOLE export unappliable, not just the one entry, so it is checked
// here rather than discovered at init time.
var kcpResourceName = regexp.MustCompile(`^[a-z][-a-z0-9]*[a-z0-9](/[a-z][-a-z0-9]*[a-z0-9])?$`)

// ParseSubresources returns every {resource, verb} coordinate the CatalogEntry
// document in raw declares, from spec.dataPlane.verbs[] and spec.actions[],
// deduplicated and sorted by (resource, verb).
//
// Sorted rather than manifest-ordered, deliberately: the two lists are
// independent and either may grow, so file order would shuffle unrelated
// entries into the diff every time an action is added next to a verb.
func ParseSubresources(raw []byte) ([]Subresource, error) {
	entry, err := parseCatalogEntry(raw)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	out := []Subresource{}
	add := func(resource, verb, source string) error {
		if resource == "" || verb == "" {
			return fmt.Errorf("%s: declares an incomplete coordinate (resource=%q verb=%q)", source, resource, verb)
		}
		sub := Subresource{Resource: resource, Verb: verb, Source: source}
		if _, ok := seen[sub.Name()]; ok {
			return nil
		}
		seen[sub.Name()] = struct{}{}
		out = append(out, sub)
		return nil
	}
	if entry.Spec.DataPlane != nil {
		for _, verb := range entry.Spec.DataPlane.Verbs {
			if err := add(verb.Resource, verb.Verb, "spec.dataPlane.verbs"); err != nil {
				return nil, err
			}
		}
	}
	for _, action := range entry.Spec.Actions {
		// An action id is "<verb>/v<n>". The verb is the coordinate; the
		// version belongs to the action's schema contract, which the APIExport
		// says nothing about.
		verb, _, _ := strings.Cut(action.ID, "/")
		if err := add(action.BoundResource.Resource, verb, "spec.actions"); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		return out[i].Verb < out[j].Verb
	})
	return out, nil
}

// LoadSubresources reads a CatalogEntry manifest and returns its coordinates.
func LoadSubresources(path string) ([]Subresource, error) {
	raw, err := readFile(path)
	if err != nil {
		return nil, err
	}
	subresources, err := ParseSubresources(raw)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return subresources, nil
}

// ValidateSubresourceNames refuses, for the whole manifest at once, every
// coordinate kcp could never accept as a custom subresource.
//
// It runs over EVERY declaration, including the ones this generator will not
// emit because their parent resource is minted at runtime rather than by
// apigen. A verb named
// `status` is not a problem that begins when its resource reaches the export —
// the coordinate is unroutable the day it is written, and the alternative is a
// generator that starts failing the moment an unrelated change makes the parent
// exported.
//
// Both refusals are collected before any is reported: a manifest with thirteen
// bad names should say so once, not thirteen times over thirteen runs.
func ValidateSubresourceNames(subresources []Subresource) error {
	var problems []string
	for _, sub := range subresources {
		switch {
		case schemaOwnedSubresources[sub.Verb]:
			problems = append(problems, fmt.Sprintf(
				"%s declares %q: %q and %q belong to the object's shape, are declared on the APIResourceSchema, and kcp's APIExport admission refuses them as custom subresources — rename the verb",
				sub.Source, sub.Name(), "status", "scale"))
		case !kcpResourceName.MatchString(sub.Name()):
			problems = append(problems, fmt.Sprintf(
				"%s declares %q, which kcp's APIExport admission rejects: spec.resources[].name must match %s (lower-case letters, digits and hyphens — no underscores). One bad name makes the whole export unappliable",
				sub.Source, sub.Name(), kcpResourceName.String()))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("data-plane coordinates cannot be exported as kcp custom subresources:\n  - %s", strings.Join(problems, "\n  - "))
}

// VerbSchema is the APIResourceSchema apigen minted for one custom
// subresource's kind: the "<Verb>Request" type the provider's API package
// declares next to its real kinds (see any provider's apis/.../subresources.go).
type VerbSchema struct {
	// Name is the schema's metadata.name, "<version>-<sha>.<verb>.<group>".
	Name string
	// Path is the file apigen wrote it to, in the apigen output dir.
	Path string
	// Group and Plural are the schema's spec.group and spec.names.plural; the
	// plural IS the verb.
	Group, Plural string
	// Kind is spec.names.kind.
	Kind string
}

// IsVerbKind reports whether the schema describes a custom subresource's kind
// rather than a stored resource: its kind is the verb in PascalCase plus
// "Request" ("mint-clone-token" → MintCloneTokenRequest). The convention is
// what lets the generator tell the two apart in apigen's output, which lists
// every kind of the group as a resource.
func (s VerbSchema) IsVerbKind() bool {
	return s.Kind == VerbKind(s.Plural)+"Request"
}

// VerbKind turns "mint-clone-token" into "MintCloneToken".
func VerbKind(verb string) string {
	var b strings.Builder
	for _, part := range strings.Split(verb, "-") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

// ReadAPIGenSchemas indexes every APIResourceSchema apigen wrote into dir by
// (group, plural).
func ReadAPIGenSchemas(dir string) (map[string]VerbSchema, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading apigen output dir %s: %w", dir, err)
	}
	out := map[string]VerbSchema{}
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		schema, err := readSchema(path)
		if err != nil {
			return nil, err
		}
		if schema == nil {
			continue
		}
		out[schema.Group+"/"+schema.Plural] = *schema
	}
	return out, nil
}

// readSchema reads one APIResourceSchema file; nil when the file holds some
// other kind (the apigen dir also carries the APIExport).
func readSchema(path string) (*VerbSchema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Group string `json:"group"`
			Names struct {
				Plural string `json:"plural"`
				Kind   string `json:"kind"`
			} `json:"names"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if doc.Kind != "APIResourceSchema" {
		return nil, nil
	}
	if doc.Metadata.Name == "" {
		return nil, fmt.Errorf("%s: metadata.name is required", path)
	}
	return &VerbSchema{
		Name:   doc.Metadata.Name,
		Path:   path,
		Group:  doc.Spec.Group,
		Plural: doc.Spec.Names.Plural,
		Kind:   doc.Spec.Names.Kind,
	}, nil
}

// splitVerbKinds separates apigen's output into the stored kinds, which stay
// resources, and the "<Verb>Request" kinds, which exist only to be the schema a
// subresource entry names and must NOT be exported as resources: nothing is
// stored under them, and a tenant listing "greet" objects would be asking kcp
// for a kind with no storage.
//
// The verb kinds are taken from the schemas in the apigen dir, not from the
// export's resource list: regeneration feeds the committed export back in, and
// that one no longer lists them. Every resource entry's schema has to be in the
// dir all the same; the one committed record of apigen's output is that
// directory, and an entry whose schema is not there is a half-run codegen.
func splitVerbKinds(resources []any, schemas map[string]VerbSchema) (kept []any, verbKinds map[string]VerbSchema, err error) {
	verbKinds = map[string]VerbSchema{}
	for key, schema := range schemas {
		if schema.IsVerbKind() {
			verbKinds[key] = schema
		}
	}
	kept = make([]any, 0, len(resources))
	for _, resource := range resources {
		entry, ok := resource.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("apigen APIExport: spec.resources entry is not a mapping")
		}
		name, _ := entry["name"].(string)
		group, _ := entry["group"].(string)
		if _, ok := schemas[group+"/"+name]; !ok {
			return nil, nil, fmt.Errorf("apigen APIExport lists %s.%s but its APIResourceSchema is not beside it; re-run codegen", name, group)
		}
		if _, ok := verbKinds[group+"/"+name]; ok {
			continue
		}
		kept = append(kept, resource)
	}
	return kept, verbKinds, nil
}

// SubresourceEntries renders one APIExport spec.resources[] entry per declared
// coordinate whose PARENT resource is already exported, in the order given, and
// returns beside them the verb schemas those entries name, keyed by the file
// name they ship under ("<verb>.<group>.yaml").
//
// THE PARENT GATE. kcp's admission refuses a subresource entry whose parent is
// not exported by the same APIExport — the entry would describe a subresource
// of nothing. So the group is not guessed: it is read off the parent's own
// entry, which is also the only way a provider serving several groups gets the
// right one. A coordinate with no parent entry is skipped, not refused, because
// a provider that mints its resource entries at runtime (provider-sdk/install
// merges them in) would otherwise never generate an export at all.
//
// THE SCHEMA. kcp requires the field — spec.resources[].schema is required on
// the CRD, and admission additionally requires a subresource entry's schema to
// END IN ".<verb>.<group>", naming the subresource's own kind rather than its
// parent's. The shard never resolves it (the request is routed from
// storage.virtual.reference), but a CLAIMER's APIExport virtual workspace does
// (kcp-dev/kcp#4388, apireconciler.customSubresourcesFrom): it builds the
// claimed subresource only when that schema exists, to learn the kind it
// serves. So the schema is real: the "<Verb>Request" type the provider's API
// package declares, run through controller-gen and apigen like every other
// kind, whose plural is the verb and whose name therefore has the form kcp
// demands. A verb with no such type is an error, not a silently unserved
// coordinate. When the verb IS the plural of a stored kind of the same group
// (App Studio's projects/sessions), that kind's schema already has the right
// name and is referenced instead.
func SubresourceEntries(exportName string, resources []any, verbKinds map[string]VerbSchema, subresources []Subresource) ([]any, map[string]VerbSchema, error) {
	groups := parentGroups(resources)
	stored := storedSchemas(resources)
	reference := dataplaneendpoints.Reference(exportName)
	out := make([]any, 0, len(subresources))
	shipped := map[string]VerbSchema{}
	var missing []string
	for _, sub := range subresources {
		group, ok := groups[sub.Resource]
		if !ok {
			continue
		}
		var schema string
		switch verb, ok := verbKinds[group+"/"+sub.Verb]; {
		case ok:
			schema = verb.Name
			shipped[sub.Verb+"."+group+".yaml"] = verb
		case stored[group+"/"+sub.Verb] != "":
			schema = stored[group+"/"+sub.Verb]
		default:
			missing = append(missing, fmt.Sprintf("%s (%s): declare `type %sRequest` in the %s API package", sub.Name(), sub.Source, VerbKind(sub.Verb), group))
			continue
		}
		out = append(out, map[string]any{
			"name":   sub.Name(),
			"group":  group,
			"schema": schema,
			"storage": map[string]any{
				"virtual": map[string]any{"reference": copyReference(reference)},
			},
		})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, nil, fmt.Errorf("custom subresource(s) without a kind — every verb needs a \"<Verb>Request\" type so apigen mints the schema its entry names:\n  - %s", strings.Join(missing, "\n  - "))
	}
	return out, shipped, nil
}

// storedSchemas indexes the schema name of every non-subresource entry by
// "<group>/<resource>".
func storedSchemas(resources []any) map[string]string {
	out := make(map[string]string, len(resources))
	for _, resource := range resources {
		entry, ok := resource.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		group, _ := entry["group"].(string)
		out[group+"/"+name], _ = entry["schema"].(string)
	}
	return out
}

// parentGroups indexes the group of every non-subresource entry by resource
// name. A resource exported in two groups at once is not a thing kcp allows on
// one export, so first-wins is the same as only-wins.
func parentGroups(resources []any) map[string]string {
	groups := make(map[string]string, len(resources))
	for _, resource := range resources {
		entry, ok := resource.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		if _, seen := groups[name]; seen {
			continue
		}
		groups[name], _ = entry["group"].(string)
	}
	return groups
}

// copyReference hands every entry its own map. They are marshalled together
// and a shared map would be an alias waiting to be mutated by a caller that
// edits one entry.
func copyReference(reference map[string]any) map[string]any {
	out := make(map[string]any, len(reference))
	for key, value := range reference {
		out[key] = value
	}
	return out
}

// stripSubresourceEntries drops the entries this generator owns from a
// spec.resources it is READING.
//
// Regeneration feeds a provider's committed APIExport back in as the resource
// source (the export is the only committed record of what apigen produced), so
// without this the entries written last time would be read as though apigen had
// produced them and then written again beside the freshly derived ones. apigen
// itself never emits a subresource, so nothing legitimate is lost.
func stripSubresourceEntries(resources []any) []any {
	out := make([]any, 0, len(resources))
	for _, resource := range resources {
		if entry, ok := resource.(map[string]any); ok {
			if name, _ := entry["name"].(string); strings.Contains(name, "/") {
				continue
			}
		}
		out = append(out, resource)
	}
	return out
}
