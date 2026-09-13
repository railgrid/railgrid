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

package instance

// Tenant runtime-namespace ingress isolation: the per-namespace NetworkPolicy
// that stops one workspace's pods from reaching another workspace's pods on
// the shared runtime cluster. Policy shape: networkpolicy.Build.

import (
	"context"
	"fmt"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/faroshq/provider-infrastructure/networkpolicy"
)

var networkPolicyGVR = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}

// networkPolicyResync bounds how long a converged namespace is trusted before
// the policy is re-read: long enough that the warm reconcile path (every
// instance requeues every 10-60s) makes no extra API call, short enough that a
// hand-edited or deleted policy is restored promptly.
const networkPolicyResync = 10 * time.Minute

// networkPolicySync is one networkPolicySynced entry: the namespace instance
// the policy was converged in, and when.
type networkPolicySync struct {
	namespaceUID types.UID
	at           time.Time
}

// ensureTenantNetworkPolicy converges the isolation policy of one runtime
// namespace on the configuration: create-or-update it when enabled, remove
// the provider-owned copy when disabled. It runs from ensureNamespace, i.e.
// before the runtime CR (and therefore any workload) is written.
//
// The skip cache is keyed by the namespace's UID as well as its name, so a
// namespace deleted and recreated under the same name gets its policy before
// its first workload rather than after the resync window.
//
// Enabled, an error fails the reconcile: the operator asked for isolation, so
// workloads must not be materialized without it. Disabled, removal is
// best-effort and never fails a reconcile, so a runtime credential without
// networkpolicies permissions keeps working with the default configuration.
func (c *Controller) ensureTenantNetworkPolicy(ctx context.Context, ns, tenant string, namespaceUID types.UID) error {
	if v, ok := c.networkPolicySynced.Load(ns); ok {
		if s := v.(networkPolicySync); s.namespaceUID == namespaceUID && time.Since(s.at) < networkPolicyResync {
			return nil
		}
	}
	if c.cfg.NetworkPolicy.Enabled {
		if err := c.applyTenantNetworkPolicy(ctx, ns, tenant); err != nil {
			return err
		}
	} else {
		c.removeTenantNetworkPolicy(ctx, ns)
	}
	c.networkPolicySynced.Store(ns, networkPolicySync{namespaceUID: namespaceUID, at: time.Now()})
	return nil
}

// applyTenantNetworkPolicy creates the policy, or updates an existing object
// of the same name whose spec or ownership labels drifted. Idempotent: an
// already-converged policy is left untouched.
func (c *Controller) applyTenantNetworkPolicy(ctx context.Context, ns, tenant string) error {
	desired := networkpolicy.Build(ns, tenant, c.cfg.NetworkPolicy)
	client := c.cfg.Runtime.Resource(networkPolicyGVR).Namespace(ns)

	existing, err := client.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		obj, cerr := toUnstructuredNetworkPolicy(desired)
		if cerr != nil {
			return cerr
		}
		_, cerr = client.Create(ctx, obj, metav1.CreateOptions{})
		if cerr == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(cerr) {
			return fmt.Errorf("create networkpolicy %s/%s: %w", ns, desired.Name, cerr)
		}
		// A concurrent writer created it first: converge what is there.
		existing, err = client.Get(ctx, desired.Name, metav1.GetOptions{})
	}
	if err != nil {
		return fmt.Errorf("get networkpolicy %s/%s: %w", ns, desired.Name, err)
	}

	current := &networkingv1.NetworkPolicy{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(existing.Object, current); err != nil {
		return fmt.Errorf("decode networkpolicy %s/%s: %w", ns, desired.Name, err)
	}
	labelsCurrent := true
	for k, v := range desired.Labels {
		if current.Labels[k] != v {
			labelsCurrent = false
			break
		}
	}
	if labelsCurrent && equality.Semantic.DeepEqual(current.Spec, desired.Spec) {
		return nil
	}

	// Keep foreign labels/annotations (operators may add their own), own the
	// spec and our labels.
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	for k, v := range desired.Labels {
		current.Labels[k] = v
	}
	current.Spec = desired.Spec
	current.TypeMeta = desired.TypeMeta
	obj, err := toUnstructuredNetworkPolicy(current)
	if err != nil {
		return err
	}
	if _, err := client.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update networkpolicy %s/%s: %w", ns, desired.Name, err)
	}
	return nil
}

// removeTenantNetworkPolicy deletes the provider-owned policy if one exists.
// Objects without the ownership labels are left alone, and every failure is
// logged, not returned (see ensureTenantNetworkPolicy).
func (c *Controller) removeTenantNetworkPolicy(ctx context.Context, ns string) {
	log := klog.FromContext(ctx).WithValues("namespace", ns, "networkPolicy", networkpolicy.Name)
	client := c.cfg.Runtime.Resource(networkPolicyGVR).Namespace(ns)
	existing, err := client.Get(ctx, networkpolicy.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		log.V(2).Info("tenant network policy disabled; could not check for a stale policy", "err", err)
		return
	}
	if !networkpolicy.IsProviderOwned(existing.GetLabels()) {
		return
	}
	uid := existing.GetUID()
	err = client.Delete(ctx, networkpolicy.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil && !apierrors.IsNotFound(err) {
		log.Info("tenant network policy disabled; failed to remove the provider-owned policy", "err", err)
		return
	}
	log.Info("tenant network policy disabled; removed the provider-owned policy")
}

func toUnstructuredNetworkPolicy(np *networkingv1.NetworkPolicy) (*unstructured.Unstructured, error) {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(np)
	if err != nil {
		return nil, fmt.Errorf("encode networkpolicy %s/%s: %w", np.Namespace, np.Name, err)
	}
	return &unstructured.Unstructured{Object: obj}, nil
}
