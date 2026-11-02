// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"

	sdkinstall "github.com/railgrid/provider-sdk/install"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/kcp/byolimits"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// OrgConsumerPolicyConfig limits quota-governed live allocations per org.
// MaxLiveBytes is a conservative object-count times admitted-object-size bound,
// NOT an etcd capacity guarantee: structural native objects, encoding overhead,
// MVCC revisions, tombstones and compaction/backup capacity need separate reserve.
// MaxOrganizations bounds concurrently tracked lifecycle locks in this process.
type OrgConsumerPolicyConfig struct {
	Quota            ConsumerQuotaLimits
	MaxConsumers     int64
	MaxLiveBytes     int64
	MaxOrganizations int
}

func checkedProduct(a, b int64) (int64, error) {
	if a < 0 || b < 0 || b != 0 && a > math.MaxInt64/b {
		return 0, fmt.Errorf("consumer allocation bound overflows int64")
	}
	return a * b, nil
}
func checkedSum(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, fmt.Errorf("consumer allocation bound overflows int64")
	}
	return a + b, nil
}

// Validate checks all operator inputs and derives the bound from the same hard
// counters used in provider provisioning, rather than duplicating their totals.
func (c OrgConsumerPolicyConfig) Validate(maxObjectBytes int64) error {
	if err := c.Quota.Validate(); err != nil {
		return err
	}
	if c.MaxConsumers <= 0 || c.MaxLiveBytes <= 0 || c.MaxOrganizations <= 0 || maxObjectBytes <= 0 {
		return fmt.Errorf("organization consumer policy requires explicit positive consumer, byte, organization and object-size limits")
	}
	providerObjects := int64(0)
	for key, value := range orgProviderBaseQuota() {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid provider quota counter %s", key)
		}
		providerObjects, err = checkedSum(providerObjects, n)
		if err != nil {
			return err
		}
	}
	privateObjects, err := checkedProduct(OrgProviderPrivateResourceLimit, orgProviderPrivateObjectLimit)
	if err != nil {
		return err
	}
	providerObjects, err = checkedSum(providerObjects, privateObjects)
	if err != nil {
		return err
	}
	providerObjects, err = checkedProduct(providerObjects, OrgProviderWorkspaceLimit)
	if err != nil {
		return err
	}
	consumerObjects, err := checkedProduct(int64(c.Quota.MaxResourceTypes), c.Quota.MaxObjectsPerResource)
	if err != nil {
		return err
	}
	consumerObjects, err = checkedProduct(consumerObjects, c.MaxConsumers)
	if err != nil {
		return err
	}
	total, err := checkedSum(providerObjects, consumerObjects)
	if err != nil {
		return err
	}
	total, err = checkedSum(total, c.MaxConsumers)
	if err != nil {
		return err
	} // persistent allocation Leases
	bytes, err := checkedProduct(total, maxObjectBytes)
	if err != nil {
		return err
	}
	if bytes > c.MaxLiveBytes {
		return fmt.Errorf("quota-governed live allocation bound %d bytes exceeds configured per-organization allocation %d bytes (physical etcd overhead excluded)", bytes, c.MaxLiveBytes)
	}
	return nil
}

var ErrOrgProviderInUse = errors.New("organization provider still has referencing APIBindings")
var orgConsumerBindingGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}

const orgPolicyInventoryPageSize int64 = 128

// OrgConsumerPolicy uses hub-admin clients only. Callers must hold Lock for the
// org across their entire Register/Enable/Bootstrap/Delete operation, including
// the writes after these preflight methods. Methods do not acquire nested locks.
// This lock contract supports one embedded hub process, not multiple replicas.
type OrgConsumerPolicy struct {
	config              OrgConsumerPolicyConfig
	registry            *Registry
	clientFor           func(string) (dynamic.Interface, error)
	discoveryFor        func(string) (discovery.DiscoveryInterface, error)
	maxObjectBytes      int64
	auditClientFor      func(string, int64) (dynamic.Interface, error)
	consumerMarkerReady func(context.Context, string) error
	mu                  sync.Mutex
	locks               map[string]bool
}

// SetConsumerMarkerReadiness installs the native admission guard's cached
// marker observer. Consumer preparation is disabled until this integration is
// wired so the marker cannot race ahead of the guard's informer cache.
func (p *OrgConsumerPolicy) SetConsumerMarkerReadiness(wait func(context.Context, string) error) {
	p.consumerMarkerReady = wait
}

func (p *OrgConsumerPolicy) waitForConsumerMarker(ctx context.Context, cluster string) error {
	if p.consumerMarkerReady == nil {
		return fmt.Errorf("consumer quota marker readiness is unavailable")
	}
	if err := p.consumerMarkerReady(ctx, cluster); err != nil {
		return fmt.Errorf("waiting for consumer quota marker readiness: %w", err)
	}
	return nil
}

// markConsumerQuotaRequired persists the native-quota coverage contract on
// the authoritative LogicalCluster. The byolimits admission guard reads this
// protected annotation from kcp's informer; request headers cannot set it.
func (p *OrgConsumerPolicy) markConsumerQuotaRequired(ctx context.Context, client dynamic.Interface) error {
	lc, err := client.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading consumer LogicalCluster: %w", err)
	}
	annotations := lc.GetAnnotations()
	if annotations[byolimits.ConsumerQuotaRequiredAnnotation] == byolimits.ConsumerQuotaRequiredValue {
		return nil
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[byolimits.ConsumerQuotaRequiredAnnotation] = byolimits.ConsumerQuotaRequiredValue
	lc.SetAnnotations(annotations)
	if _, err := client.Resource(logicalClusterGVR).Update(ctx, lc, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("marking consumer LogicalCluster for quota enforcement: %w", err)
	}
	return nil
}

func NewOrgConsumerPolicy(config *rest.Config, reg *Registry, policy OrgConsumerPolicyConfig, maxObjectBytes int64) (*OrgConsumerPolicy, error) {
	if config == nil || config.Host == "" || reg == nil {
		return nil, fmt.Errorf("consumer policy requires hub configuration and registry")
	}
	if err := policy.Validate(maxObjectBytes); err != nil {
		return nil, err
	}
	trusted := rest.CopyConfig(config)
	return &OrgConsumerPolicy{config: policy, registry: reg, locks: map[string]bool{}, maxObjectBytes: maxObjectBytes, clientFor: func(path string) (dynamic.Interface, error) {
		cfg := rest.CopyConfig(trusted)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, path)
		return dynamic.NewForConfig(cfg)
	}, discoveryFor: func(path string) (discovery.DiscoveryInterface, error) {
		cfg := rest.CopyConfig(trusted)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, path)
		return discovery.NewDiscoveryClientForConfig(cfg)
	}, auditClientFor: func(path string, maxResponseBytes int64) (dynamic.Interface, error) {
		return boundedInventoryClientFor(trusted, path, maxResponseBytes)
	}}, nil
}

// Lock serializes lifecycle work for one org without queuing waiting HTTP
// requests. A busy org is rejected immediately and cannot fill an unbounded
// waiter queue. Unrelated orgs proceed; idle keys are removed on unlock.
func (p *OrgConsumerPolicy) Lock(ctx context.Context, orgUUID string) (func(), error) {
	if len(validation.IsDNS1123Label(orgUUID)) != 0 {
		return nil, fmt.Errorf("invalid organization identity")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.locks[orgUUID] {
		p.mu.Unlock()
		return nil, fmt.Errorf("organization lifecycle operation already in progress")
	}
	if len(p.locks) >= p.config.MaxOrganizations {
		p.mu.Unlock()
		return nil, fmt.Errorf("organization lifecycle capacity exhausted")
	}
	p.locks[orgUUID] = true
	p.mu.Unlock()
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.locks, orgUUID)
			p.mu.Unlock()
		})
	}
	if err := ctx.Err(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

func (p *OrgConsumerPolicy) teamWorkspace(ctx context.Context, orgUUID, workspaceUUID string) (string, string, error) {
	if len(validation.IsDNS1123Label(orgUUID)) != 0 || !kcppaths.IsTeamWorkspaceName(workspaceUUID) {
		return "", "", fmt.Errorf("expected organization team workspace")
	}
	client, err := p.clientFor(kcppaths.OrgPath(orgUUID))
	if err != nil {
		return "", "", err
	}
	workspace, err := client.Resource(workspaceGVR).Get(ctx, workspaceUUID, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	cluster, _, _ := unstructured.NestedString(workspace.Object, "spec", "cluster")
	kind, _, _ := unstructured.NestedString(workspace.Object, "spec", "type", "name")
	phase, _, _ := unstructured.NestedString(workspace.Object, "status", "phase")
	if workspace.GetDeletionTimestamp() != nil || kind != "workspace" || phase != "Ready" || cluster == "root" || len(validation.IsDNS1123Label(cluster)) != 0 {
		return "", "", fmt.Errorf("consumer workspace is not an active canonical team workspace")
	}
	return kcppaths.WorkspacePath(orgUUID, workspaceUUID), cluster, nil
}

func (p *OrgConsumerPolicy) registeredExport(orgUUID, path, name string) bool {
	if path == kcppaths.SystemControllers && name == "core.railgrid.ai" {
		return true
	}
	for _, provider := range p.registry.List() {
		if (provider.OrgUUID == "" || provider.OrgUUID == orgUUID) && provider.APIExportPath == path && provider.APIExportName == name && path != "" && name != "" {
			return true
		}
	}
	return false
}

func (p *OrgConsumerPolicy) exportResources(ctx context.Context, orgUUID, path, name string) ([]schema.GroupResource, error) {
	if !p.registeredExport(orgUUID, path, name) {
		return nil, fmt.Errorf("consumer references an unregistered or foreign export")
	}
	client, err := p.clientFor(path)
	if err != nil {
		return nil, err
	}
	return ResolveOrgConsumerQuotaResources(ctx, client, name)
}

func (p *OrgConsumerPolicy) boundResources(ctx context.Context, orgUUID, inventoryPath string, client dynamic.Interface) ([]schema.GroupResource, error) {
	var resources []schema.GroupResource
	seen := map[schema.GroupResource]bool{}
	continuation := ""
	seenContinuations := map[string]bool{}
	for {
		listClient := client
		if p.auditClientFor != nil {
			maxResponseBytes, err := inventoryResponseBudget(orgPolicyInventoryPageSize, p.maxObjectBytes)
			if err != nil {
				return nil, err
			}
			listClient, err = p.auditClientFor(inventoryPath, maxResponseBytes)
			if err != nil {
				return nil, fmt.Errorf("creating bounded binding inventory client: %w", err)
			}
		}
		bindings, err := listClient.Resource(orgConsumerBindingGVR).List(ctx, metav1.ListOptions{Limit: orgPolicyInventoryPageSize, Continue: continuation})
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings.Items {
			path, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "path")
			name, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
			// Include terminating/unready references too: they may retain data or
			// become bound later without another Enable request.
			kinds, err := p.exportResources(ctx, orgUUID, path, name)
			if err != nil {
				return nil, err
			}
			for _, kind := range kinds {
				if seen[kind] {
					continue
				}
				if len(seen) >= p.config.Quota.MaxResourceTypes {
					return nil, fmt.Errorf("consumer resource inventory exceeds configured family budget")
				}
				seen[kind] = true
				resources = append(resources, kind)
			}
		}
		next := bindings.GetContinue()
		if next == "" {
			break
		}
		if seenContinuations[next] {
			return nil, fmt.Errorf("consumer binding inventory pagination did not advance")
		}
		seenContinuations[next] = true
		continuation = next
	}
	return resources, nil
}

func (p *OrgConsumerPolicy) dependencyResources(ctx context.Context, orgUUID string, requires []providersv1alpha1.ProviderRequirement) ([]schema.GroupResource, error) {
	var resources []schema.GroupResource
	for _, name := range providersv1alpha1.Dependencies(requires) {
		dependency, ok := p.registry.GetForOrg(orgUUID, name)
		if !ok {
			return nil, fmt.Errorf("provider dependency %s is not registered", name)
		}
		kinds, err := p.exportResources(ctx, orgUUID, dependency.APIExportPath, dependency.APIExportName)
		if err != nil {
			return nil, err
		}
		resources = append(resources, kinds...)
	}
	return resources, nil
}

func consumerInventoryResources(proposed, bound, prior []schema.GroupResource) ([]schema.GroupResource, map[schema.GroupResource]bool) {
	capacity := len(proposed) + len(bound) + len(prior) + len(orgConsumerBuiltinResources)
	seen := make(map[schema.GroupResource]bool, capacity)
	optional := map[schema.GroupResource]bool{}
	mandatory := make(map[schema.GroupResource]bool, len(bound)+len(prior)+len(orgConsumerBuiltinResources))
	out := make([]schema.GroupResource, 0, capacity)
	add := func(resource schema.GroupResource) {
		if seen[resource] {
			return
		}
		seen[resource] = true
		out = append(out, resource)
	}
	for _, resource := range orgConsumerBuiltinResources {
		mandatory[resource] = true
		add(resource)
	}
	for _, resource := range prior {
		mandatory[resource] = true
		add(resource)
	}
	for _, resource := range bound {
		mandatory[resource] = true
		add(resource)
	}
	for _, resource := range proposed {
		add(resource)
		if !mandatory[resource] {
			optional[resource] = true
		}
	}
	return out, optional
}

// NeedsPreparation preserves ordinary platform Enable behavior outside BYO
// allocations. Once allocated, later platform bindings must also reserve their
// ordinary resource families before granting access.
func (p *OrgConsumerPolicy) NeedsPreparation(ctx context.Context, orgUUID, workspaceUUID string, prov Provider) (bool, error) {
	if prov.OrgUUID != "" {
		if prov.OrgUUID != orgUUID {
			return false, fmt.Errorf("foreign organization provider")
		}
		return true, nil
	}
	path, cluster, err := p.teamWorkspace(ctx, orgUUID, workspaceUUID)
	if err != nil {
		return false, err
	}
	parent, err := p.clientFor(kcppaths.OrgProvidersParent(orgUUID))
	if err != nil {
		return false, err
	}
	allocation, err := parent.Resource(orgConsumerLeaseGVR).Namespace("admin").Get(ctx, "consumer-"+cluster, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	annotations := allocation.GetAnnotations()
	if allocation.GetDeletionTimestamp() != nil || allocation.GetLabels()[orgConsumerAllocationLabel] != "true" || annotations[orgConsumerAllocationOrg] != orgUUID || annotations[orgConsumerAllocationPath] != path || annotations[orgConsumerAllocationCluster] != cluster {
		return false, fmt.Errorf("consumer allocation identity mismatch")
	}
	return true, nil
}

// OrgConsumerNeedsPreparation lets a disabled policy still refuse new
// authority in a previously allocated consumer. It makes no quota/lease writes.
func OrgConsumerNeedsPreparation(ctx context.Context, config *rest.Config, orgUUID, workspaceUUID string, prov Provider) (bool, error) {
	if config == nil || config.Host == "" {
		return false, fmt.Errorf("consumer allocation audit requires hub configuration")
	}
	policy := &OrgConsumerPolicy{clientFor: func(path string) (dynamic.Interface, error) {
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, path)
		return dynamic.NewForConfig(cfg)
	}}
	return policy.NeedsPreparation(ctx, orgUUID, workspaceUUID, prov)
}

// requireActiveOrgProviderSource fences asynchronous source deletion. Registry
// entries and APIExports may survive after DELETE starts; neither proves the
// source is still enrolled. The caller holds the org lifecycle lock through the
// subsequent binding write, so a local Delete cannot race this live check.
func (p *OrgConsumerPolicy) requireActiveOrgProviderSource(ctx context.Context, orgUUID string, prov Provider) error {
	if len(validation.IsDNS1123Label(orgUUID)) != 0 || len(validation.IsDNS1123Label(prov.Name)) != 0 || prov.OrgUUID != orgUUID || prov.APIExportName == "" || prov.APIExportPath != kcppaths.OrgProviderPath(orgUUID, prov.Name) {
		return fmt.Errorf("invalid organization provider source")
	}
	current, ok := p.registry.GetForOrg(orgUUID, prov.Name)
	if !ok || current.OrgUUID != orgUUID || current.APIExportPath != prov.APIExportPath || current.APIExportName != prov.APIExportName {
		return fmt.Errorf("organization provider source changed; refresh the catalog")
	}
	parent, err := p.clientFor(kcppaths.OrgProvidersParent(orgUUID))
	if err != nil {
		return err
	}
	workspace, err := parent.Resource(workspaceGVR).Get(ctx, prov.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading organization provider source workspace: %w", err)
	}
	cluster, _, _ := unstructured.NestedString(workspace.Object, "spec", "cluster")
	phase, _, _ := unstructured.NestedString(workspace.Object, "status", "phase")
	if workspace.GetDeletionTimestamp() != nil || workspace.GetAnnotations()[OrgProviderBootstrapPolicyAnnotation] != OrgProviderBootstrapPolicy || phase != "Ready" || cluster == "root" || len(validation.IsDNS1123Label(cluster)) != 0 {
		return fmt.Errorf("organization provider source is not an active restricted workspace")
	}
	if current.CatalogEntryCluster != cluster || prov.CatalogEntryCluster != cluster || current.WorkspaceCluster != "" && current.WorkspaceCluster != cluster || prov.WorkspaceCluster != "" && prov.WorkspaceCluster != cluster {
		return fmt.Errorf("organization provider catalog refers to a different workspace instance")
	}
	return nil
}

func (p *OrgConsumerPolicy) PrepareEnable(ctx context.Context, orgUUID, workspaceUUID string, prov Provider) error {
	if prov.OrgUUID != "" && prov.OrgUUID != orgUUID {
		return fmt.Errorf("foreign organization provider")
	}
	if prov.OrgUUID != "" {
		if err := p.requireActiveOrgProviderSource(ctx, orgUUID, prov); err != nil {
			return err
		}
	}
	path, cluster, err := p.teamWorkspace(ctx, orgUUID, workspaceUUID)
	if err != nil {
		return err
	}
	consumer, err := p.clientFor(cluster)
	if err != nil {
		return err
	}
	if err := p.markConsumerQuotaRequired(ctx, consumer); err != nil {
		return err
	}
	if err := p.waitForConsumerMarker(ctx, cluster); err != nil {
		return err
	}
	if prov.OrgUUID != "" {
		if err := p.ValidateProviderStorage(ctx, prov.APIExportPath); err != nil {
			return fmt.Errorf("auditing existing provider storage before enable: %w", err)
		}
	}
	resources, err := p.exportResources(ctx, orgUUID, prov.APIExportPath, prov.APIExportName)
	if err != nil {
		return err
	}
	deps, err := p.dependencyResources(ctx, orgUUID, prov.Requires)
	if err != nil {
		return err
	}
	resources = append(resources, deps...)
	bound, err := p.boundResources(ctx, orgUUID, path, consumer)
	if err != nil {
		return err
	}
	prior, err := p.priorConsumerQuotaResources(ctx, consumer)
	if err != nil {
		return err
	}
	inventory, optionalMissing := consumerInventoryResources(resources, bound, prior)
	pendingStatus, err := p.validateObjectInventoryWithOptionalMissing(ctx, path, consumer, consumerResourceLimits(inventory, p.config.Quota.MaxObjectsPerResource), optionalMissing)
	if err != nil {
		return fmt.Errorf("auditing existing consumer storage: %w", err)
	}
	parent, err := p.clientFor(kcppaths.OrgProvidersParent(orgUUID))
	if err != nil {
		return err
	}
	if err := EnsureOrgConsumerAllocation(ctx, parent, orgUUID, path, cluster, p.config.MaxConsumers); err != nil {
		return err
	}
	return ensureOrgConsumerQuotaWithPendingStatus(ctx, consumer, inventory, p.config.Quota, pendingStatus)
}

func (p *OrgConsumerPolicy) PrepareBootstrap(ctx context.Context, orgUUID, providerName string, admitted sdkinstall.OrganizationBootstrap) error {
	if len(validation.IsDNS1123Label(orgUUID)) != 0 || len(validation.IsDNS1123Label(providerName)) != 0 || admitted.CatalogEntry == nil {
		return fmt.Errorf("invalid organization bootstrap identity")
	}
	if err := p.ValidateProviderStorage(ctx, kcppaths.OrgProviderPath(orgUUID, providerName)); err != nil {
		return fmt.Errorf("auditing existing provider storage before bootstrap: %w", err)
	}
	resources, err := OrgConsumerQuotaResources(admitted.APIExport, admitted.Schemas)
	if err != nil {
		return err
	}
	var entry providersv1alpha1.CatalogEntry
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(admitted.CatalogEntry.Object, &entry); err != nil {
		return err
	}
	if entry.Name != providerName || entry.Spec.Export == nil || entry.Spec.Export.Name != admitted.APIExport.GetName() {
		return fmt.Errorf("bootstrap export identity mismatch")
	}
	deps, err := p.dependencyResources(ctx, orgUUID, entry.Spec.Requires)
	if err != nil {
		return err
	}
	resources = append(resources, deps...)
	parent, err := p.clientFor(kcppaths.OrgProvidersParent(orgUUID))
	if err != nil {
		return err
	}
	allocations, err := parent.Resource(orgConsumerLeaseGVR).Namespace("admin").List(ctx, metav1.ListOptions{LabelSelector: orgConsumerAllocationLabel + "=true", Limit: p.config.MaxConsumers + 1})
	if err != nil {
		return err
	}
	if allocations.GetContinue() != "" || int64(len(allocations.Items)) > p.config.MaxConsumers {
		return fmt.Errorf("consumer allocation inventory exceeds configured capacity")
	}
	for _, allocation := range allocations.Items {
		annotations := allocation.GetAnnotations()
		path := annotations[orgConsumerAllocationPath]
		cluster := annotations[orgConsumerAllocationCluster]
		owner, workspace, ok := kcppaths.SplitWorkspacePath(path)
		if !ok || owner != orgUUID || annotations[orgConsumerAllocationOrg] != orgUUID || allocation.GetLabels()[orgConsumerAllocationLabel] != "true" || allocation.GetName() != "consumer-"+cluster || allocation.GetDeletionTimestamp() != nil {
			return fmt.Errorf("invalid consumer allocation identity")
		}
		actualPath, actualCluster, err := p.teamWorkspace(ctx, orgUUID, workspace)
		if err != nil {
			return err
		}
		if actualPath != path || actualCluster != cluster {
			return fmt.Errorf("consumer allocation refers to a replaced workspace")
		}
		consumer, err := p.clientFor(cluster)
		if err != nil {
			return err
		}
		if err := p.markConsumerQuotaRequired(ctx, consumer); err != nil {
			return err
		}
		if err := p.waitForConsumerMarker(ctx, cluster); err != nil {
			return err
		}
		bound, err := p.boundResources(ctx, orgUUID, path, consumer)
		if err != nil {
			return err
		}
		prior, err := p.priorConsumerQuotaResources(ctx, consumer)
		if err != nil {
			return err
		}
		all, optionalMissing := consumerInventoryResources(resources, bound, prior)
		pendingStatus, err := p.validateObjectInventoryWithOptionalMissing(ctx, path, consumer, consumerResourceLimits(all, p.config.Quota.MaxObjectsPerResource), optionalMissing)
		if err != nil {
			return fmt.Errorf("auditing existing consumer storage in %s: %w", workspace, err)
		}
		if err := ensureOrgConsumerQuotaWithPendingStatus(ctx, consumer, all, p.config.Quota, pendingStatus); err != nil {
			return err
		}
	}
	return nil
}

// CheckDelete requires every matching binding to be fully absent, including
// bindings whose phase is not Bound or whose deletion finalizers are still held.
func (p *OrgConsumerPolicy) CheckDelete(ctx context.Context, orgUUID, providerName, exportName string) error {
	return checkOrgProviderUnbound(ctx, p.clientFor, p.auditClientFor, p.maxObjectBytes, orgUUID, providerName)
}

// CheckOrgProviderUnbound is also usable when BYO enrollment is disabled. It
// protects cleanup and fresh registration from reusing an exporter's path while
// a binding still retains the old identity. Match every export in that workspace,
// not only the current catalog name. Call under the same org lifecycle lock as
// Enable/Bootstrap and the subsequent deletion or new registration.
func CheckOrgProviderUnbound(ctx context.Context, config *rest.Config, orgUUID, providerName string) error {
	if config == nil || config.Host == "" {
		return fmt.Errorf("provider binding audit requires hub configuration")
	}
	clientFor := func(path string) (dynamic.Interface, error) {
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, path)
		return dynamic.NewForConfig(cfg)
	}
	auditClientFor := func(path string, maxBytes int64) (dynamic.Interface, error) {
		return boundedInventoryClientFor(config, path, maxBytes)
	}
	return checkOrgProviderUnbound(ctx, clientFor, auditClientFor, 1<<20, orgUUID, providerName)
}

func checkOrgProviderUnbound(ctx context.Context, clientFor func(string) (dynamic.Interface, error), auditClientFor func(string, int64) (dynamic.Interface, error), maxObjectBytes int64, orgUUID, providerName string) error {
	if len(validation.IsDNS1123Label(orgUUID)) != 0 || len(validation.IsDNS1123Label(providerName)) != 0 {
		return fmt.Errorf("invalid provider deletion identity")
	}
	client, err := clientFor(kcppaths.OrgPath(orgUUID))
	if err != nil {
		return err
	}
	expectedPath := kcppaths.OrgProviderPath(orgUUID, providerName)
	workspaceContinuation := ""
	seenWorkspaceContinuations := map[string]bool{}
	for {
		workspaceClient := client
		if auditClientFor != nil {
			maxBytes, err := inventoryResponseBudget(orgPolicyInventoryPageSize, maxObjectBytes)
			if err != nil {
				return err
			}
			workspaceClient, err = auditClientFor(kcppaths.OrgPath(orgUUID), maxBytes)
			if err != nil {
				return fmt.Errorf("creating bounded workspace inventory client: %w", err)
			}
		}
		workspaces, err := workspaceClient.Resource(workspaceGVR).List(ctx, metav1.ListOptions{Limit: orgPolicyInventoryPageSize, Continue: workspaceContinuation})
		if err != nil {
			return err
		}
		for _, workspace := range workspaces.Items {
			if workspace.GetName() == kcppaths.OrgProvidersWorkspaceName {
				continue
			}
			if !kcppaths.IsTeamWorkspaceName(workspace.GetName()) {
				return fmt.Errorf("unrecognized organization child workspace")
			}
			consumer, err := clientFor(kcppaths.WorkspacePath(orgUUID, workspace.GetName()))
			if err != nil {
				return err
			}
			bindingContinuation := ""
			seenBindingContinuations := map[string]bool{}
			for {
				bindingClient := consumer
				if auditClientFor != nil {
					maxBytes, err := inventoryResponseBudget(orgPolicyInventoryPageSize, maxObjectBytes)
					if err != nil {
						return err
					}
					bindingClient, err = auditClientFor(kcppaths.WorkspacePath(orgUUID, workspace.GetName()), maxBytes)
					if err != nil {
						return fmt.Errorf("creating bounded binding inventory client: %w", err)
					}
				}
				bindings, err := bindingClient.Resource(orgConsumerBindingGVR).List(ctx, metav1.ListOptions{Limit: orgPolicyInventoryPageSize, Continue: bindingContinuation})
				if err != nil {
					return err
				}
				for _, binding := range bindings.Items {
					path, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "path")
					if path == expectedPath {
						return fmt.Errorf("%w: workspace %s binding %s", ErrOrgProviderInUse, workspace.GetName(), binding.GetName())
					}
				}
				next := bindings.GetContinue()
				if next == "" {
					break
				}
				if seenBindingContinuations[next] {
					return fmt.Errorf("consumer binding inventory pagination did not advance")
				}
				seenBindingContinuations[next] = true
				bindingContinuation = next
			}
		}
		next := workspaces.GetContinue()
		if next == "" {
			break
		}
		if seenWorkspaceContinuations[next] {
			return fmt.Errorf("organization workspace inventory pagination did not advance")
		}
		seenWorkspaceContinuations[next] = true
		workspaceContinuation = next
	}
	return nil
}
