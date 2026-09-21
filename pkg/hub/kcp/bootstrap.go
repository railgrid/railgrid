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

// Package kcp bootstraps kcp API resources.
package kcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/railgrid/railgrid/config/kcp"
	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/bootstrap"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
	"github.com/railgrid/railgrid/pkg/util/confighelpers"
)

// kcp resource GVRs.
var (
	workspaceGVR = schema.GroupVersionResource{
		Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
	}
	apiExportGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexports",
	}
	apiBindingGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings",
	}
	membershipGVR = schema.GroupVersionResource{
		Group: "tenants.railgrid.ai", Version: "v1alpha1", Resource: "memberships",
	}
)

// Bootstrapper sets up the kcp workspace hierarchy and API exports.
type Bootstrapper struct {
	config *rest.Config
	// workspaceIdentityHash is the identity hash of the tenancy.kcp.io APIExport
	// from the root workspace. Needed for permission claims on workspaces.
	workspaceIdentityHash string
	// enabledProviders is the value of `--providers`, controlling which
	// first-party CatalogEntries get materialized. nil/empty means "all
	// known builtins" (matches the flag's default).
	enabledProviders []string
}

// NewBootstrapper creates a new bootstrapper.
func NewBootstrapper(config *rest.Config) *Bootstrapper {
	// The hub admin client fans out across many kcp workspaces (every org, every
	// child workspace, every provider export) and polls during provider Enable.
	// client-go's default 5 QPS / 10 burst throttles that fan-out and surfaces as
	// "client rate limiter Wait ... would exceed context deadline" mid-Enable
	// (e.g. while waiting for a provider's APIExport in exportClaimIdentities).
	// Give it generous headroom — matching the kuery controller's 50/100 — and
	// force RateLimiter to nil so each per-path client (configForPath copies this
	// config) builds its own limiter rather than sharing a single contended
	// bucket inherited from e.g. a loopback config.
	cfg := rest.CopyConfig(config)
	cfg.QPS = 50
	cfg.Burst = 100
	cfg.RateLimiter = nil
	return &Bootstrapper{config: cfg}
}

// WithEnabledProviders sets the subset of builtin providers the
// bootstrapper will write into root:railgrid:providers. Pass the value of
// the --providers flag; nil/empty selects every known builtin.
func (b *Bootstrapper) WithEnabledProviders(names []string) *Bootstrapper {
	b.enabledProviders = names
	return b
}

// Bootstrap creates the workspace hierarchy:
//
//	root:railgrid                          - Root railgrid workspace
//	root:railgrid:providers                - Parent of per-provider sub-workspaces
//	  root:railgrid:providers:{name}       - One provider (restricted `provider` type)
//	root:railgrid:tenants:{uuid}:{ws}:{edge}  - Tenant org/team/edge fleet
//	root:railgrid:system:controllers       - ALL platform APIExports + schemas
//	root:railgrid:system:providers         - Provider + CatalogEntry objects
//	root:railgrid:system:tenants           - User/Organization/Membership objects
func (b *Bootstrapper) Bootstrap(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Bootstrapping kcp workspace hierarchy")

	// 1. Clients targeting root workspace.
	rootDynamic, rootDiscovery, err := newClients(b.config)
	if err != nil {
		return fmt.Errorf("creating root clients: %w", err)
	}

	// 2. Bootstrap root:railgrid workspace.
	logger.Info("Bootstrapping root:railgrid workspace")
	if err := confighelpers.Bootstrap(ctx, rootDiscovery, rootDynamic, kcp.RootWorkspaceFS); err != nil {
		return fmt.Errorf("bootstrapping root:railgrid workspace: %w", err)
	}
	if err := waitForWorkspaceReady(ctx, rootDynamic, "railgrid"); err != nil {
		return fmt.Errorf("waiting for railgrid workspace: %w", err)
	}

	// 3. Bootstrap child workspaces: providers, tenants, users.
	railgridConfig := configForPath(b.config, "root:railgrid")
	railgridDynamic, railgridDiscovery, err := newClients(railgridConfig)
	if err != nil {
		return fmt.Errorf("creating railgrid clients: %w", err)
	}

	logger.Info("Bootstrapping child workspaces: providers, tenants, system")
	if err := confighelpers.Bootstrap(ctx, railgridDiscovery, railgridDynamic, kcp.RailgridWorkspaceFS); err != nil {
		return fmt.Errorf("bootstrapping child workspaces: %w", err)
	}
	for _, name := range []string{"providers", "tenants", "system"} {
		if err := waitForWorkspaceReady(ctx, railgridDynamic, name); err != nil {
			return fmt.Errorf("waiting for %s workspace: %w", name, err)
		}
	}

	// 3b. Bootstrap the system sub-workspaces: controllers (all platform
	//     APIExports), providers (Provider/CatalogEntry objects), tenants
	//     (User/Organization/Membership objects).
	systemConfig := configForPath(b.config, kcppaths.System)
	systemDynamic, systemDiscovery, err := newClients(systemConfig)
	if err != nil {
		return fmt.Errorf("creating system clients: %w", err)
	}
	logger.Info("Bootstrapping system sub-workspaces: controllers, providers, tenants")
	if err := confighelpers.Bootstrap(ctx, systemDiscovery, systemDynamic, kcp.SystemWorkspaceFS); err != nil {
		return fmt.Errorf("bootstrapping system sub-workspaces: %w", err)
	}
	for _, name := range []string{"controllers", "providers", "tenants"} {
		if err := waitForWorkspaceReady(ctx, systemDynamic, name); err != nil {
			return fmt.Errorf("waiting for system:%s workspace: %w", name, err)
		}
	}

	// 4. Fetch tenancy.kcp.io identity hash from root workspace.
	// The identity hash is set asynchronously by kcp after startup, so we
	// poll until it is available rather than failing immediately.
	logger.Info("Fetching tenancy.kcp.io identity hash from root workspace")
	var identityHash string
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		tenancyExport, getErr := rootDynamic.Resource(apiExportGVR).Get(ctx, "tenancy.kcp.io", metav1.GetOptions{})
		if getErr != nil {
			logger.V(4).Info("tenancy.kcp.io APIExport not yet available, retrying", "err", getErr)
			return false, nil
		}
		h, _, _ := unstructured.NestedString(tenancyExport.Object, "status", "identityHash")
		if h == "" {
			logger.V(4).Info("tenancy.kcp.io APIExport has no identity hash yet, retrying")
			return false, nil
		}
		identityHash = h
		return true, nil
	}); err != nil {
		return fmt.Errorf("waiting for tenancy.kcp.io identity hash: %w", err)
	}
	b.workspaceIdentityHash = identityHash
	logger.Info("Got tenancy.kcp.io identity hash", "hash", identityHash)

	// Preserve legacy initial-workspace requests while the old tenant API
	// schema still exposes them. Updating the export below prunes that field.
	tenantMigrationClient, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.SystemTenants))
	if err != nil {
		return fmt.Errorf("creating tenant migration client: %w", err)
	}
	if err := bootstrap.PreserveInitialWorkspaceRequests(ctx, tenantMigrationClient); err != nil {
		return err
	}

	// 5. Bootstrap ALL platform APIResourceSchemas + APIExports in
	//    root:railgrid:system:controllers — the single home for platform exports.
	//    The __TENANCY_IDENTITY_HASH__ placeholder in the APIExport YAML is
	//    replaced with the actual identity hash from step 4.
	controllersConfig := configForPath(b.config, kcppaths.SystemControllers)
	controllersDynamic, controllersDiscovery, err := newClients(controllersConfig)
	if err != nil {
		return fmt.Errorf("creating system:controllers clients: %w", err)
	}

	logger.Info("Bootstrapping APIResourceSchemas and APIExports in system:controllers")
	if err := confighelpers.Bootstrap(ctx, controllersDiscovery, controllersDynamic, kcp.ProvidersFS,
		confighelpers.ReplaceOption("__TENANCY_IDENTITY_HASH__", identityHash),
		// apiexport-railgrid.ai.yaml is embedded only as input for the
		// core.railgrid.ai generator (hack/gen-core-apiexport). Nothing binds the
		// standalone railgrid.ai export — tenants bind core.railgrid.ai — so we
		// never apply it to the cluster; its presence there is just confusing.
		confighelpers.SkipFilesOption("apiexport-railgrid.ai.yaml"),
	); err != nil {
		return fmt.Errorf("bootstrapping platform exports: %w", err)
	}

	// 5b. Bind the platform exports into the workspaces that hold their
	//     objects: system:providers binds providers.railgrid.ai (CatalogEntry)
	//     + admin.railgrid.ai (Provider); system:tenants binds
	//     tenants.railgrid.ai (User/Organization/Membership). All FROM
	//     system:controllers. These exports are excluded from tenant-bound
	//     core.railgrid.ai — see hack/gen-core-apiexport/main.go excludedAPIExports.
	systemProvidersDynamic, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.SystemProviders))
	if err != nil {
		return fmt.Errorf("creating system:providers client: %w", err)
	}
	for _, exportName := range []string{"providers.railgrid.ai", "admin.railgrid.ai"} {
		if err := ensureExportBinding(ctx, systemProvidersDynamic, kcppaths.SystemControllers, exportName); err != nil {
			return fmt.Errorf("binding %s in system:providers: %w", exportName, err)
		}
	}

	// 5c. First-party CatalogEntries — the portal's MCP / Edges /
	//     Workloads tabs surface as ordinary entries in the providers
	//     list. They declare spec.ui.builtinRoute (not URL) so the portal
	//     renders an in-tree Vue route instead of loading a custom
	//     element bundle. They live in system:providers alongside the
	//     admin-applied Provider/CatalogEntry objects.
	if err := ensureBuiltinCatalogEntries(ctx, systemProvidersDynamic, b.enabledProviders); err != nil {
		return fmt.Errorf("creating builtin CatalogEntries: %w", err)
	}

	// 5d. Apply post-providers workspace artefacts under root:railgrid — namely
	//     the `organization` WorkspaceType, which declares a defaultAPIBinding
	//     to tenants.railgrid.ai in root:railgrid:providers. kcp's WT
	//     admission resolves the binding's LogicalCluster and checks bind
	//     RBAC at apply time, so the APIExport (created in step 5) must
	//     exist beforehand or the apply fails with a 403 forbidden.
	logger.Info("Bootstrapping post-providers workspace artefacts (organization WorkspaceType)")
	if err := confighelpers.Bootstrap(ctx, railgridDiscovery, railgridDynamic, kcp.PostProvidersFS); err != nil {
		return fmt.Errorf("bootstrapping post-providers artefacts: %w", err)
	}

	// 6. Bind tenants.railgrid.ai APIExport in root:railgrid:system:tenants so
	//    User, Organization, Membership, and UserMembershipIndex CRs are all
	//    reachable there (this is the CR-object storage workspace; the org
	//    *fleet* lives separately under root:railgrid:tenants). Same admission rules
	//    as step 5d apply — the APIExport must exist (step 5) before this
	//    APIBinding is created.
	logger.Info("Binding tenants.railgrid.ai in system:tenants")
	if err := b.ensureTenancyObjectsBinding(ctx); err != nil {
		return fmt.Errorf("binding tenants.railgrid.ai in system:tenants: %w", err)
	}

	// 7. Namespace in system:controllers for the hub's own replica-coordination
	//    objects (leader-election Lease, shared session/code Secrets). Every hub
	//    replica needs it before it can join the election, so it is created
	//    during bootstrap rather than lazily.
	logger.Info("Ensuring hub coordination namespace in system:controllers")
	if err := b.EnsureHubSystemNamespace(ctx); err != nil {
		return fmt.Errorf("ensuring hub coordination namespace: %w", err)
	}

	logger.Info("kcp bootstrap complete")
	return nil
}

// HubSystemNamespace is the namespace inside root:railgrid:system:controllers that
// holds the hub's cross-replica coordination state: the controller
// leader-election Lease and the shared browser-session / app-access-code
// Secrets. It is hub-internal — no tenant, provider, or user identity is ever
// granted access to system:controllers.
const HubSystemNamespace = "railgrid-hub"

// ControllersConfig returns a rest.Config targeting root:railgrid:system:controllers,
// where the platform APIExports and the hub's own coordination objects live.
func (b *Bootstrapper) ControllersConfig() *rest.Config {
	return configForPath(b.config, kcppaths.SystemControllers)
}

// EnsureHubSystemNamespace creates HubSystemNamespace in system:controllers.
// Idempotent on AlreadyExists, so every replica can call it concurrently at
// startup.
func (b *Bootstrapper) EnsureHubSystemNamespace(ctx context.Context) error {
	client, err := dynamic.NewForConfig(b.ControllersConfig())
	if err != nil {
		return fmt.Errorf("creating system:controllers client: %w", err)
	}
	ns := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata":   map[string]interface{}{"name": HubSystemNamespace},
		},
	}
	if _, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).
		Create(ctx, ns, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %s: %w", HubSystemNamespace, err)
	}
	return nil
}

// ensureTenancyObjectsBinding creates an APIBinding to the
// tenants.railgrid.ai APIExport (in root:railgrid:system:controllers) inside
// root:railgrid:system:tenants. Idempotent. Without this binding the organization
// bootstrap controller's writes to User / Organization / Membership CRs in
// system:tenants would fail with "no matches for kind".
func (b *Bootstrapper) ensureTenancyObjectsBinding(ctx context.Context) error {
	tenancyDynamic, err := dynamic.NewForConfig(b.UsersConfig())
	if err != nil {
		return fmt.Errorf("creating system:tenants client: %w", err)
	}
	if err := ensureExportBinding(ctx, tenancyDynamic, kcppaths.SystemControllers, "tenants.railgrid.ai"); err != nil {
		return err
	}
	if err := waitForAPIBindingBound(ctx, tenancyDynamic, "tenants.railgrid.ai"); err != nil {
		return fmt.Errorf("waiting for tenancy binding: %w", err)
	}
	client, err := discovery.NewDiscoveryClientForConfig(b.UsersConfig())
	if err != nil {
		return err
	}
	return waitForTenancyDiscovery(ctx, client)
}

// Controller field indexes resolve kinds during setup, before their informers
// start. A created (or even Bound) APIBinding alone does not guarantee discovery
// has caught up, so fresh installations must wait before constructing managers.
func waitForTenancyDiscovery(ctx context.Context, client discovery.DiscoveryInterface) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, time.Minute, true, func(ctx context.Context) (bool, error) {
		resources, err := client.ServerResourcesForGroupVersion("tenants.railgrid.ai/v1alpha1")
		if err != nil {
			if errors.IsNotFound(err) || errors.IsServiceUnavailable(err) {
				return false, nil
			}
			return false, err
		}
		found := map[string]bool{}
		for _, resource := range resources.APIResources {
			found[resource.Name] = true
		}
		return found["users"] && found["organizations"] && found["usermembershipindices"], nil
	})
}

// UsersConfig returns a rest.Config targeting root:railgrid:system:tenants, where
// the User / Organization / Membership CR OBJECTS are stored (this replaces the
// former root:railgrid:users). The org *fleet* lives separately under
// root:railgrid:tenants (see OrgsConfig).
func (b *Bootstrapper) UsersConfig() *rest.Config {
	return configForPath(b.config, kcppaths.SystemTenants)
}

// OrgsConfig returns a rest.Config targeting the root:railgrid:tenants parent
// workspace. The Organization bootstrap controller uses this to create child
// Workspaces of type `organization` — one per Organization CR — at
// root:railgrid:tenants:{org-uuid}. The fleet location is unchanged by the
// system-workspace restructure.
func (b *Bootstrapper) OrgsConfig() *rest.Config {
	return configForPath(b.config, kcppaths.TenantsParent)
}

// EnsureOrgWorkspace creates a kcp Workspace at root:railgrid:tenants:{orgUUID}
// of type `organization` (see config/kcp/workspacetype-organization.yaml).
// Idempotent: returns nil on AlreadyExists. Blocks until the workspace is
// Ready so callers can immediately patch the corresponding Organization
// CR's status.
//
// Per docs/organizations.md decision O-10, Org workspaces are hub-mediated
// only — tenants never receive a kubeconfig pointing here. This method is
// invoked from the Organization bootstrap controller with the hub's own
// admin config; no per-User RBAC is granted inside the workspace.
//
// The "organization" WorkspaceType's defaultAPIBindings bring
// tenants.railgrid.ai (Organization, CatalogEntry, future Membership)
// and tenancy.kcp.io (Workspace for child team-workspace creation in
// PR #3) into the Org workspace.
func (b *Bootstrapper) EnsureOrgWorkspace(ctx context.Context, orgUUID string) error {
	logger := klog.FromContext(ctx).WithValues("orgUUID", orgUUID)
	orgsClient, err := dynamic.NewForConfig(b.OrgsConfig())
	if err != nil {
		return fmt.Errorf("creating orgs client: %w", err)
	}

	ws := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tenancy.kcp.io/v1alpha1",
			"kind":       "Workspace",
			"metadata": map[string]interface{}{
				"name": orgUUID,
			},
			"spec": map[string]interface{}{
				"type": map[string]interface{}{
					"name": "organization",
					"path": "root:railgrid",
				},
			},
		},
	}

	if _, err := orgsClient.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{}); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Organization workspace %s: %w", orgUUID, err)
		}
		logger.V(4).Info("Organization workspace already exists")
	} else {
		logger.Info("Created Organization workspace")
	}

	if err := waitForWorkspaceReady(ctx, orgsClient, orgUUID); err != nil {
		return fmt.Errorf("waiting for Organization workspace %s: %w", orgUUID, err)
	}
	return nil
}

// GetOrgClusterName returns the kcp logical cluster name of an Organization
// workspace at root:railgrid:tenants:{orgUUID} once it is Ready. The cluster
// name is what status.workspaceCluster on the Organization CR can record
// for observers that need the canonical kcp identifier rather than the
// human-readable path.
func (b *Bootstrapper) GetOrgClusterName(ctx context.Context, orgUUID string) (string, error) {
	orgsClient, err := dynamic.NewForConfig(b.OrgsConfig())
	if err != nil {
		return "", fmt.Errorf("creating orgs client: %w", err)
	}
	ws, err := orgsClient.Resource(workspaceGVR).Get(ctx, orgUUID, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting Organization workspace %s: %w", orgUUID, err)
	}
	clusterName, _, _ := unstructured.NestedString(ws.Object, "spec", "cluster")
	if clusterName == "" {
		return "", fmt.Errorf("organization workspace %s has no spec.cluster", orgUUID)
	}
	return clusterName, nil
}

// EnsureOrgMembership creates a Membership CR inside the Organization
// workspace at root:railgrid:tenants:{orgUUID} granting the given User the
// given role at scope=org. Idempotent — returns nil if a Membership with
// the same metadata.name already exists, regardless of role drift (an
// admin demoting a member is owned by a separate Role-patch endpoint
// per O-12 and never comes through this path).
//
// metadata.name = userName so the existence check is a cheap Get on a
// known key, not a List+filter. PR #4 ships only the bootstrap path
// (personal-Org admin Membership for the User); manual Org membership
// management lands in PR #10.
func (b *Bootstrapper) EnsureOrgMembership(ctx context.Context, orgUUID, userName, role string) error {
	if userName == "" {
		return fmt.Errorf("EnsureOrgMembership: userName is required")
	}
	if role != "admin" && role != "member" {
		return fmt.Errorf("EnsureOrgMembership: invalid role %q (want admin or member)", role)
	}

	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}

	membership := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tenants.railgrid.ai/v1alpha1",
			"kind":       "Membership",
			"metadata": map[string]interface{}{
				"name": userName,
			},
			"spec": map[string]interface{}{
				"user":  userName,
				"scope": "org",
				"role":  role,
			},
		},
	}

	if _, err := orgClient.Resource(membershipGVR).Create(ctx, membership, metav1.CreateOptions{}); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating Membership for %s in org %s: %w", userName, orgUUID, err)
	}
	return nil
}

// EnsureChildWorkspace materializes a kcp Workspace at
// root:railgrid:tenants:{orgUUID}:{wsUUID} of type `workspace` (see
// config/kcp/workspacetype-workspace.yaml). Used by the organization
// bootstrap controller to create the User's default team Workspace
// inside their personal Org so the portal can pin a default
// X-Railgrid-Workspace header. Idempotent: returns nil on AlreadyExists
// and blocks until the workspace reports Ready.
//
// The hub-mediated rule from O-10 only applies to the Organization
// workspace itself; the child team Workspace IS tenant-accessible.
// This method is invoked from the org bootstrap controller with the
// hub's admin credentials so the WorkspaceType admission's bind check
// against tenants.railgrid.ai passes (same chain that already
// powers EnsureOrgWorkspace).
func (b *Bootstrapper) EnsureChildWorkspace(ctx context.Context, orgUUID, wsUUID string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("EnsureChildWorkspace: orgUUID and wsUUID are required")
	}
	logger := klog.FromContext(ctx).WithValues("orgUUID", orgUUID, "wsUUID", wsUUID)

	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}

	ws := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tenancy.kcp.io/v1alpha1",
			"kind":       "Workspace",
			"metadata": map[string]interface{}{
				"name": wsUUID,
			},
			"spec": map[string]interface{}{
				"type": map[string]interface{}{
					"name": "workspace",
					"path": "root:railgrid",
				},
			},
		},
	}

	if _, err := orgClient.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{}); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating child Workspace %s in org %s: %w", wsUUID, orgUUID, err)
		}
		logger.V(4).Info("Child Workspace already exists")
	} else {
		logger.Info("Created child Workspace")
	}

	if err := waitForWorkspaceReady(ctx, orgClient, wsUUID); err != nil {
		return fmt.Errorf("waiting for child Workspace %s in org %s: %w", wsUUID, orgUUID, err)
	}
	return nil
}

// childWorkspacePath returns the canonical kcp workspace path for the
// default team Workspace inside an Organization workspace. Centralized
// here so all helpers compute it the same way.
func childWorkspacePath(orgUUID, wsUUID string) string {
	return kcppaths.WorkspacePath(orgUUID, wsUUID)
}

// ChildWorkspaceConfig returns a rest.Config targeting the child
// Workspace at root:railgrid:tenants:{orgUUID}:{wsUUID}. Used by REST
// endpoints that operate inside a Workspace (e.g. the ServiceAccount
// surface) so they can mint a typed kube clientset without rebuilding
// path strings themselves.
func (b *Bootstrapper) ChildWorkspaceConfig(orgUUID, wsUUID string) *rest.Config {
	return configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
}

// GetChildWorkspaceClusterName returns the kcp logical-cluster short
// hash (e.g. "2mmugqjf6k4nwuve") for the child team Workspace at
// root:railgrid:tenants:{orgUUID}:{wsUUID}. kcp sets it in
// Workspace.spec.cluster when the workspace reaches phase Ready;
// EnsureChildWorkspace blocks on Ready, so by the time this method is
// called the field is populated. The short hash is the form kubectl /
// the kcp proxy address by — using the full path in kubeconfigs makes
// for ugly URLs and breaks tools that index on cluster name.
func (b *Bootstrapper) GetChildWorkspaceClusterName(ctx context.Context, orgUUID, wsUUID string) (string, error) {
	if orgUUID == "" || wsUUID == "" {
		return "", fmt.Errorf("GetChildWorkspaceClusterName: orgUUID and wsUUID are required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return "", fmt.Errorf("creating org workspace client: %w", err)
	}
	ws, err := orgClient.Resource(workspaceGVR).Get(ctx, wsUUID, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting Workspace %s in org %s: %w", wsUUID, orgUUID, err)
	}
	cluster, _, _ := unstructured.NestedString(ws.Object, "spec", "cluster")
	if cluster == "" {
		return "", fmt.Errorf("workspace %s in org %s has empty spec.cluster (not Ready yet?)", wsUUID, orgUUID)
	}
	return cluster, nil
}

// EnsureChildWorkspaceRailgridBinding creates an APIBinding to
// root:railgrid:providers.core.railgrid.ai inside the child team Workspace,
// accepting the permission claims railgrid controllers need. This is what
// makes Edge, MCPServer, Placement, VirtualWorkload usable inside the
// user's default Workspace.
//
// The legacy tenant-workspace path (CreateTenantWorkspace) used to
// create the same binding inside root:railgrid:tenants:{userID}. PR #211
// retires that flow; the bootstrap controller now drives this method
// for every personal-Org default Workspace.
//
// The tenancy.kcp.io `workspaces` claim IS accepted here. It does not
// widen the tenant user's own RBAC — a permission claim grants the
// APIExport's controllers (railgrid, running over the core.railgrid.ai virtual
// workspace) access to Workspace objects inside this child Workspace.
// The edge mount reconciler needs it: it creates an `edge`-typed mount
// Workspace per kubernetes Edge and watches it via Owns(&Workspace{})
// (pkg/hub/controllers/edge/mount_reconciler.go). The core.railgrid.ai
// APIExport already declares this claim (config/kcp/apiexport-core.railgrid.ai.yaml);
// leaving it unaccepted is what produced the "exported but not specified"
// reconcile warnings on the binding.
//
// Preventing tenants from creating arbitrary child workspaces is a
// SEPARATE control, enforced by the `workspace` WorkspaceType
// (limitAllowedChildren caps children to the leaf `edge` type — see
// config/kcp/workspacetype-workspace.yaml), not by withholding this claim.
func (b *Bootstrapper) EnsureChildWorkspaceRailgridBinding(ctx context.Context, orgUUID, wsUUID string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("EnsureChildWorkspaceRailgridBinding: orgUUID and wsUUID are required")
	}
	wsConfig := configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
	wsClient, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return fmt.Errorf("creating child workspace client: %w", err)
	}

	allVerbs := []string{"get", "list", "watch", "create", "update", "delete"}
	binding := &apisv1alpha2.APIBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apisv1alpha2.SchemeGroupVersion.String(),
			Kind:       "APIBinding",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "railgrid"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: kcppaths.SystemControllers,
					Name: "core.railgrid.ai",
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				acceptedClaim("", "secrets", "", allVerbs),
				acceptedClaim("", "namespaces", "", []string{"get", "list", "watch", "create"}),
				acceptedClaim("", "configmaps", "", allVerbs),
				acceptedClaim("", "serviceaccounts", "", allVerbs),
				acceptedClaim("rbac.authorization.k8s.io", "clusterroles", "", allVerbs),
				acceptedClaim("rbac.authorization.k8s.io", "clusterrolebindings", "", allVerbs),
				// tenancy.kcp.io/workspaces, scoped by the tenancy APIExport's
				// identity hash, must match the claim the core.railgrid.ai export
				// declares. The edge mount reconciler creates/deletes and
				// Owns(&Workspace{}) the per-edge mount workspaces, so it needs
				// the full verb set the export offers.
				acceptedClaim("tenancy.kcp.io", "workspaces", b.workspaceIdentityHash, allVerbs),
			},
		},
	}
	u, err := toUnstructured(binding)
	if err != nil {
		return fmt.Errorf("converting railgrid APIBinding to unstructured: %w", err)
	}
	if _, err := wsClient.Resource(apiBindingGVR).Create(ctx, u, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating railgrid APIBinding in %s/%s: %w", orgUUID, wsUUID, err)
	}
	if err := waitForAPIBindingBound(ctx, wsClient, "railgrid"); err != nil {
		return err
	}
	// The `workspace` WorkspaceType deliberately does NOT extend
	// root:universal (see config/kcp/workspacetype-workspace.yaml for
	// the rationale), so kcp does not auto-create the `default`
	// namespace. Create it ourselves once the railgrid APIBinding's
	// namespaces permission claim has been accepted — without this,
	// `kubectl apply` for any namespaced resource fails with
	// `namespaces "default" not found`.
	return ensureDefaultNamespace(ctx, wsClient)
}

// ensureDefaultNamespace creates the `default` Namespace in the given
// workspace. Idempotent on AlreadyExists. Used to compensate for the
// workspace WorkspaceType dropping `extend: universal`.
func ensureDefaultNamespace(ctx context.Context, wsClient dynamic.Interface) error {
	ns := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": map[string]interface{}{
				"name": "default",
			},
		},
	}
	if _, err := wsClient.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Create(ctx, ns, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating default namespace: %w", err)
	}
	return nil
}

// EnsureChildWorkspaceAdmin grants cluster-admin in the child team
// Workspace to the given rbacIdentity. Thin wrapper over
// EnsureWorkspaceAdmin with the canonical child-workspace path.
// Idempotent.
func (b *Bootstrapper) EnsureChildWorkspaceAdmin(ctx context.Context, orgUUID, wsUUID, rbacIdentity string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("EnsureChildWorkspaceAdmin: orgUUID and wsUUID are required")
	}
	return b.EnsureWorkspaceAdmin(ctx, childWorkspacePath(orgUUID, wsUUID), rbacIdentity)
}

// EnsureChildWorkspaceDefaultMCPServer seeds the "default" MCPServer
// CR inside the child team Workspace. Thin wrapper over
// EnsureDefaultMCPServer with the canonical child-workspace path.
// Idempotent.
func (b *Bootstrapper) EnsureChildWorkspaceDefaultMCPServer(ctx context.Context, orgUUID, wsUUID string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("EnsureChildWorkspaceDefaultMCPServer: orgUUID and wsUUID are required")
	}
	return b.EnsureDefaultMCPServer(ctx, childWorkspacePath(orgUUID, wsUUID))
}

// WorkspaceDeletionAnnotation is the annotation key used to mark a kcp
// Workspace as soft-deleted. The soft-delete reconciler (roadmap step 8)
// reads this on every reconcile and triggers the cascade once the
// 30-day grace window from the annotation's RFC3339 value has elapsed.
// kept on the kcp Workspace (rather than a railgrid wrapper CRD) because
// the kcp Workspace IS the source of truth for workspace lifecycle.
const WorkspaceDeletionAnnotation = "tenants.railgrid.ai/deletion-requested-at"

// DeleteOrgWorkspace removes the kcp Workspace at
// root:railgrid:tenants:{orgUUID}. Idempotent on NotFound. Cascade callers
// should ensure all child Workspaces and the in-workspace Memberships
// have already been removed; kcp will delete the LogicalCluster.
func (b *Bootstrapper) DeleteOrgWorkspace(ctx context.Context, orgUUID string) error {
	if orgUUID == "" {
		return fmt.Errorf("DeleteOrgWorkspace: orgUUID is required")
	}
	orgsClient, err := dynamic.NewForConfig(b.OrgsConfig())
	if err != nil {
		return fmt.Errorf("creating orgs workspace client: %w", err)
	}
	if err := orgsClient.Resource(workspaceGVR).Delete(ctx, orgUUID, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting Org Workspace %s: %w", orgUUID, err)
	}
	return nil
}

// DeleteChildWorkspace removes the kcp Workspace at
// root:railgrid:tenants:{orgUUID}:{wsUUID}. Idempotent on NotFound.
func (b *Bootstrapper) DeleteChildWorkspace(ctx context.Context, orgUUID, wsUUID string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("DeleteChildWorkspace: orgUUID and wsUUID are required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}
	if err := orgClient.Resource(workspaceGVR).Delete(ctx, wsUUID, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting child Workspace %s in org %s: %w", wsUUID, orgUUID, err)
	}
	return nil
}

// ListChildWorkspaces returns the names of every child Workspace under
// root:railgrid:tenants:{orgUUID}. Empty list if the Org workspace is gone.
func (b *Bootstrapper) ListChildWorkspaces(ctx context.Context, orgUUID string) ([]string, error) {
	if orgUUID == "" {
		return nil, fmt.Errorf("ListChildWorkspaces: orgUUID is required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return nil, fmt.Errorf("creating org workspace client: %w", err)
	}
	list, err := orgClient.Resource(workspaceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing child Workspaces in org %s: %w", orgUUID, err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].GetName())
	}
	return names, nil
}

// ListChildTeamWorkspaces is ListChildWorkspaces without the infrastructure
// children an Org workspace can also hold — today just the well-known
// `providers` container for org-owned providers.
//
// Every tenant-facing consumer wants this one: the workspace switcher, the
// admin workspace list, and the kcp proxy's authorizer all treat a child of an
// Org as "a team workspace a member can belong to". The providers container is
// neither — it holds no Memberships and no UMI rows, so surfacing it yields a
// workspace row that cannot be entered (selecting it sets an X-Railgrid-Workspace
// no membership matches, and every subsequent call 403s), and authorizing it
// would hand every org member access to the workspace that parents provider
// credentials.
//
// ListChildWorkspaces stays unfiltered for lifecycle callers: the Org deletion
// cascade must delete the container too.
func (b *Bootstrapper) ListChildTeamWorkspaces(ctx context.Context, orgUUID string) ([]string, error) {
	names, err := b.ListChildWorkspaces(ctx, orgUUID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == kcppaths.OrgProvidersWorkspaceName {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// ListOrgWorkspaces returns the names (UUIDs) of every Organization
// workspace at root:railgrid:tenants. Used by the soft-delete reconciler's
// Workspace branch to fan out across Orgs at resync time without
// standing up per-Org dynamic informers.
func (b *Bootstrapper) ListOrgWorkspaces(ctx context.Context) ([]string, error) {
	orgsClient, err := dynamic.NewForConfig(b.OrgsConfig())
	if err != nil {
		return nil, fmt.Errorf("creating orgs workspace client: %w", err)
	}
	list, err := orgsClient.Resource(workspaceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing Org Workspaces: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].GetName())
	}
	return names, nil
}

// GetWorkspaceDeletionRequestedAt reads the soft-delete annotation
// (WorkspaceDeletionAnnotation) from the child Workspace and returns
// the parsed timestamp. The bool reports whether the annotation was
// present at all (so callers can distinguish "no soft-delete requested"
// from "annotation present but malformed" — malformed surfaces as an
// error rather than a missing timestamp).
func (b *Bootstrapper) GetWorkspaceDeletionRequestedAt(ctx context.Context, orgUUID, wsUUID string) (*time.Time, bool, error) {
	if orgUUID == "" || wsUUID == "" {
		return nil, false, fmt.Errorf("GetWorkspaceDeletionRequestedAt: orgUUID and wsUUID are required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return nil, false, fmt.Errorf("creating org workspace client: %w", err)
	}
	ws, err := orgClient.Resource(workspaceGVR).Get(ctx, wsUUID, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("getting Workspace %s in org %s: %w", wsUUID, orgUUID, err)
	}
	raw, found, _ := unstructured.NestedString(ws.Object, "metadata", "annotations", WorkspaceDeletionAnnotation)
	if !found || raw == "" {
		return nil, false, nil
	}
	t, parseErr := time.Parse(time.RFC3339, raw)
	if parseErr != nil {
		return nil, true, fmt.Errorf("parsing %s annotation on workspace %s/%s: %w", WorkspaceDeletionAnnotation, orgUUID, wsUUID, parseErr)
	}
	return &t, true, nil
}

// DeleteOrgMemberships removes every Membership CR inside the
// Organization workspace at root:railgrid:tenants:{orgUUID}. Used by the
// soft-delete cascade right before tearing down the workspace itself,
// so the index sync sees a clean delta. Idempotent on NotFound /
// empty list.
func (b *Bootstrapper) DeleteOrgMemberships(ctx context.Context, orgUUID string) error {
	if orgUUID == "" {
		return fmt.Errorf("DeleteOrgMemberships: orgUUID is required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}
	list, err := orgClient.Resource(membershipGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("listing Memberships in org %s: %w", orgUUID, err)
	}
	for i := range list.Items {
		name := list.Items[i].GetName()
		if err := orgClient.Resource(membershipGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting Membership %s in org %s: %w", name, orgUUID, err)
		}
	}
	return nil
}

// WorkspaceDisplayNameAnnotation is the annotation key used to mark
// the human-facing display name on a kcp Workspace. v1 stores it as
// an annotation rather than a separate CRD field because kcp's
// Workspace type doesn't carry a displayName slot. Editable via the
// REST PATCH endpoint.
const WorkspaceDisplayNameAnnotation = "tenants.railgrid.ai/display-name"

// SetWorkspaceDeletionAnnotation stamps the kcp Workspace at
// root:railgrid:tenants:{orgUUID}:{wsUUID} with the soft-delete annotation
// (WorkspaceDeletionAnnotation) carrying the given timestamp. The
// soft-delete reconciler picks it up on its next poll. Once a deletion
// timestamp exists it is never replaced: the first request owns the
// recovery-window start, and retries are successful no-ops. A concurrent
// update is retried against a fresh Workspace so it cannot reset that clock.
func (b *Bootstrapper) SetWorkspaceDeletionAnnotation(ctx context.Context, orgUUID, wsUUID string, at time.Time) error {
	return b.patchWorkspaceDeletionAnnotationIfAbsent(ctx, orgUUID, wsUUID, at.UTC().Format(time.RFC3339))
}

// ClearWorkspaceDeletionAnnotation removes the soft-delete annotation
// from the Workspace, signalling undelete. Idempotent on already-absent.
// It performs one conditional update and returns conflicts to the REST layer,
// which re-reads the annotation before deciding whether a retry is safe. That
// boundary prevents a stale clear from deleting a newer deletion request.
func (b *Bootstrapper) ClearWorkspaceDeletionAnnotation(ctx context.Context, orgUUID, wsUUID string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("ClearWorkspaceDeletionAnnotation: orgUUID and wsUUID are required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}
	ws, err := orgClient.Resource(workspaceGVR).Get(ctx, wsUUID, metav1.GetOptions{})
	if err != nil {
		return err
	}
	annos, _, _ := unstructured.NestedStringMap(ws.Object, "metadata", "annotations")
	if _, present := annos[WorkspaceDeletionAnnotation]; !present {
		return nil
	}
	delete(annos, WorkspaceDeletionAnnotation)
	if err := unstructured.SetNestedStringMap(ws.Object, annos, "metadata", "annotations"); err != nil {
		return fmt.Errorf("setting annotations: %w", err)
	}
	if _, err := orgClient.Resource(workspaceGVR).Update(ctx, ws, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating workspace %s/%s: %w", orgUUID, wsUUID, err)
	}
	return nil
}

// SetWorkspaceDisplayName stamps / overwrites the display-name
// annotation on the Workspace.
func (b *Bootstrapper) SetWorkspaceDisplayName(ctx context.Context, orgUUID, wsUUID, displayName string) error {
	return b.patchWorkspaceAnnotation(ctx, orgUUID, wsUUID, WorkspaceDisplayNameAnnotation, displayName)
}

// EnsureChildWorkspaceDisplayName stamps the display-name annotation
// only when the Workspace does not carry one yet. Reconcilers use this
// to give bootstrap-provisioned workspaces a human-readable default
// without clobbering a rename the user made since — the absence check
// runs inside the update loop, so a rename that lands mid-flight wins.
func (b *Bootstrapper) EnsureChildWorkspaceDisplayName(ctx context.Context, orgUUID, wsUUID, displayName string) error {
	return b.patchWorkspaceAnnotationIfAbsent(ctx, orgUUID, wsUUID, WorkspaceDisplayNameAnnotation, displayName)
}

// GetWorkspaceDisplayName reads the display-name annotation. Empty
// string if absent. Workspace-not-found surfaces as IsNotFound.
func (b *Bootstrapper) GetWorkspaceDisplayName(ctx context.Context, orgUUID, wsUUID string) (string, error) {
	if orgUUID == "" || wsUUID == "" {
		return "", fmt.Errorf("GetWorkspaceDisplayName: orgUUID and wsUUID are required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return "", fmt.Errorf("creating org workspace client: %w", err)
	}
	ws, err := orgClient.Resource(workspaceGVR).Get(ctx, wsUUID, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	v, _, _ := unstructured.NestedString(ws.Object, "metadata", "annotations", WorkspaceDisplayNameAnnotation)
	return v, nil
}

// patchWorkspaceAnnotation centralises the get-modify-update dance
// for annotation writes on the parent's Workspace CR. value="" means
// "remove the annotation".
func (b *Bootstrapper) patchWorkspaceAnnotation(ctx context.Context, orgUUID, wsUUID, key, value string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("patchWorkspaceAnnotation: orgUUID and wsUUID are required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		ws, err := orgClient.Resource(workspaceGVR).Get(ctx, wsUUID, metav1.GetOptions{})
		if err != nil {
			return err
		}
		annos, _, _ := unstructured.NestedStringMap(ws.Object, "metadata", "annotations")
		if annos == nil {
			annos = map[string]string{}
		}
		if value == "" {
			if _, present := annos[key]; !present {
				return nil
			}
			delete(annos, key)
		} else {
			if existing := annos[key]; existing == value {
				return nil
			}
			annos[key] = value
		}
		if err := unstructured.SetNestedStringMap(ws.Object, annos, "metadata", "annotations"); err != nil {
			return fmt.Errorf("setting annotations: %w", err)
		}
		if _, err := orgClient.Resource(workspaceGVR).Update(ctx, ws, metav1.UpdateOptions{}); err == nil {
			return nil
		} else {
			lastErr = err
			if !errors.IsConflict(err) {
				return fmt.Errorf("updating workspace %s/%s: %w", orgUUID, wsUUID, err)
			}
		}
	}
	return fmt.Errorf("updating workspace %s/%s after %d conflicts: %w", orgUUID, wsUUID, maxAttempts, lastErr)
}

// patchWorkspaceDeletionAnnotationIfAbsent is the conditional variant used
// by SetWorkspaceDeletionAnnotation. A plain get/modify/update would let a
// second DELETE replace the first timestamp when it races with the original
// request. The conditional read is repeated after conflicts and treats any
// existing value (including an older or malformed value) as authoritative.
func (b *Bootstrapper) patchWorkspaceDeletionAnnotationIfAbsent(ctx context.Context, orgUUID, wsUUID, value string) error {
	return b.patchWorkspaceAnnotationIfAbsent(ctx, orgUUID, wsUUID, WorkspaceDeletionAnnotation, value)
}

// patchWorkspaceAnnotationIfAbsent writes an annotation only while the
// Workspace carries no non-empty value for it. Unlike
// patchWorkspaceAnnotation, the absence check runs on every attempt of
// the get/modify/update loop: a competing writer that lands first —
// either before the first read or via a conflict retry — is treated as
// authoritative and the write is dropped.
func (b *Bootstrapper) patchWorkspaceAnnotationIfAbsent(ctx context.Context, orgUUID, wsUUID, key, value string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("patchWorkspaceAnnotationIfAbsent: orgUUID and wsUUID are required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}

	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		ws, err := orgClient.Resource(workspaceGVR).Get(ctx, wsUUID, metav1.GetOptions{})
		if err != nil {
			return err
		}
		annos, _, _ := unstructured.NestedStringMap(ws.Object, "metadata", "annotations")
		if existing, present := annos[key]; present && existing != "" {
			return nil
		}
		if annos == nil {
			annos = map[string]string{}
		}
		annos[key] = value
		if err := unstructured.SetNestedStringMap(ws.Object, annos, "metadata", "annotations"); err != nil {
			return fmt.Errorf("setting annotations: %w", err)
		}
		if _, err := orgClient.Resource(workspaceGVR).Update(ctx, ws, metav1.UpdateOptions{}); err == nil {
			return nil
		} else {
			lastErr = err
			if !errors.IsConflict(err) {
				return fmt.Errorf("updating workspace %s/%s: %w", orgUUID, wsUUID, err)
			}
		}
	}
	return fmt.Errorf("updating workspace %s/%s after %d conflicts: %w", orgUUID, wsUUID, maxAttempts, lastErr)
}

// GetOrgMembershipRole returns the role of a single Membership in the
// Org workspace. NotFound if the user has no Membership; "" plus nil
// error if the Membership exists but has no role (shouldn't happen).
func (b *Bootstrapper) GetOrgMembershipRole(ctx context.Context, orgUUID, userName string) (string, error) {
	if orgUUID == "" || userName == "" {
		return "", fmt.Errorf("GetOrgMembershipRole: orgUUID and userName are required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return "", fmt.Errorf("creating org workspace client: %w", err)
	}
	got, err := orgClient.Resource(membershipGVR).Get(ctx, userName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	role, _, _ := unstructured.NestedString(got.Object, "spec", "role")
	return role, nil
}

// PatchOrgMembershipRole updates the role on an existing Membership
// CR in the Org workspace. NotFound if the Membership doesn't exist.
// No-op when the role already matches.
func (b *Bootstrapper) PatchOrgMembershipRole(ctx context.Context, orgUUID, userName, role string) error {
	if orgUUID == "" || userName == "" {
		return fmt.Errorf("PatchOrgMembershipRole: orgUUID and userName are required")
	}
	if role != "admin" && role != "member" {
		return fmt.Errorf("PatchOrgMembershipRole: invalid role %q", role)
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}
	got, err := orgClient.Resource(membershipGVR).Get(ctx, userName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	current, _, _ := unstructured.NestedString(got.Object, "spec", "role")
	if current == role {
		return nil
	}
	if err := unstructured.SetNestedField(got.Object, role, "spec", "role"); err != nil {
		return fmt.Errorf("setting spec.role: %w", err)
	}
	if _, err := orgClient.Resource(membershipGVR).Update(ctx, got, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating Membership %s in org %s: %w", userName, orgUUID, err)
	}
	return nil
}

// DeleteOrgMembership removes a single Membership CR from the Org
// workspace. Idempotent on NotFound.
func (b *Bootstrapper) DeleteOrgMembership(ctx context.Context, orgUUID, userName string) error {
	if orgUUID == "" || userName == "" {
		return fmt.Errorf("DeleteOrgMembership: orgUUID and userName are required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}
	if err := orgClient.Resource(membershipGVR).Delete(ctx, userName, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting Membership %s in org %s: %w", userName, orgUUID, err)
	}
	return nil
}

// ListOrgMemberships returns the user names (Membership.metadata.name)
// of every Membership in the Organization workspace. Used by the
// soft-delete cascade to find which UMIs to mark / strip when an Org
// or one of its Workspaces enters / exits its grace window. Empty
// slice if the Org workspace has no Memberships or has been deleted.
func (b *Bootstrapper) ListOrgMemberships(ctx context.Context, orgUUID string) ([]string, error) {
	if orgUUID == "" {
		return nil, fmt.Errorf("ListOrgMemberships: orgUUID is required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return nil, fmt.Errorf("creating org workspace client: %w", err)
	}
	list, err := orgClient.Resource(membershipGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing Memberships in org %s: %w", orgUUID, err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].GetName())
	}
	return names, nil
}

// ListOrgMembershipRoles returns user name → role for every Membership in
// the Organization workspace. Used to find the Org's admins when a child
// workspace is created, so O-15 (org admin = implicit admin in every child
// workspace) holds at the kcp RBAC layer too. Empty map if the Org
// workspace has no Memberships or has been deleted.
func (b *Bootstrapper) ListOrgMembershipRoles(ctx context.Context, orgUUID string) (map[string]string, error) {
	if orgUUID == "" {
		return nil, fmt.Errorf("ListOrgMembershipRoles: orgUUID is required")
	}
	orgConfig := configForPath(b.config, kcppaths.OrgPath(orgUUID))
	orgClient, err := dynamic.NewForConfig(orgConfig)
	if err != nil {
		return nil, fmt.Errorf("creating org workspace client: %w", err)
	}
	list, err := orgClient.Resource(membershipGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("listing Memberships in org %s: %w", orgUUID, err)
	}
	roles := make(map[string]string, len(list.Items))
	for i := range list.Items {
		role, _, _ := unstructured.NestedString(list.Items[i].Object, "spec", "role")
		roles[list.Items[i].GetName()] = role
	}
	return roles, nil
}

// mcpServerGVR is the tenant-workspace MCPServer resource (distributed via the
// core.railgrid.ai APIExport). The in-core reconciler
// (pkg/hub/controllers/mcpserver) provisions each server's identity.
var mcpServerGVR = schema.GroupVersionResource{Group: "railgrid.ai", Version: "v1alpha1", Resource: "mcpservers"}

// MCPServerInfo is a portal-facing view of an MCPServer CR.
type MCPServerInfo struct {
	Name         string `json:"name"`
	DisplayName  string `json:"displayName,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	ReadOnly     bool   `json:"readOnly,omitempty"`
	Phase        string `json:"phase,omitempty"`
	URL          string `json:"url,omitempty"`
	// FederatedProviders is the live tool inventory the reconciler stamped on
	// status.federatedProviders — which providers this server federates and the
	// tools each advertises to it.
	FederatedProviders []MCPFederatedProviderInfo `json:"federatedProviders,omitempty"`
	// ToolsRefreshedTime is when the inventory above was last recomputed (RFC3339).
	ToolsRefreshedTime string `json:"toolsRefreshedTime,omitempty"`
}

// MCPFederatedProviderInfo mirrors railgridv1alpha1.FederatedMCPProvider for the portal.
type MCPFederatedProviderInfo struct {
	Name        string                 `json:"name"`
	DisplayName string                 `json:"displayName,omitempty"`
	Reachable   bool                   `json:"reachable"`
	Message     string                 `json:"message,omitempty"`
	Tools       []MCPFederatedToolInfo `json:"tools,omitempty"`
}

// MCPFederatedToolInfo mirrors railgridv1alpha1.FederatedMCPTool for the portal.
type MCPFederatedToolInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// EnsureDefaultMCPServer creates a "default" MCPServer CR in the tenant
// workspace if absent, so every tenant has one ready-to-use endpoint. The
// in-core reconciler provisions its identity. Idempotent.
func (b *Bootstrapper) EnsureDefaultMCPServer(ctx context.Context, clusterName string) error {
	if clusterName == "" {
		return nil
	}
	if err := b.CreateMCPServer(ctx, clusterName, "default", "Default", "", false); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (b *Bootstrapper) mcpClient(clusterName string) (dynamic.ResourceInterface, error) {
	dc, err := dynamic.NewForConfig(configForPath(b.config, clusterName))
	if err != nil {
		return nil, fmt.Errorf("creating tenant client for %s: %w", clusterName, err)
	}
	return dc.Resource(mcpServerGVR), nil
}

func mcpInfoFrom(obj *unstructured.Unstructured) MCPServerInfo {
	displayName, _, _ := unstructured.NestedString(obj.Object, "spec", "displayName")
	instructions, _, _ := unstructured.NestedString(obj.Object, "spec", "instructions")
	readOnly, _, _ := unstructured.NestedBool(obj.Object, "spec", "readOnly")
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	url, _, _ := unstructured.NestedString(obj.Object, "status", "URL")
	refreshed, _, _ := unstructured.NestedString(obj.Object, "status", "toolsRefreshedTime")
	return MCPServerInfo{
		Name:               obj.GetName(),
		DisplayName:        displayName,
		Instructions:       instructions,
		ReadOnly:           readOnly,
		Phase:              phase,
		URL:                url,
		FederatedProviders: mcpFederatedProvidersFrom(obj.Object),
		ToolsRefreshedTime: refreshed,
	}
}

// mcpFederatedProvidersFrom projects status.federatedProviders (an unstructured
// slice stamped by the reconciler) into the portal view type.
func mcpFederatedProvidersFrom(obj map[string]interface{}) []MCPFederatedProviderInfo {
	raw, found, err := unstructured.NestedSlice(obj, "status", "federatedProviders")
	if !found || err != nil {
		return nil
	}
	out := make([]MCPFederatedProviderInfo, 0, len(raw))
	for _, item := range raw {
		p, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(p, "name")
		display, _, _ := unstructured.NestedString(p, "displayName")
		reachable, _, _ := unstructured.NestedBool(p, "reachable")
		message, _, _ := unstructured.NestedString(p, "message")
		info := MCPFederatedProviderInfo{Name: name, DisplayName: display, Reachable: reachable, Message: message}
		if toolsRaw, found, _ := unstructured.NestedSlice(p, "tools"); found {
			for _, t := range toolsRaw {
				tm, ok := t.(map[string]interface{})
				if !ok {
					continue
				}
				tn, _, _ := unstructured.NestedString(tm, "name")
				tt, _, _ := unstructured.NestedString(tm, "title")
				td, _, _ := unstructured.NestedString(tm, "description")
				info.Tools = append(info.Tools, MCPFederatedToolInfo{Name: tn, Title: tt, Description: td})
			}
		}
		out = append(out, info)
	}
	return out
}

// ListMCPServers returns every MCPServer in the tenant workspace.
func (b *Bootstrapper) ListMCPServers(ctx context.Context, clusterName string) ([]MCPServerInfo, error) {
	res, err := b.mcpClient(clusterName)
	if err != nil {
		return nil, err
	}
	list, err := res.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]MCPServerInfo, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, mcpInfoFrom(&list.Items[i]))
	}
	return out, nil
}

// CreateMCPServer creates an MCPServer CR. The reconciler provisions identity.
func (b *Bootstrapper) CreateMCPServer(ctx context.Context, clusterName, name, displayName, instructions string, readOnly bool) error {
	res, err := b.mcpClient(clusterName)
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "railgrid.ai/v1alpha1",
		"kind":       "MCPServer",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"displayName":  displayName,
			"instructions": instructions,
			"readOnly":     readOnly,
		},
	}}
	_, err = res.Create(ctx, obj, metav1.CreateOptions{})
	return err
}

// UpdateMCPServer patches an MCPServer's spec fields.
func (b *Bootstrapper) UpdateMCPServer(ctx context.Context, clusterName, name, displayName, instructions string, readOnly bool) error {
	res, err := b.mcpClient(clusterName)
	if err != nil {
		return err
	}
	obj, err := res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	_ = unstructured.SetNestedField(obj.Object, displayName, "spec", "displayName")
	_ = unstructured.SetNestedField(obj.Object, instructions, "spec", "instructions")
	_ = unstructured.SetNestedField(obj.Object, readOnly, "spec", "readOnly")
	_, err = res.Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// DeleteMCPServer removes an MCPServer; its identity objects GC via owner refs.
func (b *Bootstrapper) DeleteMCPServer(ctx context.Context, clusterName, name string) error {
	res, err := b.mcpClient(clusterName)
	if err != nil {
		return err
	}
	return res.Delete(ctx, name, metav1.DeleteOptions{})
}

// GetMCPServerToken reads the long-lived token for a named MCPServer by
// following status.tokenSecretRef. Returns "" (not an error) when the server or
// token is not provisioned yet, so the UI can show a "provisioning" state.
func (b *Bootstrapper) GetMCPServerToken(ctx context.Context, clusterName, name string) (string, error) {
	if clusterName == "" || name == "" {
		return "", nil
	}
	res, err := b.mcpClient(clusterName)
	if err != nil {
		return "", err
	}
	obj, err := res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	ref, found, _ := unstructured.NestedMap(obj.Object, "status", "tokenSecretRef")
	if !found {
		return "", nil
	}
	secretName, _ := ref["name"].(string)
	secretNS, _ := ref["namespace"].(string)
	if secretName == "" || secretNS == "" {
		return "", nil
	}
	kube, err := kubernetes.NewForConfig(configForPath(b.config, clusterName))
	if err != nil {
		return "", fmt.Errorf("creating tenant kube client for %s: %w", clusterName, err)
	}
	secret, err := kube.CoreV1().Secrets(secretNS).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading MCP token Secret %s/%s: %w", secretNS, secretName, err)
	}
	return string(secret.Data["token"]), nil
}

// catalogEntryGVR is the resource the bootstrap writes when materializing
// the first-party CatalogEntries declared by the providers/<name>/
// packages (registered via providers.RegisterBuiltin in their init()).
var catalogEntryGVR = schema.GroupVersionResource{
	Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries",
}

// builtinAnnotation marks CatalogEntries the hub bootstrap owns. The
// reconcile-delete step ignores any entry without this annotation, so a
// third-party CatalogEntry that happens to share a name with a deleted
// builtin is never touched.
const builtinAnnotation = "providers.railgrid.ai/builtin"

// ValidateProviders is a thin re-export of providers.ResolveEnabledBuiltins
// that discards the resolved spec list. Used at process start (server.Run)
// to fail fast on a bad --providers flag BEFORE embedded kcp boots —
// callers in this package import providers anyway for the registry, so
// the indirection only saves callers in pkg/hub from learning about the
// providers.BuiltinSpec type when they just want a yes/no answer.
func ValidateProviders(enabled []string) error {
	_, err := providers.ResolveEnabledBuiltins(enabled)
	return err
}

// ensureBuiltinCatalogEntries reconciles the enabled set against kcp:
// writes (or updates) every entry in `enabled`, and deletes any
// builtin-annotated entries that the user has disabled since the last
// start. Third-party CatalogEntries with the same name are left alone —
// only entries carrying providers.railgrid.ai/builtin=true are touched.
//
// Waits for the providers.railgrid.ai APIBinding to be Bound first;
// without that wait the CatalogEntry resource isn't discoverable yet on
// a fresh hub and we'd race-fail with "no matches for kind".
func ensureBuiltinCatalogEntries(ctx context.Context, providersDynamic dynamic.Interface, enabled []string) error {
	if err := waitForAPIBindingBound(ctx, providersDynamic, "providers.railgrid.ai"); err != nil {
		return fmt.Errorf("waiting for providers.railgrid.ai APIBinding: %w", err)
	}
	picked, err := providers.ResolveEnabledBuiltins(enabled)
	if err != nil {
		return err
	}

	// Apply each enabled entry.
	enabledSet := map[string]struct{}{}
	for _, e := range picked {
		enabledSet[e.Name] = struct{}{}

		ui := map[string]interface{}{
			"builtinRoute": e.BuiltinRoute,
		}
		if len(e.Children) > 0 {
			children := make([]interface{}, 0, len(e.Children))
			for _, c := range e.Children {
				children = append(children, map[string]interface{}{
					"displayName":  c.DisplayName,
					"builtinRoute": c.BuiltinRoute,
				})
			}
			ui["children"] = children
		}

		desired := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "providers.railgrid.ai/v1alpha1",
			"kind":       "CatalogEntry",
			"metadata": map[string]interface{}{
				"name":        e.Name,
				"annotations": map[string]interface{}{builtinAnnotation: "true"},
			},
			"spec": map[string]interface{}{
				"displayName": e.DisplayName,
				"description": e.Description,
				"vendor":      "railgrid",
				"iconURL":     e.IconURL,
				"category":    e.Category,
				"ui":          ui,
			},
		}}
		existing, err := providersDynamic.Resource(catalogEntryGVR).Get(ctx, e.Name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			if _, err := providersDynamic.Resource(catalogEntryGVR).Create(ctx, desired, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("creating builtin CatalogEntry %s: %w", e.Name, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("getting builtin CatalogEntry %s: %w", e.Name, err)
		}
		if existing.GetAnnotations()[builtinAnnotation] != "true" {
			continue
		}
		desired.SetResourceVersion(existing.GetResourceVersion())
		if _, err := providersDynamic.Resource(catalogEntryGVR).Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating builtin CatalogEntry %s: %w", e.Name, err)
		}
	}

	// Reconcile delete: walk every annotated builtin currently in kcp
	// that isn't in the enabled set. This covers user removing a name
	// from --providers without manually `kubectl delete`-ing the entry.
	list, err := providersDynamic.Resource(catalogEntryGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing CatalogEntries for orphan cleanup: %w", err)
	}
	for _, item := range list.Items {
		anns := item.GetAnnotations()
		if anns[builtinAnnotation] != "true" {
			continue // not ours
		}
		name := item.GetName()
		if _, keep := enabledSet[name]; keep {
			continue
		}
		if err := providersDynamic.Resource(catalogEntryGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting orphan builtin CatalogEntry %s: %w", name, err)
		}
	}
	return nil
}

// ensureExportBinding creates (idempotently) an APIBinding in the workspace the
// given dynamic client targets, pointing at exportName located at exportPath.
// Used to bind platform exports (in system:controllers) into the workspaces
// that hold their objects (system:providers, system:tenants). Without the
// binding, kcp serves the export's schemas only to workspaces that bound it.
func ensureExportBinding(ctx context.Context, bindDynamic dynamic.Interface, exportPath, exportName string) error {
	bindingName := exportName

	existing, err := bindDynamic.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing APIBindings: %w", err)
	}
	for _, b := range existing.Items {
		path, _, _ := unstructured.NestedString(b.Object, "spec", "reference", "export", "path")
		name, _, _ := unstructured.NestedString(b.Object, "spec", "reference", "export", "name")
		if path == exportPath && name == exportName {
			return nil
		}
	}

	binding := &apisv1alpha2.APIBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apisv1alpha2.SchemeGroupVersion.String(),
			Kind:       "APIBinding",
		},
		ObjectMeta: metav1.ObjectMeta{Name: bindingName},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: exportPath,
					Name: exportName,
				},
			},
		},
	}
	u, err := toUnstructured(binding)
	if err != nil {
		return fmt.Errorf("converting %s APIBinding to unstructured: %w", exportName, err)
	}
	if _, err := bindDynamic.Resource(apiBindingGVR).Create(ctx, u, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating %s APIBinding: %w", exportName, err)
	}
	return nil
}

// EnsureWorkspaceAdmin ensures cluster-admin is granted to rbacIdentity in the
// workspace identified by clusterName. Idempotent — safe to call on every login.
func (b *Bootstrapper) EnsureWorkspaceAdmin(ctx context.Context, clusterName, rbacIdentity string) error {
	if clusterName == "" || rbacIdentity == "" {
		return nil
	}
	tenantConfig := configForPath(b.config, clusterName)
	tenantClient, err := dynamic.NewForConfig(tenantConfig)
	if err != nil {
		return fmt.Errorf("creating tenant client for %s: %w", clusterName, err)
	}
	return ensureWorkspaceAdmin(ctx, tenantClient, rbacIdentity)
}

// RevokeWorkspaceAdmin removes the cluster-admin grant for rbacIdentity in
// the workspace identified by clusterName. Idempotent on NotFound. Only the
// caller's own per-user binding is touched; every other member keeps theirs.
func (b *Bootstrapper) RevokeWorkspaceAdmin(ctx context.Context, clusterName, rbacIdentity string) error {
	if clusterName == "" || rbacIdentity == "" {
		return nil
	}
	tenantConfig := configForPath(b.config, clusterName)
	tenantClient, err := dynamic.NewForConfig(tenantConfig)
	if err != nil {
		return fmt.Errorf("creating tenant client for %s: %w", clusterName, err)
	}
	return revokeWorkspaceAdmin(ctx, tenantClient, rbacIdentity)
}

// RevokeChildWorkspaceAdmin is RevokeWorkspaceAdmin with the canonical
// child-workspace path. Idempotent.
func (b *Bootstrapper) RevokeChildWorkspaceAdmin(ctx context.Context, orgUUID, wsUUID, rbacIdentity string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("RevokeChildWorkspaceAdmin: orgUUID and wsUUID are required")
	}
	return b.RevokeWorkspaceAdmin(ctx, childWorkspacePath(orgUUID, wsUUID), rbacIdentity)
}

var clusterRoleBindingGVR = schema.GroupVersionResource{
	Group:    "rbac.authorization.k8s.io",
	Version:  "v1",
	Resource: "clusterrolebindings",
}

// legacyWorkspaceAdminCRB is the single, shared cluster-admin binding the
// hub used to keep per workspace. It held exactly one subject and every
// grant overwrote it, so the last person granted was the only one with
// kcp RBAC. ensureWorkspaceAdmin migrates it to per-user bindings on the
// next grant or revoke in that workspace and then deletes it.
const legacyWorkspaceAdminCRB = "railgrid-cluster-admin"

// userAdminCRBPrefix prefixes the per-user cluster-admin bindings. The
// suffix is a hash of the rbacIdentity: identities are emails, which are
// not valid object names, and the hash keeps the name stable across the
// identity's spelling while never colliding between users.
const userAdminCRBPrefix = "railgrid-user-admin-"

// userAdminCRBName returns the per-user cluster-admin binding name for an
// rbacIdentity.
func userAdminCRBName(rbacIdentity string) string {
	sum := sha256.Sum256([]byte(rbacIdentity))
	return userAdminCRBPrefix + hex.EncodeToString(sum[:])[:16]
}

func userAdminCRB(rbacIdentity string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRoleBinding",
			"metadata": map[string]interface{}{
				"name": userAdminCRBName(rbacIdentity),
				"labels": map[string]interface{}{
					"tenancy.railgrid.ai/managed-by": "hub",
				},
				"annotations": map[string]interface{}{
					"tenancy.railgrid.ai/rbac-identity": rbacIdentity,
				},
			},
			"roleRef": map[string]interface{}{
				"apiGroup": "rbac.authorization.k8s.io",
				"kind":     "ClusterRole",
				"name":     "cluster-admin",
			},
			"subjects": []interface{}{
				map[string]interface{}{
					"apiGroup": "rbac.authorization.k8s.io",
					"kind":     "User",
					"name":     rbacIdentity,
				},
			},
		},
	}
}

// ensureWorkspaceAdmin creates a per-user cluster-admin ClusterRoleBinding for
// the given rbacIdentity in the workspace targeted by tenantClient. Grants are
// additive: each member holds their own binding, so granting one user never
// disturbs another's access. Idempotent.
func ensureWorkspaceAdmin(ctx context.Context, tenantClient dynamic.Interface, rbacIdentity string) error {
	if err := migrateLegacyWorkspaceAdmin(ctx, tenantClient); err != nil {
		return err
	}
	want := userAdminCRB(rbacIdentity)
	_, err := tenantClient.Resource(clusterRoleBindingGVR).Create(ctx, want, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating workspace-admin ClusterRoleBinding: %w", err)
	}
	existing, getErr := tenantClient.Resource(clusterRoleBindingGVR).Get(ctx, want.GetName(), metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("getting existing workspace-admin ClusterRoleBinding: %w", getErr)
	}
	wantSubjects, _, _ := unstructured.NestedSlice(want.Object, "subjects")
	gotSubjects, _, _ := unstructured.NestedSlice(existing.Object, "subjects")
	if reflect.DeepEqual(gotSubjects, wantSubjects) {
		return nil
	}
	if err := unstructured.SetNestedSlice(existing.Object, wantSubjects, "subjects"); err != nil {
		return fmt.Errorf("rewriting workspace-admin subjects: %w", err)
	}
	if _, err := tenantClient.Resource(clusterRoleBindingGVR).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating workspace-admin ClusterRoleBinding: %w", err)
	}
	return nil
}

// revokeWorkspaceAdmin deletes the per-user cluster-admin binding for
// rbacIdentity. The legacy shared binding is migrated first so a user who
// only ever held access through it is revoked too. Idempotent on NotFound.
func revokeWorkspaceAdmin(ctx context.Context, tenantClient dynamic.Interface, rbacIdentity string) error {
	if err := migrateLegacyWorkspaceAdmin(ctx, tenantClient); err != nil {
		return err
	}
	if err := tenantClient.Resource(clusterRoleBindingGVR).Delete(ctx, userAdminCRBName(rbacIdentity), metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting workspace-admin ClusterRoleBinding: %w", err)
	}
	return nil
}

// migrateLegacyWorkspaceAdmin converts the shared railgrid-cluster-admin
// binding, when present, into one per-user binding per subject and deletes
// it. Whoever held access through the shared binding keeps it. No-op once
// the workspace is on per-user bindings.
func migrateLegacyWorkspaceAdmin(ctx context.Context, tenantClient dynamic.Interface) error {
	legacy, err := tenantClient.Resource(clusterRoleBindingGVR).Get(ctx, legacyWorkspaceAdminCRB, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting legacy workspace-admin ClusterRoleBinding: %w", err)
	}
	subjects, _, _ := unstructured.NestedSlice(legacy.Object, "subjects")
	for _, s := range subjects {
		m, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		kind, _ := m["kind"].(string)
		name, _ := m["name"].(string)
		if kind != "User" || name == "" {
			continue
		}
		if _, err := tenantClient.Resource(clusterRoleBindingGVR).Create(ctx, userAdminCRB(name), metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("migrating legacy workspace-admin subject %q: %w", name, err)
		}
	}
	if err := tenantClient.Resource(clusterRoleBindingGVR).Delete(ctx, legacyWorkspaceAdminCRB, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting legacy workspace-admin ClusterRoleBinding: %w", err)
	}
	return nil
}

// newClients creates dynamic and discovery clients from a rest.Config.
func newClients(cfg *rest.Config) (dynamic.Interface, discovery.DiscoveryInterface, error) {
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("creating dynamic client: %w", err)
	}
	discClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("creating discovery client: %w", err)
	}
	return dynClient, discClient, nil
}

// configForPath returns a rest.Config targeting the given kcp workspace path.
func configForPath(base *rest.Config, clusterPath string) *rest.Config {
	cfg := rest.CopyConfig(base)
	cfg.Host = AppendClusterPath(cfg.Host, clusterPath)
	return cfg
}

// waitForWorkspaceReady polls until a workspace has phase "Ready".
// Uses a 3-minute timeout to accommodate slower CI environments where kcp
// workspaces may take longer to become ready after initial deployment.
func waitForWorkspaceReady(ctx context.Context, client dynamic.Interface, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		ws, err := client.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		// A terminating workspace keeps reporting Ready until it is actually
		// gone, so phase alone would hand back a workspace whose RBAC is being
		// garbage-collected. Writing into it then fails as
		// "workspace access not permitted" — a permission error for what is
		// really a lifecycle race, and one that looks nothing like its cause.
		if ws.GetDeletionTimestamp() != nil {
			return false, nil
		}
		phase, _, _ := unstructured.NestedString(ws.Object, "status", "phase")
		return phase == "Ready", nil
	})
}

// waitForWorkspaceGone blocks until name no longer exists under client. Used
// before recreating a workspace that is still terminating: kcp accepts the
// Create only once the old object is gone, and until then returns AlreadyExists
// for an object nobody can usefully write to.
func waitForWorkspaceGone(ctx context.Context, client dynamic.Interface, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := client.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

// ProviderClaim is the wire shape the REST handler hands
// EnsureProviderAPIBinding — one entry per permission claim the
// provider DECLARED in its CatalogEntry, plus a flag whether the
// user accepted or rejected it in the Enable confirmation dialog.
// Mirrors providers.PermissionClaim but lives here so the bootstrap
// package stays free of an import on pkg/hub/providers.
type ProviderClaim struct {
	Group    string
	Resource string
	Verbs    []string
	Accepted bool
	// MatchLabels is the claim's declared scope: the label set an object must
	// carry for this claim to reach it. Empty accepts the claim for every
	// object of the resource in the workspace (kcp's matchAll).
	//
	// This is the field that turns "the provider may read every Secret in your
	// workspace" into "the provider may read the Secrets it labelled as its
	// own". kcp enforces it on both sides of the APIExport virtual workspace:
	// its permission-claim labeler stamps the internal
	// claimed.internal.apis.kcp.io/<export> label only on objects the selector
	// matches, the virtual workspace filters LIST/WATCH by that label and 404s
	// a GET without it, and its virtual-workspace admission refuses a write
	// whose object does not match (stamping the matchLabels when they are
	// simply absent). See docs/cross-provider-simplification.md X-4.
	MatchLabels map[string]string
}

// EnsureProviderAPIBinding creates (or no-ops on AlreadyExists) an
// APIBinding named `bindingName` in the child workspace
// root:railgrid:tenants:{orgUUID}:{wsUUID}, pointing at exportPath/exportName.
//
// Used by the server-side POST /api/orgs/{org}/workspaces/{ws}/providers/{name}/enable
// handler so the portal doesn't have to talk to /clusters/{cluster}/apis/...
// directly — the hub's user-facing kcp proxy pins every user to their
// User.Spec.DefaultCluster and would 403 any non-default workspace
// even when commit #220's per-workspace RBAC grants are in place. The
// proxy's defaultCluster check happens BEFORE forwarding to kcp, so
// even valid RBAC can't get through. Routing the enable action server-
// side via the kcp-admin client sidesteps that pre-check.
//
// PermissionClaims state: Accepted iff the user ticked the claim in
// the confirmation dialog, Rejected otherwise. kcp refuses to mark
// the binding Bound when any provider-required claim is Rejected, so
// the response surfaces the mismatch to the user automatically.
func (b *Bootstrapper) EnsureProviderAPIBinding(
	ctx context.Context,
	orgUUID, wsUUID, bindingName, exportPath, exportName string,
	claims []ProviderClaim,
) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("EnsureProviderAPIBinding: orgUUID and wsUUID are required")
	}
	if bindingName == "" || exportPath == "" || exportName == "" {
		return fmt.Errorf("EnsureProviderAPIBinding: bindingName, exportPath, exportName are required")
	}
	wsConfig := configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
	wsClient, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return fmt.Errorf("creating child workspace client: %w", err)
	}

	// kcp marks the binding's PermissionClaimsValid=False (and refuses to
	// surface the claimed resource through the export's virtual workspace)
	// unless a claim on a non-built-in type carries the SAME identityHash the
	// export it binds to declares for that claim. Rather than re-derive the
	// hash by scanning sibling APIExports — which races core.railgrid.ai
	// regeneration and previously left edges claims with an empty hash, so the
	// bound provider saw zero claimed objects (e.g. kuery engaged no edges) —
	// read it straight from the export we're binding to. That value is the one
	// kcp validates against, and the provisioner (ApplyAPIExport) has already
	// resolved and stamped it; we wait for it below if provisioning is still in
	// flight.
	identities, err := b.exportClaimIdentities(ctx, exportPath, exportName, claims)
	if err != nil {
		return err
	}

	// Those identities are whatever this export pins, which is only correct if
	// the workspace binds the same copy of the dependency the export was built
	// against. Resolve them against what this workspace actually binds before
	// creating anything: a stale pin produces a binding kcp reports as perfectly
	// healthy while serving none of the claimed resources, whose only downstream
	// symptom is a 404 the dependent provider retries forever.
	//
	// For a self-hosted (single-tenant) export this repoints it and returns the
	// updated identities, which is what lets "swap the dependency, then
	// Disable/Enable" work unattended. For a platform export it refuses.
	identities, err = b.verifyClaimIdentities(ctx, orgUUID, wsUUID, bindingName, exportPath, exportName, claims, identities)
	if err != nil {
		return err
	}

	specClaims := make([]apisv1alpha2.AcceptablePermissionClaim, 0, len(claims))
	for _, c := range claims {
		state := apisv1alpha2.ClaimRejected
		if c.Accepted {
			state = apisv1alpha2.ClaimAccepted
		}
		specClaims = append(specClaims, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    c.Group,
						Resource: c.Resource,
					},
					Verbs:        c.Verbs,
					IdentityHash: identities[c.Group+"/"+c.Resource],
				},
				Selector: claimSelector(c),
			},
			State: state,
		})
	}

	binding := &apisv1alpha2.APIBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apisv1alpha2.SchemeGroupVersion.String(),
			Kind:       "APIBinding",
		},
		ObjectMeta: metav1.ObjectMeta{Name: bindingName},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: exportPath,
					Name: exportName,
				},
			},
			PermissionClaims: specClaims,
		},
	}
	u, err := toUnstructured(binding)
	if err != nil {
		return fmt.Errorf("converting APIBinding to unstructured: %w", err)
	}
	if _, err := wsClient.Resource(apiBindingGVR).Create(ctx, u, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating APIBinding %q in %s/%s: %w", bindingName, orgUUID, wsUUID, err)
	}
	if err := waitForAPIBindingBound(ctx, wsClient, bindingName); err != nil {
		return fmt.Errorf("waiting for APIBinding %q to bind in %s/%s: %w", bindingName, orgUUID, wsUUID, err)
	}
	return nil
}

// claimSelector renders a declared claim's scope as the kcp selector written
// onto the accepted claim in the tenant's APIBinding.
//
// A claim with no declared scope becomes matchAll, which is what every claim
// was before X-4 and what a claim on a resource only the provider ever creates
// still legitimately is. A claim WITH a scope becomes a label selector, and
// from that point kcp serves the provider only the matching objects.
//
// Note for upgrades: the selector on an accepted claim is IMMUTABLE in kcp
// (apis.kcp.io_apibindings.yaml, "Permission claim selector is immutable"), and
// EnsureProviderAPIBinding only ever creates the binding — it no-ops on
// AlreadyExists. A workspace that enabled the provider before the claim was
// narrowed therefore keeps its matchAll binding, wider than the provider now
// asks for, until the provider is disabled and re-enabled there. kcp surfaces
// the gap as PermissionClaimsValid=False / PermissionClaimsMismatch on the
// binding; it does not stop the binding from being Bound.
func claimSelector(c ProviderClaim) apisv1alpha2.PermissionClaimSelector {
	if len(c.MatchLabels) == 0 {
		return apisv1alpha2.PermissionClaimSelector{MatchAll: true}
	}
	matchLabels := make(map[string]string, len(c.MatchLabels))
	for key, value := range c.MatchLabels {
		matchLabels[key] = value
	}
	return apisv1alpha2.PermissionClaimSelector{
		LabelSelector: metav1.LabelSelector{MatchLabels: matchLabels},
	}
}

// exportClaimIdentities returns, per claim, the identityHash the bound
// APIExport (exportPath/exportName) declares for it — keyed "group/resource".
// This is the value kcp validates the binding's claim against, so sourcing it
// from the export (rather than re-deriving it by scanning sibling APIExports'
// spec.resources, which races core.railgrid.ai regeneration and silently yielded
// an empty hash → PermissionClaimsValid=False → the provider sees zero claimed
// objects) keeps the two in lockstep by construction.
//
// The provisioner (ApplyAPIExport) resolves and stamps these identities on the
// export. A first-party railgrid claim (*.railgrid.ai) MUST end up with a non-empty
// hash; if the export does not carry one yet, provisioning is still in flight
// (it races the Enable call), so we poll rather than write an empty hash.
// Built-in / kcp-system claims (core k8s, apis.kcp.io, empty group) legitimately
// carry no identity, so a missing/empty entry for those is the terminal answer.
func (b *Bootstrapper) exportClaimIdentities(ctx context.Context, exportPath, exportName string, claims []ProviderClaim) (map[string]string, error) {
	exportConfig := configForPath(b.config, exportPath)
	exportClient, err := dynamic.NewForConfig(exportConfig)
	if err != nil {
		return nil, fmt.Errorf("creating export workspace client for %s: %w", exportPath, err)
	}

	key := func(group, resource string) string { return group + "/" + resource }

	out := map[string]string{}
	lookup := func(ctx context.Context) (bool, error) {
		ex, err := exportClient.Resource(apiExportGVR).Get(ctx, exportName, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			// The export itself doesn't exist yet. After the bootstrap split the
			// provider's own init (Helm init-container) creates the APIExport, and
			// that races a tenant clicking Enable — so keep polling until it
			// appears rather than hard-failing the whole Enable on the first miss.
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("getting APIExport %q in %s: %w", exportName, exportPath, err)
		}
		pcs, _, _ := unstructured.NestedSlice(ex.Object, "spec", "permissionClaims")
		got := map[string]string{}
		for _, pc := range pcs {
			m, ok := pc.(map[string]any)
			if !ok {
				continue
			}
			g, _ := m["group"].(string)
			r, _ := m["resource"].(string)
			h, _, _ := unstructured.NestedString(m, "identityHash")
			got[key(g, r)] = h
		}
		// Wait for the provisioner to stamp every first-party claim's identity.
		for _, c := range claims {
			if strings.HasSuffix(c.Group, ".railgrid.ai") && got[key(c.Group, c.Resource)] == "" {
				return false, nil
			}
		}
		out = got
		return true, nil
	}

	// immediate=true returns on the first hit in the common case where the
	// export is already fully provisioned; otherwise poll until it is.
	if err := wait.PollUntilContextTimeout(ctx, time.Second, 90*time.Second, true, lookup); err != nil {
		return nil, fmt.Errorf("APIExport %q (%s) not yet created, or its permissionClaims not yet stamped with identityHashes, by the provider init: %w", exportName, exportPath, err)
	}
	return out, nil
}

// ListProviderAPIBindings returns the set of Bound provider APIBindings
// present in the child workspace root:railgrid:tenants:{orgUUID}:{wsUUID},
// keyed by provider name. Used by the GET /api/orgs/{org}/workspaces/{ws}/
// providers/enabled handler so the portal can render the
// per-workspace "enabled providers" set on every workspace switch —
// without going through the kcp user-proxy, which 403s any
// non-default workspace path even when commit #220's per-workspace
// RBAC would have allowed the read.
//
// Filtering rule: a binding counts as a "provider binding" iff its
// spec.reference.export.path names a provider workspace and its status.phase is
// Bound. Two shapes qualify:
//
//	root:railgrid:providers:<name>                    a platform provider
//	root:railgrid:tenants:<orgUUID>:providers:<name>   an org-owned provider
//
// Org-owned exports count only for their OWN org, so a binding that somehow
// referenced another Org's provider is not reported as enabled here. The
// trailing segment is the provider name; the binding's own metadata.name is the
// value (existing convention is binding.name == provider.name).
func (b *Bootstrapper) ListProviderAPIBindings(ctx context.Context, orgUUID, wsUUID string) (map[string]ProviderBinding, error) {
	if orgUUID == "" || wsUUID == "" {
		return nil, fmt.Errorf("ListProviderAPIBindings: orgUUID and wsUUID are required")
	}
	wsConfig := configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
	wsClient, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return nil, fmt.Errorf("creating child workspace client: %w", err)
	}
	list, err := wsClient.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing APIBindings in %s/%s: %w", orgUUID, wsUUID, err)
	}
	out := make(map[string]ProviderBinding, len(list.Items))
	for _, item := range list.Items {
		path, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "path")
		providerName, ok := providerNameFromExportPath(path, orgUUID)
		if !ok {
			continue
		}
		// A terminating binding keeps phase Bound (and keeps serving) until
		// kcp's cascade cleanup finishes, so it must stay in the list — but
		// flagged, or a Disable that is stuck on leftover CR finalizers is
		// indistinguishable from one that never happened.
		terminating := item.GetDeletionTimestamp() != nil
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if phase != "Bound" && !terminating {
			continue
		}
		out[providerName] = ProviderBinding{
			Name:            item.GetName(),
			ExportPath:      path,
			SelfHosted:      strings.HasPrefix(path, kcppaths.TenantsParent+":"),
			Terminating:     terminating,
			DeletionBlocked: deletionBlockedMessage(&item),
		}
	}
	return out, nil
}

// deletionBlockedMessage returns kcp's explanation of why a terminating
// APIBinding cannot finish deleting, or "" when deletion is not blocked (or
// not in progress). kcp's apibindingdeletion controller records the blocker on
// the BindingResourceDeleteSuccess condition — e.g. "Some content in the
// workspace has finalizers remaining: <finalizer> in N resource instances" —
// which names exactly what a user (or their provider) must clean up.
func deletionBlockedMessage(item *unstructured.Unstructured) string {
	if item.GetDeletionTimestamp() == nil {
		return ""
	}
	conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] != string(apisv1alpha2.BindingResourceDeleteSuccess) || cond["status"] != "False" {
			continue
		}
		if msg, ok := cond["message"].(string); ok && msg != "" {
			return msg
		}
		if reason, ok := cond["reason"].(string); ok && reason != "" {
			return reason
		}
		return "deletion is not progressing"
	}
	return ""
}

// ProviderBinding describes one bound provider APIBinding in a workspace.
//
// SelfHosted is the field that earns this type: once an Org can self-host a
// provider under the same name as the platform one, "is `edges` enabled here?"
// stops being a yes/no question. A workspace that enabled the platform edges
// before the Org started self-hosting still points at the platform export, and
// showing that as plain "Enabled" would tell the user they are running their own
// instance when they are not. Switching is a Disable + Enable, not an in-place
// retarget, so the distinction has to survive all the way to the UI.
type ProviderBinding struct {
	// Name is the APIBinding's metadata.name (by convention, the provider name).
	Name string
	// ExportPath is the workspace path of the APIExport it binds.
	ExportPath string
	// SelfHosted is true when ExportPath is under the tenant fleet, i.e. the
	// binding targets an Org's own provider rather than a platform one.
	SelfHosted bool
	// Terminating is true when the binding has been deleted but kcp's cascade
	// cleanup (which removes every CR of the bound APIs first) has not finished.
	// The binding still serves until then, so it still counts as enabled.
	Terminating bool
	// DeletionBlocked is kcp's explanation of what is holding a terminating
	// binding's deletion open — typically leftover CR finalizers whose
	// controller is gone. Empty when deletion is progressing normally or the
	// binding is not terminating.
	DeletionBlocked string
}

// providerNameFromExportPath maps an APIBinding's export path to the provider
// name it enables, for either a platform provider or one owned by orgUUID.
// Returns ok=false for any other path — including a provider owned by a
// different Org.
func providerNameFromExportPath(path, orgUUID string) (string, bool) {
	if name, found := strings.CutPrefix(path, kcppaths.ProvidersParent+":"); found {
		if name == "" || strings.Contains(name, ":") {
			return "", false
		}
		return name, true
	}
	owner, name, ok := kcppaths.SplitOrgProviderPath(path)
	if !ok || owner != orgUUID {
		return "", false
	}
	return name, true
}

// ProviderBindingRef locates one provider APIBinding in the tenant fleet: the
// (org, workspace) pair whose logical cluster holds it, plus its name. The
// per-workspace reads already know where they are; a fleet-wide walk does not,
// so the coordinates have to travel with the result.
type ProviderBindingRef struct {
	OrgUUID       string
	WorkspaceUUID string
	BindingName   string
}

// ListProviderAPIBindingsForExport returns every APIBinding in the tenant
// fleet that binds exportPath/exportName — the cross-workspace counterpart to
// ListProviderAPIBindings, which answers "what is enabled in THIS workspace".
//
// It exists for fleet-wide claim migrations (AGENTS.md §5.1: a provider's
// permission claims live on its APIExport, but what a tenant actually granted
// lives on that tenant's own APIBinding, in that tenant's own workspace, and
// `init` never touches those). Two deliberate differences from the
// per-workspace variant:
//
//   - No status.phase filter. A binding held out of Bound because it is
//     missing a claim is exactly the one a claims migration has to reach.
//   - Matching is on the export reference, not on the path-derived provider
//     name, so an Org that self-hosts a provider under the same name as the
//     platform one is not swept up by a migration of the platform copy.
//
// A workspace that cannot be listed is skipped rather than failing the walk:
// one unreachable tenant must not hide the rest of the fleet from an operator.
func (b *Bootstrapper) ListProviderAPIBindingsForExport(ctx context.Context, exportPath, exportName string) ([]ProviderBindingRef, error) {
	if exportPath == "" || exportName == "" {
		return nil, fmt.Errorf("ListProviderAPIBindingsForExport: exportPath and exportName are required")
	}
	orgs, err := b.ListOrgWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing Org Workspaces: %w", err)
	}
	var out []ProviderBindingRef
	for _, orgUUID := range orgs {
		// Unfiltered on purpose: this is lifecycle, not a tenant-facing view.
		// The org-providers container holds no provider APIBindings, so it
		// costs one empty List and keeps the walk honest if that ever changes.
		workspaces, err := b.ListChildWorkspaces(ctx, orgUUID)
		if err != nil {
			klog.FromContext(ctx).Error(err, "skipping org while listing provider APIBindings", "org", orgUUID)
			continue
		}
		for _, wsUUID := range workspaces {
			wsClient, err := dynamic.NewForConfig(configForPath(b.config, childWorkspacePath(orgUUID, wsUUID)))
			if err != nil {
				return nil, fmt.Errorf("creating child workspace client for %s/%s: %w", orgUUID, wsUUID, err)
			}
			list, err := wsClient.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
			if err != nil {
				klog.FromContext(ctx).Error(err, "skipping workspace while listing provider APIBindings", "org", orgUUID, "workspace", wsUUID)
				continue
			}
			for i := range list.Items {
				item := &list.Items[i]
				path, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "path")
				name, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "name")
				if path != exportPath || name != exportName {
					continue
				}
				out = append(out, ProviderBindingRef{OrgUUID: orgUUID, WorkspaceUUID: wsUUID, BindingName: item.GetName()})
			}
		}
	}
	return out, nil
}

// ReacceptProviderAPIBindingClaims rewrites one binding's
// spec.permissionClaims to `claims` — the provider's CatalogEntry claim set as
// it stands today — accepting each one, and reports whether anything changed.
//
// This is the migration half of AGENTS.md §5.1. A provider that starts
// requiring a newly-declared claim breaks every already-enabled tenant on
// rollout, because `init` only updates the provider-side APIExport: the
// tenant's binding keeps the claim set it accepted when it was enabled. Bound
// or not, that binding is the object that decides what the provider is
// actually allowed to touch.
//
// Two rules make re-running this safe:
//
//   - A claim the tenant EXPLICITLY REJECTED stays Rejected. Rejecting is a
//     decision the tenant made about their own workspace; a migration
//     propagates the provider's claim set, it does not overturn consent.
//   - A claim already on the binding keeps its identityHash and selector.
//     Those were resolved against what this workspace binds when it was
//     enabled (see verifyClaimIdentities); re-deriving them from the export
//     here could re-pin a workspace to a stale copy of a dependency. Only
//     genuinely new claims take their identity from the export — and only they
//     take their scope from it, because kcp makes an accepted claim's selector
//     immutable, so a claim narrowed after a workspace enabled the provider
//     keeps that workspace's original (wider) scope until the provider is
//     disabled and re-enabled there.
//
// Claims the provider no longer declares are dropped: the target is the
// CatalogEntry's current set, not the union with history. ProviderClaim.Accepted
// is ignored — the type is shared with the Enable flow, where a human ticked
// each box; here the whole point is that the provider already declares them.
func (b *Bootstrapper) ReacceptProviderAPIBindingClaims(
	ctx context.Context,
	ref ProviderBindingRef,
	exportPath, exportName string,
	claims []ProviderClaim,
) (bool, error) {
	if ref.OrgUUID == "" || ref.WorkspaceUUID == "" || ref.BindingName == "" {
		return false, fmt.Errorf("ReacceptProviderAPIBindingClaims: org, workspace and binding name are required")
	}
	if len(claims) == 0 {
		return false, fmt.Errorf("ReacceptProviderAPIBindingClaims: refusing to clear every permission claim on %s", ref.BindingName)
	}
	wsClient, err := dynamic.NewForConfig(configForPath(b.config, childWorkspacePath(ref.OrgUUID, ref.WorkspaceUUID)))
	if err != nil {
		return false, fmt.Errorf("creating child workspace client: %w", err)
	}

	key := func(group, resource string) string { return group + "/" + resource }
	// Resolved at most once, and only when some claim is new to this binding:
	// exportClaimIdentities polls for the provisioner to stamp a first-party
	// claim's hash, which is wasted latency per binding when (as in the common
	// migration) every new claim is a built-in type that carries none.
	var identities map[string]string

	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		u, err := wsClient.Resource(apiBindingGVR).Get(ctx, ref.BindingName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("getting APIBinding %q in %s/%s: %w", ref.BindingName, ref.OrgUUID, ref.WorkspaceUUID, err)
		}
		var binding apisv1alpha2.APIBinding
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &binding); err != nil {
			return false, fmt.Errorf("decoding APIBinding %q in %s/%s: %w", ref.BindingName, ref.OrgUUID, ref.WorkspaceUUID, err)
		}
		existing := make(map[string]apisv1alpha2.AcceptablePermissionClaim, len(binding.Spec.PermissionClaims))
		for _, pc := range binding.Spec.PermissionClaims {
			existing[key(pc.Group, pc.Resource)] = pc
		}
		if identities == nil {
			for _, c := range claims {
				if _, ok := existing[key(c.Group, c.Resource)]; ok {
					continue
				}
				identities, err = b.exportClaimIdentities(ctx, exportPath, exportName, claims)
				if err != nil {
					return false, err
				}
				break
			}
		}

		desired := make([]apisv1alpha2.AcceptablePermissionClaim, 0, len(claims))
		for _, c := range claims {
			entry := apisv1alpha2.AcceptablePermissionClaim{
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: apisv1alpha2.PermissionClaim{
						GroupResource: apisv1alpha2.GroupResource{Group: c.Group, Resource: c.Resource},
						Verbs:         c.Verbs,
						IdentityHash:  identities[key(c.Group, c.Resource)],
					},
					Selector: claimSelector(c),
				},
				State: apisv1alpha2.ClaimAccepted,
			}
			if prev, ok := existing[key(c.Group, c.Resource)]; ok {
				entry.IdentityHash = prev.IdentityHash
				entry.Selector = prev.Selector
				if prev.State == apisv1alpha2.ClaimRejected {
					entry.State = apisv1alpha2.ClaimRejected
				}
			}
			desired = append(desired, entry)
		}
		if reflect.DeepEqual(binding.Spec.PermissionClaims, desired) {
			return false, nil
		}

		// Set only spec.permissionClaims on the object as read, rather than
		// re-serializing the decoded APIBinding: a round-trip through the typed
		// struct would silently drop anything this build's kcp SDK does not
		// know about.
		items := make([]any, 0, len(desired))
		for i := range desired {
			raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&desired[i])
			if err != nil {
				return false, fmt.Errorf("encoding permission claim: %w", err)
			}
			items = append(items, raw)
		}
		if err := unstructured.SetNestedSlice(u.Object, items, "spec", "permissionClaims"); err != nil {
			return false, fmt.Errorf("setting spec.permissionClaims: %w", err)
		}
		if _, err := wsClient.Resource(apiBindingGVR).Update(ctx, u, metav1.UpdateOptions{}); err == nil {
			return true, nil
		} else if !errors.IsConflict(err) {
			return false, fmt.Errorf("updating APIBinding %q in %s/%s: %w", ref.BindingName, ref.OrgUUID, ref.WorkspaceUUID, err)
		} else {
			lastErr = err
		}
	}
	return false, fmt.Errorf("updating APIBinding %q in %s/%s after %d conflicts: %w", ref.BindingName, ref.OrgUUID, ref.WorkspaceUUID, maxAttempts, lastErr)
}

// DeleteProviderAPIBinding removes the named provider APIBinding from the
// child workspace root:railgrid:tenants:{orgUUID}:{wsUUID}. NotFound is a no-op so
// the Disable action is idempotent. Counterpart to EnsureProviderAPIBinding.
func (b *Bootstrapper) DeleteProviderAPIBinding(ctx context.Context, orgUUID, wsUUID, bindingName string) error {
	if orgUUID == "" || wsUUID == "" || bindingName == "" {
		return fmt.Errorf("DeleteProviderAPIBinding: orgUUID, wsUUID, bindingName are required")
	}
	wsConfig := configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
	wsClient, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return fmt.Errorf("creating child workspace client: %w", err)
	}
	if err := wsClient.Resource(apiBindingGVR).Delete(ctx, bindingName, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting APIBinding %q in %s/%s: %w", bindingName, orgUUID, wsUUID, err)
	}
	return nil
}

// appAccessGrantLabel marks the ClusterRoleBindings that invite one platform
// user into one private published app (`get` on the instance's `access`
// subresource). App Studio's share dialog authors them; this label is the
// shared contract that makes them enumerable here without any provider
// coupling. Must stay in lockstep with providers/app-studio/api
// (appAccessLabel) and docs/app-studio-publishing.md.
const (
	appAccessGrantLabel     = "railgrid.ai/app-access"
	appAccessGrantUserLabel = "app-studio.railgrid.ai/user"
)

// AppAccessGrant is the portal-facing view of one published-app invitation:
// plain workspace RBAC, surfaced so tenant settings can show who can open
// which private app.
type AppAccessGrant struct {
	// Binding is the ClusterRoleBinding name (the revocation handle).
	Binding string `json:"binding"`
	// App is the published instance name the grant opens.
	App string `json:"app"`
	// User is the platform User metadata.name the grant was issued to.
	User string `json:"user"`
	// CreatedAt is the binding's creation timestamp.
	CreatedAt time.Time `json:"createdAt"`
}

// ListAppAccessGrants lists the published-app access grants (labeled
// ClusterRoleBindings) in the child workspace. Same kcp-admin/proxy-avoidance
// rationale as ListProviderAPIBindings.
func (b *Bootstrapper) ListAppAccessGrants(ctx context.Context, orgUUID, wsUUID string) ([]AppAccessGrant, error) {
	if orgUUID == "" || wsUUID == "" {
		return nil, fmt.Errorf("ListAppAccessGrants: orgUUID and wsUUID are required")
	}
	wsConfig := configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
	wsClient, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return nil, fmt.Errorf("creating child workspace client: %w", err)
	}
	list, err := wsClient.Resource(clusterRoleBindingGVR).List(ctx, metav1.ListOptions{LabelSelector: appAccessGrantLabel})
	if err != nil {
		return nil, fmt.Errorf("listing app-access grants in %s/%s: %w", orgUUID, wsUUID, err)
	}
	out := make([]AppAccessGrant, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		app := item.GetLabels()[appAccessGrantLabel]
		if app == "" {
			continue
		}
		user := item.GetLabels()[appAccessGrantUserLabel]
		if user == "" {
			// Fall back to the binding's User subject for grants authored
			// outside App Studio (kubectl and friends).
			subjects, _, _ := unstructured.NestedSlice(item.Object, "subjects")
			for _, rawSubject := range subjects {
				subject, _ := rawSubject.(map[string]any)
				if subject["kind"] == "User" {
					user, _ = subject["name"].(string)
					break
				}
			}
		}
		if user == "" {
			continue
		}
		out = append(out, AppAccessGrant{
			Binding:   item.GetName(),
			App:       app,
			User:      user,
			CreatedAt: item.GetCreationTimestamp().Time,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].User < out[j].User
	})
	return out, nil
}

// RemoveAppAccessGrant revokes one published-app invitation by deleting its
// ClusterRoleBinding. It refuses to touch bindings that do not carry the
// app-access label so this endpoint can never delete unrelated RBAC.
// NotFound is a no-op for idempotent revocation.
func (b *Bootstrapper) RemoveAppAccessGrant(ctx context.Context, orgUUID, wsUUID, bindingName string) error {
	if orgUUID == "" || wsUUID == "" || bindingName == "" {
		return fmt.Errorf("RemoveAppAccessGrant: orgUUID, wsUUID, bindingName are required")
	}
	wsConfig := configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
	wsClient, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return fmt.Errorf("creating child workspace client: %w", err)
	}
	binding, err := wsClient.Resource(clusterRoleBindingGVR).Get(ctx, bindingName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting ClusterRoleBinding %q: %w", bindingName, err)
	}
	if binding.GetLabels()[appAccessGrantLabel] == "" {
		return fmt.Errorf("ClusterRoleBinding %q is not an app-access grant", bindingName)
	}
	if err := wsClient.Resource(clusterRoleBindingGVR).Delete(ctx, bindingName, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting ClusterRoleBinding %q: %w", bindingName, err)
	}
	return nil
}

func acceptedClaim(group, resource, identityHash string, verbs []string) apisv1alpha2.AcceptablePermissionClaim {
	return apisv1alpha2.AcceptablePermissionClaim{
		ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    group,
					Resource: resource,
				},
				Verbs:        verbs,
				IdentityHash: identityHash,
			},
			Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		},
		State: apisv1alpha2.ClaimAccepted,
	}
}

// toUnstructured converts a typed runtime.Object to an Unstructured object.
func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: data}, nil
}

// AppendClusterPath sets the /clusters/<path> segment on a kcp URL.
// If the host already contains a /clusters/ path (e.g. from the admin
// kubeconfig), it is replaced rather than appended.
//
// Deprecated: use apiurl.KCPClusterURL directly.
func AppendClusterPath(host, clusterPath string) string {
	return apiurl.KCPClusterURL(host, clusterPath)
}

// waitForAPIBindingBound polls until an APIBinding has phase "Bound".
func waitForAPIBindingBound(ctx context.Context, client dynamic.Interface, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		obj, err := client.Resource(apiBindingGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if !errors.IsNotFound(err) {
				return false, err
			}
			return false, nil
		}
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		return phase == "Bound", nil
	})
}
