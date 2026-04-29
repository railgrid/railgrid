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
	"embed"
	"fmt"
	"time"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/faroshq/faros-kedge/config/kcp"
	"github.com/faroshq/faros-kedge/pkg/apiurl"
	"github.com/faroshq/faros-kedge/pkg/util/confighelpers"
)

//go:embed user-crd/kedge.faros.sh_users.yaml
var userCRDFS embed.FS

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
	kubernetesGVR = schema.GroupVersionResource{
		Group: "mcp.kedge.faros.sh", Version: "v1alpha1", Resource: "kubernetes",
	}
)

// Bootstrapper sets up the kcp workspace hierarchy and API exports.
type Bootstrapper struct {
	config *rest.Config
	// workspaceIdentityHash is the identity hash of the tenancy.kcp.io APIExport
	// from the root workspace. Needed for permission claims on workspaces.
	workspaceIdentityHash string
}

// NewBootstrapper creates a new bootstrapper.
func NewBootstrapper(config *rest.Config) *Bootstrapper {
	return &Bootstrapper{config: config}
}

// Bootstrap creates the workspace hierarchy:
//
//	root:kedge                     - Root kedge workspace
//	root:kedge:providers           - Holds APIExport "kedge.faros.sh"
//	root:kedge:tenants             - Parent for tenant workspaces
//	  root:kedge:tenants:{userID}  - Per-user workspace (created on login)
//	root:kedge:users               - Stores User CRDs
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

	logger.Info("Bootstrapping child workspaces: providers, tenants, users")
	if err := confighelpers.Bootstrap(ctx, kedgeDiscovery, kedgeDynamic, kcp.KedgeWorkspaceFS); err != nil {
		return fmt.Errorf("bootstrapping child workspaces: %w", err)
	}
	for _, name := range []string{"providers", "tenants", "users"} {
		if err := waitForWorkspaceReady(ctx, kedgeDynamic, name); err != nil {
			return fmt.Errorf("waiting for %s workspace: %w", name, err)
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

	// 5. Bootstrap APIResourceSchemas and APIExport in root:kedge:providers.
	//    The __TENANCY_IDENTITY_HASH__ placeholder in the APIExport YAML is
	//    replaced with the actual identity hash from step 4.
	providersConfig := configForPath(b.config, "root:kedge:providers")
	providersDynamic, providersDiscovery, err := newClients(providersConfig)
	if err != nil {
		return fmt.Errorf("creating providers clients: %w", err)
	}

	logger.Info("Bootstrapping APIResourceSchemas and APIExport")
	if err := confighelpers.Bootstrap(ctx, providersDiscovery, providersDynamic, kcp.ProvidersFS,
		confighelpers.ReplaceOption("__TENANCY_IDENTITY_HASH__", identityHash),
	); err != nil {
		return fmt.Errorf("bootstrapping providers: %w", err)
	}

	// 6. Install User CRD in root:kedge:users workspace.
	logger.Info("Installing User CRD in root:kedge:users")
	if err := b.installUserCRD(ctx); err != nil {
		return fmt.Errorf("installing User CRD: %w", err)
	}

	logger.Info("kcp bootstrap complete")
	return nil
}

// UsersConfig returns a rest.Config targeting the root:kedge:users workspace
// where User CRDs are stored.
func (b *Bootstrapper) UsersConfig() *rest.Config {
	return configForPath(b.config, "root:kedge:users")
}

// installUserCRD installs the User CRD in the root:kedge:users workspace.
func (b *Bootstrapper) installUserCRD(ctx context.Context) error {
	usersConfig := b.UsersConfig()

	apiextClient, err := apiextensionsclient.NewForConfig(usersConfig)
	if err != nil {
		return fmt.Errorf("creating apiextensions client: %w", err)
	}

	data, err := userCRDFS.ReadFile("user-crd/kedge.faros.sh_users.yaml")
	if err != nil {
		return fmt.Errorf("reading embedded User CRD: %w", err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		return fmt.Errorf("unmarshaling User CRD: %w", err)
	}

	existing, err := apiextClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		if _, err := apiextClient.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, &crd, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating User CRD: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting User CRD: %w", err)
	} else {
		crd.ResourceVersion = existing.ResourceVersion
		if _, err := apiextClient.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, &crd, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating User CRD: %w", err)
		}
	}

	// Wait for CRD to be established.
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		c, err := apiextClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		for _, cond := range c.Status.Conditions {
			if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

// CreateTenantWorkspace creates a workspace for a user, binds the kedge API,
// and returns the workspace's logical cluster name assigned by kcp.
// rbacIdentity is the kcp user identity (e.g. "kedge:static:abc123") that will
// be granted cluster-admin in the new workspace.
func (b *Bootstrapper) CreateTenantWorkspace(ctx context.Context, userID, rbacIdentity string) (string, error) {
	logger := klog.FromContext(ctx)

	// Client targeting root:kedge:tenants.
	tenantsConfig := configForPath(b.config, "root:kedge:tenants")
	tenantsClient, err := dynamic.NewForConfig(tenantsConfig)
	if err != nil {
		return "", fmt.Errorf("creating tenants client: %w", err)
	}

	// Create workspace for the user.
	ws := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "tenancy.kcp.io/v1alpha1",
			"kind":       "Workspace",
			"metadata": map[string]interface{}{
				"name": userID,
			},
			"spec": map[string]interface{}{
				"type": map[string]interface{}{
					"name": "universal",
					"path": "root",
				},
			},
		},
	}

	_, err = tenantsClient.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating tenant workspace %s: %w", userID, err)
	}

	if err := waitForWorkspaceReady(ctx, tenantsClient, userID); err != nil {
		return "", fmt.Errorf("waiting for tenant workspace %s: %w", userID, err)
	}

	// Read the workspace to get the logical cluster name assigned by kcp.
	readyWS, err := tenantsClient.Resource(workspaceGVR).Get(ctx, userID, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting workspace %s: %w", userID, err)
	}
	clusterName, _, _ := unstructured.NestedString(readyWS.Object, "spec", "cluster")
	if clusterName == "" {
		return "", fmt.Errorf("workspace %s has no spec.cluster after becoming ready", userID)
	}

	// Client targeting root:kedge:tenants:<userID>.
	tenantConfig := configForPath(b.config, "root:kedge:tenants:"+userID)
	tenantClient, err := dynamic.NewForConfig(tenantConfig)
	if err != nil {
		return "", fmt.Errorf("creating tenant client: %w", err)
	}

	// Create APIBinding with accepted permission claims for core resources.
	allVerbs := []string{"get", "list", "watch", "create", "update", "delete"}
	binding := &apisv1alpha2.APIBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apisv1alpha2.SchemeGroupVersion.String(),
			Kind:       "APIBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "kedge",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: "root:kedge:providers",
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
				acceptedClaim("tenancy.kcp.io", "workspaces", b.workspaceIdentityHash, allVerbs),
			},
		},
	}

	u, err := toUnstructured(binding)
	if err != nil {
		return "", fmt.Errorf("converting APIBinding to unstructured: %w", err)
	}

	_, err = tenantClient.Resource(apiBindingGVR).Create(ctx, u, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		// Update existing binding to ensure permission claims are current.
		existing, getErr := tenantClient.Resource(apiBindingGVR).Get(ctx, "kedge", metav1.GetOptions{})
		if getErr != nil {
			return "", fmt.Errorf("getting existing APIBinding in tenant workspace %s: %w", userID, getErr)
		}
		u.SetResourceVersion(existing.GetResourceVersion())
		if _, err := tenantClient.Resource(apiBindingGVR).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
			return "", fmt.Errorf("updating APIBinding in tenant workspace %s: %w", userID, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("creating APIBinding in tenant workspace %s: %w", userID, err)
	}

	// Wait for the core.faros.sh APIBinding to be Bound — this single binding
	// gives access to all kedge API groups (kedge.faros.sh, mcp.kedge.faros.sh, etc.).
	if waitErr := waitForAPIBindingBound(ctx, tenantClient, "kedge"); waitErr != nil {
		logger.Error(waitErr, "kedge APIBinding did not become Bound (non-fatal)", "userID", userID)
	}

	// Grant the user cluster-admin in their own workspace so they can manage
	// their resources directly (e.g. via GraphQL or kubectl with their token).
	if rbacIdentity != "" {
		if err := ensureWorkspaceAdmin(ctx, tenantClient, rbacIdentity); err != nil {
			// Non-fatal: log and continue.
			logger.Error(err, "Failed to create workspace-admin ClusterRoleBinding (non-fatal)", "userID", userID)
		}
	}

	// Ensure a "default" Kubernetes MCP object exists in the tenant workspace.
	// This object enables the multi-edge MCP endpoint without requiring the
	// user to create it manually.  An empty EdgeSelector selects all edges.
	defaultKubernetes := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "mcp.kedge.faros.sh/v1alpha1",
			"kind":       "Kubernetes",
			"metadata": map[string]interface{}{
				"name": "default",
			},
			"spec": map[string]interface{}{},
		},
	}
	if _, createErr := tenantClient.Resource(kubernetesGVR).Create(ctx, defaultKubernetes, metav1.CreateOptions{}); createErr != nil {
		if !errors.IsAlreadyExists(createErr) {
			// Non-fatal: log and continue.
			logger.Error(createErr, "Failed to create default Kubernetes MCP in tenant workspace (non-fatal)", "userID", userID)
		}
	}

	logger.Info("Tenant workspace created", "userID", userID, "clusterName", clusterName)
	return clusterName, nil
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
			"subjects": []interface{}{
				map[string]interface{}{
					"apiGroup": "rbac.authorization.k8s.io",
					"kind":     "User",
					"name":     rbacIdentity,
				},
			},
		},
	}
	_, err := tenantClient.Resource(clusterRoleBindingGVR).Create(ctx, crb, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating workspace-admin ClusterRoleBinding: %w", err)
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

// acceptedClaim builds an AcceptablePermissionClaim with matchAll selector.
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
			return false, nil
		}
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		return phase == "Bound", nil
	})
}
