// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

// In-place instance updates. update_instance merge-patches an instance's
// values (RFC 7386 semantics: send only what changes, null unsets) and writes
// the CR back — kro reconciles the delta into the children, so an image bump
// is a rolling Deployment update with the database child untouched. This is
// the same lifecycle the portal's applyYaml path drives; without it MCP
// agents had to delete+re-provision, wiping managed state.
//
// Guardrails: identity and platform-owned fields are always immutable
// (name, kedgeMode, expose, credentialsSecretName); templates declare their
// own immutable inputs via the kedge.faros.sh/immutable-inputs annotation
// (e.g. database.version — a Postgres major upgrade is not an in-place
// operation). A rejected update names the offending path and why.

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/faroshq/provider-infrastructure/kro"
)

// alwaysImmutableInputs are denied on every template. name is child-resource
// (and host) identity; kedgeMode is a lifecycle transition (dev↔production
// swaps the resource graph — recreate instead); expose/credentialsSecretName
// are platform-stamped.
var alwaysImmutableInputs = []string{"name", "kedgeMode", "expose", "credentialsSecretName"}

// updateInstance locates the named instance, merge-patches its spec, rejects
// immutable-path changes, and writes the CR back. Returns the refreshed
// instance and the dot-paths that actually changed (empty patch or no-op
// patch → no write, empty changed list).
func updateInstance(ctx context.Context, dyn dynamic.Interface, templates []kro.Template, name string, patch map[string]any) (*kro.Instance, []string, error) {
	for i := range templates {
		t := &templates[i]
		if t.InstanceGVR.Resource == "" {
			continue
		}
		u, err := dyn.Resource(t.InstanceGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		spec, _, _ := unstructured.NestedMap(u.Object, "spec")
		if spec == nil {
			spec = map[string]any{}
		}
		merged := mergePatchValues(spec, patch)
		changed := changedValuePaths(spec, merged)
		if len(changed) == 0 {
			inst := instanceFromUnstructured(u, t.Name)
			return inst, nil, nil
		}
		if err := rejectImmutableChanges(changed, t); err != nil {
			return nil, nil, err
		}
		u.Object["spec"] = merged
		updated, err := dyn.Resource(t.InstanceGVR).Update(ctx, u, metav1.UpdateOptions{})
		if err != nil {
			return nil, nil, fmt.Errorf("update %s/%s: %w", t.InstanceGVR.Resource, name, err)
		}
		return instanceFromUnstructured(updated, t.Name), changed, nil
	}
	return nil, nil, kro.ErrInstanceNotFound
}

// rejectImmutableChanges fails when any changed path is covered by the
// always-immutable set or the template's declared immutable inputs.
func rejectImmutableChanges(changed []string, t *kro.Template) error {
	immutable := append(append([]string{}, alwaysImmutableInputs...), t.ImmutableInputs...)
	for _, path := range changed {
		for _, imm := range immutable {
			if path == imm || strings.HasPrefix(path, imm+".") {
				return fmt.Errorf(
					"value %q cannot be changed on a live instance (immutable: %s); recreate the instance to change it",
					path, immutableReason(imm))
			}
		}
	}
	return nil
}

// immutableReason maps the built-in immutable inputs to a why; template-
// declared ones get a generic "declared immutable by the template".
func immutableReason(input string) string {
	switch input {
	case "name":
		return "it is the instance's identity — child resources and the public hostname derive from it"
	case "kedgeMode":
		return "development↔production is a lifecycle transition that swaps the resource graph, not a config edit"
	case "expose", "credentialsSecretName":
		return "it is platform-stamped"
	default:
		return "declared immutable by the template"
	}
}

// mergePatchValues applies RFC 7386 JSON-merge-patch semantics: objects merge
// recursively, null deletes a key, everything else (scalars, arrays) replaces.
// The inputs are not mutated; patched branches are fresh maps.
func mergePatchValues(target, patch map[string]any) map[string]any {
	out := make(map[string]any, len(target)+len(patch))
	maps.Copy(out, target)
	for k, v := range patch {
		if v == nil {
			delete(out, k)
			continue
		}
		if pm, ok := v.(map[string]any); ok {
			tm, _ := out[k].(map[string]any)
			if tm == nil {
				tm = map[string]any{}
			}
			out[k] = mergePatchValues(tm, pm)
			continue
		}
		out[k] = v
	}
	return out
}

// changedValuePaths returns the sorted dot-paths whose values differ between
// old and new: additions, removals, and changes. Nested maps recurse; any
// other type is a leaf compared by deep equality.
func changedValuePaths(old, new map[string]any) []string {
	var out []string
	collectChangedPaths(old, new, "", &out)
	sort.Strings(out)
	return out
}

func collectChangedPaths(old, new map[string]any, prefix string, out *[]string) {
	keys := map[string]struct{}{}
	for k := range old {
		keys[k] = struct{}{}
	}
	for k := range new {
		keys[k] = struct{}{}
	}
	for k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		ov, oOK := old[k]
		nv, nOK := new[k]
		om, oIsMap := ov.(map[string]any)
		nm, nIsMap := nv.(map[string]any)
		// Recurse whenever either side is a map — an added or removed subtree
		// must report its LEAF paths, or a wholesale-added object (e.g.
		// database: {version: ...} where none existed) would bypass an
		// immutability rule on a nested path.
		if oIsMap || nIsMap {
			if om == nil {
				om = map[string]any{}
			}
			if nm == nil {
				nm = map[string]any{}
			}
			collectChangedPaths(om, nm, path, out)
			// A map replaced by a scalar (or vice versa) also changes the
			// path itself.
			if oOK && nOK && oIsMap != nIsMap {
				*out = append(*out, path)
			}
			continue
		}
		if oOK && nOK {
			if !reflect.DeepEqual(ov, nv) {
				*out = append(*out, path)
			}
			continue
		}
		// Added or removed scalar.
		*out = append(*out, path)
	}
}
