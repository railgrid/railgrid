// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Sync routing — the workspacePath→component algorithm from app-studio's
// development_sync.go. Files are routed by each component's declared
// workspacePath prefix and delivered COMPONENT-RELATIVE (prefix stripped) to
// the dev-agent's /sync endpoint via the dataplane.

// DevInfo is what the provision flow needs from a Template, resolved
// once as the caller: the instance CRD coordinates, the dev components, and
// the scaffold pin.
type DevInfo struct {
	Group    string
	Version  string
	Resource string
	Kind     string
	// Components maps component name → workspacePath.
	Components map[string]string
	// ImageInputs maps component name → the production schema input its
	// built image lands in (empty when a component is not buildable).
	ImageInputs map[string]string
	// Scaffold repo pin; empty Repository means no scaffold.
	ScaffoldRepository string
	ScaffoldRef        string
}

// ParseDevInfo extracts the fields from an unstructured Template.
func ParseDevInfo(tmpl *unstructured.Unstructured) (DevInfo, error) {
	info := DevInfo{Components: map[string]string{}, ImageInputs: map[string]string{}}
	var ok bool
	info.Group, _, _ = unstructured.NestedString(tmpl.Object, "spec", "instanceCRD", "group")
	info.Version, _, _ = unstructured.NestedString(tmpl.Object, "spec", "instanceCRD", "version")
	info.Resource, _, _ = unstructured.NestedString(tmpl.Object, "spec", "instanceCRD", "resource")
	info.Kind, _, _ = unstructured.NestedString(tmpl.Object, "spec", "instanceCRD", "kind")
	if info.Group == "" {
		info.Group = "infrastructure.faros.sh"
	}
	if info.Version == "" {
		info.Version = "v1alpha1"
	}
	if info.Resource == "" || info.Kind == "" {
		return info, fmt.Errorf("template has no instanceCRD (resource=%q kind=%q)", info.Resource, info.Kind)
	}
	comps, ok, _ := unstructured.NestedMap(tmpl.Object, "spec", "development", "components")
	if ok {
		for name, v := range comps {
			cm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			// ONE NAME RULE: a component's directory is its own name. The
			// field is optional and, when set, validated equal to the key —
			// so an absent value simply means "the component's own name".
			wp, _ := cm["workspacePath"].(string)
			if strings.TrimSpace(wp) == "" {
				wp = name
			}
			info.Components[name] = wp
			if ii, _ := cm["imageInput"].(string); ii != "" {
				info.ImageInputs[name] = ii
			}
		}
	}
	info.ScaffoldRepository, _, _ = unstructured.NestedString(tmpl.Object, "spec", "development", "scaffold", "repository")
	info.ScaffoldRef, _, _ = unstructured.NestedString(tmpl.Object, "spec", "development", "scaffold", "ref")
	return info, nil
}

// routeSyncFiles splits workspace-relative files per component by workspacePath
// prefix, stripping the prefix. Pure.
func RouteFiles(components map[string]string, files []File) map[string][]File {
	out := map[string][]File{}
	for comp, wp := range components {
		wp = path.Clean(strings.TrimSpace(wp))
		if wp == "." {
			out[comp] = files
			continue
		}
		prefix := wp + "/"
		for _, f := range files {
			if strings.HasPrefix(f.Path, prefix) {
				out[comp] = append(out[comp], File{Path: strings.TrimPrefix(f.Path, prefix), Content: f.Content})
			}
		}
	}
	return out
}

// checkScaffoldLayout fails loudly when a scaffold's files route nowhere —
// the silent version of this bug syncs zero files and leaves a sandbox that
// looks provisioned but serves nothing.
func CheckScaffoldLayout(components map[string]string, files []File) error {
	if len(files) == 0 {
		return nil
	}
	routed := RouteFiles(components, files)
	total := 0
	for _, batch := range routed {
		total += len(batch)
	}
	if total > 0 {
		return nil
	}
	dirs := map[string]bool{}
	for _, f := range files {
		if i := strings.Index(f.Path, "/"); i > 0 {
			dirs[f.Path[:i]] = true
		}
	}
	have := make([]string, 0, len(dirs))
	for d := range dirs {
		have = append(have, d)
	}
	sort.Strings(have)
	want := make([]string, 0, len(components))
	for name, wp := range components {
		want = append(want, fmt.Sprintf("%s (%s/)", name, wp))
	}
	sort.Strings(want)
	return fmt.Errorf("scaffold layout does not match the template: none of its %d files fall under any component directory. "+
		"Template components: %s. Scaffold top-level directories: %s",
		len(files), strings.Join(want, ", "), strings.Join(have, ", "))
}

// syncFilesToSandbox pushes routed files to each component's dev-agent via the
// dataplane sync verb. Returns the total file count delivered.
func (c *Client) SyncFiles(ctx context.Context, ref Ref, components map[string]string, files []File) (int, error) {
	routed := RouteFiles(components, files)
	names := make([]string, 0, len(routed))
	for name := range routed {
		names = append(names, name)
	}
	sort.Strings(names)

	type syncFilePayload struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	total := 0
	for _, comp := range names {
		batch := routed[comp]
		if len(batch) == 0 {
			continue
		}
		payloadFiles := make([]syncFilePayload, 0, len(batch))
		for _, f := range batch {
			payloadFiles = append(payloadFiles, syncFilePayload{Path: f.Path, Content: f.Content})
		}
		payload, err := json.Marshal(map[string]any{"files": payloadFiles, "restart": "auto"})
		if err != nil {
			return total, err
		}
		compRef := ref
		compRef.Component = comp
		body, status, err := c.Call(ctx, compRef, VerbSync, "POST", payload)
		if err != nil {
			return total, fmt.Errorf("component %s: %w", comp, err)
		}
		if status < 200 || status >= 300 {
			return total, fmt.Errorf("component %s sync returned %d: %s", comp, status, strings.TrimSpace(string(body)))
		}
		total += len(batch)
	}
	return total, nil
}

// syncDeleteToSandbox removes one workspace-relative path from its component.
func (c *Client) SyncDelete(ctx context.Context, ref Ref, components map[string]string, wsPath string) error {
	for comp, wp := range components {
		wp = path.Clean(strings.TrimSpace(wp))
		rel := ""
		if wp == "." {
			rel = wsPath
		} else if after, ok := strings.CutPrefix(wsPath, wp+"/"); ok {
			rel = after
		}
		if rel == "" {
			continue
		}
		payload, err := json.Marshal(map[string]any{"deletePaths": []string{rel}, "restart": "auto"})
		if err != nil {
			return err
		}
		compRef := ref
		compRef.Component = comp
		body, status, err := c.Call(ctx, compRef, VerbSync, "POST", payload)
		if err != nil {
			return err
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("component %s delete returned %d: %s", comp, status, strings.TrimSpace(string(body)))
		}
		return nil
	}
	return nil
}

// sandboxReady probes the first component's process endpoint; 200 means the
// dev-agent is up and the dataplane resolves (instance dev status published).
func (c *Client) SandboxReady(ctx context.Context, ref Ref, components map[string]string) bool {
	for comp := range components {
		compRef := ref
		compRef.Component = comp
		_, status, err := c.Call(ctx, compRef, VerbProcess, "GET", nil)
		return err == nil && status == 200
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// InputSchemas returns a template's declared instance inputs, name → its
// JSON-schema fragment. The wizard filters proposed values through these.
func InputSchemas(tmpl *unstructured.Unstructured) map[string]any {
	props, ok, _ := unstructured.NestedMap(tmpl.Object, "spec", "schema", "properties")
	if !ok {
		return nil
	}
	return props
}

// InputNames lists the declared input names with their types, for prompts.
func InputNames(tmpl *unstructured.Unstructured) []string {
	props := InputSchemas(tmpl)
	out := make([]string, 0, len(props))
	for k, v := range props {
		t := "any"
		if m, ok := v.(map[string]any); ok {
			if s, ok := m["type"].(string); ok {
				t = s
			}
		}
		out = append(out, k+" ("+t+")")
	}
	sort.Strings(out)
	return out
}

// FilterValues keeps only values the template declares AND whose type matches
// the declared one. Name-only filtering is not enough: a model will happily
// send `expose: true` for an object-typed input, and the API server rejects
// the whole instance — which a controller can only retry, forever. A nil
// schema set means "unknown", so values pass through untouched.
func FilterValues(values map[string]any, schemas map[string]any) map[string]any {
	if schemas == nil {
		return values
	}
	out := make(map[string]any, len(values))
	for k, v := range values {
		spec, ok := schemas[k]
		if !ok {
			continue
		}
		if !typeMatches(v, spec) {
			continue
		}
		out[k] = v
	}
	return out
}

// typeMatches reports whether a value fits the declared JSON-schema type.
// An undeclared type accepts anything.
func typeMatches(v, spec any) bool {
	m, ok := spec.(map[string]any)
	if !ok {
		return true
	}
	want, ok := m["type"].(string)
	if !ok || want == "" {
		return true
	}
	switch want {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "integer", "number":
		switch v.(type) {
		case float64, float32, int, int32, int64:
			return true
		}
		return false
	}
	return true
}
