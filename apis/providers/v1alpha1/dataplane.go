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
)

var (
	providerDataPlaneResourcePattern = regexp.MustCompile(`^[a-z][a-z0-9]*([a-z0-9-]*[a-z0-9])?$`)
	providerDataPlaneVerbPattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// providerDataPlaneReservedVerbs are the standard Kubernetes verbs. A
// data-plane verb becomes the subresource half of a {resource}/{verb} RBAC
// coordinate, so a provider declaring `get` there would be declaring the
// coordinate `instances/get` — which reads like the ordinary `get` on
// `instances` and is not. Refusing them keeps the two vocabularies apart.
var providerDataPlaneReservedVerbs = map[string]struct{}{
	"get": {}, "list": {}, "watch": {}, "create": {}, "update": {},
	"patch": {}, "delete": {}, "deletecollection": {},
}

// ValidateProviderDataPlane validates a provider's declared data-plane verb
// surface. It fails closed: one malformed entry rejects the whole declaration,
// because a partially admitted verb list would let the hub mint a capability
// for a coordinate the provider never meant to publish.
func ValidateProviderDataPlane(dataPlane *ProviderDataPlane) error {
	if dataPlane == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(dataPlane.Verbs))
	for i, verb := range dataPlane.Verbs {
		if err := ValidateProviderDataPlaneVerb(verb); err != nil {
			return fmt.Errorf("dataPlane.verbs[%d] (%s/%s): %w", i, verb.Resource, verb.Verb, err)
		}
		key := verb.Resource + "/" + verb.Verb
		if _, ok := seen[key]; ok {
			return fmt.Errorf("dataPlane.verbs[%d] (%s): duplicate resource/verb coordinate", i, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ValidateProviderDataPlaneVerb validates one declaration independently of its
// siblings.
func ValidateProviderDataPlaneVerb(verb ProviderDataPlaneVerb) error {
	if !providerDataPlaneResourcePattern.MatchString(verb.Resource) || len(verb.Resource) > 63 {
		return fmt.Errorf("resource must be a lowercase DNS-like plural name")
	}
	if !providerDataPlaneVerbPattern.MatchString(verb.Verb) || len(verb.Verb) > 63 {
		return fmt.Errorf("verb must be a lowercase name with no version and no slash")
	}
	if _, reserved := providerDataPlaneReservedVerbs[verb.Verb]; reserved {
		return fmt.Errorf("verb %q is a standard Kubernetes verb and cannot be a data-plane verb", verb.Verb)
	}
	if len(verb.Description) > 512 {
		return fmt.Errorf("description exceeds 512 characters")
	}
	if strings.ContainsAny(verb.Description, "\r\n\x00") {
		return fmt.Errorf("description contains prohibited characters")
	}
	return nil
}

// ProviderDataPlaneVerbs returns the verbs declared on resource. The result is
// the authoritative answer to "may a {resource}/{verb} capability be minted
// for this provider", used by the hub scoped-identity policy.
func ProviderDataPlaneVerbs(dataPlane *ProviderDataPlane, resource string) []string {
	if dataPlane == nil {
		return nil
	}
	verbs := make([]string, 0, len(dataPlane.Verbs))
	for _, verb := range dataPlane.Verbs {
		if verb.Resource == resource {
			verbs = append(verbs, verb.Verb)
		}
	}
	return verbs
}
