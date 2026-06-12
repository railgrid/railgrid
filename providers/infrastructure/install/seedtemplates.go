/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

// Seed Templates baked into the binary. Without these the catalog is
// empty on a fresh workspace, the portal renders "No templates match
// the current filters", and tenants have nothing to APIBind against
// to demonstrate the platform end-to-end.
//
// The set lives under install/templates/*.yaml and is embedded at
// build time so init does not depend on a host kubectl + path. The
// caller (init_cmd.go) invokes SeedTemplates after CRDs +
// PlatformSchemaInAPIExport + PlatformCachedResources so the
// Template CRD exists by the time we POST.
//
// Operators who maintain their own catalog can disable seeding via
// INFRASTRUCTURE_SKIP_SEED_TEMPLATES=1 — useful for production
// clusters where the catalog is managed by GitOps and the dev seed
// would only add noise.

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

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

//go:embed templates/*.yaml
var seedTemplatesFS embed.FS

// templateGVR is what the dynamic client writes against to upsert
// Template CRs. Matches the CRD installed by install.CRDs.
var templateGVR = schema.GroupVersionResource{
	Group:    infrav1alpha1.GroupName,
	Version:  infrav1alpha1.Version,
	Resource: "templates",
}

// SeedTemplates upserts every Template YAML baked into install/templates/
// into the workspace the supplied rest.Config points at. Idempotent —
// existing Templates are patched in place, ResourceVersion preserved.
//
// Skipped when INFRASTRUCTURE_SKIP_SEED_TEMPLATES is set to any
// non-empty value. Errors are non-fatal at the call-site: a failed
// seed should not block the rest of the init chain (operators can
// hand-apply later), but we still log loudly.
func SeedTemplates(ctx context.Context, config *rest.Config) error {
	log := klog.FromContext(ctx).WithName("install.seedtemplates")

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client for seed Templates: %w", err)
	}

	entries, err := fs.ReadDir(seedTemplatesFS, "templates")
	if err != nil {
		return fmt.Errorf("read embedded templates/: %w", err)
	}

	var applied int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := fs.ReadFile(seedTemplatesFS, "templates/"+e.Name())
		if err != nil {
			return fmt.Errorf("read embedded templates/%s: %w", e.Name(), err)
		}
		if err := applyTemplate(ctx, dyn, raw); err != nil {
			return fmt.Errorf("apply %s: %w", e.Name(), err)
		}
		applied++
	}
	log.Info("seeded Templates", "count", applied)
	return nil
}

// applyTemplate CREATEs or UPDATEs a single Template from raw YAML.
// Parsed through utilyaml (handles document separators + JSON-tagged
// fields uniformly) and re-serialized through encoding/json so the
// dynamic client gets a clean map[string]any with no leftover YAML
// node metadata.
func applyTemplate(ctx context.Context, dyn dynamic.Interface, raw []byte) error {
	// utilyaml.Unmarshal happily decodes YAML into a struct, but we
	// don't have a typed scheme registered here — the dynamic client
	// path stays JSON-only. Go via map[string]any to avoid pulling in
	// the apis package's runtime.Scheme just for this upsert.
	var obj map[string]any
	if err := utilyaml.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("unmarshal YAML: %w", err)
	}
	if obj == nil {
		return fmt.Errorf("empty Template document")
	}
	name, _, _ := unstructured.NestedString(obj, "metadata", "name")
	if name == "" {
		return fmt.Errorf("Template missing metadata.name")
	}

	// Round-trip through JSON so any numeric / bool YAML scalars land
	// as the typed Go values the apiserver expects under
	// spec.schema and spec.backendConfig (both are
	// XPreserveUnknownFields, so they survive verbatim).
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshal YAML→JSON: %w", err)
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &u.Object); err != nil {
		return fmt.Errorf("unmarshal JSON→Unstructured: %w", err)
	}

	existing, err := dyn.Resource(templateGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get existing Template: %w", err)
	}
	if apierrors.IsNotFound(err) {
		_, err = dyn.Resource(templateGVR).Create(ctx, u, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create Template: %w", err)
		}
		return nil
	}

	// Preserve the apiserver-supplied ResourceVersion so the update
	// is a proper compare-and-set rather than a blind overwrite that
	// would race against the Template controller's status patches.
	u.SetResourceVersion(existing.GetResourceVersion())
	// Don't overwrite the apiserver-assigned UID or status either —
	// status lives on the existing object's tree, and our seed YAML
	// has no status section anyway.
	if status, ok, _ := unstructured.NestedMap(existing.Object, "status"); ok {
		_ = unstructured.SetNestedMap(u.Object, status, "status")
	}
	_, err = dyn.Resource(templateGVR).Update(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update Template: %w", err)
	}
	return nil
}
