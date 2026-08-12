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
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/http"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/client"
	"github.com/faroshq/provider-vibe-studio/provision"
	"github.com/faroshq/provider-vibe-studio/session"
)

// Promotion.
//
// Promoting is a SPEC WRITE, nothing more: append a production environment to
// the Project whose binding runs the same template in farosMode: production
// with the built images pinned. The Project reconciler converges any binding
// it finds, so the production instance is created, updated, and torn down by
// exactly the machinery that already runs the development sandbox — no
// separate promotion pipeline, and re-promoting a newer image is just
// another write.
//
// The development environment keeps running: production is a second
// environment, not a mode flip.

const productionEnvironment = vibev1alpha1.ProductionEnvironment

// promoteRequest names the image to run for each buildable component. Keys
// are component names ("web"), values are fully-qualified image references —
// digests preferred, because a tag can move under a running deployment.
type promoteRequest struct {
	Images map[string]string `json:"images"`
}

// handlePromote appends (or updates) the Project's production environment.
func (s *Server) handlePromote(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	cl := s.callerClient(w, r)
	if cl == nil {
		return
	}
	id := r.PathValue("id")
	var req promoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"images\": {\"<component>\": \"<image ref>\"}}"})
		return
	}

	state, err := s.foldSession(r.Context(), scope, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if state.ProjectName == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this session has no project to promote"})
		return
	}
	p, err := cl.GetProject(r.Context(), state.ProjectName)
	if err != nil {
		writeError(w, err)
		return
	}

	// Tether: production runs a git revision, so everything in the workspace
	// must be committed first. Promoting ahead of the commit would ship an
	// image nobody can trace back to a tree — the app-studio bug this
	// replaces. The commit reconciler converges within a reconcile pass, so
	// "wait a moment" is an accurate instruction, not a dead end.
	sess, err := cl.GetSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if sess.Status.WorkspaceRevision != sess.Status.CommittedRevision {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     "workspace has uncommitted changes; promotion waits for the commit to land",
			"workspace": sess.Status.WorkspaceRevision,
			"committed": sess.Status.CommittedRevision,
		})
		return
	}
	if sess.Status.LastCommitSHA == "" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "nothing has been committed to git yet; there is no revision to promote",
		})
		return
	}

	s.backfillImageInputs(r.Context(), cl, p)
	s.backfillGitToken(r.Context(), cl, p)

	// The caller may name images explicitly; anything it leaves out comes
	// from this project's own build of the committed revision.
	images := map[string]string{}
	for k, v := range resolveBuiltImages(r.Context(), cl, p, sess.Status.LastCommitSHA) {
		images[k] = v
	}
	for k, v := range req.Images {
		if strings.TrimSpace(v) != "" {
			images[k] = v
		}
	}

	prodName, missing, err := promoteProject(p, images, sess.Status.LastCommitSHA)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(missing) > 0 {
		// Say exactly what is needed rather than deploying a half-built app.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "no image built from " + shortSHA(sess.Status.LastCommitSHA) + " yet for: " +
				strings.Join(missing, ", ") + " — wait for the build to publish, or name an image explicitly",
			"missing": missing,
		})
		return
	}
	if _, err := cl.ApplyProject(r.Context(), p); err != nil {
		writeError(w, err)
		return
	}
	if err := s.setCheckpoint(r.Context(), scope, id, session.Checkpoint{
		Name: session.CheckpointProduction, State: session.CheckpointPending,
		Reason: "promoting " + prodName,
	}); err != nil {
		log.Printf("promotion checkpoint for session %s: %v", id, err)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"instance": prodName})
}

// backfillImageInputs fills in a development contract written before image
// inputs were recorded, by re-reading the Template as the calling user. It is
// best-effort: a project whose template has since changed shape still gets a
// named error from promoteProject rather than a silent half-promotion.
func (s *Server) backfillImageInputs(ctx context.Context, cl *client.Client, p *vibev1alpha1.Project) {
	if p.Spec.Development == nil || p.Spec.Template == nil || p.Spec.Template.Name == "" {
		return
	}
	for _, c := range p.Spec.Development.Components {
		if c.ImageInput != "" {
			return // already recorded
		}
	}
	tmpl, err := cl.GetTemplate(ctx, p.Spec.Template.Name)
	if err != nil {
		log.Printf("backfilling image inputs for %s: %v", p.Name, err)
		return
	}
	info, err := provision.ParseDevInfo(tmpl)
	if err != nil {
		log.Printf("backfilling image inputs for %s: %v", p.Name, err)
		return
	}
	for i, c := range p.Spec.Development.Components {
		p.Spec.Development.Components[i].ImageInput = info.ImageInputs[c.Name]
	}
}

// shortSHA abbreviates a commit the way git does.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// backfillGitToken records where the project's git token lives, for projects
// created before the field existed. Without it the reconciler cannot mint the
// registry credential and production sits in ErrImagePull on private images.
func (s *Server) backfillGitToken(ctx context.Context, cl *client.Client, p *vibev1alpha1.Project) {
	repo := p.Spec.Repository
	if repo == nil || repo.ConnectionRef == "" || repo.TokenSecret != nil {
		return
	}
	ref, login, err := cl.GitToken(ctx, repo.ConnectionRef)
	if err != nil {
		log.Printf("resolving the git token for %s: %v", p.Name, err)
		return
	}
	if ref != nil {
		repo.TokenSecret = ref
		repo.Login = login
	}
}

// promoteProject mutates the Project spec so it declares a production
// environment for the given images. Pure apart from the mutation; returns the
// production instance name and any buildable components without an image.
func promoteProject(p *vibev1alpha1.Project, images map[string]string, revision string) (string, []string, error) {
	dev := developmentBinding(p)
	if dev == nil || dev.ResourceRef == nil {
		return "", nil, fmt.Errorf("this project has no development runtime to promote from")
	}
	if p.Spec.Development == nil {
		return "", nil, fmt.Errorf("this project has no development contract; re-create it to promote")
	}

	var missing []string
	imageValues := map[string]any{}
	for _, comp := range p.Spec.Development.Components {
		if comp.ImageInput == "" {
			continue // component ships no image (a worker sharing another's)
		}
		ref := strings.TrimSpace(images[comp.Name])
		if ref == "" {
			missing = append(missing, comp.Name)
			continue
		}
		imageValues[comp.ImageInput] = ref
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return "", missing, nil
	}

	// Production values are the development ones with the mode flipped, the
	// instance renamed, and the images pinned.
	values := map[string]any{}
	if len(dev.Values.Raw) > 0 {
		if err := json.Unmarshal(dev.Values.Raw, &values); err != nil {
			return "", nil, fmt.Errorf("reading development values: %w", err)
		}
	}
	prodName := productionInstanceName(p.Name)
	values["name"] = prodName
	values[templateFarosModeField] = templateFarosModeProduction
	maps.Copy(values, imageValues)
	raw, err := json.Marshal(values)
	if err != nil {
		return "", nil, fmt.Errorf("encoding production values: %w", err)
	}

	binding := vibev1alpha1.ProjectProviderBindingSpec{
		Name:     vibev1alpha1.BindingRuntime,
		Template: dev.Template,
		Provider: "infrastructure",
		Kind:     vibev1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &vibev1alpha1.ProjectProviderResourceReference{
			Name:       prodName,
			APIVersion: dev.ResourceRef.APIVersion,
			Kind:       dev.ResourceRef.Kind,
			Resource:   dev.ResourceRef.Resource,
		},
		Values: runtime.RawExtension{Raw: raw},
	}
	env := vibev1alpha1.ProjectEnvironmentSpec{
		Name:      productionEnvironment,
		Mode:      vibev1alpha1.ProjectEnvironmentModeArtifact,
		Promotion: vibev1alpha1.ProjectPromotionManual,
		Revision:  revision,
		Bindings:  []vibev1alpha1.ProjectProviderBindingSpec{binding},
	}
	// Re-promoting replaces the environment: same instance, newer images.
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Name == productionEnvironment {
			p.Spec.Environments[i] = env
			return prodName, nil, nil
		}
	}
	p.Spec.Environments = append(p.Spec.Environments, env)
	return prodName, nil, nil
}

// developmentBinding finds the live development runtime binding.
func developmentBinding(p *vibev1alpha1.Project) *vibev1alpha1.ProjectProviderBindingSpec {
	for i, env := range p.Spec.Environments {
		if env.Name == productionEnvironment {
			continue
		}
		for j, b := range env.Bindings {
			// By name: a project also binds a search backend, and promoting
			// that instead of the app would be a spectacular way to fail.
			if b.Name == vibev1alpha1.BindingRuntime && b.ResourceRef != nil {
				return &p.Spec.Environments[i].Bindings[j]
			}
		}
	}
	return nil
}

// productionInstanceName keeps the prod instance beside its dev sibling and
// within the 63-character name budget.
func productionInstanceName(project string) string {
	const suffix = "-prod"
	if len(project)+len(suffix) <= 63 {
		return project + suffix
	}
	return strings.TrimRight(project[:63-len(suffix)], "-") + suffix
}

// promotionView reports what the portal needs to render the ship panel.
type promotionView struct {
	// Components that need an image before promotion can run.
	Components []promotionComponent `json:"components"`
	Instance   string               `json:"instance,omitempty"`
	Phase      string               `json:"phase,omitempty"`
	URL        string               `json:"url,omitempty"`
	Revision   string               `json:"revision,omitempty"`
	// Committed reports whether the workspace is fully pushed to git, and
	// CommitSHA the revision a promotion would ship.
	Committed bool   `json:"committed"`
	CommitSHA string `json:"commitSHA,omitempty"`
}

type promotionComponent struct {
	Name       string `json:"name"`
	ImageInput string `json:"imageInput"`
	Image      string `json:"image,omitempty"`
	// Built reports that Image came from this project's CI, built from the
	// commit the workspace is on, rather than from what is already running.
	Built bool `json:"built"`
}

// handleGetPromotion describes the project's production environment.
func (s *Server) handleGetPromotion(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scope(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	cl := s.callerClient(w, r)
	if cl == nil {
		return
	}
	state, err := s.foldSession(r.Context(), scope, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if state.ProjectName == "" {
		writeJSON(w, http.StatusOK, promotionView{})
		return
	}
	p, err := cl.GetProject(r.Context(), state.ProjectName)
	if err != nil {
		writeError(w, err)
		return
	}
	// Backfill in memory too (not just on promote), so a project created
	// before image inputs were recorded still renders a usable ship panel.
	s.backfillImageInputs(r.Context(), cl, p)
	s.backfillGitToken(r.Context(), cl, p)
	view := promotionViewOf(p)
	if sess, err := cl.GetSession(r.Context(), r.PathValue("id")); err == nil {
		view.CommitSHA = sess.Status.LastCommitSHA
		view.Committed = sess.Status.LastCommitSHA != "" &&
			sess.Status.WorkspaceRevision == sess.Status.CommittedRevision
		// Offer what CI actually built from this commit, so shipping is a
		// button rather than a copy-paste exercise.
		built := resolveBuiltImages(r.Context(), cl, p, sess.Status.LastCommitSHA)
		for i, c := range view.Components {
			if ref := built[c.Name]; ref != "" {
				view.Components[i].Image = ref
				view.Components[i].Built = true
			}
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// promotionViewOf builds the ship-panel DTO from the Project. Pure.
func promotionViewOf(p *vibev1alpha1.Project) promotionView {
	out := promotionView{}
	current := map[string]string{}
	for _, env := range p.Spec.Environments {
		if env.Name != productionEnvironment {
			continue
		}
		out.Revision = env.Revision
		for _, b := range env.Bindings {
			if b.ResourceRef != nil {
				out.Instance = b.ResourceRef.Name
			}
			values := map[string]any{}
			if len(b.Values.Raw) > 0 {
				_ = json.Unmarshal(b.Values.Raw, &values)
			}
			for k, v := range values {
				if sv, ok := v.(string); ok {
					current[k] = sv
				}
			}
		}
	}
	for _, env := range p.Status.Environments {
		if env.Name != productionEnvironment {
			continue
		}
		out.Phase = env.Phase
		for _, b := range env.Bindings {
			if b.URL != "" {
				out.URL = b.URL
			}
		}
	}
	if p.Spec.Development != nil {
		for _, comp := range p.Spec.Development.Components {
			if comp.ImageInput == "" {
				continue
			}
			out.Components = append(out.Components, promotionComponent{
				Name: comp.Name, ImageInput: comp.ImageInput, Image: current[comp.ImageInput],
			})
		}
		sort.Slice(out.Components, func(i, j int) bool { return out.Components[i].Name < out.Components[j].Name })
	}
	return out
}
