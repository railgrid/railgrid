/*
Copyright 2026 The Faros Authors.

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

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/tenant"
)

// Session CRs and the Studio singleton — the API-side halves of the
// control-plane projections the reconcilers converge (controller/session,
// controller/studio). Both writes are best-effort as the caller: a workspace
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
				"ai.kedge.faros.sh/project-uid":  string(p.UID),
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
// Once per process per workspace: a Studio someone has since edited is left
// exactly as it is.
var studioEnsured sync.Map // clusterID → struct{}

// ensureStudio writes the workspace's Studio if it is missing.
func (s *Server) ensureStudio(ctx context.Context, c *asclient.Client, id identity) {
	if c == nil || id.clusterID == "" {
		return
	}
	if _, done := studioEnsured.Load(id.clusterID); done {
		return
	}
	if _, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{}); err == nil {
		studioEnsured.Store(id.clusterID, struct{}{})
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
			"search": map[string]any{"size": "small"},
		},
	}}
	if ref := s.searchResourceRef(ctx, c); ref != nil {
		_ = unstructured.SetNestedMap(st.Object, map[string]any{
			"name":       ref.Name,
			"apiVersion": ref.APIVersion,
			"kind":       ref.Kind,
			"resource":   ref.Resource,
		}, "spec", "search", "resourceRef")
	}
	if _, err := c.Resource(studioResource, "").Create(ctx, st, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		log.Printf("creating the studio for workspace %s: %v", id.clusterID, err)
		return
	}
	studioEnsured.Store(id.clusterID, struct{}{})
	log.Printf("studio created for workspace %s (search instance %s)", id.clusterID, studioSearchInstanceName)
}

// studioSearchInstanceName is the workspace's shared search backend — fixed,
// because there is exactly one and every project addresses it. Must match
// controller/studio.SearchInstanceName.
const studioSearchInstanceName = "app-studio-search"

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

// searchBackend reports the workspace's shared search backend for a turn.
// Empty when there is none — web_search then says so rather than failing
// obscurely.
func (s *Server) searchBackend(ctx context.Context, c *asclient.Client) (resource, name string) {
	if c == nil {
		return "", ""
	}
	st, err := c.Resource(studioResource, "").Get(ctx, aiv1alpha1.StudioName, metav1.GetOptions{})
	if err != nil {
		return "", ""
	}
	if disabled, _, _ := unstructured.NestedBool(st.Object, "spec", "search", "disabled"); disabled {
		return "", ""
	}
	phase, _, _ := unstructured.NestedString(st.Object, "status", "search", "phase")
	if phase != aiv1alpha1.StudioServiceReady {
		return "", ""
	}
	resource, _, _ = unstructured.NestedString(st.Object, "status", "search", "resource")
	name, _, _ = unstructured.NestedString(st.Object, "status", "search", "instance")
	return resource, name
}
