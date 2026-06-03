// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ErrTemplateNotFound is returned by GetTemplate when no RGD matches
// the requested (name, version) pair. The handler maps it to 404.
var ErrTemplateNotFound = errors.New("template not found")

func (c *realClient) ListTemplates(ctx context.Context, filter TemplateFilter) ([]Template, error) {
	// Filter at list time when possible — every published RGD must
	// carry LabelExpose so the catalog never accidentally surfaces
	// platform-internal RGDs.
	selector := LabelExpose + "=true"
	if filter.Category != "" {
		selector += "," + LabelCategory + "=" + filter.Category
	}
	if filter.Cloud != "" {
		selector += "," + LabelCloud + "=" + filter.Cloud
	}
	list, err := c.dyn.Resource(RGDGroupVersionResource).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing ResourceGraphDefinitions: %w", err)
	}
	out := make([]Template, 0, len(list.Items))
	for i := range list.Items {
		t, err := rgdToTemplate(&list.Items[i])
		if err != nil {
			// Skip malformed RGDs (missing spec.schema, bad
			// SimpleSchema) rather than failing the whole list. The
			// platform admin who shipped the bad RGD will spot the
			// gap; the rest of the catalog stays usable.
			continue
		}
		out = append(out, *t)
	}
	return out, nil
}

func (c *realClient) GetTemplate(ctx context.Context, name, version string) (*Template, error) {
	// In kro, the RGD's metadata.name is unique cluster-wide. The
	// {name, version} pair maps to either a single RGD with that
	// kedge.faros.sh/template-version label, OR (for templates
	// without explicit versioning) the RGD whose metadata.name
	// matches and whose label is the empty string. List + filter
	// rather than Get-by-name so we honour the version label
	// uniformly.
	selector := LabelExpose + "=true," + LabelTemplateName + "=" + name
	if version != "" {
		selector += "," + LabelTemplateVersion + "=" + version
	}
	list, err := c.dyn.Resource(RGDGroupVersionResource).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		// Fall back to a Get by name when the slug label is missing —
		// some templates rely on the metadata.name doubling as the
		// slug (the LabelTemplateName default).
		obj, gerr := c.dyn.Resource(RGDGroupVersionResource).Get(ctx, name, metav1.GetOptions{})
		if gerr != nil {
			if apierrors.IsNotFound(gerr) {
				return nil, ErrTemplateNotFound
			}
			return nil, fmt.Errorf("getting ResourceGraphDefinition %q: %w", name, err)
		}
		return rgdToTemplate(obj)
	}
	if len(list.Items) == 0 {
		return nil, ErrTemplateNotFound
	}
	// When multiple match (admin published the same slug across
	// versions without specifying which one we wanted), pick the
	// lexicographically-greatest version — a stable-enough proxy for
	// "latest" until a richer selection lands.
	pick := &list.Items[0]
	for i := 1; i < len(list.Items); i++ {
		if list.Items[i].GetLabels()[LabelTemplateVersion] > pick.GetLabels()[LabelTemplateVersion] {
			pick = &list.Items[i]
		}
	}
	return rgdToTemplate(pick)
}

// rgdToTemplate projects a kro ResourceGraphDefinition into our portal
// Template shape. Returns an error if the RGD is missing the
// spec.schema.{group,version,kind} triple the instance GVR derives
// from — that RGD is unusable to us regardless of what else it has.
func rgdToTemplate(obj *unstructured.Unstructured) (*Template, error) {
	labels := obj.GetLabels()
	annotations := obj.GetAnnotations()

	specSchema, found, err := unstructured.NestedMap(obj.Object, "spec", "schema")
	if err != nil || !found {
		return nil, fmt.Errorf("rgd %s: missing spec.schema", obj.GetName())
	}
	kind, _ := specSchema["kind"].(string)
	group, _ := specSchema["group"].(string)
	if group == "" {
		// kro default — see api/v1alpha1/resourcegraphdefinition_types.go.
		group = "kro.run"
	}
	version, _ := specSchema["apiVersion"].(string)
	if version == "" {
		version = "v1alpha1"
	}
	if kind == "" {
		return nil, fmt.Errorf("rgd %s: missing spec.schema.kind", obj.GetName())
	}

	// SimpleSchema → JSON-schema. The schema body is one of:
	//   spec.schema.spec     (kro convention — instance.spec fields)
	// or the older flat layout where each field is a direct child of
	// spec.schema. ConvertSimpleSchema handles both.
	inputs, sample := convertSimpleSchemaBlock(specSchema)

	name := labels[LabelTemplateName]
	if name == "" {
		name = obj.GetName()
	}
	displayName := annotations[AnnotationDisplayName]
	if displayName == "" {
		displayName = name
	}
	t := &Template{
		Name:         name,
		DisplayName:  displayName,
		Description:  annotations[AnnotationDescription],
		Category:     labels[LabelCategory],
		Cloud:        labels[LabelCloud],
		Version:      labels[LabelTemplateVersion],
		IconURL:      annotations[AnnotationIconURL],
		InstanceKind: kind,
		InstanceGVR: schema.GroupVersionResource{
			Group:    group,
			Version:  version,
			Resource: strings.ToLower(kind) + "s",
		},
		InputsSchema: inputs,
		SampleValues: sample,
	}
	if raw := annotations[AnnotationSampleValues]; raw != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			t.SampleValues = parsed
		}
	}
	return t, nil
}
