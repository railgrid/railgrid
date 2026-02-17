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
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/faroshq/faros-kedge/config/kcp"
)

//go:embed user-crd/kedge.faros.sh_users.yaml
var userCRDFS embed.FS

// kcp resource GVRs (no kcp-dev/kcp Go dependency).
var (
	workspaceGVR = schema.GroupVersionResource{
		Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces",
	}
	apiResourceSchemaGVR = schema.GroupVersionResource{
		Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas",
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

	// 1. Create dynamic client targeting root workspace.
	rootClient, err := dynamic.NewForConfig(b.config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	// 2. Apply the root:kedge workspace.
	logger.Info("Creating root:kedge workspace")
	if err := applyResourcesFromFS(ctx, rootClient, kcp.WorkspaceFS, "workspace-kedge.yaml"); err != nil {
		return fmt.Errorf("applying kedge workspace: %w", err)
	}

	if err := waitForWorkspaceReady(ctx, rootClient, "kedge"); err != nil {
		return fmt.Errorf("waiting for kedge workspace: %w", err)
	}

	// 3. Client targeting root:kedge.
	kedgeConfig := rest.CopyConfig(b.config)
	kedgeConfig.Host = AppendClusterPath(kedgeConfig.Host, "root:kedge")
	kedgeClient, err := dynamic.NewForConfig(kedgeConfig)
	if err != nil {
		return fmt.Errorf("creating kedge client: %w", err)
	}

	// 4. Apply child workspaces: providers, tenants, users.
	logger.Info("Creating child workspaces: providers, tenants, users")
	if err := applyResourcesFromFS(ctx, kedgeClient, kcp.WorkspaceFS, "workspace-providers.yaml", "workspace-tenants.yaml", "workspace-users.yaml"); err != nil {
		return fmt.Errorf("applying child workspaces: %w", err)
	}

	if err := waitForWorkspaceReady(ctx, kedgeClient, "providers"); err != nil {
		return fmt.Errorf("waiting for providers workspace: %w", err)
	}
	if err := waitForWorkspaceReady(ctx, kedgeClient, "tenants"); err != nil {
		return fmt.Errorf("waiting for tenants workspace: %w", err)
	}
	if err := waitForWorkspaceReady(ctx, kedgeClient, "users"); err != nil {
		return fmt.Errorf("waiting for users workspace: %w", err)
	}

	// 5. Client targeting root:kedge:providers.
	providersConfig := rest.CopyConfig(b.config)
	providersConfig.Host = AppendClusterPath(providersConfig.Host, "root:kedge:providers")
	providersClient, err := dynamic.NewForConfig(providersConfig)
	if err != nil {
		return fmt.Errorf("creating providers client: %w", err)
	}

	// 6. Apply APIResourceSchemas.
	logger.Info("Applying APIResourceSchemas")
	if err := applyAllFromFS(ctx, providersClient, kcp.APIResourceSchemaFS); err != nil {
		return fmt.Errorf("applying APIResourceSchemas: %w", err)
	}

	// 7. Apply APIExport.
	logger.Info("Applying APIExport")
	if err := applyAllFromFS(ctx, providersClient, kcp.APIExportFS); err != nil {
		return fmt.Errorf("applying APIExport: %w", err)
	}

	// 8. Install User CRD in root:kedge:users workspace.
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
	usersConfig := rest.CopyConfig(b.config)
	usersConfig.Host = AppendClusterPath(usersConfig.Host, "root:kedge:users")
	return usersConfig
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
	tenantsConfig := rest.CopyConfig(b.config)
	tenantsConfig.Host = AppendClusterPath(tenantsConfig.Host, "root:kedge:tenants")
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
	tenantConfig := rest.CopyConfig(b.config)
	tenantConfig.Host = AppendClusterPath(tenantConfig.Host, "root:kedge:tenants:"+userID)
	tenantClient, err := dynamic.NewForConfig(tenantConfig)
	if err != nil {
		return "", fmt.Errorf("creating tenant client: %w", err)
	}

	// Create APIBinding with accepted permission claims for core resources.
	binding := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apis.kcp.io/v1alpha2",
			"kind":       "APIBinding",
			"metadata": map[string]interface{}{
				"name": "kedge",
			},
			"spec": map[string]interface{}{
				"reference": map[string]interface{}{
					"export": map[string]interface{}{
						"path": "root:kedge:providers",
						"name": "kedge.faros.sh",
					},
				},
				"permissionClaims": []interface{}{
					map[string]interface{}{
						"group":    "",
						"resource": "secrets",
						"state":    "Accepted",
						"verbs":    []interface{}{"get", "list", "watch", "create", "update", "delete"},
						"selector": map[string]interface{}{"matchAll": true},
					},
					map[string]interface{}{
						"group":    "",
						"resource": "namespaces",
						"state":    "Accepted",
						"verbs":    []interface{}{"get", "list", "watch", "create"},
						"selector": map[string]interface{}{"matchAll": true},
					},
					map[string]interface{}{
						"group":    "",
						"resource": "configmaps",
						"state":    "Accepted",
						"verbs":    []interface{}{"get", "list", "watch", "create", "update", "delete"},
						"selector": map[string]interface{}{"matchAll": true},
					},
					map[string]interface{}{
						"group":    "",
						"resource": "serviceaccounts",
						"state":    "Accepted",
						"verbs":    []interface{}{"get", "list", "watch", "create", "update", "delete"},
						"selector": map[string]interface{}{"matchAll": true},
					},
					map[string]interface{}{
						"group":    "rbac.authorization.k8s.io",
						"resource": "clusterroles",
						"state":    "Accepted",
						"verbs":    []interface{}{"get", "list", "watch", "create", "update", "delete"},
						"selector": map[string]interface{}{"matchAll": true},
					},
					map[string]interface{}{
						"group":    "rbac.authorization.k8s.io",
						"resource": "clusterrolebindings",
						"state":    "Accepted",
						"verbs":    []interface{}{"get", "list", "watch", "create", "update", "delete"},
						"selector": map[string]interface{}{"matchAll": true},
					},
				},
			},
		},
	}

	_, err = tenantClient.Resource(apiBindingGVR).Create(ctx, binding, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		// Update existing binding to ensure permission claims are current.
		existing, getErr := tenantClient.Resource(apiBindingGVR).Get(ctx, "kedge", metav1.GetOptions{})
		if getErr != nil {
			return "", fmt.Errorf("getting existing APIBinding in tenant workspace %s: %w", userID, getErr)
		}
		binding.SetResourceVersion(existing.GetResourceVersion())
		if _, err := tenantClient.Resource(apiBindingGVR).Update(ctx, binding, metav1.UpdateOptions{}); err != nil {
			return "", fmt.Errorf("updating APIBinding in tenant workspace %s: %w", userID, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("creating APIBinding in tenant workspace %s: %w", userID, err)
	}

	// TODO: Wait for APIBinding to be ready before returning, to ensure the tenant can use the API immediately after login.

	logger.Info("Tenant workspace created", "userID", userID, "clusterName", clusterName)
	return clusterName, nil
}

// applyResourcesFromFS reads specific YAML files from an embed.FS and applies them.
func applyResourcesFromFS(ctx context.Context, client dynamic.Interface, fsys fs.FS, filenames ...string) error {
	for _, name := range filenames {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		if err := applyYAML(ctx, client, data); err != nil {
			return fmt.Errorf("applying %s: %w", name, err)
		}
	}
	return nil
}

// applyAllFromFS reads all YAML files from an embed.FS and applies them.
func applyAllFromFS(ctx context.Context, client dynamic.Interface, fsys fs.FS) error {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("reading directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		if err := applyYAML(ctx, client, data); err != nil {
			return fmt.Errorf("applying %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// applyYAML unmarshals YAML to unstructured, determines GVR, and create-or-updates.
// Workspaces are create-only (skip if exists) because the kcp system sets spec.URL
// and spec.cluster after creation, and those fields cannot be unset on update.
func applyYAML(ctx context.Context, client dynamic.Interface, data []byte) error {
	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		return fmt.Errorf("converting YAML to JSON: %w", err)
	}

	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(jsonData, &obj.Object); err != nil {
		return fmt.Errorf("unmarshaling JSON: %w", err)
	}

	gvr := gvkToGVR(obj.GetObjectKind().GroupVersionKind())
	name := obj.GetName()

	// Workspaces have system-managed fields (URL, cluster) that cannot be
	// unset on update, so we only create and skip if already exists.
	if obj.GetKind() == "Workspace" {
		_, err := client.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}

	existing, err := client.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = client.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
		return err
	} else if err != nil {
		return err
	}

	// Update: carry over resource version.
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = client.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// kindToResource maps kcp kinds to their plural resource names.
// TODO: Move to restMapper-based approach.
var kindToResource = map[string]string{
	"Workspace":         "workspaces",
	"APIResourceSchema": "apiresourceschemas",
	"APIExport":         "apiexports",
	"APIBinding":        "apibindings",
}

// gvkToGVR converts a GVK to a GVR using the version from the object.
func gvkToGVR(gvk schema.GroupVersionKind) schema.GroupVersionResource {
	if resource, ok := kindToResource[gvk.Kind]; ok {
		return schema.GroupVersionResource{
			Group:    gvk.Group,
			Version:  gvk.Version,
			Resource: resource,
		}
	}
	// Best-effort: lowercase + "s" pluralization.
	return schema.GroupVersionResource{
		Group:    gvk.Group,
		Version:  gvk.Version,
		Resource: strings.ToLower(gvk.Kind) + "s",
	}
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
