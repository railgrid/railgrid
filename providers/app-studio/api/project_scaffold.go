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
	"fmt"
	"net/http"

	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/scaffold"
	"github.com/faroshq/provider-app-studio/workspace"
)

// Scaffold seeding — the wizard-first "attach starter code to the template"
// step. A dev-capable template pins a scaffold repo (spec.development.scaffold);
// its tree is laid verbatim into the project workspace at creation so the
// project opens on a runnable placeholder instead of an empty directory. The
// existing dev-sync then pushes the seeded workspace into the sandbox once the
// Project reconciler provisions it.

// seedProjectScaffold fetches the template's scaffold and writes it into the
// project workspace. Best-effort by contract: a scaffold-less template, an
// already-populated workspace, or a fetch failure all leave a valid (empty)
// project the assistant can still build — never fail creation over it.
// Returns the number of files seeded (0 when nothing was seeded).
func (s *Server) seedProjectScaffold(ctx context.Context, id identity, p *aiv1alpha1.Project, info projectTemplateInfo) (int, error) {
	if s.workspaces == nil || p == nil || info.ScaffoldRepo == "" {
		return 0, nil
	}
	scope := projectWorkspaceScope(id, p)

	// Do not clobber a workspace that already has content (re-create races, an
	// adopted repo hydrate, or a prior seed).
	existing, err := s.workspaces.ListFiles(ctx, scope, workspace.ListOptions{})
	if err == nil && len(existing.Files) > 0 {
		return 0, nil
	}

	files, err := scaffold.Fetch(ctx, info.ScaffoldRepo, info.ScaffoldRef)
	if err != nil {
		return 0, fmt.Errorf("fetching scaffold %s@%s: %w", info.ScaffoldRepo, info.ScaffoldRef, err)
	}
	if err := scaffold.CheckLayout(info.WorkspacePaths(), files); err != nil {
		return 0, err
	}
	if err := s.workspaces.ApplyFiles(ctx, scope, files); err != nil {
		return 0, fmt.Errorf("seeding workspace: %w", err)
	}
	// Register the seeded files as uncommitted so the rest of the workspace
	// machinery sees them: the Project reconciler's commit convergence lands
	// them as the FIRST commit (git repo = scaffold), and development sync
	// pushes them into the sandbox. ApplyFiles writes bytes + bumps the
	// source revision but does NOT touch the uncommitted-paths ledger, so
	// without this the scaffold is invisible to both — the symptom being a
	// project that "just started working" with an empty repo and no dev sync.
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	if _, err := s.workspaces.AddUncommittedPaths(ctx, scope, paths); err != nil {
		return 0, fmt.Errorf("tracking seeded files: %w", err)
	}
	return len(files), nil
}

// reseedProjectScaffold is POST /api/projects/{project}/scaffold — re-attach
// the template scaffold to an empty workspace (retry after a fetch failure at
// creation, or seed a project created before scaffolding existed). It resolves
// the scaffold from the project's recorded spec.template.Name.
func (s *Server) reseedProjectScaffold(w http.ResponseWriter, r *http.Request) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if project.Spec.Template == nil || project.Spec.Template.Name == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "project has no template; select one before attaching a scaffold")
		return
	}
	info, err := fetchProjectTemplate(r.Context(), c, project.Spec.Template.Name)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if info.ScaffoldRepo == "" {
		writeStatus(w, http.StatusUnprocessableEntity, "NoScaffold", fmt.Sprintf("template %q ships no scaffold", info.Name))
		return
	}
	seeded, err := s.seedProjectScaffold(r.Context(), id, project, info)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"template": info.Name,
		"scaffold": map[string]string{"repository": info.ScaffoldRepo, "ref": info.ScaffoldRef},
		"seeded":   seeded,
	})
}

// emitScaffoldSeed runs the seed step during creation with a wizard status
// line, downgrading any failure to a warning (creation must not fail here).
func (s *Server) emitScaffoldSeed(ctx context.Context, id identity, p *aiv1alpha1.Project, info projectTemplateInfo, onStatus projectCreationStatusFunc, c *asclient.Client) {
	if info.ScaffoldRepo == "" {
		return
	}
	_ = emitProjectCreationStatus(onStatus, "Attaching scaffold to "+info.Name)
	seeded, err := s.seedProjectScaffold(ctx, id, p, info)
	if err != nil {
		klog.V(1).Infof("scaffold seed failed for project %s (template %s): %v", p.Name, info.Name, err)
		_ = emitProjectCreationStatus(onStatus, "Scaffold unavailable — starting from an empty project")
		return
	}
	if seeded > 0 {
		_ = emitProjectCreationStatus(onStatus, fmt.Sprintf("Scaffold attached (%d files)", seeded))
	}
}
