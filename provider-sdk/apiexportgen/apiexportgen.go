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

// Package apiexportgen turns the two things a provider author actually writes
// into the one APIExport the provider ships.
//
// A provider declares exactly two objects:
//
//   - the CatalogEntry (`manifest.yaml`), hand-written, the single source for
//     the export's NAME, its PERMISSION CLAIMS and its custom subresources
//     (plus everything the portal shows);
//   - the APIExport (`config/kcp/apiexport-<exportName>.yaml`), generated, the
//     single source for the export's stored RESOURCES.
//
// kcp's apigen already emits the correct spec.resources — every kind with its
// immutable, versioned APIResourceSchema name — but it names the export after
// the API GROUP and knows nothing about claims. This package takes apigen's
// output, renames the export to the manifest's spec.export.name, stamps
// spec.permissionClaims from the manifest, and writes deterministic YAML.
//
// spec.permissionClaims has exactly ONE source: spec.requires, the list of
// everything the provider needs from groups it does not own. It is keyed by API
// group — a group belongs to one provider, so a repeated group is invalid input
// rather than something to merge — and each of its resources becomes one claim
// (see ExportClaims). There is no second place to write a claim, so there is
// nothing for two lists to drift apart about.
//
// No claim carries an identityHash: a first-party claim is identity-agnostic,
// resolved by kcp per consumer workspace against whatever export that
// workspace bound and admitted by the platform's PermissionClaimPolicy.
//
// The same parser serves runtime callers: a provider whose resources are minted
// at runtime rather than by apigen still reads its claims from the manifest
// through LoadRequirements, so a claim is written in exactly one place no
// matter how the provider gets its schemas.
package apiexportgen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Header is prepended to every generated file. It carries the repo boilerplate
// and, more importantly, says the file is an output so nobody edits it in place
// and loses the edit on the next codegen run. It is a constant (no timestamps,
// no source paths) so the generator is byte-deterministic across checkouts.
const Header = `# Copyright 2026 The Railgrid Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# GENERATED FILE — DO NOT EDIT.
#
# Written by provider-sdk/cmd/apiexportgen from two inputs:
#   metadata.name                         <- manifest.yaml spec.export.name
#   spec.permissionClaims                 <- manifest.yaml spec.requires
#   spec.resources                        <- kcp apigen (hack/apigen.sh), plus
#                                            one "<resource>/<verb>" custom
#                                            subresource per manifest.yaml
#                                            spec.export.resources[].verbs[] and
#                                            spec.export.resources[].actions[]
#                                            entry, naming the schema apigen
#                                            minted for the verb's
#                                            "<Verb>Request" kind
# Regenerate with: make codegen-<provider>-provider. No claim carries an
# identityHash: kcp resolves a first-party claim per consumer workspace against
# whatever export that workspace bound (PermissionClaimPolicy admits it).
`

// coordinateClaimVerbs is what a claim on a VERB coordinate ("instances/exec")
// spells. The verb is the capability; which HTTP method reaches it — which is
// what kcp maps onto an RBAC verb on a custom subresource — is the serving
// provider's transport detail, so the claim covers every verb rather than
// guessing one.
var coordinateClaimVerbs = []any{"*"}

// LabelSelector mirrors apis/providers/v1alpha1.ProviderLabelSelector, and is
// rendered into the generated APIExport as kcp's PermissionClaimSelector.
//
// It is duplicated here rather than imported because provider-sdk is a module
// of its own with no dependency on the railgrid monorepo — that independence is
// what lets a provider be built outside this repository. The JSON tags MUST
// stay identical to the CatalogEntry type's, since that is what makes a
// manifest parse the same way here and in the hub.
//
// Only matchLabels is offered. kcp's virtual-workspace admission stamps a
// claim's matchLabels onto objects the provider writes through the export, but
// deliberately does not try to synthesize labels for a matchExpressions
// selector — a provider declaring one could not create the objects it claims.
type LabelSelector struct {
	// MatchLabels is the label set a claimed object must carry, ANDed.
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// ExportDecl is a CatalogEntry's spec.export: everything the manifest says
// about the provider's APIExport.
type ExportDecl struct {
	// Name is the APIExport name, which is NOT an API group: each resource
	// below names its own apiVersion.
	Name string `json:"name"`
	// Resources are the kinds the export serves that carry a verb or an
	// action. A kind with neither needs no entry at all.
	Resources []ExportResource `json:"resources,omitempty"`
}

// ExportResource is one entry of spec.export.resources[]: one of the
// provider's own kinds with the verbs and actions it serves on it. The
// apiVersion is declared once, here, for every coordinate hanging off it.
type ExportResource struct {
	Name       string   `json:"name"`
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Verbs      []Verb   `json:"verbs,omitempty"`
	Actions    []Action `json:"actions,omitempty"`
}

// Requirement mirrors apis/providers/v1alpha1.ProviderRequirement: everything
// this provider needs from ONE API group it does not own — another provider's
// kinds and verbs, or a platform builtin.
type Requirement struct {
	// Provider is the CatalogEntry name of the provider serving Group, empty
	// for a platform builtin. It drives the hub's Enable ordering and has no
	// counterpart on an APIExport.
	Provider string `json:"provider,omitempty"`
	// Group is the API group claimed, empty for the core group.
	Group string `json:"group,omitempty"`
	// Resources are the coordinates claimed in Group.
	Resources []RequiredResource `json:"resources,omitempty"`
}

// RequiredResource is one claimed coordinate: a kind with the verbs needed on
// it, or "<resource>/<verb>" to claim one of that provider's declared verbs.
type RequiredResource struct {
	Name     string         `json:"name"`
	Verbs    []string       `json:"verbs,omitempty"`
	Selector *LabelSelector `json:"selector,omitempty"`
}

type catalogEntryDoc struct {
	Kind string `json:"kind"`
	Spec struct {
		// Export is where every coordinate the provider publishes is
		// declared: the verbs and the actions of each resource it serves.
		// Both become kcp custom subresources; see subresources.go.
		Export *ExportDecl `json:"export,omitempty"`
		// Requires is the single source of the export's permission claims.
		Requires []Requirement `json:"requires,omitempty"`
	} `json:"spec"`
}

// documentSeparator matches a YAML document break on its own line. The
// manifests open with a comment block and a leading `---`, and some carry more
// than one document, so the CatalogEntry has to be picked out by kind.
var documentSeparator = regexp.MustCompile(`(?m)^---\s*$`)

// parseCatalogEntry returns the CatalogEntry document in raw.
func parseCatalogEntry(raw []byte) (*catalogEntryDoc, error) {
	for _, document := range documentSeparator.Split(string(raw), -1) {
		if strings.TrimSpace(document) == "" {
			continue
		}
		var entry catalogEntryDoc
		if err := yaml.Unmarshal([]byte(document), &entry); err != nil {
			// A document that is not a CatalogEntry may legitimately fail to
			// fit this shape (e.g. a Provider CR shipped alongside it); only a
			// total absence of a CatalogEntry is an error.
			continue
		}
		if entry.Kind != "CatalogEntry" {
			continue
		}
		if entry.Spec.Export == nil || entry.Spec.Export.Name == "" {
			return nil, fmt.Errorf("CatalogEntry has no spec.export.name")
		}
		return &entry, nil
	}
	return nil, fmt.Errorf("no kind: CatalogEntry document")
}

// ParseExport returns the spec.export of the CatalogEntry document in raw.
func ParseExport(raw []byte) (*ExportDecl, error) {
	entry, err := parseCatalogEntry(raw)
	if err != nil {
		return nil, err
	}
	return entry.Spec.Export, nil
}

// ParseRequirements returns the spec.requires of the CatalogEntry document in
// raw, in manifest order. File order is what makes the generated claim list
// deterministic.
func ParseRequirements(raw []byte) ([]Requirement, error) {
	entry, err := parseCatalogEntry(raw)
	if err != nil {
		return nil, err
	}
	return entry.Spec.Requires, nil
}

// readFile reads a manifest, wrapping the error the way every Load* here does.
func readFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", path, err)
	}
	return raw, nil
}

// LoadExport reads a CatalogEntry manifest and returns its spec.export.
func LoadExport(path string) (*ExportDecl, error) {
	raw, err := readFile(path)
	if err != nil {
		return nil, err
	}
	decl, err := ParseExport(raw)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return decl, nil
}

// LoadRequirements reads a CatalogEntry manifest and returns its spec.requires.
func LoadRequirements(path string) ([]Requirement, error) {
	raw, err := readFile(path)
	if err != nil {
		return nil, err
	}
	requirements, err := ParseRequirements(raw)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return requirements, nil
}

// ExportClaims renders spec.requires as the export's spec.permissionClaims, in
// the kcp apis.kcp.io/v1alpha2 shape: {group?, resource, verbs,
// defaultSelector?}.
//
// One requires entry is one API GROUP, and each of its resources is one claim:
//
//   - a plain resource claims exactly the verbs it declares;
//   - a "<resource>/<verb>" coordinate claims every verb ("*"), because kcp
//     authorizes the HTTP METHOD as the RBAC verb on a custom subresource (GET
//     for a log, POST for a sync, DELETE for a proxied session) and which one a
//     verb uses is the serving provider's business;
//   - an omitted group is the core group, and is left out of the claim
//     entirely, which is what kcp's own serialization does.
//
// Two further omissions are deliberate. Tenant scope is no longer a field:
// everything under spec.requires is tenant-scoped by definition, and the kcp
// claim has no counterpart for it, so nothing is emitted. And identityHash
// never appears: claims are identity-agnostic, resolved by kcp per consumer
// workspace against whichever copy of the serving provider that workspace
// bound — which is exactly what keeps working when an organization self-hosts
// the dependency.
//
// The manifest's `selector` becomes the export claim's `defaultSelector`,
// kcp's name for the scope an APIExport SUGGESTS. It is advisory on the export
// — what actually narrows access is the `selector` on the accepted claim in
// each tenant's APIBinding, which the hub writes from the same manifest field
// (pkg/hub/kcp/bootstrap.go, EnsureProviderAPIBinding). Publishing it here is
// what lets kcp flag a binding whose accepted scope has drifted from the one
// the provider asks for (condition PermissionClaimsValid, reason
// PermissionClaimsMismatch), and it is the scope kcp uses for WorkspaceType
// default bindings, which never pass through the hub at all.
//
// It fails closed on input the CatalogEntry contract forbids, rather than
// generating an export kcp would refuse or one that says more than its author
// did: a repeated group (there is one entry per group, so the second is a lost
// edit, not something to merge), a resource claimed twice in one group (kcp
// refuses an export claiming one group/resource more than once), a plain
// resource with no verbs, and verbs or a selector on a verb coordinate.
func ExportClaims(requirements []Requirement) ([]any, error) {
	out := make([]any, 0, len(requirements)*2)
	seenGroup := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		if _, dup := seenGroup[requirement.Group]; dup {
			return nil, fmt.Errorf("spec.requires declares the group %s twice: one group belongs to one provider, so state everything needed from it in a single entry", groupLabel(requirement.Group))
		}
		seenGroup[requirement.Group] = struct{}{}
		seen := make(map[string]struct{}, len(requirement.Resources))
		for _, resource := range requirement.Resources {
			if _, dup := seen[resource.Name]; dup {
				return nil, fmt.Errorf("spec.requires[%s] claims %q twice: kcp refuses an APIExport that claims one group/resource more than once", groupLabel(requirement.Group), resource.Name)
			}
			seen[resource.Name] = struct{}{}
			entry := map[string]any{"resource": resource.Name}
			if requirement.Group != "" {
				entry["group"] = requirement.Group
			}
			if strings.Contains(resource.Name, "/") {
				if len(resource.Verbs) > 0 {
					return nil, fmt.Errorf("spec.requires[%s] claims %q with verbs: the verb IS the capability, and which HTTP method reaches it is the serving provider's business, so the generated claim spells every verb — drop the verbs", groupLabel(requirement.Group), resource.Name)
				}
				if resource.Selector != nil {
					return nil, fmt.Errorf("spec.requires[%s] claims %q with a selector: a selector narrows which objects a claim covers, and a verb is invoked on an object the parent claim already covers", groupLabel(requirement.Group), resource.Name)
				}
				entry["verbs"] = append([]any(nil), coordinateClaimVerbs...)
				out = append(out, entry)
				continue
			}
			if len(resource.Verbs) == 0 {
				return nil, fmt.Errorf("spec.requires[%s] claims %q with no verbs: a claim on a kind has to say what it needs on it", groupLabel(requirement.Group), resource.Name)
			}
			verbs := make([]any, 0, len(resource.Verbs))
			for _, verb := range resource.Verbs {
				verbs = append(verbs, verb)
			}
			entry["verbs"] = verbs
			if selector := exportSelector(resource.Selector); selector != nil {
				entry["defaultSelector"] = selector
			}
			out = append(out, entry)
		}
	}
	return out, nil
}

// groupLabel names a group in an error the way its author wrote it.
func groupLabel(group string) string {
	if group == "" {
		return "core"
	}
	return group
}

// exportSelector renders a manifest selector as kcp's PermissionClaimSelector,
// or nil when the claim carries none (which kcp reads as matchAll).
func exportSelector(selector *LabelSelector) map[string]any {
	if selector == nil || len(selector.MatchLabels) == 0 {
		return nil
	}
	matchLabels := make(map[string]any, len(selector.MatchLabels))
	for key, value := range selector.MatchLabels {
		matchLabels[key] = value
	}
	return map[string]any{"matchLabels": matchLabels}
}

// BuildExport assembles the APIExport object. resources is apigen's
// spec.resources plus this generator's subresource entries (nil for a provider
// whose resources are minted at runtime); claims are ExportClaims' output.
func BuildExport(name string, resources, claims []any) map[string]any {
	if resources == nil {
		resources = []any{}
	}
	if claims == nil {
		claims = []any{}
	}
	return map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIExport",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"permissionClaims": claims,
			"resources":        resources,
		},
	}
}

// Marshal renders an export object as the file contents: the header comment
// followed by key-sorted YAML. sigs.k8s.io/yaml round-trips through JSON, so
// map keys come out in a stable order and the generator is idempotent.
func Marshal(export map[string]any) ([]byte, error) {
	body, err := yaml.Marshal(export)
	if err != nil {
		return nil, fmt.Errorf("marshalling APIExport: %w", err)
	}
	return append([]byte(Header), body...), nil
}

// Options are the generator's inputs.
type Options struct {
	// ManifestPath is the provider's CatalogEntry (manifest.yaml). Required.
	ManifestPath string
	// APIGenExportPath is the APIExport apigen wrote, named after the API
	// group. Empty means the provider has no apigen output at all — a
	// provider that mints every schema at runtime — and the export is
	// generated with an empty resource list. The APIResourceSchemas apigen
	// wrote beside it are read from the same directory.
	APIGenExportPath string
	// SchemasDir, when set, is the set of schemas the provider actually ships
	// (the chart's files/schemas/). Resources whose schema is not in it are
	// dropped — kuery's private Engagement CRD lives in config/crds so its
	// controller can install it, but must never reach the export — and a
	// shipped schema with no resource entry is an error. The verb schemas the
	// subresource entries name are copied into it by WriteVerbSchemas.
	SchemasDir string
}

// Output is what one run produces.
type Output struct {
	// Export is the APIExport file's contents.
	Export []byte
	// VerbSchemas are the APIResourceSchemas the export's custom subresource
	// entries name, keyed by the file name they ship under in SchemasDir
	// ("<verb>.<group>.yaml"), each pointing at apigen's copy.
	VerbSchemas map[string]VerbSchema
}

// Generate produces the file contents for the provider's APIExport.
func Generate(opts Options) ([]byte, error) {
	out, err := GenerateAll(opts)
	if err != nil {
		return nil, err
	}
	return out.Export, nil
}

// GenerateAll produces the APIExport and the verb schemas it references.
func GenerateAll(opts Options) (*Output, error) {
	decl, err := LoadExport(opts.ManifestPath)
	if err != nil {
		return nil, err
	}
	resources, err := apigenResources(opts.APIGenExportPath)
	if err != nil {
		return nil, err
	}
	subresources, err := LoadSubresources(opts.ManifestPath)
	if err != nil {
		return nil, err
	}
	if err := ValidateSubresourceNames(subresources); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", opts.ManifestPath, err)
	}
	// apigen lists every kind of the group as a resource, the "<Verb>Request"
	// kinds included. Those are schemas for the subresource entries, not
	// resources: set them aside before anything reads the resource list.
	verbKinds := map[string]VerbSchema{}
	if opts.APIGenExportPath != "" {
		schemas, err := ReadAPIGenSchemas(filepath.Dir(opts.APIGenExportPath))
		if err != nil {
			return nil, err
		}
		if resources, verbKinds, err = splitVerbKinds(resources, schemas); err != nil {
			return nil, err
		}
	}
	// Every declared verb and every catalogued action is a kcp custom
	// subresource: one more spec.resources[] entry named "<resource>/<verb>",
	// routed to the provider's own server through the endpoint object
	// provider-sdk/install publishes, naming the schema apigen minted for the
	// verb's kind. The declarations are unchanged and the hub-proxied routes
	// are untouched; this only tells kcp about them.
	entries, verbSchemas, err := SubresourceEntries(decl.Name, resources, verbKinds, subresources)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", opts.ManifestPath, err)
	}
	if opts.SchemasDir != "" {
		if resources, err = filterToShippedSchemas(resources, opts.SchemasDir, entries); err != nil {
			return nil, err
		}
		// A parent that was dropped as unshipped takes its subresources with
		// it: kcp refuses an entry whose parent the export does not carry.
		if entries, verbSchemas, err = SubresourceEntries(decl.Name, resources, verbKinds, subresources); err != nil {
			return nil, fmt.Errorf("manifest %s: %w", opts.ManifestPath, err)
		}
	}
	// spec.requires is the ONE source of the export's claims: the single place
	// a provider says what it reaches on a group it does not own, which also
	// drives the hub's minted RBAC and config/kcp/permissionclaimpolicy.yaml.
	requirements, err := LoadRequirements(opts.ManifestPath)
	if err != nil {
		return nil, err
	}
	claims, err := ExportClaims(requirements)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", opts.ManifestPath, err)
	}
	resources = append(resources, entries...)
	export, err := Marshal(BuildExport(decl.Name, resources, claims))
	if err != nil {
		return nil, err
	}
	return &Output{Export: export, VerbSchemas: verbSchemas}, nil
}

// WriteVerbSchemas copies the verb schemas GenerateAll returned into dir, under
// their shipping names, and removes any verb schema there that the export no
// longer names, so a verb that leaves the manifest leaves the chart too. The
// stored kinds' schemas in dir are the Makefile's copies and are left alone.
// Returns the file names written, sorted.
func WriteVerbSchemas(dir string, schemas map[string]VerbSchema) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading schemas dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		if _, still := schemas[entry.Name()]; still {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		schema, err := readSchema(path)
		if err != nil {
			return nil, err
		}
		if schema != nil && schema.IsVerbKind() {
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("removing stale verb schema %s: %w", path, err)
			}
		}
	}
	names := make([]string, 0, len(schemas))
	for name, schema := range schemas {
		raw, err := os.ReadFile(schema.Path)
		if err != nil {
			return nil, fmt.Errorf("reading verb schema %s: %w", schema.Path, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			return nil, fmt.Errorf("writing verb schema %s: %w", name, err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// apigenResources reads spec.resources out of apigen's APIExport.
func apigenResources(path string) ([]any, error) {
	if path == "" {
		return []any{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading apigen APIExport %s: %w", path, err)
	}
	var export struct {
		Kind string `json:"kind"`
		Spec struct {
			Resources []any `json:"resources"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &export); err != nil {
		return nil, fmt.Errorf("parsing apigen APIExport %s: %w", path, err)
	}
	if export.Kind != "APIExport" {
		return nil, fmt.Errorf("%s: expected kind APIExport, got %q", path, export.Kind)
	}
	if export.Spec.Resources == nil {
		return []any{}, nil
	}
	// Regeneration feeds the committed export back in as the resource source,
	// so drop the entries this generator owns before deriving them again.
	return stripSubresourceEntries(export.Spec.Resources), nil
}

// filterToShippedSchemas keeps only the resources whose schema is one of the
// APIResourceSchemas in dir, and fails when a shipped schema has no resource
// entry — that pairing is the whole point of the check: the export a tenant
// binds and the schemas the workspace holds must describe the same API. A
// shipped verb schema counts as referenced when a subresource entry names it;
// one nothing names is stale and is pruned by WriteVerbSchemas rather than
// reported, since the generator owns those copies.
func filterToShippedSchemas(resources []any, dir string, subresources []any) ([]any, error) {
	shipped, err := SchemaNames(dir)
	if err != nil {
		return nil, err
	}
	referenced := map[string]bool{}
	for _, entry := range subresources {
		if entry, ok := entry.(map[string]any); ok {
			name, _ := entry["schema"].(string)
			referenced[name] = true
		}
	}
	verbNames, err := verbSchemaNames(dir)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(shipped))
	for _, name := range shipped {
		if verbNames[name] {
			continue
		}
		want[name] = false
	}
	out := make([]any, 0, len(shipped))
	for _, resource := range resources {
		entry, ok := resource.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("apigen APIExport: spec.resources entry is not a mapping")
		}
		name, _ := entry["schema"].(string)
		if _, ok := want[name]; !ok {
			continue
		}
		want[name] = true
		out = append(out, resource)
	}
	missing := make([]string, 0, len(want))
	for name, matched := range want {
		if !matched && !referenced[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%s ships APIResourceSchema(s) %s that apigen's APIExport does not reference; re-run codegen", dir, strings.Join(missing, ", "))
	}
	return out, nil
}

// verbSchemaNames returns the names of the verb-kind schemas in dir.
func verbSchemaNames(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading schemas dir %s: %w", dir, err)
	}
	out := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		schema, err := readSchema(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if schema != nil && schema.IsVerbKind() {
			out[schema.Name] = true
		}
	}
	return out, nil
}

// SchemaNames returns the metadata.name of every APIResourceSchema in dir,
// sorted. It is exported because install reads the same directory layout.
func SchemaNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading schemas dir %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading schema %s: %w", path, err)
		}
		var schema struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal(raw, &schema); err != nil {
			return nil, fmt.Errorf("parsing schema %s: %w", path, err)
		}
		if schema.Kind != "APIResourceSchema" {
			return nil, fmt.Errorf("%s: expected kind APIResourceSchema, got %q", path, schema.Kind)
		}
		if schema.Metadata.Name == "" {
			return nil, fmt.Errorf("%s: metadata.name is required", path)
		}
		names = append(names, schema.Metadata.Name)
	}
	sort.Strings(names)
	return names, nil
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}
