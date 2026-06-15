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

// Package admin implements the platform-admin surface mounted at /api/admin/*
// and surfaced in the portal's gated /bonkers area. It lets a platform admin
// see all users / organizations / providers / root identities. It is read-only:
// provider provisioning (workspace + ServiceAccount + kubeconfig) is driven
// declaratively by the Provider CR reconciler
// (pkg/hub/providers/provider_controller.go), not by an admin HTTP action.
package admin

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros-kedge/pkg/apiurl"
	"github.com/faroshq/faros-kedge/pkg/hub/providers"
	"github.com/faroshq/faros-kedge/pkg/kcppaths"
)

// exportsWorkspace is where the platform APIExports live (system:controllers).
// ListRootIdentities reads APIExport identity hashes from here.
const exportsWorkspace = kcppaths.SystemControllers

var apiExportGVR = schema.GroupVersionResource{
	Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports",
}

// providerGVR is the declarative Provider provisioning record. Provider objects
// live in root:kedge:system:providers; creating one drives the Provider
// reconciler (pkg/hub/providers/provider_controller.go) to provision the
// sub-workspace + ServiceAccount + kubeconfig Secret.
var providerGVR = schema.GroupVersionResource{
	Group: "admin.kedge.faros.sh", Version: "v1alpha1", Resource: "providers",
}

// CreateProvider create-or-updates a Provider object in
// root:kedge:system:providers. name drives the provisioned sub-workspace
// (root:kedge:providers:<name>); displayName is informational. Idempotent.
func (s *Service) CreateProvider(ctx context.Context, name, displayName string) error {
	cfg := rest.CopyConfig(s.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client for %s: %w", kcppaths.SystemProviders, err)
	}
	spec := map[string]any{}
	if displayName != "" {
		spec["displayName"] = displayName
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.kedge.faros.sh/v1alpha1",
		"kind":       "Provider",
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
	if _, err := cl.Resource(providerGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating Provider %q in %s: %w", name, kcppaths.SystemProviders, err)
	}
	return nil
}

// GetProviderKubeconfig returns the minted kubeconfig the Provider controller
// wrote into a Secret in root:kedge:system:providers. It reads the Provider's
// status.secretRef to locate the Secret (falling back to the
// "<name>-kubeconfig" / "default" / "kubeconfig" conventions). Returns a nil
// slice + nil error when the Provider exists but hasn't been provisioned yet
// (no Secret), so callers can surface "not ready".
func (s *Service) GetProviderKubeconfig(ctx context.Context, name string) ([]byte, error) {
	cfg := rest.CopyConfig(s.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for %s: %w", kcppaths.SystemProviders, err)
	}
	prov, err := dyn.Resource(providerGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting Provider %q: %w", name, err)
	}
	secretNS, _, _ := unstructured.NestedString(prov.Object, "status", "secretRef", "namespace")
	secretName, _, _ := unstructured.NestedString(prov.Object, "status", "secretRef", "name")
	secretKey, _, _ := unstructured.NestedString(prov.Object, "status", "secretRef", "key")
	if secretNS == "" {
		secretNS = "default"
	}
	if secretName == "" {
		secretName = name + "-kubeconfig"
	}
	if secretKey == "" {
		secretKey = "kubeconfig"
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("typed client for %s: %w", kcppaths.SystemProviders, err)
	}
	secret, err := cs.CoreV1().Secrets(secretNS).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil // provisioned not complete yet
	}
	if err != nil {
		return nil, fmt.Errorf("getting kubeconfig Secret %s/%s: %w", secretNS, secretName, err)
	}
	return secret.Data[secretKey], nil
}

// DeleteProvider removes a Provider object from root:kedge:system:providers.
// The reconciler's finalizer then tears down the sub-workspace. Idempotent.
func (s *Service) DeleteProvider(ctx context.Context, name string) error {
	cfg := rest.CopyConfig(s.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client for %s: %w", kcppaths.SystemProviders, err)
	}
	if err := cl.Resource(providerGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting Provider %q in %s: %w", name, kcppaths.SystemProviders, err)
	}
	return nil
}

// Service performs read-only admin queries against kcp.
type Service struct {
	prov *providers.Provisioner
	// kcpConfig is the admin kcp rest.Config (used for cross-workspace reads
	// like root-identity discovery).
	kcpConfig *rest.Config
}

// NewService returns an admin Service. hubExternalURL/providerInternalURL are
// accepted for call-site compatibility but no longer used now that provider
// provisioning (which baked a server URL into minted kubeconfigs) moved to the
// Provider CR reconciler.
func NewService(kcpConfig *rest.Config, _, _ string) *Service {
	return &Service{
		prov:      providers.NewProvisioner(kcpConfig),
		kcpConfig: kcpConfig,
	}
}

// OnboardedWorkspace mirrors providers.OnboardedWorkspace for the admin API.
type OnboardedWorkspace struct {
	Name    string
	Cluster string
	Phase   string
}

// ListOnboardedWorkspaces returns the provider sub-workspaces created by
// onboarding, so the admin UI can show providers that have been onboarded but
// whose Helm chart (and CatalogEntry) is not yet installed.
func (s *Service) ListOnboardedWorkspaces(ctx context.Context) ([]OnboardedWorkspace, error) {
	ws, err := s.prov.ListProviderWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]OnboardedWorkspace, 0, len(ws))
	for _, w := range ws {
		out = append(out, OnboardedWorkspace{Name: w.Name, Cluster: w.Cluster, Phase: w.Phase})
	}
	return out, nil
}

// RootIdentity is one (group, resource) served by a first-party APIExport,
// together with the identityHash kcp minted for it. The admin copies the hash a
// provider needs (e.g. edges.kedge.faros.sh for kuery) into that provider's
// Helm values so its `init` can stamp it onto the APIExport's permissionClaim.
type RootIdentity struct {
	Group        string `json:"group"`
	Resource     string `json:"resource"`
	IdentityHash string `json:"identityHash"`
	Export       string `json:"export"`
	Path         string `json:"path"`
}

// ListRootIdentities returns the (group, resource → identityHash) tuples served
// by the APIExports in the providers parent workspace. An empty identityHash
// means kcp has not minted the export's identity yet.
func (s *Service) ListRootIdentities(ctx context.Context) ([]RootIdentity, error) {
	cfg := rest.CopyConfig(s.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, exportsWorkspace)
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client for %s: %w", exportsWorkspace, err)
	}
	list, err := cl.Resource(apiExportGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing APIExports in %s: %w", exportsWorkspace, err)
	}
	out := make([]RootIdentity, 0)
	for i := range list.Items {
		ex := &list.Items[i]
		hash, _, _ := unstructured.NestedString(ex.Object, "status", "identityHash")
		resources, _, _ := unstructured.NestedSlice(ex.Object, "spec", "resources")
		for _, r := range resources {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			group, _ := rm["group"].(string)
			resource, _ := rm["name"].(string)
			if group == "" {
				continue // built-in types need no identityHash
			}
			out = append(out, RootIdentity{
				Group:        group,
				Resource:     resource,
				IdentityHash: hash,
				Export:       ex.GetName(),
				Path:         exportsWorkspace,
			})
		}
	}
	return out, nil
}
