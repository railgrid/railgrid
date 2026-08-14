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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/tenant"
)

// The development preview has the same two-state visibility as a published
// production app, and for the same reason: it is served on a public hostname
// through the platform access gate, so "who can open this URL" is a real
// choice rather than an implementation detail.
//
// The difference is the default. Production is something you decide to publish;
// a preview is the project's in-progress state, reachable while you are still
// building it. It defaults to private — the workspace's own members, and nobody
// else — and becomes public only when asked.
//
// Where publishing writes the access value onto the production binding, this
// writes Project.spec.sharing.preview.mode and lets the reconciler overlay it
// onto the development binding on the next pass. That indirection is what lets
// a project created before this existed converge without a migration.

// projectPreviewAccessRequest is POST /preview {mode}. The vocabulary matches
// the publishing surface so the portal can drive both channels with one control.
type projectPreviewAccessRequest struct {
	Mode string `json:"mode,omitempty"`
}

// projectPreviewAccessResponse reports the requested mode, the URL it applies
// to, and whether the live instance has caught up yet.
type projectPreviewAccessResponse struct {
	Mode string `json:"mode"`
	URL  string `json:"url,omitempty"`
	// Converged is false while the reconciler has not yet applied the mode to
	// the running preview — the URL keeps its previous visibility until then,
	// which the portal must not present as already done.
	Converged bool `json:"converged"`
	// Supported is false for a project whose development template exposes no
	// URL. There is nothing to make private, and the portal should not offer
	// the control.
	Supported bool `json:"supported"`
	// Grants are the per-member preview grants, in the same shape publishing
	// uses, so one portal component renders both channels.
	Grants []projectPublishingGrantView `json:"grants,omitempty"`
}

func (s *Server) getProjectPreviewAccess(w http.ResponseWriter, r *http.Request) {
	c, _, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.projectPreviewAccessResponse(r.Context(), c, p))
}

// setProjectPreviewAccess is POST /preview. It records the policy on the
// Project; the reconciler converges the instance.
func (s *Server) setProjectPreviewAccess(w http.ResponseWriter, r *http.Request) {
	c, _, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	var req projectPreviewAccessRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid preview access request: "+err.Error())
			return
		}
	}
	mode, err := requestedPreviewMode(req.Mode, p)
	if err != nil {
		writeError(w, err)
		return
	}
	updated, err := s.setPreviewSharingMode(r.Context(), c, p, mode)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.projectPreviewAccessResponse(r.Context(), c, updated))
}

// requestedPreviewMode normalizes the portal vocabulary. An empty body
// preserves the current mode, so a POST that only means "re-apply" cannot
// silently widen access.
func requestedPreviewMode(requested string, p *aiv1alpha1.Project) (aiv1alpha1.ProjectSharingMode, error) {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "public":
		return aiv1alpha1.ProjectSharingModePublic, nil
	case "restricted", "members", "private":
		return aiv1alpha1.ProjectSharingModePrivate, nil
	case "":
		return currentPreviewMode(p), nil
	default:
		return "", newValidationError("unknown preview mode " + requested + ": want public or restricted")
	}
}

func currentPreviewMode(p *aiv1alpha1.Project) aiv1alpha1.ProjectSharingMode {
	if p != nil && p.Spec.Sharing.Preview.Mode == aiv1alpha1.ProjectSharingModePublic {
		return aiv1alpha1.ProjectSharingModePublic
	}
	return aiv1alpha1.ProjectSharingModePrivate
}

func (s *Server) setPreviewSharingMode(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project, mode aiv1alpha1.ProjectSharingMode) (*aiv1alpha1.Project, error) {
	if currentPreviewMode(p) == mode {
		return p, nil
	}
	next := p.DeepCopy()
	next.Spec.Sharing.Preview.Mode = mode
	return c.Projects().Update(ctx, next, metav1.UpdateOptions{})
}

// projectPreviewAccessResponse reads the desired mode from the Project and the
// applied one from the live development instance, so the portal can show a
// pending state rather than claiming a preview is private the moment the
// request returns.
func (s *Server) projectPreviewAccessResponse(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) projectPreviewAccessResponse {
	desired := bindings.PreviewAccess(p)
	resp := projectPreviewAccessResponse{
		Mode: portalModeString(desired),
		URL:  projectEnvironmentPreviewURL(p.Status.Environments, projectDevelopmentEnvironmentName, projectDevelopmentBindingName),
	}
	observed, supported := s.observedPreviewAccess(ctx, c, p)
	resp.Supported = supported
	resp.Converged = supported && observed == desired
	// Grants are advisory in this response: a lookup failure must not fail the
	// visibility read the toggle depends on.
	if supported {
		if runtime, err := s.previewRuntime(ctx, c, p); err == nil {
			if grants, err := s.appAccessGrantViews(ctx, c, runtime.target.Name); err == nil {
				resp.Grants = grants
			}
		}
	}
	return resp
}

// previewRuntime resolves the development instance as a sharing channel, so the
// grant machinery in project_publishing.go serves the preview unchanged: same
// per-app ClusterRole, same per-member ClusterRoleBinding, same invite-by-email
// path. The instance names differ ("-dev" vs "-prod"), so the deterministic
// role and binding names never collide between the two channels.
//
// desiredAccess comes from the Project's sharing policy rather than the binding
// values, because the policy is the authority the reconciler overlays onto the
// binding — reading the binding would report the previous value for the window
// between a toggle and its next reconcile, and grants would be refused as
// "public" on an app the user just made private.
func (s *Server) previewRuntime(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) (appAccessRuntime, error) {
	binding := findProjectDevelopmentBinding(p)
	if binding == nil || binding.ResourceRef == nil {
		return appAccessRuntime{}, newValidationError("this project has no development environment to share")
	}
	ref := binding.ResourceRef
	gvr, err := projectProviderResourceGVR(ref)
	if err != nil {
		return appAccessRuntime{}, err
	}
	values, err := projectProviderBindingValues(*binding)
	if err != nil {
		return appAccessRuntime{}, err
	}
	name := strings.TrimSpace(ref.Name)
	if binding.Kind == aiv1alpha1.ProjectBindingKindProviderResource {
		name = projectProviderBindingResourceName(p, *binding, values, identity{})
	}
	if name == "" {
		return appAccessRuntime{}, newValidationError("development binding has no runtime resource name")
	}
	obj, err := c.Resource(tenant.Resource{GVR: gvr, Kind: ref.Kind, Plural: ref.Resource}, "").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return appAccessRuntime{}, err
	}
	return appAccessRuntime{
		target: projectPublishingTargetView{
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
			Resource:   ref.Resource,
			Name:       name,
			UID:        string(obj.GetUID()),
		},
		instance:      obj,
		desiredAccess: bindings.PreviewAccess(p),
	}, nil
}

func (s *Server) listProjectPreviewGrants(w http.ResponseWriter, r *http.Request) {
	s.listAppAccessGrants(w, r, (*Server).previewRuntime)
}

func (s *Server) createProjectPreviewGrant(w http.ResponseWriter, r *http.Request) {
	s.createAppAccessGrant(w, r, (*Server).previewRuntime)
}

func (s *Server) revokeProjectPreviewGrant(w http.ResponseWriter, r *http.Request) {
	s.revokeAppAccessGrant(w, r, (*Server).previewRuntime)
}

// observedPreviewAccess reads spec.access off the live development instance.
// A template that exposes no URL declares no access input, so its absence is
// the signal that this project has no preview visibility to control — not a
// failure.
func (s *Server) observedPreviewAccess(ctx context.Context, c *asclient.Client, p *aiv1alpha1.Project) (string, bool) {
	binding := findProjectDevelopmentBinding(p)
	if binding == nil || binding.ResourceRef == nil {
		return "", false
	}
	ref := binding.ResourceRef
	gvr, err := projectProviderResourceGVR(ref)
	if err != nil {
		return "", false
	}
	values, err := projectProviderBindingValues(*binding)
	if err != nil {
		return "", false
	}
	name := strings.TrimSpace(ref.Name)
	if binding.Kind == aiv1alpha1.ProjectBindingKindProviderResource {
		name = projectProviderBindingResourceName(p, *binding, values, identity{})
	}
	if name == "" {
		return "", false
	}
	obj, err := c.Resource(tenant.Resource{GVR: gvr, Kind: ref.Kind, Plural: ref.Resource}, "").Get(ctx, name, metav1.GetOptions{})
	if err != nil || obj == nil {
		return "", false
	}
	access, found, err := unstructured.NestedString(obj.Object, "spec", bindings.AccessField)
	if err != nil || !found {
		return "", false
	}
	return access, true
}

// findProjectDevelopmentBinding is the development counterpart of
// findProjectProductionBinding.
func findProjectDevelopmentBinding(p *aiv1alpha1.Project) *aiv1alpha1.ProjectProviderBindingSpec {
	if p == nil {
		return nil
	}
	for i := range p.Spec.Environments {
		env := &p.Spec.Environments[i]
		if strings.TrimSpace(env.Name) != projectDevelopmentEnvironmentName {
			continue
		}
		for j := range env.Bindings {
			if strings.TrimSpace(env.Bindings[j].Name) == projectDevelopmentBindingName &&
				env.Bindings[j].Kind != aiv1alpha1.ProjectBindingKindProviderReference {
				return &env.Bindings[j]
			}
		}
	}
	return nil
}
