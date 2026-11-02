// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// ConsumerQuotaLimits must be supplied by the operator. MaxResourceTypes is a
// lifetime group/resource budget including the builtin families below; changing
// schema names or API versions does not create a new family or reset its quota.
type ConsumerQuotaLimits struct {
	MaxResourceTypes      int
	MaxObjectsPerResource int64
}

const OrgConsumerQuotaName = "railgrid-org-consumer-storage"

var orgConsumerSchemaGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}

// These are the stored builtin resources writable by the restricted tenant
// administrator role. Events and privileged infrastructure definitions are not
// writable; ephemeral review/token subresources do not create stored objects.
var orgConsumerBuiltinResources = []schema.GroupResource{
	{Resource: "secrets"}, {Resource: "configmaps"}, {Resource: "serviceaccounts"}, {Resource: "namespaces"},
	{Group: "rbac.authorization.k8s.io", Resource: "roles"}, {Group: "rbac.authorization.k8s.io", Resource: "rolebindings"},
	{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"}, {Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings"},
	{Group: "coordination.k8s.io", Resource: "leases"}, {Group: "railgrid.ai", Resource: "mcpservers"},
}

// EnsureOrgConsumerQuota installs and observes a protected cluster-wide quota
// before a consumer binding/grant is written. Callers must supply authoritative
// exported resource families, not manifest verb lists or permission claims.
// Retired families remain capped and consume the lifetime budget. This helper
// grants nothing and does not establish byte/rate admission by itself.
func EnsureOrgConsumerQuota(ctx context.Context, client dynamic.Interface, resources []schema.GroupResource, limits ConsumerQuotaLimits) error {
	return ensureOrgConsumerQuotaWithPendingStatus(ctx, client, resources, limits, nil)
}

// ensureOrgConsumerQuotaWithPendingStatus persists every hard counter before
// access is granted, but permits status.used to remain absent for the exact
// newly proposed resource families that trusted discovery proved are not yet
// served in the consumer workspace. Native admission still rejects writes to
// those families until the quota controller observes and initializes them.
func ensureOrgConsumerQuotaWithPendingStatus(ctx context.Context, client dynamic.Interface, resources []schema.GroupResource, limits ConsumerQuotaLimits, pendingStatus map[schema.GroupResource]bool) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	hard := map[string]string{}
	for _, resource := range append(append([]schema.GroupResource(nil), orgConsumerBuiltinResources...), resources...) {
		if err := validateConsumerQuotaResource(resource); err != nil {
			return err
		}
		hard[consumerCountKey(resource)] = strconv.FormatInt(limits.MaxObjectsPerResource, 10)
	}
	if err := boundConsumerQuotaHard(hard, limits); err != nil {
		return err
	}
	pendingKeys := make(map[string]bool, len(pendingStatus))
	for resource, pending := range pendingStatus {
		if !pending {
			continue
		}
		if err := validateConsumerQuotaResource(resource); err != nil {
			return err
		}
		if isOrgConsumerBuiltinResource(resource) {
			return fmt.Errorf("builtin quota family cannot have pending status: %s", resource.String())
		}
		key := consumerCountKey(resource)
		if _, found := hard[key]; !found {
			return fmt.Errorf("pending quota family %s is not reserved", resource.String())
		}
		pendingKeys[key] = true
	}
	return ensureWorkspaceQuotaWithPendingStatus(ctx, client, OrgConsumerQuotaName, hard, &limits, pendingKeys)
}

func isOrgConsumerBuiltinResource(resource schema.GroupResource) bool {
	for _, builtin := range orgConsumerBuiltinResources {
		if resource == builtin {
			return true
		}
	}
	return false
}

// Validate rejects omitted operator limits and budgets that cannot cover builtins.
func (limits ConsumerQuotaLimits) Validate() error {
	if limits.MaxResourceTypes < len(orgConsumerBuiltinResources) || limits.MaxObjectsPerResource <= 0 {
		return fmt.Errorf("consumer quotas require explicit positive object limits and a resource-type budget of at least %d builtin families", len(orgConsumerBuiltinResources))
	}
	return nil
}

func validateConsumerQuotaResource(resource schema.GroupResource) error {
	for _, builtin := range orgConsumerBuiltinResources {
		if resource == builtin {
			return nil
		}
	}
	return ValidateOrgProviderPrivateResource(resource)
}

func consumerCountKey(resource schema.GroupResource) string {
	key := "count/" + resource.Resource
	if resource.Group != "" {
		key += "." + resource.Group
	}
	return key
}

// Called inside the latest-state quota create/update retry. Checking a stale
// preflight would let concurrent providers each consume the last available slot.
func boundConsumerQuotaHard(hard map[string]string, limits ConsumerQuotaLimits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if len(hard) > limits.MaxResourceTypes {
		return fmt.Errorf("consumer resource-type lifetime budget exceeded: %d families, limit %d", len(hard), limits.MaxResourceTypes)
	}
	for key := range hard {
		name, ok := strings.CutPrefix(key, "count/")
		if !ok {
			return fmt.Errorf("unsupported consumer quota key %q", key)
		}
		resource, group, _ := strings.Cut(name, ".")
		if err := validateConsumerQuotaResource(schema.GroupResource{Group: group, Resource: resource}); err != nil {
			return err
		}
		// Apply a changed operator limit to retained families as well. Removed
		// declarations cannot leave an older, larger allowance behind.
		hard[key] = strconv.FormatInt(limits.MaxObjectsPerResource, 10)
	}
	return nil
}

// ResolveOrgConsumerQuotaResources reads the live APIExport and all schema
// references. The pinned v1alpha2 API represents the latest schemas in
// spec.resources[].schema (the successor to v1alpha1 latestResourceSchemas).
func ResolveOrgConsumerQuotaResources(ctx context.Context, providerClient dynamic.Interface, exportName string) ([]schema.GroupResource, error) {
	export, err := providerClient.Resource(apiExportGVR).Get(ctx, exportName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	resources, found, err := unstructured.NestedSlice(export.Object, "spec", "resources")
	if err != nil || !found || len(resources) == 0 {
		return nil, fmt.Errorf("export has no supported resource declarations")
	}
	schemas := []*unstructured.Unstructured{}
	seen := map[string]bool{}
	for _, raw := range resources {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid export resource declaration")
		}
		name, _ := item["schema"].(string)
		if name == "" {
			return nil, fmt.Errorf("export resource has no schema reference")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		object, err := providerClient.Resource(orgConsumerSchemaGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("reading export schema %s: %w", name, err)
		}
		schemas = append(schemas, object)
	}
	return OrgConsumerQuotaResources(export, schemas)
}

// OrgConsumerQuotaResources extracts every ordinary stored resource, including
// kinds absent from CatalogEntry verb/action declarations. Bootstrap may call it
// on its fully admitted export/schema snapshot before publishing new schemas.
// Claims and their identity hashes are deliberately not consulted.
func OrgConsumerQuotaResources(export *unstructured.Unstructured, schemas []*unstructured.Unstructured) ([]schema.GroupResource, error) {
	if export == nil {
		return nil, fmt.Errorf("missing APIExport")
	}
	byName := map[string]*unstructured.Unstructured{}
	for _, object := range schemas {
		if object == nil || object.GetName() == "" {
			return nil, fmt.Errorf("missing APIResourceSchema identity")
		}
		if byName[object.GetName()] != nil {
			return nil, fmt.Errorf("duplicate APIResourceSchema %s", object.GetName())
		}
		byName[object.GetName()] = object
	}
	resources, found, err := unstructured.NestedSlice(export.Object, "spec", "resources")
	if err != nil || !found || len(resources) == 0 {
		return nil, fmt.Errorf("export has no supported resource declarations")
	}
	families := map[schema.GroupResource]bool{}
	for _, raw := range resources {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid export resource declaration")
		}
		group, _ := item["group"].(string)
		name, _ := item["name"].(string)
		schemaName, _ := item["schema"].(string)
		parts := strings.Split(name, "/")
		if len(parts) > 2 || len(parts) == 2 && parts[1] == "" {
			return nil, fmt.Errorf("invalid exported resource %q", name)
		}
		resource := schema.GroupResource{Group: group, Resource: parts[0]}
		if err := validateConsumerQuotaResource(resource); err != nil {
			return nil, err
		}
		object := byName[schemaName]
		if object == nil {
			return nil, fmt.Errorf("missing referenced APIResourceSchema %s", schemaName)
		}
		schemaGroup, _, _ := unstructured.NestedString(object.Object, "spec", "group")
		if schemaGroup != group {
			return nil, fmt.Errorf("export/schema group mismatch")
		}
		storage, ok := item["storage"].(map[string]any)
		if !ok || len(storage) != 1 {
			return nil, fmt.Errorf("unsupported export storage")
		}
		if len(parts) == 2 {
			if _, ok := storage["virtual"].(map[string]any); !ok {
				return nil, fmt.Errorf("unsupported custom subresource storage")
			}
			continue
		}
		if _, ok := storage["crd"].(map[string]any); !ok {
			return nil, fmt.Errorf("ordinary consumer resource must use counted CRD storage")
		}
		plural, _, _ := unstructured.NestedString(object.Object, "spec", "names", "plural")
		scope, _, _ := unstructured.NestedString(object.Object, "spec", "scope")
		if plural != name || (scope != "Cluster" && scope != "Namespaced") {
			return nil, fmt.Errorf("invalid ordinary resource schema identity or scope")
		}
		families[resource] = true
	}
	out := make([]schema.GroupResource, 0, len(families))
	for resource := range families {
		out = append(out, resource)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Resource < out[j].Resource
	})
	return out, nil
}
