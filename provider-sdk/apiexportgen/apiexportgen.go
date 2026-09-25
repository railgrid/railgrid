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
//     the export's NAME and its PERMISSION CLAIMS (plus everything the portal
//     shows);
//   - the APIExport (`config/kcp/apiexport-<exportName>.yaml`), generated, the
//     single source for the export's RESOURCES.
//
// kcp's apigen already emits the correct spec.resources — every kind with its
// immutable, versioned APIResourceSchema name — but it names the export after
// the API GROUP and knows nothing about claims. This package takes apigen's
// output, renames the export to the manifest's spec.apiExport.name, stamps
// spec.permissionClaims from the manifest, and writes deterministic YAML.
//
// spec.permissionClaims has two sources in the manifest, and always both:
// spec.apiExport.permissionClaims verbatim, then one claim per
// spec.dependencies[].composes[] entry the first list does not already cover
// (see MergeCompositionClaims). A composition is the provider saying it creates
// and manages a dependency's kind inside the tenant workspace, so it IS a
// permission claim; writing it twice would let the two drift.
//
// No claim carries an identityHash: a first-party claim is identity-agnostic,
// resolved by kcp per consumer workspace against whatever export that
// workspace bound and admitted by the platform's PermissionClaimPolicy.
//
// The same parser serves runtime callers: a provider whose resources are minted
// at runtime rather than by apigen still reads its claims from
// the manifest through LoadManifest, so a claim is written in exactly one place
// no matter how the provider gets its schemas.
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
#   metadata.name, spec.permissionClaims  <- manifest.yaml spec.apiExport
#   spec.resources                        <- kcp apigen (hack/apigen.sh), plus
#                                            one "<resource>/<verb>" custom
#                                            subresource per manifest.yaml
#                                            spec.dataPlane.verbs[] entry and
#                                            spec.actions[] entry, naming the
#                                            schema apigen minted for the verb's
#                                            "<Verb>Request" kind
# Regenerate with: make codegen-<provider>-provider. No claim carries an
# identityHash: kcp resolves a first-party claim per consumer workspace against
# whatever export that workspace bound (PermissionClaimPolicy admits it).
`

// PermissionClaim mirrors apis/providers/v1alpha1.ProviderPermissionClaim.
//
// It is duplicated here rather than imported because provider-sdk is a module
// of its own with no dependency on the railgrid monorepo — that independence is
// what lets a provider be built outside this repository. The JSON tags MUST
// stay identical to the CatalogEntry type's, since that is what makes a
// manifest parse the same way here and in the hub.
type PermissionClaim struct {
	// Group is the API group, empty for core kubernetes types.
	Group string `json:"group,omitempty"`
	// Resource is the plural resource name.
	Resource string `json:"resource"`
	// Verbs are the claimed verbs.
	Verbs []string `json:"verbs,omitempty"`
	// TenantScoped is a CatalogEntry/Enable concept (it drives auto-accept in
	// the portal) and is deliberately NOT part of the kcp APIExport spec. It is
	// parsed so a manifest round-trips, and dropped when the export is built.
	TenantScoped bool `json:"tenantScoped,omitempty"`
	// Selector narrows the claim to the objects carrying a label set. It is
	// rendered into the export as kcp's spec.permissionClaims[].defaultSelector.
	Selector *PermissionClaimSelector `json:"selector,omitempty"`
}

// PermissionClaimSelector mirrors
// apis/providers/v1alpha1.ProviderPermissionClaimSelector, and is rendered into
// the generated APIExport as kcp's PermissionClaimSelector.
//
// Only matchLabels is offered. kcp's virtual-workspace admission stamps a
// claim's matchLabels onto objects the provider writes through the export, but
// deliberately does not try to synthesize labels for a matchExpressions
// selector — a provider declaring one could not create the objects it claims.
type PermissionClaimSelector struct {
	// MatchLabels is the label set a claimed object must carry, ANDed.
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// APIExportDecl is a CatalogEntry's spec.apiExport: everything the manifest
// says about the provider's APIExport.
type APIExportDecl struct {
	Name             string            `json:"name"`
	PermissionClaims []PermissionClaim `json:"permissionClaims,omitempty"`
}

// Composition mirrors apis/providers/v1alpha1.ProviderComposition: one
// (group, resource) of a DEPENDENCY provider that this provider's reconcilers
// create and manage inside the tenant workspace, with the verbs they need.
//
// It is duplicated here for the same reason PermissionClaim is — provider-sdk
// is its own module — and the JSON tags must stay identical to the
// CatalogEntry type's.
type Composition struct {
	Group    string   `json:"group"`
	Resource string   `json:"resource"`
	Verbs    []string `json:"verbs,omitempty"`
}

// Dependency mirrors apis/providers/v1alpha1.ProviderDependency.
type Dependency struct {
	Name     string        `json:"name"`
	Composes []Composition `json:"composes,omitempty"`
}

type catalogEntryDoc struct {
	Kind string `json:"kind"`
	Spec struct {
		APIExport    *APIExportDecl `json:"apiExport,omitempty"`
		Dependencies []Dependency   `json:"dependencies,omitempty"`
		// DataPlane and Actions are the two places a provider declares a
		// {resource, verb} coordinate. Both become kcp custom subresources;
		// see subresources.go.
		DataPlane *DataPlaneDecl `json:"dataPlane,omitempty"`
		Actions   []Action       `json:"actions,omitempty"`
	} `json:"spec"`
}

// DataPlaneDecl is a CatalogEntry's spec.dataPlane.
type DataPlaneDecl struct {
	Verbs []DataPlaneVerb `json:"verbs,omitempty"`
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
		if entry.Spec.APIExport == nil || entry.Spec.APIExport.Name == "" {
			return nil, fmt.Errorf("CatalogEntry has no spec.apiExport.name")
		}
		return &entry, nil
	}
	return nil, fmt.Errorf("no kind: CatalogEntry document")
}

// ParseManifest returns the spec.apiExport of the CatalogEntry document in raw.
func ParseManifest(raw []byte) (*APIExportDecl, error) {
	entry, err := parseCatalogEntry(raw)
	if err != nil {
		return nil, err
	}
	return entry.Spec.APIExport, nil
}

// ParseCompositions returns every spec.dependencies[].composes[] entry of the
// CatalogEntry document in raw, flattened in manifest order (dependencies in
// the order written, each dependency's compositions in the order written).
// File order is what makes the generated claim list deterministic.
func ParseCompositions(raw []byte) ([]Composition, error) {
	entry, err := parseCatalogEntry(raw)
	if err != nil {
		return nil, err
	}
	return flattenCompositions(entry.Spec.Dependencies), nil
}

func flattenCompositions(dependencies []Dependency) []Composition {
	out := []Composition{}
	for _, dependency := range dependencies {
		out = append(out, dependency.Composes...)
	}
	return out
}

// readFile reads a manifest, wrapping the error the way every Load* here does.
func readFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", path, err)
	}
	return raw, nil
}

// LoadManifest reads a CatalogEntry manifest and returns its spec.apiExport.
func LoadManifest(path string) (*APIExportDecl, error) {
	raw, err := readFile(path)
	if err != nil {
		return nil, err
	}
	decl, err := ParseManifest(raw)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return decl, nil
}

// LoadCompositions reads a CatalogEntry manifest and returns its flattened
// spec.dependencies[].composes[].
func LoadCompositions(path string) ([]Composition, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", path, err)
	}
	compositions, err := ParseCompositions(raw)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return compositions, nil
}

// ExportClaims renders manifest claims in the kcp apis.kcp.io/v1alpha2 APIExport
// shape: {group?, resource, verbs, defaultSelector?}. Three deliberate
// omissions:
//
//   - an empty group is left out entirely (core types), which is what kcp's own
//     serialization does and what install.ApplyAPIExport wrote before the
//     export became a file;
//   - tenantScoped is a CatalogEntry concept with no kcp counterpart;
//   - identityHash never appears: claims are identity-agnostic.
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
func ExportClaims(claims []PermissionClaim) []any {
	out := make([]any, 0, len(claims))
	for _, claim := range claims {
		entry := map[string]any{"resource": claim.Resource}
		if claim.Group != "" {
			entry["group"] = claim.Group
		}
		if len(claim.Verbs) > 0 {
			verbs := make([]any, 0, len(claim.Verbs))
			for _, verb := range claim.Verbs {
				verbs = append(verbs, verb)
			}
			entry["verbs"] = verbs
		}
		if selector := exportSelector(claim.Selector); selector != nil {
			entry["defaultSelector"] = selector
		}
		out = append(out, entry)
	}
	return out
}

// MergeCompositionClaims appends one permission claim per composition that the
// manifest's own spec.apiExport.permissionClaims does not already cover, and
// returns the combined list. The manifest's claims keep their order and their
// contents; composition claims follow, in manifest order.
//
// IDENTITY-AGNOSTIC: the appended claims carry no identityHash, deliberately.
// A composition is resolved per consumer workspace — against whatever APIExport
// that workspace bound for the claimed group — which is exactly what keeps
// working when an organization runs its own copy of the dependency provider. A
// claim with no identityHash is admitted by a kcp that serves admin.kcp.io
// PermissionClaimPolicy (kcp-dev/kcp#4388) when its cluster-scoped policy pairs
// this export's own API group (the claimer) with the claimed group; see
// hack/generate-permission-claim-policy.mjs and config/kcp/permissionclaimpolicy.yaml,
// which are generated from these same composes[] entries. That is the normal
// behaviour: every generated export carries its compositions as claims.
//
// Duplicates are decided on (group, resource) alone: if the manifest already
// claims that pair, its entry is left EXACTLY as written — same verbs, same
// selector, same identityHash policy — and no second claim is appended. A verb
// disagreement therefore resolves in the manifest's favour, silently and by
// design: an explicit spec.apiExport.permissionClaims entry is a deliberate,
// hand-written statement about kcp's wire contract, while a composition is a
// statement about what the hub will mint; letting a composition widen a
// hand-written claim would make the generated export say more than its author
// did. The narrower or wider composition verb set is simply not used.
func MergeCompositionClaims(claims []PermissionClaim, compositions []Composition) []PermissionClaim {
	declared := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		declared[claim.Group+"/"+claim.Resource] = struct{}{}
	}
	merged := make([]PermissionClaim, 0, len(claims)+len(compositions))
	merged = append(merged, claims...)
	for _, composition := range compositions {
		key := composition.Group + "/" + composition.Resource
		if _, ok := declared[key]; ok {
			continue
		}
		declared[key] = struct{}{}
		verbs := append([]string(nil), composition.Verbs...)
		merged = append(merged, PermissionClaim{
			Group:    composition.Group,
			Resource: composition.Resource,
			Verbs:    verbs,
		})
	}
	return merged
}

// exportSelector renders a manifest selector as kcp's PermissionClaimSelector,
// or nil when the claim carries none (which kcp reads as matchAll).
func exportSelector(selector *PermissionClaimSelector) map[string]any {
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
// spec.resources verbatim (nil for a provider whose resources are minted at
// runtime); claims come from the manifest.
func BuildExport(name string, resources []any, claims []PermissionClaim) map[string]any {
	if resources == nil {
		resources = []any{}
	}
	return map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIExport",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"permissionClaims": ExportClaims(claims),
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
	decl, err := LoadManifest(opts.ManifestPath)
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
	// Every declared data-plane verb and catalogued action is a kcp custom
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
	// Every spec.dependencies[].composes[] entry is a permission claim, always:
	// the composition declaration is the single place a provider says it
	// manages a dependency's kind, and the kcp claim, the hub's minted RBAC and
	// config/kcp/permissionclaimpolicy.yaml all come from it.
	compositions, err := LoadCompositions(opts.ManifestPath)
	if err != nil {
		return nil, err
	}
	claims := MergeCompositionClaims(decl.PermissionClaims, compositions)
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
