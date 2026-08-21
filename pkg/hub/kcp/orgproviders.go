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

// Org-owned ("bring your own") provider workspaces.
//
// An Org can register a provider it runs itself — typically inside its own
// Kubernetes cluster, reached over an edge — instead of consuming only the
// platform-global providers under root:faros:providers. The kcp layout mirrors
// the platform one exactly, one level down:
//
//	root:faros:providers:<name>                       platform provider
//	root:faros:tenants:<org>:providers:<name>          org-owned provider
//
// The well-known `providers` child of an Org workspace is a plain `universal`
// workspace, just like root:faros:providers. That is what lets each provider
// under it reuse the SAME restricted `provider` WorkspaceType
// (config/kcp/workspacetype-provider.yaml), whose limitAllowedParents requires
// a universal parent. The payoff is that the provider install path — the SA
// mint, provider-sdk/install's APIExport + endpoint slice, and CatalogEntry
// self-registration — runs completely unchanged against an org-owned provider.
//
// Everything here uses the hub's kcp-admin credentials. Per decision O-10 the
// Org workspace is hub-mediated only: tenants hold no credential that reaches
// it, so an Org admin cannot create any of this by hand. Registration goes
// through the REST surface in pkg/hub/restapi/org_providers.go, which is where
// the membership + role check lives.

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/kcppaths"
)

// EnsureOrgProvidersWorkspace materializes the well-known `providers` child of
// an Org workspace (root:faros:tenants:{orgUUID}:providers) as a `universal`
// workspace, and blocks until it is Ready. Idempotent.
//
// It is created lazily on the first org-provider registration rather than
// during org bootstrap: most Orgs never register one, and an empty universal
// workspace per Org would be pure overhead in the common case.
func (b *Bootstrapper) EnsureOrgProvidersWorkspace(ctx context.Context, orgUUID string) error {
	if orgUUID == "" {
		return fmt.Errorf("EnsureOrgProvidersWorkspace: orgUUID is required")
	}
	logger := klog.FromContext(ctx).WithValues("orgUUID", orgUUID)

	orgClient, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.OrgPath(orgUUID)))
	if err != nil {
		return fmt.Errorf("creating org workspace client: %w", err)
	}

	ws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": kcppaths.OrgProvidersWorkspaceName},
		"spec": map[string]any{
			// `universal`, not `workspace`: this is a container for provider
			// sub-workspaces, not a team workspace. The `provider` WorkspaceType
			// requires a universal parent, and `workspace` additionally forbids
			// any child but `edge`.
			"type": map[string]any{"name": "universal", "path": "root"},
		},
	}}

	if _, err := orgClient.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{}); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating org providers workspace in org %s: %w", orgUUID, err)
		}
		logger.V(4).Info("Org providers workspace already exists")
	} else {
		logger.Info("Created org providers workspace")
	}

	if err := waitForWorkspaceReady(ctx, orgClient, kcppaths.OrgProvidersWorkspaceName); err != nil {
		return fmt.Errorf("waiting for org providers workspace in org %s: %w", orgUUID, err)
	}
	return nil
}

// EnsureOrgProviderWorkspace materializes one org-owned provider workspace at
// root:faros:tenants:{orgUUID}:providers:{name}, creating the parent `providers`
// workspace first if needed. Blocks until Ready and returns the workspace's
// logical cluster ID (Workspace.spec.cluster) — the same value kcp embeds in
// the provider SA's token claims, which callers need to build qualified RBAC
// subjects. Idempotent.
func (b *Bootstrapper) EnsureOrgProviderWorkspace(ctx context.Context, orgUUID, name string) (string, error) {
	if orgUUID == "" || name == "" {
		return "", fmt.Errorf("EnsureOrgProviderWorkspace: orgUUID and name are required")
	}
	if err := b.EnsureOrgProvidersWorkspace(ctx, orgUUID); err != nil {
		return "", err
	}
	logger := klog.FromContext(ctx).WithValues("orgUUID", orgUUID, "provider", name)

	parent, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.OrgProvidersParent(orgUUID)))
	if err != nil {
		return "", fmt.Errorf("creating org providers workspace client: %w", err)
	}

	ws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			// The same restricted type platform providers use: no `universal`
			// extension (so the provider cannot spawn workspaces even though it
			// holds cluster-admin over its own), and a defaultAPIBinding to
			// providers.faros.sh so it can self-register its CatalogEntry.
			"type": map[string]any{"name": "provider", "path": kcppaths.Root},
		},
	}}

	// Re-registering right after a delete is the common case — the portal offers
	// both actions side by side — and kcp keeps the old Workspace around while
	// it terminates. Adopting that object looks like it works (Create returns
	// AlreadyExists, and the workspace still reports Ready) and then fails at
	// the first write with "workspace access not permitted", because the
	// bindings granting that access are already being collected. Wait it out and
	// make a genuinely new one instead.
	if existing, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{}); err == nil && existing.GetDeletionTimestamp() != nil {
		logger.Info("Waiting for the previous org provider workspace to finish deleting before recreating it")
		if werr := waitForWorkspaceGone(ctx, parent, name, 2*time.Minute); werr != nil {
			return "", fmt.Errorf("org provider workspace %s in org %s is still deleting; retry once it is gone: %w", name, orgUUID, werr)
		}
	}

	if _, err := parent.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{}); err != nil {
		if !errors.IsAlreadyExists(err) {
			return "", fmt.Errorf("creating org provider workspace %s in org %s: %w", name, orgUUID, err)
		}
		logger.V(4).Info("Org provider workspace already exists")
	} else {
		logger.Info("Created org provider workspace")
	}

	if err := waitForWorkspaceReady(ctx, parent, name); err != nil {
		return "", fmt.Errorf("waiting for org provider workspace %s in org %s: %w", name, orgUUID, err)
	}
	got, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting org provider workspace %s in org %s: %w", name, orgUUID, err)
	}
	cluster, _, _ := unstructured.NestedString(got.Object, "spec", "cluster")
	if cluster == "" {
		return "", fmt.Errorf("org provider workspace %s in org %s has empty spec.cluster after Ready", name, orgUUID)
	}
	return cluster, nil
}

// OrgProviderWorkspace is one org-owned provider sub-workspace as listed from
// kcp. It reports what exists in the workspace tree, independent of whether the
// provider's chart has been installed and has registered a CatalogEntry yet —
// that gap is exactly the "registered but not yet connected" state the org
// admin UI needs to show.
type OrgProviderWorkspace struct {
	Name    string
	Cluster string
	Phase   string
}

// ListOrgProviderWorkspaces returns the provider sub-workspaces under
// root:faros:tenants:{orgUUID}:providers. An Org that has never registered one
// has no `providers` workspace at all, which is not an error — it lists empty.
func (b *Bootstrapper) ListOrgProviderWorkspaces(ctx context.Context, orgUUID string) ([]OrgProviderWorkspace, error) {
	if orgUUID == "" {
		return nil, fmt.Errorf("ListOrgProviderWorkspaces: orgUUID is required")
	}
	parent, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.OrgProvidersParent(orgUUID)))
	if err != nil {
		return nil, fmt.Errorf("creating org providers workspace client: %w", err)
	}
	list, err := parent.Resource(workspaceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// No `providers` workspace yet — the Org has registered none.
			return nil, nil
		}
		return nil, fmt.Errorf("listing org provider workspaces in org %s: %w", orgUUID, err)
	}
	out := make([]OrgProviderWorkspace, 0, len(list.Items))
	for i := range list.Items {
		w := &list.Items[i]
		cluster, _, _ := unstructured.NestedString(w.Object, "spec", "cluster")
		phase, _, _ := unstructured.NestedString(w.Object, "status", "phase")
		out = append(out, OrgProviderWorkspace{Name: w.GetName(), Cluster: cluster, Phase: phase})
	}
	return out, nil
}

// GetOrgProviderWorkspace returns one org-owned provider workspace, or
// (nil, nil) when it does not exist.
func (b *Bootstrapper) GetOrgProviderWorkspace(ctx context.Context, orgUUID, name string) (*OrgProviderWorkspace, error) {
	if orgUUID == "" || name == "" {
		return nil, fmt.Errorf("GetOrgProviderWorkspace: orgUUID and name are required")
	}
	parent, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.OrgProvidersParent(orgUUID)))
	if err != nil {
		return nil, fmt.Errorf("creating org providers workspace client: %w", err)
	}
	got, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting org provider workspace %s in org %s: %w", name, orgUUID, err)
	}
	cluster, _, _ := unstructured.NestedString(got.Object, "spec", "cluster")
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	return &OrgProviderWorkspace{Name: got.GetName(), Cluster: cluster, Phase: phase}, nil
}

// DeleteOrgProviderWorkspace deletes one org-owned provider workspace. kcp
// cascades everything inside it: the ServiceAccount and its token, the
// APIExport, the APIResourceSchemas, and the CatalogEntry — which is what
// removes the provider from the catalog registry. Idempotent (NotFound is a
// no-op).
//
// APIBindings that tenant workspaces already hold against the deleted export
// are NOT removed here; per kcp's semantics they go NotReady. Callers that want
// a clean teardown should Disable the provider in each workspace first.
func (b *Bootstrapper) DeleteOrgProviderWorkspace(ctx context.Context, orgUUID, name string) error {
	if orgUUID == "" || name == "" {
		return fmt.Errorf("DeleteOrgProviderWorkspace: orgUUID and name are required")
	}
	parent, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.OrgProvidersParent(orgUUID)))
	if err != nil {
		return fmt.Errorf("creating org providers workspace client: %w", err)
	}
	if err := parent.Resource(workspaceGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("deleting org provider workspace %s in org %s: %w", name, orgUUID, err)
	}
	return nil
}

// OrgProviderConfig returns a rest.Config targeting one org-owned provider
// workspace. Used by the provisioner to mint that provider's ServiceAccount and
// kubeconfig without rebuilding path strings.
func (b *Bootstrapper) OrgProviderConfig(orgUUID, name string) *rest.Config {
	return configForPath(b.config, kcppaths.OrgProviderPath(orgUUID, name))
}
