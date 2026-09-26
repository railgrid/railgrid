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

package v1alpha1

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// resourceNamePattern is a lowercase DNS-like plural resource name.
	resourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*([a-z0-9-]*[a-z0-9])?$`)
	// verbNamePattern is the verb half of a coordinate: no version, no slash.
	verbNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	// kindPattern is a Kubernetes kind.
	kindPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	// actionVersionPattern is an action's contract revision.
	actionVersionPattern = regexp.MustCompile(`^v[1-9][0-9]{0,7}$`)
)

// reservedVerbs are the standard Kubernetes verbs. A declared verb becomes the
// subresource half of a {resource}/{verb} coordinate, so a provider declaring
// `get` there would be declaring `instances/get` — which reads like the
// ordinary `get` on `instances` and is not. Refusing them keeps the two
// vocabularies apart.
var reservedVerbs = map[string]struct{}{
	"get": {}, "list": {}, "watch": {}, "create": {}, "update": {},
	"patch": {}, "delete": {}, "deletecollection": {},
}

// schemaOwnedVerbs are the subresources a kind's own schema owns. kcp refuses
// them as custom subresources outright, and one bad name makes a whole
// APIExport unappliable, so they are caught here.
var schemaOwnedVerbs = map[string]struct{}{"status": {}, "scale": {}}

// ValidateProviderExport validates a provider's declared export surface.
//
// It fails closed: one malformed entry rejects the whole declaration, because
// a partially admitted export would let the hub mint a capability for a
// coordinate the provider never meant to publish, and would publish an
// APIExport kcp refuses to apply.
func ValidateProviderExport(export *ProviderExport) error {
	if export == nil {
		return nil
	}
	if strings.TrimSpace(export.Name) == "" {
		return fmt.Errorf("export.name is required")
	}
	if len(export.Name) > 253 {
		return fmt.Errorf("export.name exceeds 253 characters")
	}
	seenResource := make(map[string]struct{}, len(export.Resources))
	for i, resource := range export.Resources {
		if err := ValidateProviderExportResource(resource); err != nil {
			return fmt.Errorf("export.resources[%d] (%s): %w", i, resource.Name, err)
		}
		if _, dup := seenResource[resource.Name]; dup {
			return fmt.Errorf("export.resources[%d] (%s): duplicate resource", i, resource.Name)
		}
		seenResource[resource.Name] = struct{}{}
	}
	return nil
}

// ValidateProviderExportResource validates one resource and everything
// declared on it. Verbs and actions share one coordinate namespace: a verb and
// an action of the same name on the same resource would generate the same
// APIExport entry, so the second is refused here rather than silently winning.
func ValidateProviderExportResource(resource ProviderExportResource) error {
	if !resourceNamePattern.MatchString(resource.Name) || len(resource.Name) > 63 {
		return fmt.Errorf("name must be a lowercase DNS-like plural name")
	}
	// ParseGroupVersion reads an unqualified string as a bare version, so the
	// slash is checked first: a provider exports no core kinds, and an
	// apiVersion without a group would otherwise parse as one.
	gv, err := schema.ParseGroupVersion(resource.APIVersion)
	if err != nil || strings.Count(resource.APIVersion, "/") != 1 || gv.Group == "" || gv.Version == "" {
		return fmt.Errorf("apiVersion must be group/version, naming the group this export serves")
	}
	if !kindPattern.MatchString(resource.Kind) {
		return fmt.Errorf("kind must be UpperCamelCase")
	}
	seen := make(map[string]struct{}, len(resource.Verbs)+len(resource.Actions))
	claim := func(name, what string) error {
		if _, dup := seen[name]; dup {
			return fmt.Errorf("%s %q: the coordinate %s/%s is declared twice", what, name, resource.Name, name)
		}
		seen[name] = struct{}{}
		return nil
	}
	for i, verb := range resource.Verbs {
		if err := ValidateProviderVerb(verb); err != nil {
			return fmt.Errorf("verbs[%d] (%s): %w", i, verb.Name, err)
		}
		if err := claim(verb.Name, "verb"); err != nil {
			return err
		}
	}
	for i, action := range resource.Actions {
		if err := ValidateProviderAction(action); err != nil {
			return fmt.Errorf("actions[%d] (%s): %w", i, action.Name, err)
		}
		if err := claim(action.Name, "action"); err != nil {
			return err
		}
	}
	return nil
}

// ValidateProviderVerb validates one verb declaration independently of its
// siblings.
func ValidateProviderVerb(verb ProviderVerb) error {
	if err := validateCoordinateName(verb.Name); err != nil {
		return err
	}
	if len(verb.Description) > 512 {
		return fmt.Errorf("description exceeds 512 characters")
	}
	if strings.ContainsAny(verb.Description, "\r\n\x00") {
		return fmt.Errorf("description contains prohibited characters")
	}
	return nil
}

// validateCoordinateName is the shared check on the verb half of a
// {resource}/{verb} coordinate, applied to verbs and actions alike.
func validateCoordinateName(name string) error {
	if !verbNamePattern.MatchString(name) || len(name) > 63 {
		return fmt.Errorf("name must be a lowercase name with no version and no slash")
	}
	if _, reserved := reservedVerbs[name]; reserved {
		return fmt.Errorf("%q is a standard Kubernetes verb and cannot name a coordinate", name)
	}
	if _, owned := schemaOwnedVerbs[name]; owned {
		return fmt.Errorf("%q is a subresource the kind's own schema owns", name)
	}
	return nil
}

// ProviderCoordinate is one flattened {resource}/{verb} coordinate: what kcp
// publishes as a custom subresource, what a caller addresses, and what a grant
// names. Everything that walks a provider's callable surface — the APIExport
// generator, the server's route table, the hub's identity policy — walks these
// rather than the nested declaration.
type ProviderCoordinate struct {
	// Resource is the plural parent resource.
	Resource string
	// Verb is the verb or action name.
	Verb string
	// APIVersion and Kind come from the parent resource, so a consumer can
	// address the coordinate without knowing the provider's group.
	APIVersion string
	Kind       string
	// Action is true when this coordinate is a catalogued action, and Version
	// is then its contract revision. A plain verb has neither.
	Action  bool
	Version string
}

// String renders the coordinate as kcp names it.
func (c ProviderCoordinate) String() string { return c.Resource + "/" + c.Verb }

// GroupVersion parses the coordinate's apiVersion.
func (c ProviderCoordinate) GroupVersion() schema.GroupVersion {
	gv, _ := schema.ParseGroupVersion(c.APIVersion)
	return gv
}

// Coordinates flattens the export into every coordinate it publishes, in
// declaration order: each resource's verbs, then its actions.
func (e *ProviderExport) Coordinates() []ProviderCoordinate {
	if e == nil {
		return nil
	}
	out := make([]ProviderCoordinate, 0, len(e.Resources)*4)
	for _, resource := range e.Resources {
		for _, verb := range resource.Verbs {
			out = append(out, ProviderCoordinate{
				Resource: resource.Name, Verb: verb.Name,
				APIVersion: resource.APIVersion, Kind: resource.Kind,
			})
		}
		for _, action := range resource.Actions {
			out = append(out, ProviderCoordinate{
				Resource: resource.Name, Verb: action.Name,
				APIVersion: resource.APIVersion, Kind: resource.Kind,
				Action: true, Version: action.Version,
			})
		}
	}
	return out
}

// Resource returns the declared resource by plural name.
func (e *ProviderExport) Resource(name string) (ProviderExportResource, bool) {
	if e == nil {
		return ProviderExportResource{}, false
	}
	for _, resource := range e.Resources {
		if resource.Name == name {
			return resource, true
		}
	}
	return ProviderExportResource{}, false
}

// Actions returns every action the export declares, paired with the resource
// it is bound to.
func (e *ProviderExport) Actions() []ProviderCoordinate {
	out := make([]ProviderCoordinate, 0, 8)
	for _, coordinate := range e.Coordinates() {
		if coordinate.Action {
			out = append(out, coordinate)
		}
	}
	return out
}

// ProviderDeclaredVerbs returns every verb and action name declared on
// resource. It is the authoritative answer to "may a {resource}/{verb}
// capability be minted for this provider", used by the hub's scoped-identity
// policy, which draws no distinction between a verb and an action: both are
// one coordinate somebody may be granted.
func ProviderDeclaredVerbs(export *ProviderExport, resource string) []string {
	if export == nil {
		return nil
	}
	declared, ok := export.Resource(resource)
	if !ok {
		return nil
	}
	verbs := make([]string, 0, len(declared.Verbs)+len(declared.Actions))
	for _, verb := range declared.Verbs {
		verbs = append(verbs, verb.Name)
	}
	for _, action := range declared.Actions {
		verbs = append(verbs, action.Name)
	}
	return verbs
}
