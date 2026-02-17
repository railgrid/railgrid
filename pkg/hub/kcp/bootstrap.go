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

package kcp

import (
	"context"
	"embed"
	"fmt"
	"strings"
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
	logger.Info("Fetching tenancy.kcp.io identity hash from root workspace")
	tenancyExport, err := rootDynamic.Resource(apiExportGVR).Get(ctx, "tenancy.kcp.io", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting tenancy.kcp.io APIExport from root: %w", err)
	}
	identityHash, _, _ := unstructured.NestedString(tenancyExport.Object, "status", "identityHash")
	if identityHash == "" {
		return fmt.Errorf("tenancy.kcp.io APIExport has no identity hash yet")
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
func (b *Bootstrapper) CreateTenantWorkspace(ctx context.Context, userID string) (string, error) {
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
					Name: "kedge.faros.sh",
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

	// TODO: Wait for APIBinding to be ready before returning, to ensure the tenant can use the API immediately after login.

	logger.Info("Tenant workspace created", "userID", userID, "clusterName", clusterName)
	return clusterName, nil
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
func waitForWorkspaceReady(ctx context.Context, client dynamic.Interface, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 60*time.Second, true, func(ctx context.Context) (bool, error) {
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
func AppendClusterPath(host, clusterPath string) string {
	host = strings.TrimSuffix(host, "/")
	if idx := strings.Index(host, "/clusters/"); idx != -1 {
		host = host[:idx]
	}
	return host + "/clusters/" + clusterPath
}
