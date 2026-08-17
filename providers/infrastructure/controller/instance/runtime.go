// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package instance

// Runtime-cluster half of the reconcile: the per-template kro CR the
// Instance materializes into, the namespace it lives in, the status mirror
// back onto the Instance, and finalization of all of it.

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/kro"
)

// runtimeRefKey is the status field recording where the runtime CR was
// written, so finalization still works when the Template is retired while
// instances exist.
const runtimeRefKey = "runtimeRef"

// runtimeGVRFor is the runtime-cluster resource a Template's instances
// materialize as.
func runtimeGVRFor(tmpl *infrav1alpha1.Template) schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    tmpl.Spec.InstanceCRD.Group,
		Version:  tmpl.Spec.InstanceCRD.Version,
		Resource: tmpl.Spec.InstanceCRD.Resource,
	}
}

// syncRuntime ensures the tenant's runtime namespace and converges the
// per-template kro CR on the stamped values. Returns the live runtime
// object (its status feeds the mirror).
func (c *Controller) syncRuntime(ctx context.Context, tenant string, tmpl *infrav1alpha1.Template, inst *unstructured.Unstructured, values map[string]any) (*unstructured.Unstructured, error) {
	ns := kro.RuntimeNamespace(tenant, inst.GetNamespace())
	if err := c.ensureNamespace(ctx, ns, tenant); err != nil {
		return nil, err
	}

	gvr := runtimeGVRFor(tmpl)
	if values == nil {
		values = map[string]any{}
	}
	labels := map[string]any{
		kro.LabelTemplate:  tmpl.Name,
		kro.LabelTenant:    kro.LabelTenantValue(tenant),
		kro.LabelManagedBy: kro.ManagedByValue,
	}

	existing, err := c.cfg.Runtime.Resource(gvr).Namespace(ns).Get(ctx, inst.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		desired := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": gvr.GroupVersion().String(),
			"kind":       tmpl.Spec.InstanceCRD.Kind,
			"metadata": map[string]any{
				"name":      inst.GetName(),
				"namespace": ns,
				"labels":    labels,
			},
			"spec": runtime.DeepCopyJSON(values),
		}}
		created, cerr := c.cfg.Runtime.Resource(gvr).Namespace(ns).Create(ctx, desired, metav1.CreateOptions{})
		if cerr != nil {
			if apierrors.IsAlreadyExists(cerr) {
				return c.cfg.Runtime.Resource(gvr).Namespace(ns).Get(ctx, inst.GetName(), metav1.GetOptions{})
			}
			return nil, fmt.Errorf("create runtime instance: %w", cerr)
		}
		return created, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get runtime instance: %w", err)
	}

	// Converge spec + labels. The runtime apiserver applies the RGD schema's
	// defaults on write, so compare the desired values against the stored
	// spec field-by-field: a stored spec that only ADDS defaulted fields is
	// current. (Writing our sparse values over it would churn defaults every
	// pass; instead only fields we set are compared and written.)
	curSpec, _, _ := unstructured.NestedMap(existing.Object, "spec")
	curLabels := existing.GetLabels()
	labelsCurrent := curLabels[kro.LabelTemplate] == tmpl.Name &&
		curLabels[kro.LabelTenant] == kro.LabelTenantValue(tenant) &&
		curLabels[kro.LabelManagedBy] == kro.ManagedByValue
	if specSubset(values, curSpec) && labelsCurrent {
		return existing, nil
	}

	merged := runtime.DeepCopyJSON(curSpec)
	overlayValues(merged, values)
	if err := unstructured.SetNestedMap(existing.Object, merged, "spec"); err != nil {
		return nil, fmt.Errorf("set runtime spec: %w", err)
	}
	newLabels := map[string]string{}
	for k, v := range curLabels {
		newLabels[k] = v
	}
	newLabels[kro.LabelTemplate] = tmpl.Name
	newLabels[kro.LabelTenant] = kro.LabelTenantValue(tenant)
	newLabels[kro.LabelManagedBy] = kro.ManagedByValue
	existing.SetLabels(newLabels)

	updated, err := c.cfg.Runtime.Resource(gvr).Namespace(ns).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update runtime instance: %w", err)
	}
	return updated, nil
}

// specSubset reports whether every field in want is present with an equal
// value in got (recursing into maps). got may carry extra fields — the
// runtime CRD's schema defaults — without breaking currency.
func specSubset(want, got map[string]any) bool {
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			return false
		}
		if wm, wok := wv.(map[string]any); wok {
			gm, gok := gv.(map[string]any)
			if !gok || !specSubset(wm, gm) {
				return false
			}
			continue
		}
		if !equality.Semantic.DeepEqual(wv, gv) {
			return false
		}
	}
	return true
}

// overlayValues writes want's fields over dst (recursing into maps), leaving
// dst's extra fields (runtime defaults) alone.
func overlayValues(dst, want map[string]any) {
	for k, wv := range want {
		if wm, wok := wv.(map[string]any); wok {
			if dm, dok := dst[k].(map[string]any); dok {
				overlayValues(dm, wm)
				continue
			}
		}
		dst[k] = runtime.DeepCopyJSON(map[string]any{"v": wv})["v"]
	}
}

// ensureNamespace creates the runtime per-tenant namespace if absent.
func (c *Controller) ensureNamespace(ctx context.Context, ns, tenant string) error {
	_, err := c.cfg.Runtime.Resource(namespaceGVR).Get(ctx, ns, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get namespace %s: %w", ns, err)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name": ns,
			"labels": map[string]any{
				kro.LabelManagedBy: kro.ManagedByValue,
				kro.LabelTenant:    kro.LabelTenantValue(tenant),
			},
		},
	}}
	if _, err := c.cfg.Runtime.Resource(namespaceGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", ns, err)
	}
	return nil
}

// mirrorStatus composes the Instance's status from the runtime CR's status
// (backend truth) plus the provider-owned conditions, and writes it when it
// changed. When runtimeObj is nil (validation failure, template missing) the
// previously mirrored fields are preserved so a running instance's status
// isn't wiped by a bad spec edit. Returns whether the instance is Ready.
func (c *Controller) mirrorStatus(ctx context.Context, tenantClient client.Client, inst *unstructured.Unstructured, tmpl *infrav1alpha1.Template, runtimeObj *unstructured.Unstructured, valid, oidc *conditionSpec) (bool, error) {
	prevStatus, _, _ := unstructured.NestedMap(inst.Object, "status")

	var next map[string]any
	if runtimeObj != nil {
		if rs, found, _ := unstructured.NestedMap(runtimeObj.Object, "status"); found {
			next = runtime.DeepCopyJSON(rs)
		} else {
			next = map[string]any{}
		}
	} else if prevStatus != nil {
		next = runtime.DeepCopyJSON(prevStatus)
	} else {
		next = map[string]any{}
	}

	conds, _ := next["conditions"].([]any)
	prevConds, _ := prevStatus["conditions"].([]any)
	conds = upsertCondition(conds, prevConds, valid, inst.GetGeneration())
	if oidc != nil {
		conds = upsertCondition(conds, prevConds, oidc, inst.GetGeneration())
	}
	next["conditions"] = conds

	// Phase: backend-projected value wins; otherwise derive from conditions,
	// and a failed validation is always Failed.
	phase, _ := next["phase"].(string)
	if valid != nil && valid.status == metav1.ConditionFalse {
		phase = "Failed"
		next["message"] = valid.message
	} else if phase == "" {
		phase = derivePhase(conds)
	}
	next["phase"] = phase

	next["observedGeneration"] = inst.GetGeneration()
	if tmpl != nil {
		next["template"] = tmpl.Name
		next["templateVersion"] = tmpl.Spec.Version
		if runtimeObj != nil {
			next[runtimeRefKey] = map[string]any{
				"apiVersion": runtimeGVRFor(tmpl).GroupVersion().String(),
				"kind":       tmpl.Spec.InstanceCRD.Kind,
				"resource":   tmpl.Spec.InstanceCRD.Resource,
				"namespace":  runtimeObj.GetNamespace(),
				"name":       runtimeObj.GetName(),
			}
		}
	}
	if prev, ok := prevStatus[runtimeRefKey]; ok && next[runtimeRefKey] == nil {
		next[runtimeRefKey] = prev
	}

	ready := conditionTrue(conds, "Ready")

	if equality.Semantic.DeepEqual(prevStatus, next) {
		return ready, nil
	}
	if err := unstructured.SetNestedMap(inst.Object, next, "status"); err != nil {
		return ready, fmt.Errorf("set status: %w", err)
	}
	if err := tenantClient.Status().Update(ctx, inst); err != nil {
		return ready, fmt.Errorf("update status: %w", err)
	}
	return ready, nil
}

// upsertCondition replaces the entry of cond's type. To avoid churning
// lastTransitionTime on every pass, an unchanged condition keeps the
// previous entry's timestamp.
func upsertCondition(conds, prevConds []any, cond *conditionSpec, generation int64) []any {
	if cond == nil {
		return conds
	}
	transition := metav1.Now().UTC().Format(time.RFC3339)
	for _, raw := range prevConds {
		if m, ok := raw.(map[string]any); ok && m["type"] == cond.condType {
			if m["status"] == string(cond.status) && m["reason"] == cond.reason && m["message"] == cond.message {
				if t, ok := m["lastTransitionTime"].(string); ok && t != "" {
					transition = t
				}
			}
		}
	}
	next := make([]any, 0, len(conds)+1)
	for _, raw := range conds {
		if m, ok := raw.(map[string]any); ok && m["type"] == cond.condType {
			continue
		}
		next = append(next, raw)
	}
	return append(next, map[string]any{
		"type":               cond.condType,
		"status":             string(cond.status),
		"reason":             cond.reason,
		"message":            cond.message,
		"lastTransitionTime": transition,
		"observedGeneration": generation,
	})
}

// conditionTrue reports whether the named condition is present with
// status True.
func conditionTrue(conds []any, condType string) bool {
	for _, raw := range conds {
		if m, ok := raw.(map[string]any); ok && m["type"] == condType {
			return m["status"] == "True"
		}
	}
	return false
}

// derivePhase summarizes conditions into one word, mirroring the platform
// convention (Ready=True → Ready, Ready=False → Failed, else Pending).
func derivePhase(conds []any) string {
	for _, raw := range conds {
		m, ok := raw.(map[string]any)
		if !ok || m["type"] != "Ready" {
			continue
		}
		switch m["status"] {
		case "True":
			return "Ready"
		case "False":
			return "Failed"
		}
	}
	return "Pending"
}

// finalize cleans up the cross-cluster state an Instance owns — the runtime
// kro CR (waiting for kro to tear its children down), the bridged OIDC
// Secret, and the registry pull Secret — then drops the finalizer.
func (c *Controller) finalize(ctx context.Context, tenantClient client.Client, tenant string, inst *unstructured.Unstructured) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(inst, finalizer) {
		return ctrl.Result{}, nil
	}

	gvr, ns, name, found := c.runtimeTarget(ctx, tenant, inst)
	if found {
		err := c.cfg.Runtime.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete runtime instance: %w", err)
		}
		// kro finalizes the runtime CR after tearing down its children; hold
		// the Instance until it is actually gone so "deleted" means deleted.
		if _, err := c.cfg.Runtime.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("check runtime instance gone: %w", err)
		}
	}

	if err := c.deleteBridgedSecret(ctx, tenant, inst.GetNamespace(), inst.GetName()); err != nil {
		return ctrl.Result{}, fmt.Errorf("cleanup bridged secret: %w", err)
	}
	if err := c.cleanupRegistryPullSecret(ctx, tenant, inst.GetNamespace(), inst.GetName()); err != nil {
		return ctrl.Result{}, fmt.Errorf("cleanup registry pull secret: %w", err)
	}

	controllerutil.RemoveFinalizer(inst, finalizer)
	if err := tenantClient.Update(ctx, inst); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// runtimeTarget resolves where the Instance's runtime CR lives: preferably
// from status.runtimeRef (recorded at sync time, survives Template
// retirement), else derived from the still-existing Template. found=false
// means there is nothing addressable to delete — either the instance never
// synced, or both the ref and the Template are gone.
func (c *Controller) runtimeTarget(ctx context.Context, tenant string, inst *unstructured.Unstructured) (schema.GroupVersionResource, string, string, bool) {
	if ref, found, _ := unstructured.NestedMap(inst.Object, "status", runtimeRefKey); found {
		apiVersion, _ := ref["apiVersion"].(string)
		resource, _ := ref["resource"].(string)
		ns, _ := ref["namespace"].(string)
		name, _ := ref["name"].(string)
		if gv, err := schema.ParseGroupVersion(apiVersion); err == nil && resource != "" && ns != "" && name != "" {
			return gv.WithResource(resource), ns, name, true
		}
	}
	templateName, _, _ := unstructured.NestedString(inst.Object, "spec", "template")
	tmpl, _, err := c.resolveTemplate(ctx, templateName)
	if err != nil || tmpl == nil {
		return schema.GroupVersionResource{}, "", "", false
	}
	return runtimeGVRFor(tmpl), kro.RuntimeNamespace(tenant, inst.GetNamespace()), inst.GetName(), true
}
