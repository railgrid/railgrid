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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

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

// hydrateProjectWorkspace is POST /api/projects/{project}/hydrate-workspace:
// read the project repository's text tree through the Code provider and write
// it into the workspace (existing files are overwritten; workspace-only files
// are left in place). A development sync follows so the running environment
// picks the tree up.
func (s *Server) hydrateProjectWorkspace(w http.ResponseWriter, r *http.Request) {
	c, id, p, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	_ = c
	if s.workspaces == nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "project workspace store is not configured")
		return
	}
	repositoryRef := ""
	if p.Spec.Repository != nil {
		repositoryRef = strings.TrimSpace(p.Spec.Repository.RepositoryRef)
	}
	if repositoryRef == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "project has no Code repository to hydrate from")
		return
	}
	if strings.TrimSpace(id.clusterID) == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "no workspace cluster on request — cannot address the tenant MCP endpoint")
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

	args := map[string]any{"repositoryRef": repositoryRef}
	if ref := strings.TrimSpace(req.Ref); ref != "" {
		args["ref"] = ref
	}
	raw, err := callProjectMCPTool(r.Context(), s.mcpEndpoint(id.clusterID), r, id.tenantPath, s.mcpInsecureSkipTLSVerify, projectToolCodeCheckoutRepository, args)
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", "checkout repository: "+err.Error())
		return
	}
	var checkout checkoutToolResult
	if err := json.Unmarshal([]byte(raw), &checkout); err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", "decode checkout result: "+err.Error())
		return
	}

	scope := projectWorkspaceScope(id, p.Name)
	resp := projectHydrateResponse{
		RepositoryRef: repositoryRef,
		Ref:           checkout.Ref,
		CommitSHA:     checkout.CommitSHA,
		Skipped:       checkout.Skipped,
	}
	for _, f := range checkout.Files {
		if _, err := s.workspaces.WriteFile(r.Context(), scope, workspace.WriteOptions{Path: f.Path, Content: f.Content}); err != nil {
			resp.Skipped = append(resp.Skipped, fmt.Sprintf("%s (workspace: %v)", f.Path, err))
			continue
		}
		resp.Written = append(resp.Written, f.Path)
	}

	// Push the hydrated tree into the live development environment.
	go s.syncDevelopmentAfterMutation(id, p.DeepCopy(), projectToolHydrateWorkspace)

	writeJSON(w, http.StatusOK, resp)
}
