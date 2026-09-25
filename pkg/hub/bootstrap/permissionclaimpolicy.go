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

package bootstrap

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	configkcp "github.com/railgrid/railgrid/config/kcp"
)

// PermissionClaimPolicyGroupVersion is the group/version the policy lives in.
// kcp-dev/kcp#4385 is unmerged, so today's kcp does not serve it and this hub
// carries no dependency on the type: the object is applied as unstructured
// YAML through the dynamic client, and discovery decides whether to apply it
// at all.
const PermissionClaimPolicyGroupVersion = "admin.kcp.io/v1alpha1"

// PermissionClaimPolicyResource is the resource name discovery is probed for.
const PermissionClaimPolicyResource = "permissionclaimpolicies"

// permissionClaimPolicyGVR is the coordinate the dynamic client applies on.
// The type is cluster-scoped (kubebuilder:resource:scope=Cluster).
var permissionClaimPolicyGVR = schema.GroupVersionResource{
	Group:    "admin.kcp.io",
	Version:  "v1alpha1",
	Resource: PermissionClaimPolicyResource,
}

// permissionClaimPolicyAbsentOnce keeps the "API not served, skipping" line to
// one info log per process. Every hub replica reaches this path on every
// startup retry, and on today's kcp the answer is always the same; repeating it
// would train operators to ignore it.
var permissionClaimPolicyAbsentOnce sync.Once

// AdminVirtualWorkspacePath is where kcp serves the admin virtual workspace,
// which is the only place a PermissionClaimPolicy can be written. The object is
// installation-wide and lives in the admin workspace's root cluster, so the
// path is fixed rather than derived from whatever workspace the hub is pointed
// at.
//
// Verified against kcp's custom-subresources build (kcp-dev/kcp#4388): applying
// this repository's generated policy to
// <base>/services/admin/clusters/root created it and it reads back unchanged.
// The hub's own kcp admin identity is accepted, because the admin workspace
// authorizer admits system:kcp:admin and system:masters, and
// permissionclaimpolicies is one of the resources that workspace owns and
// therefore accepts a create for.
const AdminVirtualWorkspacePath = "/services/admin/clusters/root"

// PermissionClaimPolicyTargetConfig returns the rest.Config the policy is
// applied through: the hub's kcp admin credentials against the admin virtual
// workspace, not the ordinary cluster path the rest of bootstrap uses.
//
// The base URL is rebuilt rather than appended to, because the hub's config may
// already carry a /clusters/<name> path and the admin workspace is not reached
// underneath one.
func PermissionClaimPolicyTargetConfig(kcpAdminConfig *rest.Config) *rest.Config {
	if kcpAdminConfig == nil {
		return nil
	}
	target := rest.CopyConfig(kcpAdminConfig)
	if base, err := url.Parse(target.Host); err == nil && base.Host != "" {
		base.Path = AdminVirtualWorkspacePath
		base.RawQuery = ""
		base.Fragment = ""
		target.Host = base.String()
	}
	target.APIPath = "/apis"
	return target
}

// PermissionClaimPolicyServed reports whether the cluster serves
// admin.kcp.io/v1alpha1 permissionclaimpolicies.
//
// A missing group/version is the expected answer on today's kcp and is not an
// error. Any other discovery failure is returned: "I could not tell" must not
// look like "it is not there", or a transient outage would silently skip the
// policy on a cluster that does serve it.
func PermissionClaimPolicyServed(discoveryClient discovery.DiscoveryInterface) (bool, error) {
	resources, err := discoveryClient.ServerResourcesForGroupVersion(PermissionClaimPolicyGroupVersion)
	if err != nil {
		if apierrors.IsNotFound(err) || discovery.IsGroupDiscoveryFailedError(err) {
			return false, nil
		}
		return false, fmt.Errorf("discovering %s: %w", PermissionClaimPolicyGroupVersion, err)
	}
	if resources == nil {
		return false, nil
	}
	for _, resource := range resources.APIResources {
		if resource.Name == PermissionClaimPolicyResource {
			return true, nil
		}
	}
	return false, nil
}

// InstallPermissionClaimPolicy applies the generated PermissionClaimPolicy when
// — and only when — the cluster serves the API.
//
// The policy is config/kcp/permissionclaimpolicy.yaml, generated from every
// provider manifest's spec.dependencies[].composes[] by
// hack/generate-permission-claim-policy.mjs. It whitelists, per claiming API
// group, the groups that claimer may claim without an identityHash, which is
// the kcp-side expression of the same relationships that already drive tenant
// consent at Enable and the hub-minted scoped identity.
//
// On a cluster without the API this logs once, at info, and returns nil. Today
// that is every cluster.
func InstallPermissionClaimPolicy(ctx context.Context, discoveryClient discovery.DiscoveryInterface, dynamicClient dynamic.Interface) error {
	logger := klog.FromContext(ctx)

	served, err := PermissionClaimPolicyServed(discoveryClient)
	if err != nil {
		return err
	}
	if !served {
		permissionClaimPolicyAbsentOnce.Do(func() {
			logger.Info("Skipping PermissionClaimPolicy: this kcp does not serve the API",
				"groupVersion", PermissionClaimPolicyGroupVersion,
				"resource", PermissionClaimPolicyResource,
				"file", configkcp.PermissionClaimPolicyFile)
		})
		return nil
	}

	desired, err := permissionClaimPolicyObject()
	if err != nil {
		return err
	}

	name := desired.GetName()
	client := dynamicClient.Resource(permissionClaimPolicyGVR)

	// Same shape as InstallCRDs: every replica applies the same idempotent
	// object at startup, so a lost create race is a conflict to retry on the
	// freshly observed resource version, not a reason to crash-loop.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		object := desired.DeepCopy()
		existing, err := client.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			logger.Info("Creating PermissionClaimPolicy", "name", name)
			_, err := client.Create(ctx, object, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(
					schema.GroupResource{Group: permissionClaimPolicyGVR.Group, Resource: permissionClaimPolicyGVR.Resource},
					name, err)
			}
			return err
		} else if err != nil {
			return err
		}
		logger.Info("Updating PermissionClaimPolicy", "name", name)
		object.SetResourceVersion(existing.GetResourceVersion())
		_, err = client.Update(ctx, object, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return fmt.Errorf("installing PermissionClaimPolicy %s: %w", name, err)
	}

	return nil
}

// permissionClaimPolicyObject decodes the embedded generated policy.
func permissionClaimPolicyObject() (*unstructured.Unstructured, error) {
	data, err := configkcp.PermissionClaimPolicyFS.ReadFile(configkcp.PermissionClaimPolicyFile)
	if err != nil {
		return nil, fmt.Errorf("reading embedded %s: %w", configkcp.PermissionClaimPolicyFile, err)
	}
	object := map[string]any{}
	if err := yaml.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("unmarshaling %s: %w", configkcp.PermissionClaimPolicyFile, err)
	}
	policy := &unstructured.Unstructured{Object: object}
	if policy.GetKind() != "PermissionClaimPolicy" || policy.GetAPIVersion() != PermissionClaimPolicyGroupVersion {
		return nil, fmt.Errorf("%s is %s %s, want %s PermissionClaimPolicy",
			configkcp.PermissionClaimPolicyFile, policy.GetAPIVersion(), policy.GetKind(), PermissionClaimPolicyGroupVersion)
	}
	if policy.GetName() == "" {
		return nil, fmt.Errorf("%s has no metadata.name", configkcp.PermissionClaimPolicyFile)
	}
	return policy, nil
}
