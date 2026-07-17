/*
Copyright 2026 The Faros Authors.

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
	"fmt"
	"reflect"
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

	"github.com/faroshq/faros-kedge/config/kcp"
	"github.com/faroshq/faros-kedge/pkg/apiurl"
	"github.com/faroshq/faros-kedge/pkg/hub/providers"
	"github.com/faroshq/faros-kedge/pkg/kcppaths"
	"github.com/faroshq/faros-kedge/pkg/util/confighelpers"
	"github.com/faroshq/faros-kedge/pkg/util/identity"
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
		Group: "tenants.kedge.faros.sh", Version: "v1alpha1", Resource: "memberships",
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
// bootstrapper will write into root:kedge:providers. Pass the value of
// the --providers flag; nil/empty selects every known builtin.
func (b *Bootstrapper) WithEnabledProviders(names []string) *Bootstrapper {
	b.enabledProviders = names
	return b
}

// Bootstrap creates the workspace hierarchy:
//
//	root:kedge                          - Root kedge workspace
//	root:kedge:providers                - Parent of per-provider sub-workspaces
//	  root:kedge:providers:{name}       - One provider (restricted `provider` type)
//	root:kedge:tenants:{uuid}:{ws}:{edge}  - Tenant org/team/edge fleet
//	root:kedge:system:controllers       - ALL platform APIExports + schemas
//	root:kedge:system:providers         - Provider + CatalogEntry objects
//	root:kedge:system:tenants           - User/Organization/Membership objects
func (b *Bootstrapper) Bootstrap(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Bootstrapping kcp workspace hierarchy")

	// 1. Clients targeting root workspace.
	rootDynamic, rootDiscovery, err := newClients(b.config)
	if err != nil {
		return fmt.Errorf("creating root clients: %w", err)
	}

	// 2. Bootstrap root:kedge workspace.
	logger.Info("Bootstrapping root:kedge workspace")
	if err := confighelpers.Bootstrap(ctx, rootDiscovery, rootDynamic, kcp.RootWorkspaceFS); err != nil {
		return fmt.Errorf("bootstrapping root:kedge workspace: %w", err)
	}
	if err := waitForWorkspaceReady(ctx, rootDynamic, "kedge"); err != nil {
		return fmt.Errorf("waiting for kedge workspace: %w", err)
	}

	// 3. Bootstrap child workspaces: providers, tenants, users.
	kedgeConfig := configForPath(b.config, "root:kedge")
	kedgeDynamic, kedgeDiscovery, err := newClients(kedgeConfig)
	if err != nil {
		return fmt.Errorf("creating kedge clients: %w", err)
	}

	logger.Info("Bootstrapping child workspaces: providers, tenants, system")
	if err := confighelpers.Bootstrap(ctx, kedgeDiscovery, kedgeDynamic, kcp.KedgeWorkspaceFS); err != nil {
		return fmt.Errorf("bootstrapping child workspaces: %w", err)
	}
	for _, name := range []string{"providers", "tenants", "system"} {
		if err := waitForWorkspaceReady(ctx, kedgeDynamic, name); err != nil {
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

	// 5. Bootstrap ALL platform APIResourceSchemas + APIExports in
	//    root:kedge:system:controllers — the single home for platform exports.
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
		// apiexport-kedge.faros.sh.yaml is embedded only as input for the
		// core.faros.sh generator (hack/gen-core-apiexport). Nothing binds the
		// standalone kedge.faros.sh export — tenants bind core.faros.sh — so we
		// never apply it to the cluster; its presence there is just confusing.
		confighelpers.SkipFilesOption("apiexport-kedge.faros.sh.yaml"),
	); err != nil {
		return fmt.Errorf("bootstrapping platform exports: %w", err)
	}

	// 5b. Bind the platform exports into the workspaces that hold their
	//     objects: system:providers binds providers.kedge.faros.sh (CatalogEntry)
	//     + admin.kedge.faros.sh (Provider); system:tenants binds
	//     tenants.kedge.faros.sh (User/Organization/Membership). All FROM
	//     system:controllers. These exports are excluded from tenant-bound
	//     core.faros.sh — see hack/gen-core-apiexport/main.go excludedAPIExports.
	systemProvidersDynamic, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.SystemProviders))
	if err != nil {
		return fmt.Errorf("creating system:providers client: %w", err)
	}
	for _, exportName := range []string{"providers.kedge.faros.sh", "admin.kedge.faros.sh"} {
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

	// 5d. Apply post-providers workspace artefacts under root:kedge — namely
	//     the `organization` WorkspaceType, which declares a defaultAPIBinding
	//     to tenants.kedge.faros.sh in root:kedge:providers. kcp's WT
	//     admission resolves the binding's LogicalCluster and checks bind
	//     RBAC at apply time, so the APIExport (created in step 5) must
	//     exist beforehand or the apply fails with a 403 forbidden.
	logger.Info("Bootstrapping post-providers workspace artefacts (organization WorkspaceType)")
	if err := confighelpers.Bootstrap(ctx, kedgeDiscovery, kedgeDynamic, kcp.PostProvidersFS); err != nil {
		return fmt.Errorf("bootstrapping post-providers artefacts: %w", err)
	}

	// 6. Bind tenants.kedge.faros.sh APIExport in root:kedge:system:tenants so
	//    User, Organization, Membership, and UserMembershipIndex CRs are all
	//    reachable there (this is the CR-object storage workspace; the org
	//    *fleet* lives separately under root:kedge:tenants). Same admission rules
	//    as step 5d apply — the APIExport must exist (step 5) before this
	//    APIBinding is created.
	logger.Info("Binding tenants.kedge.faros.sh in system:tenants")
	if err := b.ensureTenancyObjectsBinding(ctx); err != nil {
		return fmt.Errorf("binding tenants.kedge.faros.sh in system:tenants: %w", err)
	}

	logger.Info("kcp bootstrap complete")
	return nil
}

// ensureTenancyObjectsBinding creates an APIBinding to the
// tenants.kedge.faros.sh APIExport (in root:kedge:system:controllers) inside
// root:kedge:system:tenants. Idempotent. Without this binding the organization
// bootstrap controller's writes to User / Organization / Membership CRs in
// system:tenants would fail with "no matches for kind".
func (b *Bootstrapper) ensureTenancyObjectsBinding(ctx context.Context) error {
	tenancyDynamic, err := dynamic.NewForConfig(b.UsersConfig())
	if err != nil {
		return fmt.Errorf("creating system:tenants client: %w", err)
	}
	return ensureExportBinding(ctx, tenancyDynamic, kcppaths.SystemControllers, "tenants.kedge.faros.sh")
}

// UsersConfig returns a rest.Config targeting root:kedge:system:tenants, where
// the User / Organization / Membership CR OBJECTS are stored (this replaces the
// former root:kedge:users). The org *fleet* lives separately under
// root:kedge:tenants (see OrgsConfig).
func (b *Bootstrapper) UsersConfig() *rest.Config {
	return configForPath(b.config, kcppaths.SystemTenants)
}

// OrgsConfig returns a rest.Config targeting the root:kedge:tenants parent
// workspace. The Organization bootstrap controller uses this to create child
// Workspaces of type `organization` — one per Organization CR — at
// root:kedge:tenants:{org-uuid}. The fleet location is unchanged by the
// system-workspace restructure.
func (b *Bootstrapper) OrgsConfig() *rest.Config {
	return configForPath(b.config, kcppaths.TenantsParent)
}

// EnsureOrgWorkspace creates a kcp Workspace at root:kedge:tenants:{orgUUID}
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
// tenants.kedge.faros.sh (Organization, CatalogEntry, future Membership)
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
					"path": "root:kedge",
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
// workspace at root:kedge:tenants:{orgUUID} once it is Ready. The cluster
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
// workspace at root:kedge:tenants:{orgUUID} granting the given User the
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
			"apiVersion": "tenants.kedge.faros.sh/v1alpha1",
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
// root:kedge:tenants:{orgUUID}:{wsUUID} of type `workspace` (see
// config/kcp/workspacetype-workspace.yaml). Used by the organization
// bootstrap controller to create the User's default team Workspace
// inside their personal Org so the portal can pin a default
// X-Kedge-Workspace header. Idempotent: returns nil on AlreadyExists
// and blocks until the workspace reports Ready.
//
// The hub-mediated rule from O-10 only applies to the Organization
// workspace itself; the child team Workspace IS tenant-accessible.
// This method is invoked from the org bootstrap controller with the
// hub's admin credentials so the WorkspaceType admission's bind check
// against tenants.kedge.faros.sh passes (same chain that already
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
					"path": "root:kedge",
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
// Workspace at root:kedge:tenants:{orgUUID}:{wsUUID}. Used by REST
// endpoints that operate inside a Workspace (e.g. the ServiceAccount
// surface) so they can mint a typed kube clientset without rebuilding
// path strings themselves.
func (b *Bootstrapper) ChildWorkspaceConfig(orgUUID, wsUUID string) *rest.Config {
	return configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
}

// GetChildWorkspaceClusterName returns the kcp logical-cluster short
// hash (e.g. "2mmugqjf6k4nwuve") for the child team Workspace at
// root:kedge:tenants:{orgUUID}:{wsUUID}. kcp sets it in
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

// EnsureChildWorkspaceKedgeBinding creates an APIBinding to
// root:kedge:providers.core.faros.sh inside the child team Workspace,
// accepting the permission claims kedge controllers need. This is what
// makes Edge, MCPServer, Placement, VirtualWorkload usable inside the
// user's default Workspace.
//
// The legacy tenant-workspace path (CreateTenantWorkspace) used to
// create the same binding inside root:kedge:tenants:{userID}. PR #211
// retires that flow; the bootstrap controller now drives this method
// for every personal-Org default Workspace.
//
// The tenancy.kcp.io `workspaces` claim IS accepted here. It does not
// widen the tenant user's own RBAC — a permission claim grants the
// APIExport's controllers (kedge, running over the core.faros.sh virtual
// workspace) access to Workspace objects inside this child Workspace.
// The edge mount reconciler needs it: it creates an `edge`-typed mount
// Workspace per kubernetes Edge and watches it via Owns(&Workspace{})
// (pkg/hub/controllers/edge/mount_reconciler.go). The core.faros.sh
// APIExport already declares this claim (config/kcp/apiexport-core.faros.sh.yaml);
// leaving it unaccepted is what produced the "exported but not specified"
// reconcile warnings on the binding.
//
// Preventing tenants from creating arbitrary child workspaces is a
// SEPARATE control, enforced by the `workspace` WorkspaceType
// (limitAllowedChildren caps children to the leaf `edge` type — see
// config/kcp/workspacetype-workspace.yaml), not by withholding this claim.
func (b *Bootstrapper) EnsureChildWorkspaceKedgeBinding(ctx context.Context, orgUUID, wsUUID string) error {
	if orgUUID == "" || wsUUID == "" {
		return fmt.Errorf("EnsureChildWorkspaceKedgeBinding: orgUUID and wsUUID are required")
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
		ObjectMeta: metav1.ObjectMeta{Name: "kedge"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: kcppaths.SystemControllers,
					Name: "core.faros.sh",
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
				// identity hash, must match the claim the core.faros.sh export
				// declares. The edge mount reconciler creates/deletes and
				// Owns(&Workspace{}) the per-edge mount workspaces, so it needs
				// the full verb set the export offers.
				acceptedClaim("tenancy.kcp.io", "workspaces", b.workspaceIdentityHash, allVerbs),
				// apibindings (apis.kcp.io): accepted so kcp labels EVERY
				// APIBinding in this workspace with core.faros.sh's claim label,
				// making them visible through the core.faros.sh APIExport virtual
				// workspace. The GraphQL listener watches apibindings over that VW
				// to trigger a schema rebuild; without this claim the VW shows
				// only this reflexive binding, so enabling another provider would
				// not rebuild the schema until the next informer resync. Must
				// stay in sync with the matching claim core.faros.sh declares
				// (see hack/gen-core-apiexport).
				acceptedClaim("apis.kcp.io", "apibindings", "", []string{"get", "list", "watch"}),
			},
		},
	}
	u, err := toUnstructured(binding)
	if err != nil {
		return fmt.Errorf("converting kedge APIBinding to unstructured: %w", err)
	}
	if _, err := wsClient.Resource(apiBindingGVR).Create(ctx, u, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating kedge APIBinding in %s/%s: %w", orgUUID, wsUUID, err)
	}
	if err := waitForAPIBindingBound(ctx, wsClient, "kedge"); err != nil {
		return err
	}
	// The `workspace` WorkspaceType deliberately does NOT extend
	// root:universal (see config/kcp/workspacetype-workspace.yaml for
	// the rationale), so kcp does not auto-create the `default`
	// namespace. Create it ourselves once the kedge APIBinding's
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
// kept on the kcp Workspace (rather than a kedge wrapper CRD) because
// the kcp Workspace IS the source of truth for workspace lifecycle.
const WorkspaceDeletionAnnotation = "tenants.kedge.faros.sh/deletion-requested-at"

// DeleteOrgWorkspace removes the kcp Workspace at
// root:kedge:tenants:{orgUUID}. Idempotent on NotFound. Cascade callers
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
// root:kedge:tenants:{orgUUID}:{wsUUID}. Idempotent on NotFound.
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
// root:kedge:tenants:{orgUUID}. Empty list if the Org workspace is gone.
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

// ListOrgWorkspaces returns the names (UUIDs) of every Organization
// workspace at root:kedge:tenants. Used by the soft-delete reconciler's
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
// Organization workspace at root:kedge:tenants:{orgUUID}. Used by the
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
const WorkspaceDisplayNameAnnotation = "tenants.kedge.faros.sh/display-name"

// SetWorkspaceDeletionAnnotation stamps the kcp Workspace at
// root:kedge:tenants:{orgUUID}:{wsUUID} with the soft-delete annotation
// (WorkspaceDeletionAnnotation) carrying the given timestamp. The
// soft-delete reconciler picks it up on its next poll. Idempotent
// when the annotation is already set to the same value.
func (b *Bootstrapper) SetWorkspaceDeletionAnnotation(ctx context.Context, orgUUID, wsUUID string, at time.Time) error {
	return b.patchWorkspaceAnnotation(ctx, orgUUID, wsUUID, WorkspaceDeletionAnnotation, at.UTC().Format(time.RFC3339))
}

// ClearWorkspaceDeletionAnnotation removes the soft-delete annotation
// from the Workspace, signalling undelete. Idempotent on already-absent.
func (b *Bootstrapper) ClearWorkspaceDeletionAnnotation(ctx context.Context, orgUUID, wsUUID string) error {
	return b.patchWorkspaceAnnotation(ctx, orgUUID, wsUUID, WorkspaceDeletionAnnotation, "")
}

// SetWorkspaceDisplayName stamps / overwrites the display-name
// annotation on the Workspace.
func (b *Bootstrapper) SetWorkspaceDisplayName(ctx context.Context, orgUUID, wsUUID, displayName string) error {
	return b.patchWorkspaceAnnotation(ctx, orgUUID, wsUUID, WorkspaceDisplayNameAnnotation, displayName)
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
	if _, err := orgClient.Resource(workspaceGVR).Update(ctx, ws, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating workspace %s/%s: %w", orgUUID, wsUUID, err)
	}
	return nil
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

// mcpServerGVR is the tenant-workspace MCPServer resource (distributed via the
// core.faros.sh APIExport). The in-core reconciler
// (pkg/hub/controllers/mcpserver) provisions each server's identity.
var mcpServerGVR = schema.GroupVersionResource{Group: "kedge.faros.sh", Version: "v1alpha1", Resource: "mcpservers"}

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

// MCPFederatedProviderInfo mirrors kedgev1alpha1.FederatedMCPProvider for the portal.
type MCPFederatedProviderInfo struct {
	Name        string                 `json:"name"`
	DisplayName string                 `json:"displayName,omitempty"`
	Reachable   bool                   `json:"reachable"`
	Message     string                 `json:"message,omitempty"`
	Tools       []MCPFederatedToolInfo `json:"tools,omitempty"`
}

// MCPFederatedToolInfo mirrors kedgev1alpha1.FederatedMCPTool for the portal.
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
		"apiVersion": "kedge.faros.sh/v1alpha1",
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
	Group: "providers.kedge.faros.sh", Version: "v1alpha1", Resource: "catalogentries",
}

// builtinAnnotation marks CatalogEntries the hub bootstrap owns. The
// reconcile-delete step ignores any entry without this annotation, so a
// third-party CatalogEntry that happens to share a name with a deleted
// builtin is never touched.
const builtinAnnotation = "providers.kedge.faros.sh/builtin"

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
// only entries carrying providers.kedge.faros.sh/builtin=true are touched.
//
// Waits for the providers.kedge.faros.sh APIBinding to be Bound first;
// without that wait the CatalogEntry resource isn't discoverable yet on
// a fresh hub and we'd race-fail with "no matches for kind".
func ensureBuiltinCatalogEntries(ctx context.Context, providersDynamic dynamic.Interface, enabled []string) error {
	if err := waitForAPIBindingBound(ctx, providersDynamic, "providers.kedge.faros.sh"); err != nil {
		return fmt.Errorf("waiting for providers.kedge.faros.sh APIBinding: %w", err)
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
			"apiVersion": "providers.kedge.faros.sh/v1alpha1",
			"kind":       "CatalogEntry",
			"metadata": map[string]interface{}{
				"name":        e.Name,
				"annotations": map[string]interface{}{builtinAnnotation: "true"},
			},
			"spec": map[string]interface{}{
				"displayName": e.DisplayName,
				"description": e.Description,
				"vendor":      "kedge",
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

var clusterRoleBindingGVR = schema.GroupVersionResource{
	Group:    "rbac.authorization.k8s.io",
	Version:  "v1",
	Resource: "clusterrolebindings",
}

// ensureWorkspaceAdmin creates a cluster-admin ClusterRoleBinding for the given
// rbacIdentity in the workspace targeted by tenantClient. Idempotent.
// Uses the name "kedge-user-admin" to avoid conflicting with the kcp-provisioned
// "workspace-admin" binding.
func ensureWorkspaceAdmin(ctx context.Context, tenantClient dynamic.Interface, rbacIdentity string) error {
	wantSubjects := []interface{}{
		map[string]interface{}{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "User",
			"name":     rbacIdentity,
		},
	}
	crb := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRoleBinding",
			"metadata": map[string]interface{}{
				"name": "kedge-cluster-admin",
			},
			"roleRef": map[string]interface{}{
				"apiGroup": "rbac.authorization.k8s.io",
				"kind":     "ClusterRole",
				"name":     "cluster-admin",
			},
			"subjects": wantSubjects,
		},
	}
	_, err := tenantClient.Resource(clusterRoleBindingGVR).Create(ctx, crb, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating workspace-admin ClusterRoleBinding: %w", err)
	}

	// Reconcile subjects so legacy bindings (e.g. left over from the sub→email
	// RBAC switch) get their subject rewritten to the current rbacIdentity
	// instead of being silently stale.
	existing, getErr := tenantClient.Resource(clusterRoleBindingGVR).Get(ctx, "kedge-cluster-admin", metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("getting existing workspace-admin ClusterRoleBinding: %w", getErr)
	}
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
		phase, _, _ := unstructured.NestedString(ws.Object, "status", "phase")
		return phase == "Ready", nil
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
}

// EnsureProviderAPIBinding creates (or no-ops on AlreadyExists) an
// APIBinding named `bindingName` in the child workspace
// root:kedge:tenants:{orgUUID}:{wsUUID}, pointing at exportPath/exportName.
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
	// hash by scanning sibling APIExports — which races core.faros.sh
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
				Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
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

// exportClaimIdentities returns, per claim, the identityHash the bound
// APIExport (exportPath/exportName) declares for it — keyed "group/resource".
// This is the value kcp validates the binding's claim against, so sourcing it
// from the export (rather than re-deriving it by scanning sibling APIExports'
// spec.resources, which races core.faros.sh regeneration and silently yielded
// an empty hash → PermissionClaimsValid=False → the provider sees zero claimed
// objects) keeps the two in lockstep by construction.
//
// The provisioner (ApplyAPIExport) resolves and stamps these identities on the
// export. A first-party kedge claim (*.faros.sh) MUST end up with a non-empty
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
			if strings.HasSuffix(c.Group, ".faros.sh") && got[key(c.Group, c.Resource)] == "" {
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
// present in the child workspace root:kedge:tenants:{orgUUID}:{wsUUID},
// keyed by provider name. Used by the GET /api/orgs/{org}/workspaces/{ws}/
// providers/enabled handler so the portal can render the
// per-workspace "enabled providers" set on every workspace switch —
// without going through the kcp user-proxy, which 403s any
// non-default workspace path even when commit #220's per-workspace
// RBAC would have allowed the read.
//
// Filtering rule: a binding counts as a "provider binding" iff its
// spec.reference.export.path starts with "root:kedge:providers:" and its
// status.phase is Bound. The trailing segment is the provider name; the binding's own
// metadata.name is the value (existing convention is binding.name ==
// provider.name).
func (b *Bootstrapper) ListProviderAPIBindings(ctx context.Context, orgUUID, wsUUID string) (map[string]string, error) {
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
	out := make(map[string]string, len(list.Items))
	for _, item := range list.Items {
		path, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "path")
		const prefix = "root:kedge:providers:"
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if phase != "Bound" {
			continue
		}
		providerName := path[len(prefix):]
		out[providerName] = item.GetName()
	}
	return out, nil
}

// DeleteProviderAPIBinding removes the named provider APIBinding from the
// child workspace root:kedge:tenants:{orgUUID}:{wsUUID}. NotFound is a no-op so
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

var clusterRoleGVR = schema.GroupVersionResource{
	Group:    "rbac.authorization.k8s.io",
	Version:  "v1",
	Resource: "clusterroles",
}

// edgeProxyGrantName is the name of both the ClusterRole and the
// ClusterRoleBinding the Enable-time edges-proxy grant materializes in the
// tenant workspace, parameterized by provider name so multiple providers'
// grants coexist.
func edgeProxyGrantName(providerName string) string {
	return "kedge:provider:" + providerName + ":edges-proxy"
}

// EnsureProviderEdgeProxyGrant grants `subject` (the provider SA's
// cluster-qualified identity — see pkg/util/identity) the "proxy" verb on the
// edges provider's group (edges.kedge.faros.sh, resources kubernetesclusters +
// linuxservers) in the child workspace root:kedge:tenants:{orgUUID}:{wsUUID}.
// The edges provider's tunnel edgeproxy handler SAR-checks exactly this tuple
// (provider-sdk/tunnel/auth.go), so the grant is what lets a provider with
// CatalogEntry spec.edgeProxyAccess open background connections to the tenant's
// edges. Idempotent; subjects are reconciled on
// re-Enable so a provider workspace re-provision (new cluster ID → new
// qualified subject) heals on the next Enable.
func (b *Bootstrapper) EnsureProviderEdgeProxyGrant(ctx context.Context, orgUUID, wsUUID, providerName, subject string) error {
	if orgUUID == "" || wsUUID == "" || providerName == "" || subject == "" {
		return fmt.Errorf("EnsureProviderEdgeProxyGrant: orgUUID, wsUUID, providerName, subject are required")
	}
	wsConfig := configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
	wsClient, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return fmt.Errorf("creating child workspace client: %w", err)
	}

	name := edgeProxyGrantName(providerName)
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRole",
		"metadata":   map[string]any{"name": name},
		"rules": []any{
			// Workspace access: kcp's workspaceContentAuthorizer requires
			// the "access" verb on "/" before any resource RBAC is even
			// consulted, and a foreign SA is not covered by the tenant
			// workspace's system:authenticated grants (kedge's SAR also
			// drops its groups). Same pairing kcp's own cross-workspace SA
			// e2e uses (TestAPIResourceSchemaVirtualWorkspaceAuthorization).
			map[string]any{
				"nonResourceURLs": []any{"/"},
				"verbs":           []any{"access"},
			},
			// The edge plane is the single `edges` provider owning both kinds
			// under one group edges.kedge.faros.sh. Using its OWN SA it reads +
			// writes the edge CR DIRECTLY in the tenant workspace
			// (kcpurl.ClusterURL, not the APIExport VW):
			//   - get/list/watch on the kinds: validate the agent's bootstrap
			//     join token against status.joinToken (else the tunnel is
			//     rejected "invalid join token") + read SSH creds for edgeproxy.
			//   - update/patch on the /status subresource: markEdgeConnected
			//     flips status.connected/phase and clears status.joinToken when
			//     the agent tunnel comes up (else the edge stays AwaitingAgent /
			//     connected=false forever).
			//   - proxy on the kinds: the SDK tunnel's edgeproxy consumer SAR.
			// Bound to the provider SA's cluster-qualified identity (see
			// pkg/util/identity).
			map[string]any{
				"apiGroups": []any{"edges.kedge.faros.sh"},
				"resources": []any{"kubernetesclusters", "linuxservers"},
				"verbs":     []any{"get", "list", "watch", "proxy"},
			},
			map[string]any{
				"apiGroups": []any{"edges.kedge.faros.sh"},
				"resources": []any{"kubernetesclusters/status", "linuxservers/status"},
				"verbs":     []any{"get", "update", "patch"},
			},
			// The tunnel reads AND writes Secrets + Namespaces DIRECTLY with the
			// provider SA (not the VW):
			//   - read: token-exchange reads the agent's SA kubeconfig Secret
			//     (edge-<name>-kubeconfig) + SSH-cred lookups read
			//     spec.sshCredentialsRef Secrets.
			//   - create/update: on a SERVER edge's connect, markEdgeConnected →
			//     storeSSHCredentials creates a namespace + a
			//     <edge>-ssh-credentials Secret and records it in
			//     status.sshCredentials. Without create access the Secret write
			//     403s, status.sshCredentials stays null, and the SSH handler has
			//     no creds → openAgentSSHTunnel fails → the browser terminal shows
			//     "session ended".
			map[string]any{
				"apiGroups": []any{""},
				"resources": []any{"secrets"},
				"verbs":     []any{"get", "list", "watch", "create", "update"},
			},
			map[string]any{
				"apiGroups": []any{""},
				"resources": []any{"namespaces"},
				"verbs":     []any{"get", "create"},
			},
			// When an agent RECONNECTS with its SA token (after token-exchange),
			// the tunnel authenticates it via delegated authn/authz: a TokenReview
			// + SubjectAccessReview run with the provider SA in the tenant
			// workspace. The provider SA must be able to CREATE those review
			// objects — otherwise authorizeFn errors and the reconnect is rejected
			// (bad handshake), even though the JOIN-token first connect (which
			// only reads the CR) succeeds. This is the "initial join works,
			// follow-up SA-token connect fails" case.
			map[string]any{
				"apiGroups": []any{"authentication.k8s.io"},
				"resources": []any{"tokenreviews"},
				"verbs":     []any{"create"},
			},
			map[string]any{
				"apiGroups": []any{"authorization.k8s.io"},
				"resources": []any{"subjectaccessreviews"},
				"verbs":     []any{"create"},
			},
		},
	}}
	if _, err := wsClient.Resource(clusterRoleGVR).Create(ctx, role, metav1.CreateOptions{}); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating ClusterRole %q: %w", name, err)
		}
		// Reconcile the rules on an existing ClusterRole so verb/resource changes
		// (e.g. adding /status writes + secrets reads) take effect on re-Enable
		// rather than being silently skipped by the create.
		existingRole, getErr := wsClient.Resource(clusterRoleGVR).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("getting ClusterRole %q: %w", name, getErr)
		}
		wantRules, _, _ := unstructured.NestedSlice(role.Object, "rules")
		gotRules, _, _ := unstructured.NestedSlice(existingRole.Object, "rules")
		if !reflect.DeepEqual(gotRules, wantRules) {
			if err := unstructured.SetNestedSlice(existingRole.Object, wantRules, "rules"); err != nil {
				return fmt.Errorf("rewriting ClusterRole rules: %w", err)
			}
			if _, err := wsClient.Resource(clusterRoleGVR).Update(ctx, existingRole, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("updating ClusterRole %q: %w", name, err)
			}
		}
	}

	// Bind the qualified identity (the correct cross-workspace form) AND its
	// un-qualified local fallback. On the tunnel's direct CR-read path kcp only
	// qualifies the provider SA when its token carries a verified home-cluster
	// claim; when it doesn't (e.g. a legacy token not yet stamped by the token
	// controller), the request authorizes as the plain
	// system:serviceaccount:{ns}:{name}. Binding both makes the grant match
	// either way, so the join-token validation isn't rejected as "invalid".
	wantSubjects := []any{
		map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "User",
			"name":     subject,
		},
	}
	if local, ok := identity.LocalFromQualified(subject); ok {
		wantSubjects = append(wantSubjects, map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "User",
			"name":     local,
		})
	}
	crb := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]any{"name": name},
		"roleRef": map[string]any{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     name,
		},
		"subjects": wantSubjects,
	}}
	_, err = wsClient.Resource(clusterRoleBindingGVR).Create(ctx, crb, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ClusterRoleBinding %q: %w", name, err)
	}
	existing, getErr := wsClient.Resource(clusterRoleBindingGVR).Get(ctx, name, metav1.GetOptions{})
	if getErr != nil {
		return fmt.Errorf("getting ClusterRoleBinding %q: %w", name, getErr)
	}
	gotSubjects, _, _ := unstructured.NestedSlice(existing.Object, "subjects")
	if reflect.DeepEqual(gotSubjects, wantSubjects) {
		return nil
	}
	if err := unstructured.SetNestedSlice(existing.Object, wantSubjects, "subjects"); err != nil {
		return fmt.Errorf("rewriting ClusterRoleBinding subjects: %w", err)
	}
	if _, err := wsClient.Resource(clusterRoleBindingGVR).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating ClusterRoleBinding %q: %w", name, err)
	}
	return nil
}

// RemoveProviderEdgeProxyGrant deletes the ClusterRole/ClusterRoleBinding
// pair EnsureProviderEdgeProxyGrant created. NotFound is a no-op — Disable
// must succeed for providers that never had the grant.
func (b *Bootstrapper) RemoveProviderEdgeProxyGrant(ctx context.Context, orgUUID, wsUUID, providerName string) error {
	if orgUUID == "" || wsUUID == "" || providerName == "" {
		return fmt.Errorf("RemoveProviderEdgeProxyGrant: orgUUID, wsUUID, providerName are required")
	}
	wsConfig := configForPath(b.config, childWorkspacePath(orgUUID, wsUUID))
	wsClient, err := dynamic.NewForConfig(wsConfig)
	if err != nil {
		return fmt.Errorf("creating child workspace client: %w", err)
	}
	name := edgeProxyGrantName(providerName)
	if err := wsClient.Resource(clusterRoleBindingGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting ClusterRoleBinding %q: %w", name, err)
	}
	if err := wsClient.Resource(clusterRoleGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting ClusterRole %q: %w", name, err)
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
