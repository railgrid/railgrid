// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package instance

// Cross-cluster Secret bridging, carried over from the retired application
// controller: the BYO OIDC client secret must land as a Secret beside the
// oauth2-proxy pod on the runtime cluster WITHOUT sitting in the instance
// values in clear text, and the per-instance registry pull Secret (minted
// by App Studio at promote) must reach the runtime namespace's default
// ServiceAccount so production pods can pull the private image.

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/faroshq/provider-infrastructure/kro"
)

// secretGVK is used to Get Secrets via the controller-runtime client
// (tenant side) and shape the bridged Secret (runtime side).
var secretGVK = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}

var (
	secretGVR         = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	namespaceGVR      = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	serviceAccountGVR = schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
)

const (
	// oidcClientSecretKey is the key the bridged Secret carries and the RGD's
	// oauth2-proxy reads via secretKeyRef. BYO tenants put their client secret
	// under this same key in their cloud-credentials Secret.
	oidcClientSecretKey = "oidc_client_secret"

	// cloudCredentialsSecret is the well-known Secret a tenant maintains in
	// their workspace; BYO mode reads oidcClientSecretKey out of it.
	cloudCredentialsSecret = "cloud-credentials"
)

// registryPullSecretName is the per-instance image-pull Secret: minted by App
// Studio in the tenant workspace as "<instance>-registry" (dockerconfigjson)
// and bridged under the same name into the runtime namespace.
func registryPullSecretName(instance string) string { return instance + "-registry" }

// bridgeSecrets bridges whatever cross-cluster Secrets this instance needs:
// the registry pull Secret whenever the tenant minted one, and the BYO OIDC
// client secret when the exposure gate asked for it.
func (c *Controller) bridgeSecrets(ctx context.Context, tenantClient client.Client, tenant string, inst *unstructured.Unstructured, bridgeOIDC bool) error {
	hasPull, err := c.tenantHasPullSecret(ctx, tenantClient, inst.GetName())
	if err != nil {
		return fmt.Errorf("checking registry pull secret: %w", err)
	}
	if hasPull {
		if err := c.bridgeRegistryPullSecret(ctx, tenantClient, tenant, inst.GetNamespace(), inst.GetName()); err != nil {
			return fmt.Errorf("bridging registry pull secret: %w", err)
		}
	}
	if bridgeOIDC {
		if err := c.bridgeBYOSecret(ctx, tenantClient, tenant, inst.GetNamespace(), inst.GetName()); err != nil {
			return fmt.Errorf("bridging BYO OIDC client secret: %w", err)
		}
	}
	return nil
}

// bridgeBYOSecret reads oidc_client_secret out of the tenant's
// cloud-credentials Secret and writes it into the runtime per-tenant
// namespace as cloud-credentials-<name>, the name the RGD references.
func (c *Controller) bridgeBYOSecret(ctx context.Context, tenantClient client.Client, tenant, srcNamespace, name string) error {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(secretGVK)
	key := types.NamespacedName{Namespace: c.cfg.CredentialsNamespace, Name: cloudCredentialsSecret}
	if err := tenantClient.Get(ctx, key, src); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("tenant Secret %s/%s not found — create it with key %q before provisioning a BYO application",
				c.cfg.CredentialsNamespace, cloudCredentialsSecret, oidcClientSecretKey)
		}
		return fmt.Errorf("reading tenant cloud-credentials: %w", err)
	}

	// Secret.data values are base64 strings over the wire; pass them through
	// verbatim into the bridged Secret's data so we never decode the secret
	// into memory as plaintext.
	data, _, _ := unstructured.NestedStringMap(src.Object, "data")
	encoded, ok := data[oidcClientSecretKey]
	if !ok || encoded == "" {
		return fmt.Errorf("tenant Secret %s/%s has no key %q", c.cfg.CredentialsNamespace, cloudCredentialsSecret, oidcClientSecretKey)
	}
	return c.writeBridgedSecret(ctx, tenant, srcNamespace, name, map[string]string{oidcClientSecretKey: encoded})
}

// writeBridgedSecret upserts the per-instance Secret in the runtime per-tenant
// namespace. data values are base64-encoded strings (Secret .data wire form).
// Ensures the namespace exists first (the runtime sync also creates it, but
// the Secret may race ahead of the first sync).
func (c *Controller) writeBridgedSecret(ctx context.Context, tenant, srcNamespace, name string, data map[string]string) error {
	ns := kro.RuntimeNamespace(tenant, srcNamespace)
	if err := c.ensureNamespace(ctx, ns, tenant); err != nil {
		return err
	}

	secretName := kro.CredentialsSecretName(name)
	dataAny := make(map[string]any, len(data))
	for k, v := range data {
		dataAny[k] = v
	}
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      secretName,
			"namespace": ns,
			"labels": map[string]any{
				kro.LabelTenant:    kro.LabelTenantValue(tenant),
				kro.LabelManagedBy: kro.ManagedByValue,
			},
		},
		"type": "Opaque",
		"data": dataAny,
	}}

	existing, err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create bridged secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get bridged secret: %w", err)
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	if _, err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update bridged secret: %w", err)
	}
	return nil
}

// deleteBridgedSecret removes the per-instance bridged Secret from the runtime
// per-tenant namespace. NotFound is success.
func (c *Controller) deleteBridgedSecret(ctx context.Context, tenant, srcNamespace, name string) error {
	ns := kro.RuntimeNamespace(tenant, srcNamespace)
	err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).
		Delete(ctx, kro.CredentialsSecretName(name), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// bridgeRegistryPullSecret reads the tenant's per-instance registry Secret
// and, when present, bridges it into the runtime per-tenant namespace and
// attaches it to that namespace's default ServiceAccount — so every pod
// there can pull the private image, across all components and templates.
func (c *Controller) bridgeRegistryPullSecret(ctx context.Context, tenantClient client.Client, tenant, srcNamespace, name string) error {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(secretGVK)
	key := types.NamespacedName{Namespace: c.cfg.CredentialsNamespace, Name: registryPullSecretName(name)}
	if err := tenantClient.Get(ctx, key, src); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading tenant registry secret: %w", err)
	}
	// Secret .data is base64 over the wire; pass it through verbatim so the
	// credential is never decoded into memory as plaintext.
	data, _, _ := unstructured.NestedStringMap(src.Object, "data")
	encoded, ok := data[".dockerconfigjson"]
	if !ok || encoded == "" {
		return fmt.Errorf("tenant Secret %s/%s has no .dockerconfigjson", c.cfg.CredentialsNamespace, registryPullSecretName(name))
	}

	ns := kro.RuntimeNamespace(tenant, srcNamespace)
	secretName := registryPullSecretName(name)
	if err := c.writeRuntimePullSecret(ctx, ns, tenant, secretName, encoded); err != nil {
		return err
	}
	return c.ensureDefaultSAImagePullSecret(ctx, ns, secretName)
}

// tenantHasPullSecret reports whether the tenant minted an "<instance>-registry"
// Secret (i.e. the instance was promoted with a private image).
func (c *Controller) tenantHasPullSecret(ctx context.Context, tenantClient client.Client, name string) (bool, error) {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(secretGVK)
	err := tenantClient.Get(ctx, types.NamespacedName{Namespace: c.cfg.CredentialsNamespace, Name: registryPullSecretName(name)}, src)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// cleanupRegistryPullSecret removes the bridged pull Secret and detaches it from
// the default ServiceAccount when the instance is deleted. NotFound is success
// (the namespace may already be gone).
func (c *Controller) cleanupRegistryPullSecret(ctx context.Context, tenant, srcNamespace, name string) error {
	ns := kro.RuntimeNamespace(tenant, srcNamespace)
	secretName := registryPullSecretName(name)
	if err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Delete(ctx, secretName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete runtime pull secret: %w", err)
	}
	return c.detachDefaultSAImagePullSecret(ctx, ns, secretName)
}

// detachDefaultSAImagePullSecret removes secretName from the default SA's
// imagePullSecrets (idempotent). A missing namespace/SA is success.
func (c *Controller) detachDefaultSAImagePullSecret(ctx context.Context, ns, secretName string) error {
	sa, err := c.cfg.Runtime.Resource(serviceAccountGVR).Namespace(ns).Get(ctx, "default", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get default serviceaccount in %s: %w", ns, err)
	}
	pullSecrets, _, _ := unstructured.NestedSlice(sa.Object, "imagePullSecrets")
	kept := make([]any, 0, len(pullSecrets))
	changed := false
	for _, ps := range pullSecrets {
		if m, ok := ps.(map[string]any); ok && m["name"] == secretName {
			changed = true
			continue
		}
		kept = append(kept, ps)
	}
	if !changed {
		return nil
	}
	if len(kept) == 0 {
		unstructured.RemoveNestedField(sa.Object, "imagePullSecrets")
	} else if err := unstructured.SetNestedSlice(sa.Object, kept, "imagePullSecrets"); err != nil {
		return err
	}
	if _, err := c.cfg.Runtime.Resource(serviceAccountGVR).Namespace(ns).Update(ctx, sa, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("detach imagePullSecret from default serviceaccount in %s: %w", ns, err)
	}
	return nil
}

// writeRuntimePullSecret upserts the dockerconfigjson pull Secret in the runtime
// per-tenant namespace.
func (c *Controller) writeRuntimePullSecret(ctx context.Context, ns, tenant, secretName, dockerconfigjson string) error {
	if err := c.ensureNamespace(ctx, ns, tenant); err != nil {
		return err
	}
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      secretName,
			"namespace": ns,
			"labels":    map[string]any{kro.LabelManagedBy: kro.ManagedByValue},
		},
		"type": "kubernetes.io/dockerconfigjson",
		"data": map[string]any{".dockerconfigjson": dockerconfigjson},
	}}
	existing, err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create runtime pull secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get runtime pull secret: %w", err)
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	if _, err := c.cfg.Runtime.Resource(secretGVR).Namespace(ns).Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update runtime pull secret: %w", err)
	}
	return nil
}

// ensureDefaultSAImagePullSecret appends secretName to the namespace's default
// ServiceAccount imagePullSecrets (idempotent), so kubelet applies it to every
// pod in the namespace without per-workload wiring.
func (c *Controller) ensureDefaultSAImagePullSecret(ctx context.Context, ns, secretName string) error {
	sa, err := c.cfg.Runtime.Resource(serviceAccountGVR).Namespace(ns).Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		// The default SA is created by the control plane shortly after the
		// namespace; a NotFound here just re-queues.
		return fmt.Errorf("get default serviceaccount in %s: %w", ns, err)
	}
	pullSecrets, _, _ := unstructured.NestedSlice(sa.Object, "imagePullSecrets")
	for _, ps := range pullSecrets {
		if m, ok := ps.(map[string]any); ok && m["name"] == secretName {
			return nil
		}
	}
	pullSecrets = append(pullSecrets, map[string]any{"name": secretName})
	if err := unstructured.SetNestedSlice(sa.Object, pullSecrets, "imagePullSecrets"); err != nil {
		return err
	}
	if _, err := c.cfg.Runtime.Resource(serviceAccountGVR).Namespace(ns).Update(ctx, sa, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("attach imagePullSecret to default serviceaccount in %s: %w", ns, err)
	}
	return nil
}
