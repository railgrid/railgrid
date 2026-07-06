/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Workspace hydration: repo → workspace, the missing half of the code
// lifecycle (docs/app-studio-template-sandboxes.md §5). The Code provider's
// checkout tool reads the repository's text tree; this endpoint writes it
// into the project workspace, making the git repository the durable source
// of truth — the workspace filesystem becomes recoverable, template switches
// can re-hydrate, and existing repositories become importable.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

// projectToolCodeCheckoutRepository is the Code provider's checkout tool as
// exposed through the tenant MCP federation.
const projectToolCodeCheckoutRepository = "code__checkout_repository"

type projectHydrateRequest struct {
	// Ref optionally pins the branch/tag/SHA to hydrate from; empty uses the
	// repository default branch.
	Ref string `json:"ref,omitempty"`
}

type projectHydrateResponse struct {
	RepositoryRef string   `json:"repositoryRef"`
	Ref           string   `json:"ref,omitempty"`
	CommitSHA     string   `json:"commitSHA,omitempty"`
	Written       []string `json:"written,omitempty"`
	// Skipped lists repository paths that did not land in the workspace —
	// binary/oversized files the checkout left out, plus files the workspace
	// store refused (its own bounds).
	Skipped []string `json:"skipped,omitempty"`
}

// checkoutToolResult mirrors the Code provider's checkout_repository output.
type checkoutToolResult struct {
	Ref       string `json:"ref"`
	CommitSHA string `json:"commitSHA"`
	Files     []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"files"`
	Skipped []string `json:"skipped"`
}

// hydrateWorkspaceFromRepository is the shared repo→workspace core used by
// the HTTP endpoint, the assistant tool, and repository import at project
// creation. It reads the project repository's text tree through the Code
// provider's checkout tool (as the caller — httpReq carries the caller's
// Authorization) and writes it into the workspace: existing files are
// overwritten, workspace-only files are left in place. On success it kicks a
// development sync so the running environment picks the tree up.
func (s *Server) hydrateWorkspaceFromRepository(ctx context.Context, id identity, p *aiv1alpha1.Project, httpReq *http.Request, ref string) (projectHydrateResponse, error) {
	if s.workspaces == nil {
		return projectHydrateResponse{}, errors.New("project workspace store is not configured")
	}
	repositoryRef := ""
	if p.Spec.Repository != nil {
		repositoryRef = strings.TrimSpace(p.Spec.Repository.RepositoryRef)
	}
	if repositoryRef == "" {
		return projectHydrateResponse{}, newValidationError("project has no Code repository to hydrate from")
	}
	if strings.TrimSpace(id.clusterID) == "" {
		return projectHydrateResponse{}, newValidationError("no workspace cluster on request — cannot address the tenant MCP endpoint")
	}

	args := map[string]any{"repositoryRef": repositoryRef}
	if ref = strings.TrimSpace(ref); ref != "" {
		args["ref"] = ref
	}
	raw, err := callProjectMCPTool(ctx, s.mcpEndpoint(id.clusterID), httpReq, id.tenantPath, s.mcpInsecureSkipTLSVerify, projectToolCodeCheckoutRepository, args)
	if err != nil {
		return projectHydrateResponse{}, fmt.Errorf("checkout repository: %w", err)
	}
	var checkout checkoutToolResult
	if err := json.Unmarshal([]byte(raw), &checkout); err != nil {
		return projectHydrateResponse{}, fmt.Errorf("decode checkout result: %w", err)
	}

	scope := projectWorkspaceScope(id, p.Name)
	resp := projectHydrateResponse{
		RepositoryRef: repositoryRef,
		Ref:           checkout.Ref,
		CommitSHA:     checkout.CommitSHA,
		Skipped:       checkout.Skipped,
	}
	for _, f := range checkout.Files {
		if _, err := s.workspaces.WriteFile(ctx, scope, workspace.WriteOptions{Path: f.Path, Content: f.Content}); err != nil {
			resp.Skipped = append(resp.Skipped, fmt.Sprintf("%s (workspace: %v)", f.Path, err))
			continue
		}
		resp.Written = append(resp.Written, f.Path)
	}

	// Push the hydrated tree into the live development environment.
	go s.syncDevelopmentAfterMutation(id, p.DeepCopy(), projectToolHydrateWorkspace)

	return resp, nil
}

// projectImportRepositoryView is one Code repository a new project can be
// imported from (unclaimed by any App Studio project).
type projectImportRepositoryView struct {
	Ref           string `json:"ref"`
	Name          string `json:"name,omitempty"`
	ConnectionRef string `json:"connectionRef,omitempty"`
	HTMLURL       string `json:"htmlURL,omitempty"`
}

// listImportRepositories is GET /api/projects/import-repositories: Code
// repositories in the workspace that no App Studio project claims — the
// candidates for CreateProjectRequest.existingRepositoryRef.
func (s *Server) listImportRepositories(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.requireProjectClient(w, r)
	if !ok {
		return
	}
	list, err := c.Resource(codeRepositoryResource, "").List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	out := make([]projectImportRepositoryView, 0, len(list.Items))
	for i := range list.Items {
		repo := &list.Items[i]
		if strings.TrimSpace(repo.GetLabels()[projectRepositoryProjectLabel]) != "" {
			continue
		}
		view := projectImportRepositoryView{Ref: repo.GetName()}
		view.Name, _, _ = unstructured.NestedString(repo.Object, "spec", "name")
		view.ConnectionRef, _, _ = unstructured.NestedString(repo.Object, "spec", "connectionRef")
		view.HTMLURL, _, _ = unstructured.NestedString(repo.Object, "status", "htmlURL")
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	writeJSON(w, http.StatusOK, map[string]any{"repositories": out})
}

// hydrateProjectWorkspace is POST /api/projects/{project}/hydrate-workspace.
func (s *Server) hydrateProjectWorkspace(w http.ResponseWriter, r *http.Request) {
	_, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if s.workspaces == nil {
		// A server configuration gap, not an upstream failure — 503, not 502.
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "project workspace store is not configured")
		return
	}
	var req projectHydrateRequest
	if r.Body != nil {
		// An empty body is fine — hydrate from the default branch — but a
		// malformed one is a caller error, not "default branch".
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
			return
		}
	}
	resp, err := s.hydrateWorkspaceFromRepository(r.Context(), id, p, r, req.Ref)
	if err != nil {
		var validationErr *ValidationError
		if errors.As(err, &validationErr) {
			writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
			return
		}
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
