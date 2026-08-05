// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package project reconciles Project CRs: for every provider-resource binding
// the spec declares, it ensures the referenced infrastructure instance exists
// (create-if-missing, delete-on-project-delete via finalizer) and mirrors the
// instance's observed state back into Project.status. The wizard writes spec
// once; this loop owns convergence — all lifecycle decisions are code, not
// model output.
//
// The binding contract is deliberately self-contained: the approve path
// resolves the Template's instanceCRD and records the full GVR + values on
// the binding, so this reconciler never reads Templates (they ride virtual
// storage with a separate identity — see docs/vibe-studio-design.md).
package project

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
)

const (
	// finalizer guards instance teardown on Project deletion.
	finalizer = "vibe.kedge.faros.sh/instances"
	// templateLabel matches the infrastructure provider's instance attribution
	// label so vibe-created instances show up in its listings.
	templateLabel = "kedge.faros.sh/template"
	// projectLabel attributes an instance back to its Project.
	projectLabel = "vibe.kedge.faros.sh/project"
	// requeueInterval polls instance status while not Ready. Instances are not
	// watched (their kinds are per-template and dynamic); polling keeps the
	// controller simple and deterministic.
	requeueInterval = 15 * time.Second
)

// Reconciler lifecycles infrastructure instances for Project bindings.
type Reconciler struct {
	Manager mcmanager.Manager
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("vibe-studio-project").
		For(&vibev1alpha1.Project{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("cluster %q: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var p vibev1alpha1.Project
	if err := c.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !p.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, c, &p)
	}

	refs := InstanceRefs(&p)
	if len(refs) == 0 {
		// Nothing to lifecycle (wizard hasn't bound a runtime yet).
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&p, finalizer) {
		controllerutil.AddFinalizer(&p, finalizer)
		if err := c.Update(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Mint the registry credential BEFORE creating instances. Production
	// pulls private images, and the infrastructure provider bridges the
	// credential into the runtime namespace only while reconciling the
	// instance — so a credential written afterwards would not be picked up
	// until something else touched the instance. Re-derived every pass, so a
	// rotated git token reaches production on its own.
	if err := r.ensureRegistryPullSecret(ctx, c, &p); err != nil {
		return ctrl.Result{}, fmt.Errorf("registry pull secret: %w", err)
	}

	// Converge: ensure each bound instance exists, then observe it.
	observed := make(map[string]*unstructured.Unstructured, len(refs))
	invalid := map[string]string{}
	for _, ref := range refs {
		inst, err := r.ensureInstance(ctx, c, &p, ref)
		if apierrors.IsInvalid(err) {
			// The spec is rejected by the API server: retrying cannot help,
			// only a spec change can. Record it where the user will see it
			// and stop hammering (this used to loop several times a second).
			invalid[ref.Key()] = err.Error()
			continue
		}
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("instance %s/%s: %w", ref.GVR.Resource, ref.Name, err)
		}
		observed[ref.Key()] = inst
	}

	// Converge the code Repository from the spec binding (autoInit creates
	// the repo on the git host; the session flow seeds it). Repositories are
	// deliberately NOT deleted on Project delete — they hold user code.
	repo, err := r.ensureRepository(ctx, c, &p)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("repository: %w", err)
	}

	next := MirrorStatus(&p, observed, metav1.Now())
	next.Repository = repositoryStatus(&p, repo)
	for i, env := range next.Environments {
		for j, b := range env.Bindings {
			if reason, bad := invalid[env.Name+"/"+b.Name]; bad {
				next.Environments[i].Bindings[j].Phase = "Invalid"
				next.Environments[i].Bindings[j].Outputs = map[string]string{"error": reason}
			}
		}
	}
	if !statusEqual(p.Status, next) {
		p.Status = next
		if err := c.Status().Update(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
	}
	if len(invalid) > 0 {
		// Nothing to converge until the spec changes; a spec change wakes us.
		return ctrl.Result{}, nil
	}
	if next.Phase != vibev1alpha1.ProjectPhaseReady {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	// Ready: keep a slow poll so drift (instance deleted out-of-band, status
	// regressions) is noticed without watching dynamic kinds.
	return ctrl.Result{RequeueAfter: 4 * requeueInterval}, nil
}

// ensureInstance gets or creates the bound instance CR.
func (r *Reconciler) ensureInstance(ctx context.Context, c client.Client, p *vibev1alpha1.Project, ref InstanceRef) (*unstructured.Unstructured, error) {
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(ref.GVK())
	err := c.Get(ctx, types.NamespacedName{Name: ref.Name}, got)
	if err == nil {
		return got, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	inst := DesiredInstance(p, ref)
	if err := c.Create(ctx, inst); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return inst, nil
}

// repositoryGVK is the code provider's Repository resource.
var repositoryGVK = schema.GroupVersionKind{Group: "code.kedge.faros.sh", Version: "v1alpha1", Kind: "Repository"}

// repositoryStatus mirrors the Repository CR into Project status. Pure.
func repositoryStatus(p *vibev1alpha1.Project, repo *unstructured.Unstructured) *vibev1alpha1.ProjectRepositoryStatus {
	if p.Spec.Repository == nil {
		return nil
	}
	st := &vibev1alpha1.ProjectRepositoryStatus{Phase: "Provisioning"}
	if repo == nil {
		return st
	}
	conds, _, _ := unstructured.NestedSlice(repo.Object, "status", "conditions")
	for _, cond := range conds {
		cm, ok := cond.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == "Ready" {
			if s, _ := cm["status"].(string); s == "True" {
				st.Phase = "Ready"
			}
		}
	}
	st.URL, _, _ = unstructured.NestedString(repo.Object, "status", "htmlURL")
	return st
}

// ensureRepository creates the Repository CR the spec binding names, if any,
// and returns the observed object (nil right after creation).
func (r *Reconciler) ensureRepository(ctx context.Context, c client.Client, p *vibev1alpha1.Project) (*unstructured.Unstructured, error) {
	b := p.Spec.Repository
	if b == nil || b.RepositoryRef == "" || b.ConnectionRef == "" {
		return nil, nil
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(repositoryGVK)
	err := c.Get(ctx, types.NamespacedName{Name: b.RepositoryRef}, got)
	if err == nil {
		return got, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	name := b.Name
	if name == "" {
		name = b.RepositoryRef
	}
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": repositoryGVK.GroupVersion().String(),
		"kind":       repositoryGVK.Kind,
		"metadata": map[string]any{
			"name":   b.RepositoryRef,
			"labels": map[string]any{projectLabel: p.Name},
		},
		"spec": map[string]any{
			"connectionRef": b.ConnectionRef,
			"name":          name,
			"visibility":    "private",
			"description":   "Generated by Vibe Studio for " + p.Spec.DisplayName,
			"autoInit":      true,
		},
	}}
	if err := c.Create(ctx, repo); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return nil, nil
}

// finalize deletes bound instances, then releases the finalizer.
func (r *Reconciler) finalize(ctx context.Context, c client.Client, p *vibev1alpha1.Project) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(p, finalizer) {
		return ctrl.Result{}, nil
	}
	for _, ref := range InstanceRefs(p) {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(ref.GVK())
		obj.SetName(ref.Name)
		if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("deleting instance %s/%s: %w", ref.GVR.Resource, ref.Name, err)
		}
	}
	controllerutil.RemoveFinalizer(p, finalizer)
	if err := c.Update(ctx, p); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// statusEqual compares the fields this controller owns, ignoring UpdatedAt so
// a no-op reconcile doesn't churn resourceVersion every pass.
func statusEqual(a, b vibev1alpha1.ProjectStatus) bool {
	if a.Phase != b.Phase || len(a.Environments) != len(b.Environments) {
		return false
	}
	if (a.Repository == nil) != (b.Repository == nil) {
		return false
	}
	if a.Repository != nil && *a.Repository != *b.Repository {
		return false
	}
	for i := range a.Environments {
		ae, be := a.Environments[i], b.Environments[i]
		if ae.Name != be.Name || ae.Mode != be.Mode || ae.Phase != be.Phase || ae.Revision != be.Revision || len(ae.Bindings) != len(be.Bindings) {
			return false
		}
		for j := range ae.Bindings {
			ab, bb := ae.Bindings[j], be.Bindings[j]
			if ab.Name != bb.Name || ab.Provider != bb.Provider || ab.Phase != bb.Phase || ab.URL != bb.URL {
				return false
			}
			if len(ab.Outputs) != len(bb.Outputs) {
				return false
			}
			for k, v := range ab.Outputs {
				if bb.Outputs[k] != v {
					return false
				}
			}
		}
	}
	return true
}

// GVK converts the ref's GVR + Kind into the GVK client objects need.
func (ref InstanceRef) GVK() schema.GroupVersionKind {
	return ref.GVR.GroupVersion().WithKind(ref.Kind)
}
