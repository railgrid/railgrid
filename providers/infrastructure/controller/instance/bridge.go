// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package instance

// Cross-cluster Secret bridging: the BYO OIDC client secret must land as a
// Secret beside the oauth2-proxy pod on the runtime cluster WITHOUT sitting in
// the instance values in clear text, and the per-instance registry pull Secret
// (minted by App Studio at promote) must reach the runtime namespace's default
// ServiceAccount so production pods can pull the private image.
//
// Which tenant Secret each one is comes from a TYPED reference on the
// Instance — spec.imagePullSecretRef and spec.oidcBridgeSecretRef — not from a
// name this controller derives. The derived names ("<instance>-registry", the
// well-known "cloud-credentials") coupled this provider to App Studio and to
// every BYO tenant by a string nobody validated, which is finding M8 of
// docs/provider-contract-review.md. A ref that names nothing is reported on
// the Instance; it is never resolved by guessing.

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	"github.com/railgrid/provider-infrastructure/kro"
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
	// oauth2-proxy reads via secretKeyRef. A BYO tenant puts their client
	// secret under this key in the Secret spec.oidcBridgeSecretRef names.
	oidcClientSecretKey = "oidc_client_secret"

	// dockerConfigKey is the key a kubernetes.io/dockerconfigjson Secret
	// carries, and the only key the pull-secret bridge copies.
	dockerConfigKey = ".dockerconfigjson"
)

// runtimePullSecretName is the name the bridged pull Secret takes in the
// RUNTIME namespace. That namespace belongs to this provider, so the name is
// this provider's to choose and is derived from the instance; only the
// TENANT-side name is a cross-provider fact, and that one comes from
// spec.imagePullSecretRef.
func runtimePullSecretName(instance string) string { return instance + "-registry" }

// secretRefName reads a typed Secret reference off the Instance. An absent
// ref, an absent name and a blank name are all "no reference".
func secretRefName(inst *unstructured.Unstructured, field string) string {
	name, _, _ := unstructured.NestedString(inst.Object, "spec", field, "name")
	return strings.TrimSpace(name)
}

// bridgeSecrets bridges the cross-cluster Secrets this instance REFERENCES:
// the registry pull Secret named by spec.imagePullSecretRef, and — when the
// exposure gate asked for a BYO gate — the client secret named by
// spec.oidcBridgeSecretRef.
//
// The returned condition is the Instance's SecretsBridged report. A reference
// that names a Secret which does not exist, or which lacks the key the
// reference is for, is a tenant-fixable configuration fault: it comes back as
// a False condition, not an error, so the instance still converges and says
// why rather than failing the whole reconcile in a retry loop.
func (c *Controller) bridgeSecrets(ctx context.Context, tenantClient client.Client, tenant string, inst *unstructured.Unstructured, bridgeOIDC bool) (*conditionSpec, error) {
	if pullRef := secretRefName(inst, "imagePullSecretRef"); pullRef != "" {
		cond, err := c.bridgeRegistryPullSecret(ctx, tenantClient, tenant, inst.GetNamespace(), inst.GetName(), pullRef)
		if err != nil || cond != nil {
			return cond, err
		}
	}
	if bridgeOIDC {
		oidcRef := secretRefName(inst, "oidcBridgeSecretRef")
		if oidcRef == "" {
			return secretsBridgedCondition(metav1.ConditionFalse, infrav1alpha1.ReasonBridgeSecretRefMissing,
				"spec.values.oidc.mode is \"byo\" but spec.oidcBridgeSecretRef names no Secret — set it to the Secret in this workspace that holds your client secret under the key "+oidcClientSecretKey), nil
		}
		cond, err := c.bridgeBYOSecret(ctx, tenantClient, tenant, inst.GetNamespace(), inst.GetName(), oidcRef)
		if err != nil || cond != nil {
			return cond, err
		}
	}
	return secretsBridgedCondition(metav1.ConditionTrue, infrav1alpha1.ReasonSecretsBridged, ""), nil
}

// secretsBridgedCondition builds the Instance's SecretsBridged report.
func secretsBridgedCondition(status metav1.ConditionStatus, reason, message string) *conditionSpec {
	return &conditionSpec{condType: infrav1alpha1.ConditionInstanceSecretsBridged, status: status, reason: reason, message: message}
}

// bridgeBYOSecret reads oidc_client_secret out of the Secret
// spec.oidcBridgeSecretRef names and writes it into the runtime per-tenant
// namespace as cloud-credentials-<name>, the name the RGD references. A nil
// condition means it was bridged.
func (c *Controller) bridgeBYOSecret(ctx context.Context, tenantClient client.Client, tenant, srcNamespace, name, ref string) (*conditionSpec, error) {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(secretGVK)
	key := types.NamespacedName{Namespace: c.cfg.CredentialsNamespace, Name: ref}
	if err := tenantClient.Get(ctx, key, src); err != nil {
		if apierrors.IsNotFound(err) {
			return secretsBridgedCondition(metav1.ConditionFalse, infrav1alpha1.ReasonSecretRefNotFound,
				fmt.Sprintf("spec.oidcBridgeSecretRef names Secret %s/%s, which does not exist", c.cfg.CredentialsNamespace, ref)), nil
		}
		return nil, fmt.Errorf("reading OIDC bridge secret %s/%s: %w", c.cfg.CredentialsNamespace, ref, err)
	}

	// Secret.data values are base64 strings over the wire; pass them through
	// verbatim into the bridged Secret's data so we never decode the secret
	// into memory as plaintext.
	data, _, _ := unstructured.NestedStringMap(src.Object, "data")
	encoded, ok := data[oidcClientSecretKey]
	if !ok || encoded == "" {
		return secretsBridgedCondition(metav1.ConditionFalse, infrav1alpha1.ReasonSecretRefInvalid,
			fmt.Sprintf("Secret %s/%s has no key %q", c.cfg.CredentialsNamespace, ref, oidcClientSecretKey)), nil
	}
	if err := c.writeBridgedSecret(ctx, tenant, srcNamespace, name, map[string]string{oidcClientSecretKey: encoded}); err != nil {
		return nil, err
	}
	return nil, nil
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

// bridgeRegistryPullSecret reads the Secret spec.imagePullSecretRef names,
// bridges it into the runtime per-tenant namespace and attaches it to that
// namespace's default ServiceAccount — so every pod there can pull the
// private image, across all components and templates. A nil condition means
// it was bridged.
//
// A ref that names nothing is reported rather than ignored: an instance that
// says it pulls from a private registry and silently does not is the failure
// that shows up as ImagePullBackOff minutes later, on the runtime cluster the
// tenant cannot see.
func (c *Controller) bridgeRegistryPullSecret(ctx context.Context, tenantClient client.Client, tenant, srcNamespace, name, ref string) (*conditionSpec, error) {
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(secretGVK)
	key := types.NamespacedName{Namespace: c.cfg.CredentialsNamespace, Name: ref}
	if err := tenantClient.Get(ctx, key, src); err != nil {
		if apierrors.IsNotFound(err) {
			return secretsBridgedCondition(metav1.ConditionFalse, infrav1alpha1.ReasonSecretRefNotFound,
				fmt.Sprintf("spec.imagePullSecretRef names Secret %s/%s, which does not exist", c.cfg.CredentialsNamespace, ref)), nil
		}
		return nil, fmt.Errorf("reading image pull secret %s/%s: %w", c.cfg.CredentialsNamespace, ref, err)
	}
	// Secret .data is base64 over the wire; pass it through verbatim so the
	// credential is never decoded into memory as plaintext.
	data, _, _ := unstructured.NestedStringMap(src.Object, "data")
	encoded, ok := data[dockerConfigKey]
	if !ok || encoded == "" {
		return secretsBridgedCondition(metav1.ConditionFalse, infrav1alpha1.ReasonSecretRefInvalid,
			fmt.Sprintf("Secret %s/%s has no %s", c.cfg.CredentialsNamespace, ref, dockerConfigKey)), nil
	}

	ns := kro.RuntimeNamespace(tenant, srcNamespace)
	secretName := runtimePullSecretName(name)
	if err := c.writeRuntimePullSecret(ctx, ns, tenant, secretName, encoded); err != nil {
		return nil, err
	}
	if err := c.ensureDefaultSAImagePullSecret(ctx, ns, secretName); err != nil {
		return nil, err
	}
	return nil, nil
}

// cleanupRegistryPullSecret removes the bridged pull Secret and detaches it from
// the default ServiceAccount when the instance is deleted. NotFound is success
// (the namespace may already be gone).
func (c *Controller) cleanupRegistryPullSecret(ctx context.Context, tenant, srcNamespace, name string) error {
	ns := kro.RuntimeNamespace(tenant, srcNamespace)
	secretName := runtimePullSecretName(name)
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
