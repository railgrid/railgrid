package kcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	kcpconfig "github.com/faroshq/faros-kedge/config/kcp"
	"github.com/faroshq/faros-kedge/config/kcp/apiexports"
	"github.com/faroshq/faros-kedge/config/kcp/apiresourceschemas"
)

// KCP resource GVRs (no kcp-dev/kcp Go dependency).
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
		Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apibindings",
	}
)

// Bootstrapper sets up the KCP workspace hierarchy and API exports.
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
func (b *Bootstrapper) Bootstrap(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Bootstrapping KCP workspace hierarchy")

	// 1. Create dynamic client targeting root workspace.
	rootClient, err := dynamic.NewForConfig(b.config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	// 2. Apply the root:kedge workspace.
	logger.Info("Creating root:kedge workspace")
	if err := applyResourcesFromFS(ctx, rootClient, kcpconfig.WorkspaceFS(), "workspace-kedge.yaml"); err != nil {
		return fmt.Errorf("applying kedge workspace: %w", err)
	}

	if err := waitForWorkspaceReady(ctx, rootClient, "kedge"); err != nil {
		return fmt.Errorf("waiting for kedge workspace: %w", err)
	}

	// 3. Client targeting root:kedge.
	kedgeConfig := rest.CopyConfig(b.config)
	kedgeConfig.Host = appendClusterPath(kedgeConfig.Host, "root:kedge")
	kedgeClient, err := dynamic.NewForConfig(kedgeConfig)
	if err != nil {
		return fmt.Errorf("creating kedge client: %w", err)
	}

	// 4. Apply child workspaces: providers, tenants.
	logger.Info("Creating child workspaces: providers, tenants")
	if err := applyResourcesFromFS(ctx, kedgeClient, kcpconfig.WorkspaceFS(), "workspace-providers.yaml", "workspace-tenants.yaml"); err != nil {
		return fmt.Errorf("applying child workspaces: %w", err)
	}

	if err := waitForWorkspaceReady(ctx, kedgeClient, "providers"); err != nil {
		return fmt.Errorf("waiting for providers workspace: %w", err)
	}
	if err := waitForWorkspaceReady(ctx, kedgeClient, "tenants"); err != nil {
		return fmt.Errorf("waiting for tenants workspace: %w", err)
	}

	// 5. Client targeting root:kedge:providers.
	providersConfig := rest.CopyConfig(b.config)
	providersConfig.Host = appendClusterPath(providersConfig.Host, "root:kedge:providers")
	providersClient, err := dynamic.NewForConfig(providersConfig)
	if err != nil {
		return fmt.Errorf("creating providers client: %w", err)
	}

	// 6. Apply APIResourceSchemas.
	logger.Info("Applying APIResourceSchemas")
	if err := applyAllFromFS(ctx, providersClient, apiresourceschemas.FS); err != nil {
		return fmt.Errorf("applying APIResourceSchemas: %w", err)
	}

	// 7. Apply APIExport.
	logger.Info("Applying APIExport")
	if err := applyAllFromFS(ctx, providersClient, apiexports.FS); err != nil {
		return fmt.Errorf("applying APIExport: %w", err)
	}

	logger.Info("KCP bootstrap complete")
	return nil
}

// CreateTenantWorkspace creates a workspace for a user and binds the kedge API.
func (b *Bootstrapper) CreateTenantWorkspace(ctx context.Context, userID string) error {
	logger := klog.FromContext(ctx)

	// Client targeting root:kedge:tenants.
	tenantsConfig := rest.CopyConfig(b.config)
	tenantsConfig.Host = appendClusterPath(tenantsConfig.Host, "root:kedge:tenants")
	tenantsClient, err := dynamic.NewForConfig(tenantsConfig)
	if err != nil {
		return fmt.Errorf("creating tenants client: %w", err)
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
		return fmt.Errorf("creating tenant workspace %s: %w", userID, err)
	}

	if err := waitForWorkspaceReady(ctx, tenantsClient, userID); err != nil {
		return fmt.Errorf("waiting for tenant workspace %s: %w", userID, err)
	}

	// Client targeting root:kedge:tenants:<userID>.
	tenantConfig := rest.CopyConfig(b.config)
	tenantConfig.Host = appendClusterPath(tenantConfig.Host, "root:kedge:tenants:"+userID)
	tenantClient, err := dynamic.NewForConfig(tenantConfig)
	if err != nil {
		return fmt.Errorf("creating tenant client: %w", err)
	}

	// Create APIBinding.
	binding := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apis.kcp.io/v1alpha1",
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
			},
		},
	}

	_, err = tenantClient.Resource(apiBindingGVR).Create(ctx, binding, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating APIBinding in tenant workspace %s: %w", userID, err)
	}

	// TODO: Wait for APIBinding to be ready before returning, to ensure the tenant can use the API immediately after login.

	logger.Info("Tenant workspace created", "userID", userID)
	return nil
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
// Workspaces are create-only (skip if exists) because the KCP system sets spec.URL
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

// kindToResource maps KCP kinds to their plural resource names.
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

// appendClusterPath sets the /clusters/<path> segment on a KCP URL.
// If the host already contains a /clusters/ path (e.g. from the admin
// kubeconfig), it is replaced rather than appended.
func appendClusterPath(host, clusterPath string) string {
	host = strings.TrimSuffix(host, "/")
	if idx := strings.Index(host, "/clusters/"); idx != -1 {
		host = host[:idx]
	}
	return host + "/clusters/" + clusterPath
}
