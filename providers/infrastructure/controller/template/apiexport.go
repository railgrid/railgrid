/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package template

// APIExport schema projection. After the per-template CRD is in
// the apiserver, we have two more steps before tenants who APIBind
// can see it:
//
//   1. Mint an APIResourceSchema (immutable, frozen) for the CRD.
//      apis.kcp.io ships CRDToAPIResourceSchema for this — its only
//      requirement is a unique name prefix per CRD content, so a
//      schema change always produces a new APIResourceSchema name.
//      Using a content hash makes it deterministic across pod
//      restarts and idempotent across reconciles.
//
//   2. Add an entry to APIExport.spec.resources pointing at that
//      APIResourceSchema name. Tenants who already APIBind don't
//      pick up the new schema (kcp design: existing bindings see
//      the schema they bound to), but new bindings + the
//      APIExport's virtual workspace will project the kind. Removing
//      the entry on Template delete reverses this.
//
// The APIExport name is well-known: it matches the provider's
// CatalogEntry.spec.apiExport.name. PR A hardcodes the constant.
// PR B / future hardening can move it to config if needed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infrav1alpha1 "github.com/faroshq/faros-kedge/providers/infrastructure/apis/v1alpha1"
)

// APIExportName is the well-known name of the provider's APIExport,
// matching CatalogEntry.spec.apiExport.name in manifest.yaml. The
// catalog controller in the hub creates this APIExport in the
// provider's workspace; the Template controller appends per-template
// resources to its spec.
const APIExportName = "infrastructure.providers.kedge.faros.sh"

// apiExportGVR + apiResourceSchemaGVR are what the Reconciler's
// dynamic client targets when reading + writing the kcp objects.
// Using the dynamic client (rather than the typed kcp clientset)
// keeps the controller's import surface tight; the cost is hand-
// rolled marshalling, which is acceptable here because the touched
// fields are well-known.
var (
	apiExportGVR = schema.GroupVersionResource{
		Group:    apisv1alpha2.SchemeGroupVersion.Group,
		Version:  apisv1alpha2.SchemeGroupVersion.Version,
		Resource: "apiexports",
	}
	apiResourceSchemaGVR = schema.GroupVersionResource{
		Group:    apisv1alpha1.SchemeGroupVersion.Group,
		Version:  apisv1alpha1.SchemeGroupVersion.Version,
		Resource: "apiresourceschemas",
	}
)

// ensureAPIResourceSchema applies the immutable kcp schema derived
// from the per-template CRD. Idempotent in the only sense that
// matters: identical CRD content → identical APIResourceSchema name
// (the SHA-256 prefix makes that deterministic). When the schema
// changes, a NEW APIResourceSchema name is produced; the previous
// one stays in the cluster as a frozen historical artifact (kcp's
// design — existing APIBindings keep working).
//
// Returns the APIResourceSchema name so the caller can record it on
// the APIExport.
func (r *Reconciler) ensureAPIResourceSchema(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) (string, error) {
	prefix := schemaPrefix(crd)

	schemaObj, err := apisv1alpha1.CRDToAPIResourceSchema(crd, prefix)
	if err != nil {
		return "", fmt.Errorf("CRDToAPIResourceSchema: %w", err)
	}

	obj, err := apiResourceSchemaToUnstructured(schemaObj)
	if err != nil {
		return "", fmt.Errorf("to unstructured: %w", err)
	}

	// APIResourceSchemas are immutable; a Get→Create-if-NotFound is
	// the complete contract. The hash-derived name guarantees
	// content-equivalence even across pod restarts.
	_, err = r.Dynamic.Resource(apiResourceSchemaGVR).Get(ctx, schemaObj.Name, metav1.GetOptions{})
	if err == nil {
		return schemaObj.Name, nil
	}
	if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get existing schema: %w", err)
	}
	_, err = r.Dynamic.Resource(apiResourceSchemaGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create schema: %w", err)
	}
	return schemaObj.Name, nil
}

// ensureAPIExportEntry adds (or updates) the APIExport.spec.resources
// entry for the per-template kind. Idempotent. Returns the APIExport's
// name on success — useful for the caller's status condition.
//
// Pulls + patches the APIExport in a Get-Modify-Update loop. Since
// the entry is keyed by (name, group), competing reconciles for
// different Templates can race here without corrupting each other.
func (r *Reconciler) ensureAPIExportEntry(ctx context.Context, schemaName, resource, group string) error {
	export, err := r.Dynamic.Resource(apiExportGVR).Get(ctx, APIExportName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("APIExport %q not found; ensure the CatalogEntry has reconciled", APIExportName)
		}
		return fmt.Errorf("get APIExport: %w", err)
	}

	resources, err := getAPIExportResources(export)
	if err != nil {
		return fmt.Errorf("read spec.resources: %w", err)
	}

	updated := upsertResource(resources, resource, group, schemaName)
	if resourcesEqual(resources, updated) {
		return nil // No-op; spec already has the desired entry.
	}

	if err := setAPIExportResources(export, updated); err != nil {
		return fmt.Errorf("set spec.resources: %w", err)
	}
	_, err = r.Dynamic.Resource(apiExportGVR).Update(ctx, export, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update APIExport: %w", err)
	}
	return nil
}

// removeAPIExportEntry strips the entry for (resource, group) from
// APIExport.spec.resources. Used on Template delete. Idempotent; an
// already-absent entry is success. Existing APIBindings keep working
// (their schema reference is frozen) so this is safe to call without
// coordination.
func (r *Reconciler) removeAPIExportEntry(ctx context.Context, resource, group string) error {
	export, err := r.Dynamic.Resource(apiExportGVR).Get(ctx, APIExportName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get APIExport: %w", err)
	}
	resources, err := getAPIExportResources(export)
	if err != nil {
		return fmt.Errorf("read spec.resources: %w", err)
	}
	updated := removeResource(resources, resource, group)
	if resourcesEqual(resources, updated) {
		return nil
	}
	if err := setAPIExportResources(export, updated); err != nil {
		return fmt.Errorf("set spec.resources: %w", err)
	}
	_, err = r.Dynamic.Resource(apiExportGVR).Update(ctx, export, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update APIExport: %w", err)
	}
	return nil
}

// schemaPrefix computes the deterministic-per-content prefix the
// APIResourceSchema name takes. Format: "tmpl<8-hex>". The hash
// covers the CRD's group, kind, version, and OpenAPIV3Schema so any
// meaningful change produces a new prefix; cosmetic metadata
// changes (annotations, labels) are intentionally excluded.
func schemaPrefix(crd *apiextensionsv1.CustomResourceDefinition) string {
	h := sha256.New()
	_, _ = fmt.Fprintln(h, crd.Spec.Group, crd.Spec.Names.Kind)
	for _, v := range crd.Spec.Versions {
		_, _ = fmt.Fprintln(h, v.Name)
		if v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			// Hash a deterministic JSON serialization. JSON sorts object
			// keys and renders pointer fields (e.g.
			// x-kubernetes-preserve-unknown-fields, a *bool) by value, so
			// the digest is stable across processes — unlike fmt %v, which
			// prints pointer addresses for the schema's *bool fields and
			// would make the prefix (and thus the APIResourceSchema name)
			// non-deterministic.
			data, err := crdsJSONMarshal(v.Schema.OpenAPIV3Schema)
			if err != nil {
				_, _ = fmt.Fprintf(h, "%v\n", v.Schema.OpenAPIV3Schema)
				continue
			}
			_, _ = h.Write(data)
		}
	}
	digest := hex.EncodeToString(h.Sum(nil))
	return "tmpl" + digest[:8]
}

// apiResourceSchemaToUnstructured marshalls a typed APIResourceSchema
// into the map shape the dynamic client expects. Uses encoding/json
// (the type has JSON tags) so the apiserver receives the same shape
// the typed clientset would have sent.
func apiResourceSchemaToUnstructured(s *apisv1alpha1.APIResourceSchema) (*unstructured.Unstructured, error) {
	data, err := jsonMarshal(s)
	if err != nil {
		return nil, err
	}
	out := &unstructured.Unstructured{}
	if err := jsonUnmarshal(data, &out.Object); err != nil {
		return nil, err
	}
	out.SetAPIVersion(apisv1alpha1.SchemeGroupVersion.String())
	out.SetKind("APIResourceSchema")
	return out, nil
}

// getAPIExportResources decodes spec.resources from the unstructured
// APIExport into the typed slice. Returns an empty slice when the
// field is absent.
func getAPIExportResources(export *unstructured.Unstructured) ([]apisv1alpha2.ResourceSchema, error) {
	raw, found, err := unstructuredNested(export.Object, "spec", "resources")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	data, err := jsonMarshal(raw)
	if err != nil {
		return nil, err
	}
	var out []apisv1alpha2.ResourceSchema
	if err := jsonUnmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// setAPIExportResources writes spec.resources back to the unstructured
// APIExport. Allocates a brand-new slice so the caller can free the
// previous one to GC.
func setAPIExportResources(export *unstructured.Unstructured, resources []apisv1alpha2.ResourceSchema) error {
	data, err := jsonMarshal(resources)
	if err != nil {
		return err
	}
	var asAny []any
	if err := jsonUnmarshal(data, &asAny); err != nil {
		return err
	}
	return setNestedField(export.Object, asAny, "spec", "resources")
}

// upsertResource adds or replaces the (name, group) entry. Returns a
// fresh slice so the caller can compare against the input via
// resourcesEqual.
func upsertResource(in []apisv1alpha2.ResourceSchema, name, group, schemaName string) []apisv1alpha2.ResourceSchema {
	out := make([]apisv1alpha2.ResourceSchema, 0, len(in)+1)
	replaced := false
	for _, r := range in {
		if r.Name == name && r.Group == group {
			out = append(out, apisv1alpha2.ResourceSchema{
				Name:    name,
				Group:   group,
				Schema:  schemaName,
				Storage: r.Storage,
			})
			replaced = true
			continue
		}
		out = append(out, r)
	}
	if !replaced {
		out = append(out, apisv1alpha2.ResourceSchema{
			Name:   name,
			Group:  group,
			Schema: schemaName,
			Storage: apisv1alpha2.ResourceSchemaStorage{
				CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
			},
		})
	}
	return out
}

// removeResource strips the (name, group) entry. Returns the same
// slice unchanged if no entry matched, so resourcesEqual can detect
// the no-op.
func removeResource(in []apisv1alpha2.ResourceSchema, name, group string) []apisv1alpha2.ResourceSchema {
	out := make([]apisv1alpha2.ResourceSchema, 0, len(in))
	for _, r := range in {
		if r.Name == name && r.Group == group {
			continue
		}
		out = append(out, r)
	}
	if len(out) == len(in) {
		return in
	}
	return out
}

// resourcesEqual is a tiny field-by-field compare so we don't write
// the APIExport when nothing changed.
func resourcesEqual(a, b []apisv1alpha2.ResourceSchema) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			a[i].Group != b[i].Group ||
			a[i].Schema != b[i].Schema {
			return false
		}
	}
	return true
}

// Helpers below: small wrappers around json + unstructured nested
// accessors. Kept tiny so the package's behavior stays auditable in
// one file.

func jsonMarshal(v any) ([]byte, error)   { return jsonMarshalImpl(v) }
func jsonUnmarshal(b []byte, v any) error { return jsonUnmarshalImpl(b, v) }
func unstructuredNested(obj map[string]any, fields ...string) (any, bool, error) {
	return unstructured.NestedFieldNoCopy(obj, fields...)
}
func setNestedField(obj map[string]any, v any, fields ...string) error {
	return unstructured.SetNestedField(obj, v, fields...)
}

// Avoid name collision with the controller.go json import alias by
// indirecting through these wrappers. PR A keeps the package's
// import surface single-file-readable.
var (
	jsonMarshalImpl   = jsonMarshalRef
	jsonUnmarshalImpl = jsonUnmarshalRef
)

// Forward-decl-style anchors; the actual implementations live in
// crds_json.go so the controller.go + apiexport.go split stays clean.
func jsonMarshalRef(v any) ([]byte, error)   { return crdsJSONMarshal(v) }
func jsonUnmarshalRef(b []byte, v any) error { return crdsJSONUnmarshal(b, v) }

// _ keeps the infra package's reference live in the import section
// even when ensureAPIResourceSchema is the only consumer; PR B's
// CachedResource controller will share these helpers.
var _ = infrav1alpha1.GroupName
