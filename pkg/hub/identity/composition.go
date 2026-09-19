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

package identity

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// Composition is one composed kind as the policy sees it: the dependency that
// owns it, the coordinate, and the verbs the composing provider declared.
type Composition struct {
	// Dependency is the provider named in spec.dependencies[].name — the one
	// whose export must be bound in the workspace.
	Dependency string
	Group      string
	Resource   string
	Verbs      []string
}

// CompositionGrants is the consent half of clause E: whether the tenant
// accepted a composition in this workspace. A nil implementation refuses
// every composition, the same way a nil BindingChecker refuses every foreign
// rule — a hub that cannot read consent must not assume it.
type CompositionGrants interface {
	// IsComposed reports whether provider may compose group/resource in the
	// tenant workspace clusterID.
	IsComposed(clusterID, provider, group, resource string) (bool, error)
}

// Compositions implements ProviderCatalog over the provider registry.
func (c *RegistryCatalog) Compositions(provider string) []Composition {
	if c == nil || c.registry == nil {
		return nil
	}
	entry, ok := c.registry.Get(provider)
	if !ok {
		return nil
	}
	return compositionsOf(entry)
}

func compositionsOf(entry providers.Provider) []Composition {
	out := make([]Composition, 0, len(entry.Dependencies))
	for _, dependency := range entry.Dependencies {
		for _, composition := range dependency.Composes {
			out = append(out, Composition{
				Dependency: dependency.Name,
				Group:      composition.Group,
				Resource:   composition.Resource,
				Verbs:      append([]string(nil), composition.Verbs...),
			})
		}
	}
	return out
}

// GrantCompositionChecker answers IsComposed from the Grant a tenant wrote
// when they enabled the provider.
//
// The grant is keyed by (org UUID, workspace UUID, provider, provider owner
// org), and an identity request names a tenant workspace by its kcp LOGICAL
// CLUSTER. The two are bridged the way every other hub component bridges
// them: read the workspace's own LogicalCluster and take its `kcp.io/path`
// annotation, which kcp stamps at creation and nothing in a tenant workspace
// can forge (pkg/hub/mcpaggregate/verifier.go does the same for MCP
// admission). A path that is not a tenant team workspace has no grant and
// composes nothing.
type GrantCompositionChecker struct {
	clients  func(clusterID string) (dynamic.Interface, error)
	grants   *hubaccess.Store
	registry *providers.Registry
	// platformDefault mirrors --provider-hub-access-platform-default: a
	// platform provider composes what it declares in a workspace where nobody
	// has decided yet. It is the same upgrade affordance hub access has, and
	// it never applies to an org-owned provider.
	platformDefault bool
}

var logicalClusterGVR = schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}

// NewGrantCompositionChecker builds a checker over the hub's kcp config.
func NewGrantCompositionChecker(kcpConfig *rest.Config, grants *hubaccess.Store, registry *providers.Registry, platformDefault bool) (*GrantCompositionChecker, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("kcp config is required")
	}
	return &GrantCompositionChecker{clients: func(clusterID string) (dynamic.Interface, error) {
		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterID)
		return dynamic.NewForConfig(cfg)
	}, grants: grants, registry: registry, platformDefault: platformDefault}, nil
}

// NewCompositionCheckerForClient is a focused test seam: it never changes the
// rules, only where the LogicalCluster is read from.
func NewCompositionCheckerForClient(client dynamic.Interface, grants *hubaccess.Store, registry *providers.Registry, platformDefault bool) *GrantCompositionChecker {
	return &GrantCompositionChecker{
		clients:         func(string) (dynamic.Interface, error) { return client, nil },
		grants:          grants,
		registry:        registry,
		platformDefault: platformDefault,
	}
}

// IsComposed implements CompositionGrants.
func (c *GrantCompositionChecker) IsComposed(clusterID, provider, group, resource string) (bool, error) {
	if c == nil || c.clients == nil || c.grants == nil {
		return false, fmt.Errorf("composition grant checker is unavailable")
	}
	ctx := context.Background()
	orgUUID, workspaceUUID, err := c.tenantWorkspace(ctx, clusterID)
	if err != nil {
		return false, err
	}
	if orgUUID == "" || workspaceUUID == "" {
		return false, nil
	}
	// Resolve the provider the way Enable resolved it, so an Org running its
	// own copy of a provider is checked against ITS grant and never against
	// the platform copy's.
	providerOrgUUID := ""
	if c.registry != nil {
		entry, ok := c.registry.GetForOrg(orgUUID, provider)
		if !ok {
			return false, nil
		}
		providerOrgUUID = entry.OrgUUID
	}
	grant, err := c.grants.Get(ctx, hubaccess.GrantKey{
		OrgUUID: orgUUID, WorkspaceUUID: workspaceUUID,
		Provider: provider, ProviderOrgUUID: providerOrgUUID,
	})
	if err != nil {
		return false, err
	}
	allowed, _ := hubaccess.ComposeAllowed(grant,
		hubaccess.CompositionRequirement{Group: group, Resource: resource},
		providerOrgUUID == "", c.platformDefault)
	return allowed, nil
}

// tenantWorkspace resolves the (org, workspace) UUIDs of a tenant team
// workspace from its logical cluster. Anything that is not
// root:railgrid:tenants:<org>:<ws> returns empty UUIDs, not an error: an
// identity minted for a provider workspace or the root simply has no tenant
// consent to find.
func (c *GrantCompositionChecker) tenantWorkspace(ctx context.Context, clusterID string) (string, string, error) {
	dyn, err := c.clients(clusterID)
	if err != nil {
		return "", "", err
	}
	object, err := dyn.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("reading the workspace's LogicalCluster: %w", err)
	}
	orgUUID, workspaceUUID, _ := SplitTenantWorkspacePath(object.GetAnnotations()["kcp.io/path"])
	return orgUUID, workspaceUUID, nil
}

// SplitTenantWorkspacePath reports whether path names a tenant TEAM workspace
// (root:railgrid:tenants:<org>:<ws>) and returns its UUIDs. The org's own
// workspace, its `providers` child and every provider workspace under it are
// rejected: a composition is consented to per team workspace, and the reserved
// `providers` name is the one child of an org that is not one.
func SplitTenantWorkspacePath(path string) (orgUUID, workspaceUUID string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(path), kcppaths.TenantsParent+":")
	if !found {
		return "", "", false
	}
	orgUUID, rest, found = strings.Cut(rest, ":")
	if !found || orgUUID == "" || rest == "" {
		return "", "", false
	}
	if strings.Contains(rest, ":") || rest == kcppaths.OrgProvidersWorkspaceName {
		return "", "", false
	}
	return orgUUID, rest, true
}
