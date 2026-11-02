// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/apiurl"
)

// ValidateProviderStorage audits the provider's current logical objects before
// a new provider identity is issued or rotated. It requires the provider-owned
// quota and initialized status, then lists every counted family even when
// status.used reports zero; stale usage cannot certify an empty inventory.
// Physical etcd history and hidden legacy identity bytes are outside this
// logical-object inventory.
func (p *OrgConsumerPolicy) ValidateProviderStorage(ctx context.Context, workspacePath string) error {
	if p == nil || p.clientFor == nil || p.discoveryFor == nil || p.maxObjectBytes <= 0 || workspacePath == "" {
		return fmt.Errorf("provider storage audit is not configured")
	}
	client, err := p.clientFor(workspacePath)
	if err != nil {
		return err
	}
	quota, err := client.Resource(orgQuotaGVR).Namespace("admin").Get(ctx, orgQuotaName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("provider storage quota is missing")
	}
	if err != nil {
		return fmt.Errorf("reading provider storage quota: %w", err)
	}
	if quota.GetName() != orgQuotaName || quota.GetNamespace() != "admin" || quota.GetAnnotations()["experimental.quota.kcp.io/cluster-scoped"] != "true" {
		return fmt.Errorf("provider storage quota identity is not protected")
	}
	if scopes, found, err := unstructured.NestedSlice(quota.Object, "spec", "scopes"); err != nil || found && len(scopes) != 0 {
		return fmt.Errorf("provider storage quota is scoped")
	}
	if selector, found, err := unstructured.NestedMap(quota.Object, "spec", "scopeSelector"); err != nil || found && len(selector) != 0 {
		return fmt.Errorf("provider storage quota has a scope selector")
	}
	hard, found, err := unstructured.NestedStringMap(quota.Object, "spec", "hard")
	if err != nil || !found || hard == nil {
		return fmt.Errorf("provider storage quota hard limits are unavailable")
	}
	used, found, err := unstructured.NestedStringMap(quota.Object, "status", "used")
	if err != nil || !found || used == nil {
		return fmt.Errorf("provider storage quota usage is uninitialized")
	}
	statusHard, found, err := unstructured.NestedStringMap(quota.Object, "status", "hard")
	if err != nil || !found || statusHard == nil {
		return fmt.Errorf("provider storage quota hard status is uninitialized")
	}
	for key := range hard {
		if !strings.HasPrefix(key, "count/") {
			continue
		}
		if _, ok := used[key]; !ok {
			return fmt.Errorf("provider storage quota usage is uninitialized for %s", key)
		}
		if statusHard[key] != hard[key] {
			return fmt.Errorf("provider storage quota hard status is not initialized for %s", key)
		}
		limit, err := parseQuotaCount(hard[key])
		if err != nil || limit <= 0 {
			return fmt.Errorf("invalid provider quota hard limit for %s", key)
		}
		usage, err := parseQuotaCount(used[key])
		if err != nil || usage > limit {
			return fmt.Errorf("invalid provider quota usage for %s", key)
		}
	}
	limits := map[schema.GroupResource]int64{}
	for key, limitRaw := range hard {
		if !strings.HasPrefix(key, "count/") {
			continue
		}
		if statusHard[key] != limitRaw {
			return fmt.Errorf("provider quota hard status is unavailable for %s", key)
		}
		rawUsage, ok := used[key]
		if !ok {
			return fmt.Errorf("provider storage quota usage is uninitialized for %s", key)
		}
		usage, err := parseQuotaCount(rawUsage)
		if err != nil {
			return fmt.Errorf("invalid provider quota usage for %s: %w", key, err)
		}
		limit, err := parseQuotaCount(limitRaw)
		if err != nil || limit <= 0 {
			return fmt.Errorf("invalid provider quota hard limit for %s", key)
		}
		if usage > limit {
			return fmt.Errorf("provider quota usage exceeds hard limit for %s", key)
		}
		resource, err := quotaGroupResource(key)
		if err != nil {
			return err
		}
		limits[resource] = limit
	}
	for key := range used {
		if strings.HasPrefix(key, "count/") {
			if _, ok := hard[key]; !ok {
				return fmt.Errorf("provider quota usage %s has no hard limit", key)
			}
		}
	}
	return p.validateObjectInventoryWithLimits(ctx, workspacePath, client, limits)
}

// priorConsumerQuotaResources returns every family already recorded by the
// consumer quota. Those keys are lifetime reservations, including retired
// families, and must remain auditable even when no APIBinding currently names
// them. A missing quota is valid on first Enable; a malformed existing quota
// fails closed rather than being treated as an empty history.
func (p *OrgConsumerPolicy) priorConsumerQuotaResources(ctx context.Context, client dynamic.Interface) ([]schema.GroupResource, error) {
	quota, err := client.Resource(orgQuotaGVR).Namespace("admin").Get(ctx, OrgConsumerQuotaName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading consumer storage quota: %w", err)
	}
	if quota.GetName() != OrgConsumerQuotaName || quota.GetNamespace() != "admin" || quota.GetAnnotations()["experimental.quota.kcp.io/cluster-scoped"] != "true" {
		return nil, fmt.Errorf("consumer storage quota identity is not protected")
	}
	if scopes, found, err := unstructured.NestedSlice(quota.Object, "spec", "scopes"); err != nil || found && len(scopes) != 0 {
		return nil, fmt.Errorf("consumer storage quota is scoped")
	}
	if selector, found, err := unstructured.NestedMap(quota.Object, "spec", "scopeSelector"); err != nil || found && len(selector) != 0 {
		return nil, fmt.Errorf("consumer storage quota has a scope selector")
	}
	hard, found, err := unstructured.NestedStringMap(quota.Object, "spec", "hard")
	if err != nil || !found || hard == nil {
		return nil, fmt.Errorf("consumer storage quota hard limits are unavailable")
	}
	if len(hard) > p.config.Quota.MaxResourceTypes {
		return nil, fmt.Errorf("consumer storage quota exceeds configured resource-family budget")
	}
	statusHard, _, err := unstructured.NestedStringMap(quota.Object, "status", "hard")
	if err != nil {
		return nil, fmt.Errorf("consumer storage quota hard status is malformed")
	}
	statusUsed, _, err := unstructured.NestedStringMap(quota.Object, "status", "used")
	if err != nil {
		return nil, fmt.Errorf("consumer storage quota usage status is malformed")
	}
	resources := make([]schema.GroupResource, 0, len(hard))
	seen := make(map[schema.GroupResource]bool, len(hard))
	for key, raw := range hard {
		if !strings.HasPrefix(key, "count/") {
			return nil, fmt.Errorf("unsupported consumer storage quota key %q", key)
		}
		resource, err := quotaGroupResource(key)
		if err != nil {
			return nil, err
		}
		if err := validateConsumerQuotaResource(resource); err != nil {
			return nil, err
		}
		count, err := parseQuotaCount(raw)
		if err != nil || count <= 0 {
			return nil, fmt.Errorf("invalid consumer storage quota hard limit for %s", key)
		}
		seen[resource] = true
		resources = append(resources, resource)
	}
	for _, builtin := range orgConsumerBuiltinResources {
		if !seen[builtin] {
			return nil, fmt.Errorf("consumer storage quota is missing builtin family %s", builtin.String())
		}
	}
	for field, counters := range map[string]map[string]string{"status.hard": statusHard, "status.used": statusUsed} {
		for key, raw := range counters {
			if !strings.HasPrefix(key, "count/") {
				return nil, fmt.Errorf("unsupported consumer storage quota %s key %q", field, key)
			}
			resource, err := quotaGroupResource(key)
			if err != nil {
				return nil, err
			}
			if err := validateConsumerQuotaResource(resource); err != nil {
				return nil, err
			}
			if _, exists := hard[key]; !exists {
				return nil, fmt.Errorf("consumer storage quota %s %s has no hard limit", field, key)
			}
			count, err := parseQuotaCount(raw)
			if err != nil || field == "status.hard" && count <= 0 {
				return nil, fmt.Errorf("invalid consumer storage quota %s value for %s", field, key)
			}
		}
	}
	return resources, nil
}

// validateObjectInventory resolves each resource through trusted discovery and
// reads at most hard+1 objects in one response. A continuation token means the
// server has more than the permitted inventory bound, so the audit fails
// closed rather than buffering an unbounded list in memory.
func (p *OrgConsumerPolicy) validateObjectInventory(ctx context.Context, path string, client dynamic.Interface, resources []schema.GroupResource, hardCount int64) error {
	return p.validateObjectInventoryWithLimits(ctx, path, client, consumerResourceLimits(resources, hardCount))
}

func consumerResourceLimits(resources []schema.GroupResource, hardCount int64) map[schema.GroupResource]int64 {
	limits := make(map[schema.GroupResource]int64, len(resources))
	for _, resource := range resources {
		limits[resource] = hardCount
	}
	return limits
}

func (p *OrgConsumerPolicy) validateObjectInventoryWithLimits(ctx context.Context, path string, client dynamic.Interface, limits map[schema.GroupResource]int64) error {
	_, err := p.validateObjectInventoryWithOptionalMissing(ctx, path, client, limits, nil)
	return err
}

// validateObjectInventoryWithOptionalMissing permits only explicitly proposed,
// never-before-accounted families to be absent from consumer discovery. Every
// listed resource must still be fully inventoried, and all discovery/listing
// failures remain fatal. Callers must exclude currently bound and previously
// quota-governed resources from optionalMissing.
func (p *OrgConsumerPolicy) validateObjectInventoryWithOptionalMissing(ctx context.Context, path string, client dynamic.Interface, limits map[schema.GroupResource]int64, optionalMissing map[schema.GroupResource]bool) (map[schema.GroupResource]bool, error) {
	if p.maxObjectBytes <= 0 || p.discoveryFor == nil {
		return nil, fmt.Errorf("object inventory audit is not configured")
	}
	discoveryClient, err := p.discoveryFor(path)
	if err != nil {
		return nil, fmt.Errorf("creating trusted discovery client: %w", err)
	}
	preferred, err := discoveryClient.ServerPreferredResources()
	if err != nil {
		return nil, fmt.Errorf("reading preferred API resources: %w", err)
	}
	served := map[schema.GroupResource]schema.GroupVersionResource{}
	for _, list := range preferred {
		if list == nil || list.GroupVersion == "" {
			return nil, fmt.Errorf("preferred API discovery has a missing groupVersion")
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			return nil, fmt.Errorf("invalid discovery groupVersion %q: %w", list.GroupVersion, err)
		}
		for _, resource := range list.APIResources {
			if strings.Contains(resource.Name, "/") || resource.Name == "" {
				continue
			}
			key := schema.GroupResource{Group: gv.Group, Resource: resource.Name}
			served[key] = gv.WithResource(resource.Name)
		}
	}
	absent := map[schema.GroupResource]bool{}
	for resource, hardCount := range limits {
		if hardCount <= 0 || hardCount == int64(^uint64(0)>>1) {
			return nil, fmt.Errorf("invalid inventory hard count for %s", resource.String())
		}
		gvr, ok := served[resource]
		if !ok {
			if optionalMissing[resource] {
				absent[resource] = true
				continue
			}
			return nil, fmt.Errorf("resource %s is absent from preferred API discovery", resource.String())
		}
		listLimit := hardCount + 1
		var continuation string
		var total, pages int64
		for {
			remaining := listLimit - total
			if remaining <= 0 {
				return nil, fmt.Errorf("inventory for %s exceeds hard count %d", resource.String(), hardCount)
			}
			options := metav1.ListOptions{Limit: remaining, Continue: continuation}
			listClient := client
			if p.auditClientFor != nil {
				maxResponseBytes, err := inventoryResponseBudget(remaining, p.maxObjectBytes)
				if err != nil {
					return nil, err
				}
				listClient, err = p.auditClientFor(path, maxResponseBytes)
				if err != nil {
					return nil, fmt.Errorf("creating bounded inventory client: %w", err)
				}
			}
			list, err := listClient.Resource(gvr).List(ctx, options)
			if err != nil {
				return nil, fmt.Errorf("listing %s: %w", resource.String(), err)
			}
			pages++
			total += int64(len(list.Items))
			if total > hardCount {
				return nil, fmt.Errorf("inventory for %s exceeds hard count %d", resource.String(), hardCount)
			}
			for _, object := range list.Items {
				if err := validateObjectBytes(&object, p.maxObjectBytes); err != nil {
					return nil, fmt.Errorf("object %s/%s in %s: %w", object.GetNamespace(), object.GetName(), resource.String(), err)
				}
			}
			continuation = list.GetContinue()
			if continuation == "" {
				break
			}
			if pages >= listLimit {
				return nil, fmt.Errorf("inventory pagination for %s exceeds hard count %d", resource.String(), hardCount)
			}
		}
	}
	return absent, nil
}

const inventoryResponseOverhead int64 = 1 << 20

func inventoryResponseBudget(listLimit, maxObjectBytes int64) (int64, error) {
	if listLimit <= 0 || maxObjectBytes <= 0 || maxObjectBytes > int64(^uint64(0)>>1)-inventoryResponseOverhead {
		return 0, fmt.Errorf("invalid inventory response budget")
	}
	perObject := maxObjectBytes + inventoryResponseOverhead/16
	if listLimit > (int64(^uint64(0)>>1)-inventoryResponseOverhead)/perObject {
		return 0, fmt.Errorf("inventory response budget overflows int64")
	}
	return listLimit*perObject + inventoryResponseOverhead, nil
}

func boundedInventoryClientFor(config *rest.Config, path string, maxResponseBytes int64) (dynamic.Interface, error) {
	if config == nil || config.Host == "" || path == "" || maxResponseBytes <= 0 {
		return nil, fmt.Errorf("invalid bounded inventory client configuration")
	}
	cfg := rest.CopyConfig(config)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, path)
	prior := cfg.WrapTransport
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		if prior != nil {
			rt = prior(rt)
		}
		return limitedResponseTransport{next: rt, maxBytes: maxResponseBytes}
	}
	return dynamic.NewForConfig(cfg)
}

type limitedResponseTransport struct {
	next     http.RoundTripper
	maxBytes int64
}

func (t limitedResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	if t.maxBytes <= 0 {
		_ = response.Body.Close()
		return nil, fmt.Errorf("invalid bounded inventory response size")
	}
	if response.ContentLength > t.maxBytes {
		_ = response.Body.Close()
		return nil, fmt.Errorf("inventory response exceeds %d bytes", t.maxBytes)
	}
	response.Body = &limitedResponseBody{ReadCloser: response.Body, remaining: t.maxBytes}
	return response, nil
}

type limitedResponseBody struct {
	io.ReadCloser
	remaining int64
}

func (b *limitedResponseBody) Read(buffer []byte) (int, error) {
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.ReadCloser.Read(extra[:])
		if n > 0 {
			return 0, fmt.Errorf("inventory response exceeds configured byte bound")
		}
		return 0, err
	}
	if int64(len(buffer)) > b.remaining {
		buffer = buffer[:b.remaining]
	}
	n, err := b.ReadCloser.Read(buffer)
	b.remaining -= int64(n)
	return n, err
}

func validateObjectBytes(object *unstructured.Unstructured, maximum int64) error {
	encoded, err := json.Marshal(object.Object)
	if err != nil {
		return fmt.Errorf("serializing object: %w", err)
	}
	if int64(len(encoded)) > maximum {
		return fmt.Errorf("serialized object is %d bytes; maximum is %d", len(encoded), maximum)
	}
	return nil
}

func parseQuotaCount(raw string) (int64, error) {
	quantity, err := apiresource.ParseQuantity(raw)
	if err != nil {
		return 0, err
	}
	count, exact := quantity.AsInt64()
	if !exact || count < 0 {
		return 0, fmt.Errorf("expected non-negative integer count")
	}
	return count, nil
}

func quotaGroupResource(key string) (schema.GroupResource, error) {
	name, ok := strings.CutPrefix(key, "count/")
	if !ok || name == "" {
		return schema.GroupResource{}, fmt.Errorf("unsupported provider quota key %q", key)
	}
	resource, group, hasGroup := strings.Cut(name, ".")
	if resource == "" || hasGroup && group == "" {
		return schema.GroupResource{}, fmt.Errorf("invalid provider quota key %q", key)
	}
	// Kubernetes resource plurals do not contain dots; preserve all group
	// segments after the first separator for compatibility with qualified groups.
	if hasGroup {
		group = strings.TrimPrefix(name, resource+".")
	}
	return schema.GroupResource{Group: group, Resource: resource}, nil
}
