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

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/workspaceseeds"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// This file is the admin surface for platform services that are not
// providers: SystemWorkspace (the service's workspace under
// root:railgrid:system and its kubeconfig) and WorkspaceSeed (what the service
// puts into every tenant workspace). Like the provider endpoints, these only
// read and write the objects in root:railgrid:system:providers; the hub's
// reconcilers do the provisioning and the seeding.

var (
	systemWorkspaceGVR = schema.GroupVersionResource{
		Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "systemworkspaces",
	}
	workspaceSeedGVR = schema.GroupVersionResource{
		Group: "admin.railgrid.ai", Version: "v1alpha1", Resource: "workspaceseeds",
	}
)

// ErrInvalidInput marks a request the caller has to change: a bad name, a
// spec that does not parse, a seed the hub would refuse.
var ErrInvalidInput = errors.New("invalid input")

// ErrSystemWorkspaceInUse is returned when a SystemWorkspace cannot be deleted
// because WorkspaceSeeds still name exports in its workspace.
var ErrSystemWorkspaceInUse = errors.New("system workspace is referenced by workspace seeds")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

// systemProvidersClient returns a kcp-admin dynamic client for
// root:railgrid:system:providers, where SystemWorkspace and WorkspaceSeed
// objects live.
func (s *Service) systemProvidersClient() (dynamic.Interface, *rest.Config, error) {
	cfg := rest.CopyConfig(s.kcpConfig)
	cfg.Host = apiurl.KCPClusterURL(cfg.Host, kcppaths.SystemProviders)
	if s.systemProvidersDynamic != nil {
		return s.systemProvidersDynamic, cfg, nil
	}
	cl, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("dynamic client for %s: %w", kcppaths.SystemProviders, err)
	}
	return cl, cfg, nil
}

// SystemWorkspaceInfo is one SystemWorkspace as the admin area shows it.
type SystemWorkspaceInfo struct {
	Name             string `json:"name"`
	DisplayName      string `json:"displayName"`
	WorkspacePath    string `json:"workspacePath"`
	WorkspaceCluster string `json:"workspaceCluster"`
	Ready            bool   `json:"ready"`
	// Reason and Message are the Ready condition's, empty until the hub has
	// reconciled the object once.
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// Seeds names the WorkspaceSeeds that reference an export in this
	// workspace. While it is not empty the workspace cannot be deleted.
	Seeds []string `json:"seeds"`
}

// ListSystemWorkspaces returns every SystemWorkspace, sorted by name, with the
// seeds that reference each.
func (s *Service) ListSystemWorkspaces(ctx context.Context) ([]SystemWorkspaceInfo, error) {
	cl, _, err := s.systemProvidersClient()
	if err != nil {
		return nil, err
	}
	list, err := cl.Resource(systemWorkspaceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing SystemWorkspaces: %w", err)
	}
	seeds, err := s.listSeedObjects(ctx, cl)
	if err != nil {
		return nil, err
	}
	out := make([]SystemWorkspaceInfo, 0, len(list.Items))
	for i := range list.Items {
		var sw adminv1alpha1.SystemWorkspace
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &sw); err != nil {
			return nil, fmt.Errorf("decoding SystemWorkspace %q: %w", list.Items[i].GetName(), err)
		}
		info := SystemWorkspaceInfo{
			Name:             sw.Name,
			DisplayName:      sw.Spec.DisplayName,
			WorkspacePath:    kcppaths.SystemServicePath(sw.Name),
			WorkspaceCluster: sw.Status.WorkspaceCluster,
			Seeds:            seedsReferencing(seeds, kcppaths.SystemServicePath(sw.Name)),
		}
		for _, c := range sw.Status.Conditions {
			if c.Type == "Ready" {
				info.Ready = c.Status == metav1.ConditionTrue
				info.Reason, info.Message = c.Reason, c.Message
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CreateSystemWorkspace creates a SystemWorkspace object; the hub's reconciler
// then provisions root:railgrid:system:<name>. Idempotent. The name is checked
// here so the caller hears about a bad one at once rather than from a
// condition later.
func (s *Service) CreateSystemWorkspace(ctx context.Context, name, displayName string) error {
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return invalidf("name %q: %s", name, strings.Join(errs, "; "))
	}
	if kcppaths.IsReservedSystemName(name) {
		return invalidf("%q is one of the hub's own workspaces under %s", name, kcppaths.System)
	}
	cl, _, err := s.systemProvidersClient()
	if err != nil {
		return err
	}
	spec := map[string]any{}
	if displayName != "" {
		spec["displayName"] = displayName
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "admin.railgrid.ai/v1alpha1",
		"kind":       "SystemWorkspace",
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
	if _, err := cl.Resource(systemWorkspaceGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		if apierrors.IsInvalid(err) {
			return invalidf("%v", err)
		}
		return fmt.Errorf("creating SystemWorkspace %q: %w", name, err)
	}
	return nil
}

// DeleteSystemWorkspace removes a SystemWorkspace object; the reconciler's
// finalizer then deletes the workspace and everything in it. It refuses while
// a WorkspaceSeed names an export in that workspace: deleting the workspace
// would take the API, and its objects, out of every tenant workspace the seed
// bound it into. Idempotent.
func (s *Service) DeleteSystemWorkspace(ctx context.Context, name string) error {
	cl, _, err := s.systemProvidersClient()
	if err != nil {
		return err
	}
	seeds, err := s.listSeedObjects(ctx, cl)
	if err != nil {
		return err
	}
	if used := seedsReferencing(seeds, kcppaths.SystemServicePath(name)); len(used) > 0 {
		return fmt.Errorf("%w: %s", ErrSystemWorkspaceInUse, strings.Join(used, ", "))
	}
	if err := cl.Resource(systemWorkspaceGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting SystemWorkspace %q: %w", name, err)
	}
	return nil
}

// GetSystemWorkspaceKubeconfig returns the kubeconfig the hub minted for a
// SystemWorkspace, re-pointed for mode. Nil and no error mean it is not
// provisioned yet.
func (s *Service) GetSystemWorkspaceKubeconfig(ctx context.Context, name string, mode KubeconfigServerMode) ([]byte, error) {
	cl, cfg, err := s.systemProvidersClient()
	if err != nil {
		return nil, err
	}
	sw, err := cl.Resource(systemWorkspaceGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting SystemWorkspace %q: %w", name, err)
	}
	return s.kubeconfigFromSecret(ctx, cfg, sw, providers.SystemWorkspaceKubeconfigSecretName(name), mode)
}

// RotateSystemWorkspaceCredential issues a new credential for a
// SystemWorkspace's ServiceAccount, rewrites its kubeconfig Secret and returns
// the new kubeconfig, re-pointed for mode. The previous credential keeps
// working for the grace period.
func (s *Service) RotateSystemWorkspaceCredential(ctx context.Context, name string, mode KubeconfigServerMode) ([]byte, *providers.RotatedCredential, error) {
	cl, _, err := s.systemProvidersClient()
	if err != nil {
		return nil, nil, err
	}
	sw, err := cl.Resource(systemWorkspaceGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("getting SystemWorkspace %q: %w", name, err)
	}
	// Only a workspace the hub provisioned has the ServiceAccount to rotate.
	// Without this check a SystemWorkspace named after a workspace it does not
	// own would rotate, or fail confusingly, in somebody else's workspace.
	if path, _, _ := unstructured.NestedString(sw.Object, "status", "workspacePath"); path == "" {
		return nil, nil, apierrors.NewNotFound(systemWorkspaceGVR.GroupResource(), name+" (not provisioned yet)")
	}

	serverURL := s.hubInternalURL
	if serverURL == "" {
		serverURL = s.hubExternalURL
	}
	if override, _, _ := unstructured.NestedString(sw.Object, "spec", "serverURLOverride"); override != "" {
		serverURL = override
	}
	rotated, err := s.prov.RotateSystemWorkspaceCredential(ctx, name, serverURL)
	if err != nil {
		return nil, nil, err
	}
	secretName := providers.SystemWorkspaceKubeconfigSecretName(name)
	if err := s.prov.WriteSystemWorkspaceKubeconfigSecret(ctx, "default", secretName, providers.ProviderKubeconfigSecretKey, rotated.Kubeconfig, name); err != nil {
		return nil, nil, fmt.Errorf("writing rotated kubeconfig Secret %s: %w", secretName, err)
	}
	if mode == ServerModeAsMinted {
		return rotated.Kubeconfig, rotated, nil
	}
	base, err := s.serverBaseFor(mode)
	if err != nil {
		return nil, nil, err
	}
	out, err := rewriteKubeconfigServer(rotated.Kubeconfig, base)
	if err != nil {
		return nil, nil, err
	}
	return out, rotated, nil
}

// SeedBindingInfo summarises one binding of a seed.
type SeedBindingInfo struct {
	Name         string `json:"name"`
	ExportPath   string `json:"exportPath"`
	ExportName   string `json:"exportName"`
	TenantAccess string `json:"tenantAccess"`
	Claims       int    `json:"claims"`
}

// SeedObjectInfo summarises one object of a seed.
type SeedObjectInfo struct {
	Resource   string `json:"resource"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	// Gated is true when the object waits for other exports (whenBound).
	Gated bool `json:"gated"`
}

// WorkspaceSeedInfo is one WorkspaceSeed as the admin area shows and edits it.
type WorkspaceSeedInfo struct {
	Name string `json:"name"`
	// Spec is the seed's spec as YAML, the form it is edited in.
	Spec string `json:"spec"`
	// Accepted is "True", "False", or "" while the hub has not looked at this
	// generation of the seed yet.
	Accepted string            `json:"accepted"`
	Reason   string            `json:"reason"`
	Message  string            `json:"message"`
	Bindings []SeedBindingInfo `json:"bindings"`
	Objects  []SeedObjectInfo  `json:"objects"`
}

func (s *Service) listSeedObjects(ctx context.Context, cl dynamic.Interface) ([]adminv1alpha1.WorkspaceSeed, error) {
	list, err := cl.Resource(workspaceSeedGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing WorkspaceSeeds: %w", err)
	}
	out := make([]adminv1alpha1.WorkspaceSeed, 0, len(list.Items))
	for i := range list.Items {
		var seed adminv1alpha1.WorkspaceSeed
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &seed); err != nil {
			return nil, fmt.Errorf("decoding WorkspaceSeed %q: %w", list.Items[i].GetName(), err)
		}
		out = append(out, seed)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// seedsReferencing names the seeds that bind, or gate an object on, an export
// in the workspace at path.
func seedsReferencing(seeds []adminv1alpha1.WorkspaceSeed, path string) []string {
	out := []string{}
	for i := range seeds {
		if seedReferences(&seeds[i].Spec, path) {
			out = append(out, seeds[i].Name)
		}
	}
	return out
}

func seedReferences(spec *adminv1alpha1.WorkspaceSeedSpec, path string) bool {
	for _, b := range spec.Bindings {
		if b.Export.Path == path {
			return true
		}
	}
	for _, o := range spec.Objects {
		for _, ref := range o.WhenBound {
			if ref.Path == path {
				return true
			}
		}
	}
	return false
}

func seedInfo(seed *adminv1alpha1.WorkspaceSeed) (WorkspaceSeedInfo, error) {
	specYAML, err := yaml.Marshal(seed.Spec)
	if err != nil {
		return WorkspaceSeedInfo{}, fmt.Errorf("rendering spec of WorkspaceSeed %q: %w", seed.Name, err)
	}
	info := WorkspaceSeedInfo{
		Name:     seed.Name,
		Spec:     string(specYAML),
		Bindings: make([]SeedBindingInfo, 0, len(seed.Spec.Bindings)),
		Objects:  make([]SeedObjectInfo, 0, len(seed.Spec.Objects)),
	}
	for _, c := range seed.Status.Conditions {
		// A condition from an older generation says nothing about the spec
		// shown next to it.
		if c.Type == "Accepted" && c.ObservedGeneration == seed.Generation {
			info.Accepted, info.Reason, info.Message = string(c.Status), c.Reason, c.Message
		}
	}
	for _, b := range seed.Spec.Bindings {
		access := string(b.TenantAccess)
		if access == "" {
			access = string(adminv1alpha1.SeedTenantAccessReadWrite)
		}
		info.Bindings = append(info.Bindings, SeedBindingInfo{
			Name: b.Name, ExportPath: b.Export.Path, ExportName: b.Export.Name,
			TenantAccess: access, Claims: len(b.PermissionClaims),
		})
	}
	for _, o := range seed.Spec.Objects {
		obj := SeedObjectInfo{Resource: o.Resource, Gated: len(o.WhenBound) > 0}
		var u unstructured.Unstructured
		if len(o.Manifest.Raw) > 0 && u.UnmarshalJSON(o.Manifest.Raw) == nil {
			obj.APIVersion, obj.Kind, obj.Namespace, obj.Name = u.GetAPIVersion(), u.GetKind(), u.GetNamespace(), u.GetName()
		}
		info.Objects = append(info.Objects, obj)
	}
	return info, nil
}

// ListWorkspaceSeeds returns every WorkspaceSeed, sorted by name.
func (s *Service) ListWorkspaceSeeds(ctx context.Context) ([]WorkspaceSeedInfo, error) {
	cl, _, err := s.systemProvidersClient()
	if err != nil {
		return nil, err
	}
	seeds, err := s.listSeedObjects(ctx, cl)
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceSeedInfo, 0, len(seeds))
	for i := range seeds {
		info, err := seedInfo(&seeds[i])
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// ParseWorkspaceSeedSpec decodes a seed spec written as YAML (or JSON) and
// applies the hub's own validation, so what is saved is what the hub will
// accept. Unknown fields are an error: a misspelt key would otherwise be
// dropped silently and the seed would do less than its author wrote.
func ParseWorkspaceSeedSpec(specYAML string) (*adminv1alpha1.WorkspaceSeedSpec, error) {
	if strings.TrimSpace(specYAML) == "" {
		return nil, invalidf("spec is empty")
	}
	var spec adminv1alpha1.WorkspaceSeedSpec
	if err := yaml.UnmarshalStrict([]byte(specYAML), &spec); err != nil {
		return nil, invalidf("spec does not parse: %v", err)
	}
	if len(spec.Bindings) == 0 && len(spec.Objects) == 0 {
		return nil, invalidf("spec declares neither bindings nor objects")
	}
	if err := workspaceseeds.Validate(&spec); err != nil {
		return nil, invalidf("%v", err)
	}
	return &spec, nil
}

// SaveWorkspaceSeed creates the WorkspaceSeed called name, or replaces its
// spec, from specYAML. create=true refuses to overwrite an existing seed
// (AlreadyExists); create=false refuses to create one (NotFound). Saving
// reaches every tenant workspace: the hub applies an accepted seed to all of
// them.
func (s *Service) SaveWorkspaceSeed(ctx context.Context, name, specYAML string, create bool) (*WorkspaceSeedInfo, error) {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return nil, invalidf("name %q: %s", name, strings.Join(errs, "; "))
	}
	spec, err := ParseWorkspaceSeedSpec(specYAML)
	if err != nil {
		return nil, err
	}
	// Through JSON rather than the unstructured converter: the manifests are
	// RawExtensions, which only their own marshaller renders as objects.
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encoding spec: %w", err)
	}
	var specMap map[string]any
	if err := json.Unmarshal(specJSON, &specMap); err != nil {
		return nil, fmt.Errorf("encoding spec: %w", err)
	}
	cl, _, err := s.systemProvidersClient()
	if err != nil {
		return nil, err
	}
	res := cl.Resource(workspaceSeedGVR)

	var saved *unstructured.Unstructured
	if create {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "admin.railgrid.ai/v1alpha1",
			"kind":       "WorkspaceSeed",
			"metadata":   map[string]any{"name": name},
			"spec":       specMap,
		}}
		saved, err = res.Create(ctx, obj, metav1.CreateOptions{})
	} else {
		var existing *unstructured.Unstructured
		existing, err = res.Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			existing.Object["spec"] = specMap
			saved, err = res.Update(ctx, existing, metav1.UpdateOptions{})
		}
	}
	if err != nil {
		if apierrors.IsInvalid(err) {
			return nil, invalidf("%v", err)
		}
		return nil, err
	}
	var seed adminv1alpha1.WorkspaceSeed
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(saved.Object, &seed); err != nil {
		return nil, fmt.Errorf("decoding saved WorkspaceSeed %q: %w", name, err)
	}
	info, err := seedInfo(&seed)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// DeleteWorkspaceSeed removes a WorkspaceSeed. The hub stops applying it;
// nothing it created in tenant workspaces is removed. Idempotent.
func (s *Service) DeleteWorkspaceSeed(ctx context.Context, name string) error {
	cl, _, err := s.systemProvidersClient()
	if err != nil {
		return err
	}
	if err := cl.Resource(workspaceSeedGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting WorkspaceSeed %q: %w", name, err)
	}
	return nil
}
