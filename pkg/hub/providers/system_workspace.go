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

package providers

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"

	adminv1alpha1 "github.com/railgrid/railgrid/apis/admin/v1alpha1"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// ErrSystemWorkspaceNotOwned is returned when root:railgrid:system:<name>
// exists and was not created for the SystemWorkspace of that name: one of the
// hub's own workspaces, one an installer made by hand, or one of another type.
// The hub neither provisions into such a workspace nor deletes it.
var ErrSystemWorkspaceNotOwned = errors.New("workspace exists and was not created for this SystemWorkspace")

// ErrSystemWorkspaceNameReserved is returned for a name the hub keeps for its
// own children of root:railgrid:system.
var ErrSystemWorkspaceNameReserved = errors.New("name is reserved for the hub's own system workspaces")

// SystemWorkspaceKubeconfigSecretName is the Secret in
// root:railgrid:system:providers that holds a SystemWorkspace's kubeconfig. It
// differs from a Provider's "<name>-kubeconfig" so a provider and a system
// service of one name do not write the same Secret.
func SystemWorkspaceKubeconfigSecretName(name string) string {
	return name + "-system-kubeconfig"
}

// EnsureSystemWorkspace creates root:railgrid:system:<name> if it does not
// exist and waits for it to be Ready, returning its logical cluster ID.
// Idempotent. It refuses reserved names and a workspace it does not own.
func (p *Provisioner) EnsureSystemWorkspace(ctx context.Context, name string) (string, error) {
	parent, err := p.clientFor(kcppaths.System)
	if err != nil {
		return "", err
	}
	return ensureSystemWorkspace(ctx, parent, name, 500*time.Millisecond, 60*time.Second)
}

func ensureSystemWorkspace(ctx context.Context, parent dynamic.Interface, name string, interval, timeout time.Duration) (string, error) {
	if kcppaths.IsReservedSystemName(name) {
		return "", fmt.Errorf("%w: %q", ErrSystemWorkspaceNameReserved, name)
	}
	ws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata": map[string]any{
			"name":        name,
			"annotations": map[string]any{adminv1alpha1.SystemWorkspaceAnnotation: name},
		},
		"spec": map[string]any{
			"type": map[string]any{"name": adminv1alpha1.SystemWorkspaceTypeName, "path": kcppaths.Root},
		},
	}}
	_, err := parent.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating workspace %s: %w", kcppaths.SystemServicePath(name), err)
	}

	var cluster string
	var notOwned error
	if err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		got, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if !ownedSystemWorkspace(got, name) {
			notOwned = fmt.Errorf("%w: %s", ErrSystemWorkspaceNotOwned, kcppaths.SystemServicePath(name))
			return false, notOwned
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		cluster, _, _ = unstructured.NestedString(got.Object, "spec", "cluster")
		return phase == "Ready", nil
	}); err != nil {
		if notOwned != nil {
			return "", notOwned
		}
		return "", fmt.Errorf("waiting for workspace %s: %w", kcppaths.SystemServicePath(name), err)
	}
	return cluster, nil
}

// ownedSystemWorkspace reports whether ws was created for the SystemWorkspace
// called name: it carries the annotation and is of the system-service type.
func ownedSystemWorkspace(ws *unstructured.Unstructured, name string) bool {
	if ws.GetAnnotations()[adminv1alpha1.SystemWorkspaceAnnotation] != name {
		return false
	}
	typeName, _, _ := unstructured.NestedString(ws.Object, "spec", "type", "name")
	return typeName == adminv1alpha1.SystemWorkspaceTypeName
}

// EnsureSystemWorkspaceSA creates the "provider" ServiceAccount in
// root:railgrid:system:<name> and binds it to cluster-admin there, whatever
// --provider-workspace-cluster-admin says: the narrower provider role is cut
// for what a provider's `init` does and has no rule for the APIs a system
// service defines and writes in its own workspace. Idempotent.
func (p *Provisioner) EnsureSystemWorkspaceSA(ctx context.Context, name string) error {
	cl, err := p.clientFor(kcppaths.SystemServicePath(name))
	if err != nil {
		return err
	}
	// The system-service WorkspaceType does not extend universal, so nothing
	// created the `default` namespace.
	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": ProviderSANamespace},
	}}
	if _, err := cl.Resource(namespaceGVR).Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensuring namespace %s in system workspace: %w", ProviderSANamespace, err)
	}
	sa := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ServiceAccount",
		"metadata":   map[string]any{"name": ProviderSAName, "namespace": ProviderSANamespace},
	}}
	if _, err := cl.Resource(serviceAccountGVR).Namespace(ProviderSANamespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ServiceAccount %s/%s: %w", ProviderSANamespace, ProviderSAName, err)
	}
	return ensureProviderRoleBindingTo(ctx, cl, "cluster-admin")
}

// MintSystemWorkspaceKubeconfig returns the kubeconfig for the ServiceAccount
// in root:railgrid:system:<name>, minting its token on first use.
func (p *Provisioner) MintSystemWorkspaceKubeconfig(ctx context.Context, name, serverURL string) ([]byte, error) {
	return p.MintProviderKubeconfigAtPath(ctx, kcppaths.SystemServicePath(name), serverURL)
}

// RotateSystemWorkspaceCredential issues a second token for the ServiceAccount
// in root:railgrid:system:<name> and puts the previous one on the same
// deletion clock a provider's rotation uses.
func (p *Provisioner) RotateSystemWorkspaceCredential(ctx context.Context, name, serverURL string) (*RotatedCredential, error) {
	// No provider name: there is no CatalogEntry to record the rotation on.
	return p.RotateProviderCredentialAtPath(ctx, kcppaths.SystemServicePath(name), "", serverURL)
}

// WriteSystemWorkspaceKubeconfigSecret create-or-updates the kubeconfig Secret
// of a SystemWorkspace in root:railgrid:system:providers.
func (p *Provisioner) WriteSystemWorkspaceKubeconfigSecret(ctx context.Context, namespace, name, key string, kc []byte, systemWorkspace string) error {
	return p.writeKubeconfigSecret(ctx, namespace, name, key, kc, map[string]string{
		"admin.railgrid.ai/system-workspace": systemWorkspace,
		"admin.railgrid.ai/managed-by":       "system-workspace-controller",
	})
}

// DeleteSystemWorkspace deletes root:railgrid:system:<name> when it was
// created for the SystemWorkspace of that name. A missing workspace, a
// reserved name and a workspace it does not own are all left as they are and
// report no error: there is nothing of this SystemWorkspace's to remove.
func (p *Provisioner) DeleteSystemWorkspace(ctx context.Context, name string) error {
	parent, err := p.clientFor(kcppaths.System)
	if err != nil {
		return err
	}
	return deleteSystemWorkspace(ctx, parent, name)
}

func deleteSystemWorkspace(ctx context.Context, parent dynamic.Interface, name string) error {
	if kcppaths.IsReservedSystemName(name) {
		return nil
	}
	got, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting workspace %s: %w", kcppaths.SystemServicePath(name), err)
	}
	if !ownedSystemWorkspace(got, name) {
		return nil
	}
	// The UID precondition keeps a delete from landing on a workspace of the
	// same name that replaced the one just inspected.
	uid := got.GetUID()
	err = parent.Resource(workspaceGVR).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting workspace %s: %w", kcppaths.SystemServicePath(name), err)
	}
	return nil
}
