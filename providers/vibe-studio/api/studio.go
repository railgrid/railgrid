// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"errors"
	"log"
	"sync"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/client"
	"github.com/faroshq/provider-vibe-studio/controller/studio"
	"github.com/faroshq/provider-vibe-studio/provision"
)

// Studio bootstrap.
//
// The Studio singleton carries the workspace's shared services, and the
// reconciler cannot create it: reconcilers converge objects, they do not
// invent them, and resolving the searxng Template needs the CALLER's identity
// (Templates ride virtual storage with their own). So the API writes it on
// the tenant's first authenticated request after enabling the provider —
// which is when they open the portal — and the reconciler has the search
// backend warm before the first project exists.
//
// Once per process per workspace: a Studio that someone has since edited is
// left exactly as it is.
var studioEnsured sync.Map // clusterID → struct{}

// ensureStudio writes the workspace's Studio if it is missing. Best-effort:
// a workspace without one loses web search, not the ability to build.
func (s *Server) ensureStudio(ctx context.Context, cl *client.Client, clusterID string) {
	if clusterID == "" || cl == nil {
		return
	}
	if _, done := studioEnsured.Load(clusterID); done {
		return
	}
	// Single-flight across concurrent requests from the same workspace.
	key := "studio/" + clusterID
	if !s.begin(key) {
		return
	}
	defer s.end(key)

	if _, err := cl.GetStudio(ctx, vibev1alpha1.StudioName); err == nil {
		studioEnsured.Store(clusterID, struct{}{})
		return
	} else if errors.Is(err, client.ErrResourceUnavailable) {
		// The APIBinding has not caught up with the provider's schemas yet.
		return
	}

	st := &vibev1alpha1.Studio{}
	st.Name = vibev1alpha1.StudioName
	st.Spec.Search = vibev1alpha1.StudioSearch{Size: "small"}
	if ref := s.searchResourceRef(ctx, cl); ref != nil {
		st.Spec.Search.ResourceRef = ref
	}
	if _, err := cl.ApplyStudio(ctx, st); err != nil {
		log.Printf("creating the studio for workspace %s: %v", clusterID, err)
		return
	}
	studioEnsured.Store(clusterID, struct{}{})
	log.Printf("studio created for workspace %s (search instance %s)", clusterID, studio.SearchInstanceName)
}

// searchResourceRef resolves the searxng Template's instanceCRD as the
// caller, so the reconciler never has to read Templates.
func (s *Server) searchResourceRef(ctx context.Context, cl *client.Client) *vibev1alpha1.ProjectProviderResourceReference {
	tmpl, err := cl.GetTemplate(ctx, searchTemplate)
	if err != nil {
		log.Printf("web search unavailable: reading the %s template: %v", searchTemplate, err)
		return nil
	}
	info, err := provision.ParseDevInfo(tmpl)
	if err != nil || info.Resource == "" || info.Kind == "" {
		log.Printf("web search unavailable: the %s template has no instanceCRD", searchTemplate)
		return nil
	}
	return &vibev1alpha1.ProjectProviderResourceReference{
		Name:       studio.SearchInstanceName,
		APIVersion: info.Group + "/" + info.Version,
		Kind:       info.Kind,
		Resource:   info.Resource,
	}
}

// searchBackend reports the workspace's shared search backend for a turn.
// Empty when there is none — web_search then says so rather than failing
// obscurely.
func (s *Server) searchBackend(ctx context.Context, cl *client.Client) (resource, name string) {
	st, err := cl.GetStudio(ctx, vibev1alpha1.StudioName)
	if err != nil || st.Spec.Search.Disabled || st.Status.Search == nil {
		return "", ""
	}
	if st.Status.Search.Phase != vibev1alpha1.StudioServiceReady {
		return "", ""
	}
	return st.Status.Search.Resource, st.Status.Search.Instance
}
