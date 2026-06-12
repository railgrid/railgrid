/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

// PlatformSchemaInAPIExport: register the platform's own catalog CRD
// (templates.infrastructure.kedge.faros.sh) as a resource on the
// provider's APIExport. The Template controller (which mints
// per-template entries dynamically) deliberately does NOT do this —
// otherwise tenants who APIBind before the FIRST Template is applied
// wouldn't see Templates either.
//
// Lives in install/ rather than controller/template/ because it's a
// one-shot startup task tied to the binary's bootstrap, not a CR
// reconcile loop. Same code shape as the runtime path (mint
// APIResourceSchema, upsert APIExport.spec.resources) so a future
// refactor could collapse them; PR A keeps the duplication shallow.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// APIExportName must match the provider's CatalogEntry.spec.apiExport.name.
// Hardcoded constant — the hub catalog controller is the canonical writer.
const APIExportName = "infrastructure.providers.kedge.faros.sh"

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

// PlatformSchemaInAPIExport reads every embedded platform CRD,
// mints an APIResourceSchema for each, and appends a corresponding
// entry to APIExport.spec.resources. Idempotent on every axis: a
// content-equal schema reuses its existing name; an already-present
// resource entry is left alone.
//
// templatesIdentityHash, when non-empty, switches the
// templates.infrastructure.kedge.faros.sh entry to use storage.virtual
// (backed by the CachedResourceEndpointSlice from
// install/endpointslice.go) so tenants who APIBind see Templates as
// a read-only projection of the provider workspace. Empty falls back
// to storage.crd, matching the pre-CachedResource behavior.
//
// Called from init_cmd.go AFTER install.CRDs +
// install.PlatformCachedResources + install.PlatformCachedResourceEndpointSlices +
// install.WaitForCachedResourceIdentity. Errors mean "the binary boot
// didn't complete" — log + bubble.
func PlatformSchemaInAPIExport(ctx context.Context, config *rest.Config, templatesIdentityHash string) error {
	log := klog.FromContext(ctx).WithName("install.apiexport")
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	entries, err := fs.ReadDir(crdsFS, "crds")
	if err != nil {
		return fmt.Errorf("read embedded crds/: %w", err)
	}
	var processed int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(crdsFS, "crds/"+e.Name())
		if err != nil {
			return fmt.Errorf("read crds/%s: %w", e.Name(), err)
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := utilyaml.Unmarshal(raw, &crd); err != nil {
			return fmt.Errorf("parse crds/%s: %w", e.Name(), err)
		}
		schemaName, err := ensureAPIResourceSchema(ctx, dyn, &crd)
		if err != nil {
			return fmt.Errorf("ensure APIResourceSchema for %s: %w", crd.Name, err)
		}
		storage := storageForResource(crd.Spec.Group, crd.Spec.Names.Plural, templatesIdentityHash)
		if err := ensureAPIExportEntry(ctx, dyn, schemaName, crd.Spec.Names.Plural, crd.Spec.Group, storage); err != nil {
			return fmt.Errorf("upsert APIExport entry for %s: %w", crd.Name, err)
		}
		processed++
	}
	log.Info("platform schemas registered on APIExport",
		"count", processed,
		"apiExport", APIExportName,
		"templatesStorage", storageKindLabel(templatesIdentityHash),
	)
	// Anchor on the platform group import so the linter doesn't strip
	// the dependency the controller package will share once PR B
	// collapses the two install/controller flows.
	_ = infrav1alpha1.GroupName
	return nil
}

// storageForResource picks the storage type for the APIExport entry.
// For the platform-owned templates resource we use virtual storage
// (backed by the CachedResourceEndpointSlice) iff the caller supplied
// an identity hash; otherwise (and for every other resource) we fall
// back to CRD storage.
func storageForResource(group, plural, templatesIdentityHash string) apisv1alpha2.ResourceSchemaStorage {
	if templatesIdentityHash != "" && group == infrav1alpha1.GroupName && plural == "templates" {
		return apisv1alpha2.ResourceSchemaStorage{
			Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
				Reference: corev1.TypedLocalObjectReference{
					APIGroup: ptrTo("cache.kcp.io"),
					Kind:     "CachedResourceEndpointSlice",
					Name:     EndpointSliceTemplatesName,
				},
				IdentityHash: templatesIdentityHash,
			},
		}
	}
	return apisv1alpha2.ResourceSchemaStorage{
		CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
	}
}

func storageKindLabel(hash string) string {
	if hash == "" {
		return "crd"
	}
	return "virtual"
}

func ptrTo[T any](v T) *T { return &v }

// ensureAPIResourceSchema is a near-mirror of the Template
// controller's helper (controller/template/apiexport.go). Kept
// duplicated here so install/ can run before the controller-runtime
// manager starts; a follow-up PR may collapse them once the
// controller is the only callsite.
func ensureAPIResourceSchema(ctx context.Context, dyn dynamic.Interface, crd *apiextensionsv1.CustomResourceDefinition) (string, error) {
	prefix := schemaPrefix(crd)
	schemaObj, err := apisv1alpha1.CRDToAPIResourceSchema(crd, prefix)
	if err != nil {
		return "", fmt.Errorf("CRDToAPIResourceSchema: %w", err)
	}
	if _, err := dyn.Resource(apiResourceSchemaGVR).Get(ctx, schemaObj.Name, metav1.GetOptions{}); err == nil {
		return schemaObj.Name, nil
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get existing schema: %w", err)
	}
	obj, err := apiResourceSchemaToUnstructured(schemaObj)
	if err != nil {
		return "", fmt.Errorf("to unstructured: %w", err)
	}
	if _, err := dyn.Resource(apiResourceSchemaGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create schema: %w", err)
	}
	return schemaObj.Name, nil
}

func ensureAPIExportEntry(ctx context.Context, dyn dynamic.Interface, schemaName, resource, group string, storage apisv1alpha2.ResourceSchemaStorage) error {
	export, err := dyn.Resource(apiExportGVR).Get(ctx, APIExportName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("APIExport %q not found; ensure the CatalogEntry has reconciled", APIExportName)
		}
		return fmt.Errorf("get APIExport: %w", err)
	}
	raw, found, err := unstructured.NestedFieldNoCopy(export.Object, "spec", "resources")
	if err != nil {
		return err
	}
	var resources []apisv1alpha2.ResourceSchema
	if found && raw != nil {
		data, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &resources); err != nil {
			return err
		}
	}

	updated := upsertResource(resources, resource, group, schemaName, storage)
	if resourcesEqual(resources, updated) {
		return nil
	}
	data, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	var asAny []any
	if err := json.Unmarshal(data, &asAny); err != nil {
		return err
	}
	if err := unstructured.SetNestedField(export.Object, asAny, "spec", "resources"); err != nil {
		return err
	}
	if _, err := dyn.Resource(apiExportGVR).Update(ctx, export, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update APIExport: %w", err)
	}
	return nil
}

// upsertResource replaces (or appends) the {group, name} entry. The
// caller-supplied storage is always preferred over whatever the
// existing entry carried: that's how the templates resource flips
// from CRD storage to Virtual storage once the CachedResource is
// ready, without us having to special-case the storage type at the
// call site beyond storageForResource.
func upsertResource(in []apisv1alpha2.ResourceSchema, name, group, schemaName string, storage apisv1alpha2.ResourceSchemaStorage) []apisv1alpha2.ResourceSchema {
	out := make([]apisv1alpha2.ResourceSchema, 0, len(in)+1)
	replaced := false
	for _, r := range in {
		if r.Name == name && r.Group == group {
			out = append(out, apisv1alpha2.ResourceSchema{
				Name:    name,
				Group:   group,
				Schema:  schemaName,
				Storage: storage,
			})
			replaced = true
			continue
		}
		out = append(out, r)
	}
	if !replaced {
		out = append(out, apisv1alpha2.ResourceSchema{
			Name:    name,
			Group:   group,
			Schema:  schemaName,
			Storage: storage,
		})
	}
	return out
}

// resourcesEqual is deliberately a shallow check on the discriminating
// fields. Storage is included because the templates flip from CRD →
// Virtual is a meaningful change we must persist on the apiserver.
func resourcesEqual(a, b []apisv1alpha2.ResourceSchema) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Group != b[i].Group || a[i].Schema != b[i].Schema {
			return false
		}
		if !storageEqual(a[i].Storage, b[i].Storage) {
			return false
		}
	}
	return true
}

func storageEqual(a, b apisv1alpha2.ResourceSchemaStorage) bool {
	if (a.CRD == nil) != (b.CRD == nil) {
		return false
	}
	if (a.Virtual == nil) != (b.Virtual == nil) {
		return false
	}
	if a.Virtual != nil && b.Virtual != nil {
		if a.Virtual.IdentityHash != b.Virtual.IdentityHash {
			return false
		}
		if a.Virtual.Reference.Name != b.Virtual.Reference.Name ||
			a.Virtual.Reference.Kind != b.Virtual.Reference.Kind {
			return false
		}
	}
	return true
}

func schemaPrefix(crd *apiextensionsv1.CustomResourceDefinition) string {
	h := sha256.New()
	fmt.Fprintln(h, crd.Spec.Group, crd.Spec.Names.Kind)
	for _, v := range crd.Spec.Versions {
		fmt.Fprintln(h, v.Name)
		if v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			fmt.Fprintf(h, "%v\n", v.Schema.OpenAPIV3Schema)
		}
	}
	return "tmpl" + hex.EncodeToString(h.Sum(nil))[:8]
}

func apiResourceSchemaToUnstructured(s *apisv1alpha1.APIResourceSchema) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	out := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &out.Object); err != nil {
		return nil, err
	}
	out.SetAPIVersion(apisv1alpha1.SchemeGroupVersion.String())
	out.SetKind("APIResourceSchema")
	return out, nil
}
