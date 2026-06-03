// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kro

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// ErrInstanceNotFound is returned by Get/Delete when no instance with
// that name exists in the tenant's central-kro namespace, OR when the
// instance exists but belongs to a different tenant (we collapse the
// two to avoid leaking instance existence across tenants).
var ErrInstanceNotFound = errors.New("instance not found")

// secretGVR is the GVR for Secrets in the central kro cluster (NOT in
// kcp). The provider creates a `cloud-credentials-<instance>` Secret
// there from credentials it read out of the tenant workspace.
var secretGVR = namespaceGVR // same group "" — placeholder; see below

func init() {
	// Override to the secrets GVR. Done in init() rather than as a
	// const because namespaceGVR is also a var, and Go forbids
	// referring to other vars during package init at the top level.
	secretGVR.Resource = "secrets"
}

// CreateInstance writes the kro RGD instance CR into the per-tenant
// namespace and bridges cloud credentials into a sidecar Secret. The
// sequence is:
//
//	1. Marshal spec.<fields> from in.Values into the unstructured
//	   instance shaped by in.Template.InstanceGVR.
//	2. Create the instance CR (Create, not Apply — collisions become
//	   409 to the caller, signalling "pick a different name").
//	3. Create the cloud-credentials-<name> Secret in the same
//	   namespace, populated from in.Credentials. The RGD template
//	   references it by name.
//	4. Patch the Secret's ownerReferences to the just-created
//	   instance UID so Kubernetes GC handles cleanup on delete.
//
// On failure between (2) and (4) we leave an orphan Secret; the
// startup-time sweeper in sweeper.go reclaims them after 10 minutes.
func (c *realClient) CreateInstance(ctx context.Context, in CreateInstanceParams) (*Instance, error) {
	ns, err := c.EnsureTenantNamespace(ctx, in.TenantPath)
	if err != nil {
		return nil, fmt.Errorf("ensure tenant namespace: %w", err)
	}

	cr := buildInstanceUnstructured(in, ns)
	created, err := c.dyn.Resource(in.Template.InstanceGVR).Namespace(ns).
		Create(ctx, cr, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	// Bridge credentials → Secret. Skipped when the caller passed
	// none; some templates self-supply credentials via inputs and
	// won't need the bridged Secret at all.
	if len(in.Credentials) > 0 {
		secretName := credentialsSecretName(in.InstanceName)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: ns,
				Labels: map[string]string{
					// Hash the workspace path — raw form contains
					// `:` which K8s rejects in label values.
					LabelTenant:    tenantHash(in.TenantPath),
					LabelTemplate:  in.Template.Name,
					LabelManagedBy: ManagedByValue,
				},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion:         in.Template.InstanceGVR.GroupVersion().String(),
					Kind:               in.Template.InstanceKind,
					Name:               in.InstanceName,
					UID:                created.GetUID(),
					BlockOwnerDeletion: boolPtr(true),
				}},
			},
			Type: corev1.SecretTypeOpaque,
			Data: in.Credentials,
		}
		uSecret, mErr := toUnstructured(secret)
		if mErr != nil {
			return nil, fmt.Errorf("marshal credentials secret: %w", mErr)
		}
		// Pre-encode Data values to base64 — the dynamic client
		// passes the map through json.Marshal which serializes
		// []byte as base64 automatically, but the unstructured
		// "data" map holds any-typed values; we need to ensure each
		// is a base64-encoded string to match the on-wire shape
		// the apiserver expects.
		if data, ok := uSecret.Object["data"].(map[string]any); ok {
			for k, v := range data {
				if b, ok := v.([]byte); ok {
					data[k] = base64.StdEncoding.EncodeToString(b)
				}
			}
		}
		if _, err := c.dyn.Resource(secretGVR).Namespace(ns).
			Create(ctx, uSecret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create credentials secret: %w", err)
		}
	}

	return unstructuredToInstance(created, in.Template.Name), nil
}

func (c *realClient) GetInstance(ctx context.Context, tenantPath, name string) (*Instance, error) {
	ns := tenantNamespaceName(tenantPath)
	// We don't know the template (and thus the GVR) from the name
	// alone — instances live under their template's RGD-defined CRD.
	// Approach: list all GVRs we have templates for, search each
	// namespace for the name. For phase 3 this is acceptable (single
	// digit templates), but a future revision should index instance
	// → GVR by labels at create time and persist the mapping.
	templates, err := c.ListTemplates(ctx, TemplateFilter{})
	if err != nil {
		return nil, fmt.Errorf("list templates for resolve: %w", err)
	}
	for _, t := range templates {
		obj, err := c.dyn.Resource(t.InstanceGVR).Namespace(ns).
			Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if obj.GetLabels()[LabelTenant] != tenantHash(tenantPath) {
			// Cross-tenant access attempt — collapse to NotFound.
			return nil, ErrInstanceNotFound
		}
		return unstructuredToInstance(obj, t.Name), nil
	}
	return nil, ErrInstanceNotFound
}

func (c *realClient) ListInstances(ctx context.Context, tenantPath string) ([]Instance, error) {
	ns := tenantNamespaceName(tenantPath)
	templates, err := c.ListTemplates(ctx, TemplateFilter{})
	if err != nil {
		return nil, fmt.Errorf("list templates for instance list: %w", err)
	}
	var out []Instance
	selector := LabelTenant + "=" + tenantHash(tenantPath)
	for _, t := range templates {
		list, err := c.dyn.Resource(t.InstanceGVR).Namespace(ns).
			List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			// One bad GVR shouldn't fail the whole list — kro may be
			// mid-rollout and the CRD not registered yet. Skip and
			// continue.
			continue
		}
		for i := range list.Items {
			out = append(out, *unstructuredToInstance(&list.Items[i], t.Name))
		}
	}
	return out, nil
}

func (c *realClient) DeleteInstance(ctx context.Context, tenantPath, name string) error {
	// Use the same template-scan strategy as GetInstance to locate
	// the GVR. Once located, a single Delete suffices — the credential
	// Secret is GC'd by its OwnerReference.
	ns := tenantNamespaceName(tenantPath)
	templates, err := c.ListTemplates(ctx, TemplateFilter{})
	if err != nil {
		return fmt.Errorf("list templates for delete: %w", err)
	}
	for _, t := range templates {
		obj, err := c.dyn.Resource(t.InstanceGVR).Namespace(ns).
			Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if obj.GetLabels()[LabelTenant] != tenantHash(tenantPath) {
			return ErrInstanceNotFound
		}
		return c.dyn.Resource(t.InstanceGVR).Namespace(ns).
			Delete(ctx, name, metav1.DeleteOptions{})
	}
	return ErrInstanceNotFound
}

// buildInstanceUnstructured shapes the CR the apiserver will accept.
// spec is populated from in.Values verbatim — validation happens at
// the handler edge (against in.Template.InputsSchema) so by the time
// we get here the payload is known good.
func buildInstanceUnstructured(in CreateInstanceParams, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": in.Template.InstanceGVR.GroupVersion().String(),
			"kind":       in.Template.InstanceKind,
			"metadata": map[string]any{
				"name":      in.InstanceName,
				"namespace": namespace,
				"labels": map[string]any{
					// Hash because the raw path contains `:`.
					LabelTenant:   tenantHash(in.TenantPath),
					LabelUser:     in.User,
					LabelTemplate: in.Template.Name,
				},
			},
			"spec": in.Values,
		},
	}
}

// unstructuredToInstance projects a kro instance CR into the portal
// Instance shape. status.conditions and status.children are populated
// from kro's well-known status fields; missing fields produce an empty
// slice rather than nil so the JSON response always carries the key.
func unstructuredToInstance(obj *unstructured.Unstructured, templateName string) *Instance {
	inst := &Instance{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Template:  templateName,
		Phase:     "Pending",
		CreatedAt: obj.GetCreationTimestamp().Time,
		Conditions: []InstanceCondition{},
		Children:   []InstanceChild{},
	}
	if spec, _, _ := unstructured.NestedMap(obj.Object, "spec"); spec != nil {
		inst.Values = spec
	}
	if phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase"); phase != "" {
		inst.Phase = phase
	}
	if msg, _, _ := unstructured.NestedString(obj.Object, "status", "message"); msg != "" {
		inst.Message = msg
	}
	if conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); conds != nil {
		for _, c := range conds {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			cond := InstanceCondition{
				Type:    stringFrom(cm, "type"),
				Status:  stringFrom(cm, "status"),
				Reason:  stringFrom(cm, "reason"),
				Message: stringFrom(cm, "message"),
			}
			if ts := stringFrom(cm, "lastTransitionTime"); ts != "" {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					cond.Time = t
				}
			}
			inst.Conditions = append(inst.Conditions, cond)
		}
	}
	// kro doesn't write a top-level status.phase — it relies on the
	// Ready condition. Derive a useful phase string from that so the
	// portal's list page shows the real state instead of perpetual
	// "Pending". status.phase wins if kro ever starts emitting it.
	if _, hasPhase, _ := unstructured.NestedString(obj.Object, "status", "phase"); !hasPhase {
		inst.Phase = derivePhaseFromConditions(inst.Conditions)
	}
	if kids, _, _ := unstructured.NestedSlice(obj.Object, "status", "children"); kids != nil {
		for _, k := range kids {
			km, ok := k.(map[string]any)
			if !ok {
				continue
			}
			inst.Children = append(inst.Children, InstanceChild{
				APIVersion: stringFrom(km, "apiVersion"),
				Kind:       stringFrom(km, "kind"),
				Name:       stringFrom(km, "name"),
				Namespace:  stringFrom(km, "namespace"),
				Phase:      stringFrom(km, "phase"),
			})
		}
	}
	return inst
}

func stringFrom(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// derivePhaseFromConditions maps the standard kro condition set
// (InstanceManaged, GraphResolved, ResourcesReady, Ready) into a
// single status word the UI's StatusBadge can render. Rules:
//
//	Ready=True                         → "Ready"
//	Ready=False with reason            → "Failed" (UI badge: fail)
//	any condition False / Unknown      → "Pending" (still settling)
//	no Ready condition yet             → "Pending" (kro hasn't observed it)
//
// Order matters: Ready=True short-circuits because kro flips Ready
// after the dependency chain (InstanceManaged → GraphResolved →
// ResourcesReady → Ready) all turn green.
func derivePhaseFromConditions(conds []InstanceCondition) string {
	var readyCond *InstanceCondition
	for i := range conds {
		if conds[i].Type == "Ready" {
			readyCond = &conds[i]
			break
		}
	}
	if readyCond == nil {
		return "Pending"
	}
	switch readyCond.Status {
	case "True":
		return "Ready"
	case "False":
		// kro writes a useful reason on the Ready=False condition.
		// Surface it as the phase so the list page conveys "failed,
		// see details" at a glance.
		if readyCond.Reason != "" {
			return "Failed"
		}
		return "Failed"
	default:
		return "Pending"
	}
}

func credentialsSecretName(instanceName string) string {
	return "cloud-credentials-" + instanceName
}

func boolPtr(b bool) *bool { return &b }

// Compile-time check that realClient and stubClient implement Client.
// Without these the missing-method errors are buried in unrelated
// callsites; here the diagnostic points at the right file.
var (
	_ Client = (*realClient)(nil)
	_ Client = (*stubClient)(nil)
)

// _ unused but keeps types import alive when no method uses it
// elsewhere.
var _ = types.NamespacedName{}
