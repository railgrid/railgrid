/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"log"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/bindings"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/tenant"
)

// Session CRs and the Studio singleton — the API-side halves of the
// control-plane projections the reconcilers converge (controller/session,
// controller/studio). Both writes are best-effort as the provider: a workspace
// without them loses `kubectl` visibility or web search, not the ability to
// build.

var (
	sessionResource = tenant.Resource{GVR: aiv1alpha1.SchemeGroupVersion.WithResource("sessions"), Kind: "Session", Plural: "Sessions"}
	studioResource  = tenant.Resource{GVR: aiv1alpha1.SchemeGroupVersion.WithResource("studios"), Kind: "Studio", Plural: "Studios"}
)

// ensureSessionCR projects a freshly-created thread into a Session CR. The
// Session reconciler mirrors status and purges the store on CR delete; the
// ownerReference makes Project deletion take the conversations with it.
func (s *Server) ensureSessionCR(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project, threadID, actorID string) {
	if c == nil || p == nil {
		return
	}
	sess := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Session",
		"metadata": map[string]any{
			"name": threadID,
			"annotations": map[string]any{
				bindings.OrgUUIDAnnotation:       id.orgUUID,
				bindings.WorkspaceUUIDAnnotation: id.workspaceUUID,
				"ai.railgrid.ai/project-uid":     string(p.UID),
			},
		},
		"spec": map[string]any{
			"projectRef": p.Name,
			"threadID":   threadID,
			"actorID":    actorID,
		},
	}}
	if owner := bindings.OwnerRef(p); owner != nil {
		sess.SetOwnerReferences([]metav1.OwnerReference{*owner})
	}
	s.noteWorkspaceCluster(id)
	if _, err := c.Resource(sessionResource, "").Create(ctx, sess, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		log.Printf("session CR for thread %s: %v", threadID, err)
	}
}

// deleteSessionCR removes the thread's projection after an interactive
// deletion. Best-effort: the Session reconciler also deletes projections
// whose store row is gone.
func (s *Server) deleteSessionCR(ctx context.Context, c *asclient.Client, threadID string) {
	if c == nil {
		return
	}
	if err := c.Resource(sessionResource, "").Delete(ctx, threadID, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		log.Printf("deleting session CR for thread %s: %v", threadID, err)
	}
}

// The Studio is written when a workspace first creates a project, so the
// reconciler has the search backend warm before the assistant needs it.
// Once per process per workspace — but only once every shared service has
// its instance reference resolved. A fresh workspace races its infrastructure
// APIBinding: the first project can be created before the searxng/browser
// Templates are visible, and a Studio written then carries no resourceRef for
// the service. The reconciler parks such a service in Pending ("waiting for
// the … template to be resolved") and never re-resolves it, so the API keeps
// retrying on later project creations until every reference is in place. A
// Studio someone has since edited is otherwise left exactly as it is.
var studioEnsured sync.Map // clusterID → struct{}

// ensureStudio writes the workspace's Studio if it is missing, and fills in any
// shared-service reference it is still missing.
func (s *Server) ensureStudio(ctx context.Context, c *asclient.Client, id identity) {
	if c == nil || id.clusterID == "" {
		return
	}
	if _, done := studioEnsured.Load(id.clusterID); done {
		return
	}
	if existing, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{}); err == nil {
		// A Studio created before a service existed (e.g. browser, added after
		// search), or while its Template was not yet resolvable, is missing
		// that service's reference. Retrofit it so existing workspaces gain
		// the backend without recreating the Studio.
		if s.retrofitStudioServices(ctx, c, existing) {
			studioEnsured.Store(id.clusterID, struct{}{})
		}
		return
	} else if !apierrors.IsNotFound(err) {
		// The APIBinding may not have caught up with the schemas yet.
		return
	}

	st := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Studio",
		"metadata":   map[string]any{"name": aiv1alpha1.StudioName},
		"spec": map[string]any{
			"search":  map[string]any{"size": "small"},
			"browser": map[string]any{"size": "small"},
		},
	}}
	resolved := true
	for _, svc := range studioSharedServices {
		ref := svc.resourceRef(s, ctx, c)
		if ref == nil {
			resolved = false
			continue
		}
		setStudioResourceRef(st, svc.name, ref)
	}
	if _, err := c.Resource(studioResource, "").Create(ctx, st, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		log.Printf("creating the studio for workspace %s: %v", id.clusterID, err)
		return
	}
	if resolved {
		studioEnsured.Store(id.clusterID, struct{}{})
	} else {
		log.Printf("studio for workspace %s created with an unresolved shared service; it is retrofitted on the next project creation", id.clusterID)
	}
	log.Printf("studio created for workspace %s (search %s, browser %s)", id.clusterID, studioSearchInstanceName, studioBrowserInstanceName)
}

// studioSharedService is one backend the Studio owns and every project shares.
// Its resourceRef is resolved by the API from the infrastructure Template, so
// the reconciler never reads Templates. Keep in step with controller/studio.
type studioSharedService struct {
	name        string // spec block: "search" / "browser"
	resourceRef func(s *Server, ctx context.Context, c *asclient.Client) *aiv1alpha1.ProjectProviderResourceReference
}

var studioSharedServices = []studioSharedService{
	{name: "search", resourceRef: (*Server).searchResourceRef},
	{name: "browser", resourceRef: (*Server).browserResourceRef},
}

// retrofitStudioServices fills in the spec block of every shared service the
// Studio is missing a resourceRef for, whether the service was introduced after
// the Studio was first created or its Template was simply not resolvable at the
// time. It patches only what is missing, so a workspace whose Studio predates a
// backend gains it without a recreate, and a service the workspace disabled is
// left alone. It reports whether every service now has its reference, so the
// caller knows whether to stop retrying. It is a no-op until the studios schema
// carries the field — kcp prunes unknown fields, so the write simply does not
// persist before then.
func (s *Server) retrofitStudioServices(ctx context.Context, c *asclient.Client, st *unstructured.Unstructured) (complete bool) {
	complete = true
	changed := false
	for _, svc := range studioSharedServices {
		if disabled, _, _ := unstructured.NestedBool(st.Object, "spec", svc.name, "disabled"); disabled {
			continue
		}
		if resource, _, _ := unstructured.NestedString(st.Object, "spec", svc.name, "resourceRef", "resource"); strings.TrimSpace(resource) != "" {
			continue // already present
		}
		ref := svc.resourceRef(s, ctx, c)
		if ref == nil {
			complete = false
			continue
		}
		if size, _, _ := unstructured.NestedString(st.Object, "spec", svc.name, "size"); strings.TrimSpace(size) == "" {
			_ = unstructured.SetNestedField(st.Object, "small", "spec", svc.name, "size")
		}
		setStudioResourceRef(st, svc.name, ref)
		changed = true
	}
	if !changed {
		return complete
	}
	if _, err := c.Resource(studioResource, "").Update(ctx, st, metav1.UpdateOptions{}); err != nil {
		log.Printf("retrofitting shared services onto studio for workspace %s: %v", st.GetName(), err)
		return false
	}
	return complete
}

// setStudioResourceRef writes a resolved instance reference under
// spec.<service>.resourceRef on the Studio being created.
func setStudioResourceRef(st *unstructured.Unstructured, service string, ref *aiv1alpha1.ProjectProviderResourceReference) {
	_ = unstructured.SetNestedMap(st.Object, map[string]any{
		"name":       ref.Name,
		"apiVersion": ref.APIVersion,
		"kind":       ref.Kind,
		"resource":   ref.Resource,
	}, "spec", service, "resourceRef")
}

// studioSearchInstanceName is the workspace's shared search backend — fixed,
// because there is exactly one and every project addresses it. Must match
// controller/studio.SearchInstanceName.
const studioSearchInstanceName = "app-studio-search"

// studioBrowserInstanceName is the workspace's shared headless browser — fixed,
// like the search backend. Must match controller/studio.BrowserInstanceName.
const studioBrowserInstanceName = "app-studio-browser"

// searchResourceRef resolves the searxng Template's instanceCRD as the
// caller, so the Studio reconciler never has to read Templates.
func (s *Server) searchResourceRef(ctx context.Context, c *asclient.Client) *aiv1alpha1.ProjectProviderResourceReference {
	info, err := fetchProjectTemplate(ctx, c, "searxng")
	if err != nil || strings.TrimSpace(info.Resource) == "" || strings.TrimSpace(info.Kind) == "" {
		log.Printf("web search unavailable: resolving the searxng template: %v", err)
		return nil
	}
	return &aiv1alpha1.ProjectProviderResourceReference{
		Name:       studioSearchInstanceName,
		APIVersion: info.APIVersion,
		Kind:       info.Kind,
		Resource:   info.Resource,
	}
}

// browserResourceRef resolves the browser Template's instanceCRD as the
// caller, so the Studio reconciler never has to read Templates.
func (s *Server) browserResourceRef(ctx context.Context, c *asclient.Client) *aiv1alpha1.ProjectProviderResourceReference {
	info, err := fetchProjectTemplate(ctx, c, "browser")
	if err != nil || strings.TrimSpace(info.Resource) == "" || strings.TrimSpace(info.Kind) == "" {
		log.Printf("preview browser unavailable: resolving the browser template: %v", err)
		return nil
	}
	return &aiv1alpha1.ProjectProviderResourceReference{
		Name:       studioBrowserInstanceName,
		APIVersion: info.APIVersion,
		Kind:       info.Kind,
		Resource:   info.Resource,
	}
}

// searchBackend reports the workspace's shared search backend for a turn.
// Empty when there is none — web_search then says so rather than failing
// obscurely.
func (s *Server) searchBackend(ctx context.Context, c *asclient.Client) (resource, name string) {
	return s.studioBackend(ctx, c, "search")
}

// browserBackend reports the workspace's shared headless browser for a turn.
// Empty when there is none — preview inspection then reports unavailable
// rather than failing obscurely.
func (s *Server) browserBackend(ctx context.Context, c *asclient.Client) (resource, name string) {
	return s.studioBackend(ctx, c, "browser")
}

// studioBackend reads one ready shared service (search/browser) off the Studio
// singleton, returning its plural resource and instance name, or empty when the
// service is absent, disabled, or not yet Ready.
func (s *Server) studioBackend(ctx context.Context, c *asclient.Client, service string) (resource, name string) {
	if c == nil {
		return "", ""
	}
	st, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return "", ""
	}
	if disabled, _, _ := unstructured.NestedBool(st.Object, "spec", service, "disabled"); disabled {
		return "", ""
	}
	phase, _, _ := unstructured.NestedString(st.Object, "status", service, "phase")
	if phase != aiv1alpha1.StudioServiceReady {
		return "", ""
	}
	resource, _, _ = unstructured.NestedString(st.Object, "status", service, "resource")
	name, _, _ = unstructured.NestedString(st.Object, "status", service, "instance")
	return resource, name
}
