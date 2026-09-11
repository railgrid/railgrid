/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

type projectRestoreRequest struct {
	CommitSHA              string  `json:"commitSHA"`
	ExpectedSourceRevision *uint64 `json:"expectedSourceRevision"`
}

type projectRestoreResponse struct {
	CommitSHA      string   `json:"commitSHA"`
	Written        []string `json:"written"`
	Deleted        []string `json:"deleted"`
	SourceRevision uint64   `json:"sourceRevision"`
	// Skipped lists repository paths the checkout could not return (too
	// large, or binaries on a Code provider without base64 support). Their
	// current workspace copies are left as they are.
	Skipped []string `json:"skipped,omitempty"`
}

// restoreProjectWorkspace is POST /api/projects/{project}/restore-workspace.
// Unlike hydration, restore replaces the managed workspace tree exactly, except
// that files the checkout skipped keep their workspace copies (and nothing is
// deleted when the checkout could not list every skipped path). It does not
// move the repository branch, create a commit, or change production.
func (s *Server) restoreProjectWorkspace(w http.ResponseWriter, r *http.Request) {
	c, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	release, ok := s.reserveProjectExternalOperation(w, r.Context(), id, project, "restoring project files")
	if !ok {
		return
	}
	defer release()

	if s.workspaces == nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "project workspace store is not configured")
		return
	}
	var req projectRestoreRequest
	if r.Body == nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "commitSHA is required")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	req.CommitSHA = strings.TrimSpace(req.CommitSHA)
	if req.ExpectedSourceRevision == nil || *req.ExpectedSourceRevision == 0 {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "expectedSourceRevision is required")
		return
	}
	repositoryRef := projectLinkedRepositoryRef(project)
	if _, err := projectRepositoryCommitForSHA(r.Context(), c, repositoryRef, req.CommitSHA); err != nil {
		var validationErr *ValidationError
		if errors.As(err, &validationErr) {
			writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
			return
		}
		writeProjectError(w, err)
		return
	}
	if strings.TrimSpace(id.clusterID) == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "no workspace cluster on request — cannot address the tenant MCP endpoint")
		return
	}

	scope := projectWorkspaceScope(id, project)
	currentRevision, err := s.workspaces.SourceRevision(r.Context(), scope)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "read workspace source revision: "+err.Error())
		return
	}
	if currentRevision != *req.ExpectedSourceRevision {
		writeStatus(w, http.StatusConflict, "Conflict", "project files changed since History was loaded; refresh History and try again")
		return
	}
	checkout, err := s.checkoutProjectRepository(r, id, repositoryRef, req.CommitSHA)
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
	files, err := exactRestoreFiles(req.CommitSHA, checkout)
	if err != nil {
		writeStatus(w, http.StatusBadGateway, "BadGateway", err.Error())
		return
	}
	preservePaths, skipListComplete := checkoutSkippedPaths(checkout.Skipped)
	result, err := s.workspaces.ReplaceTree(r.Context(), scope, workspace.ReplaceTreeOptions{
		Files:                  files,
		ExpectedSourceRevision: req.ExpectedSourceRevision,
		PreservePaths:          preservePaths,
		PreserveOmitted:        !skipListComplete,
	})
	if err != nil {
		if errors.Is(err, workspace.ErrSourceRevisionConflict) || errors.Is(err, workspace.ErrMutationConflict) {
			writeStatus(w, http.StatusConflict, "Conflict", "project files changed while the selected commit was loading; refresh History and try again")
			return
		}
		writeStatus(w, http.StatusInternalServerError, "InternalError", "replace project files: "+err.Error())
		return
	}

	// Keep development synchronization ordered with assistant mutations. No
	// production resource participates in this source-only operation.
	s.scheduleDevelopmentSyncAfterMutation(id, project, projectActionRestoreWorkspace)
	writeJSON(w, http.StatusOK, projectRestoreResponse{
		CommitSHA:      req.CommitSHA,
		Written:        result.Written,
		Deleted:        result.Deleted,
		SourceRevision: result.SourceRevision,
		Skipped:        checkout.Skipped,
	})
}

func (s *Server) checkoutProjectRepository(r *http.Request, id identity, repositoryRef, commitSHA string) (checkoutToolResult, error) {
	raw, err := callProjectMCPTool(
		r.Context(),
		s.mcpEndpoint(id.clusterID),
		r,
		id.tenantPath,
		s.mcpInsecureSkipTLSVerify,
		projectToolCodeCheckoutRepository,
		s.checkoutArgs(r.Context(), r, id, map[string]any{"repositoryRef": repositoryRef, "ref": commitSHA}),
	)
	if err != nil {
		return checkoutToolResult{}, fmt.Errorf("checkout repository at commit %s: %w", commitSHA, err)
	}
	var checkout checkoutToolResult
	if err := json.Unmarshal([]byte(raw), &checkout); err != nil {
		return checkoutToolResult{}, fmt.Errorf("decode checkout result: %w", err)
	}
	return checkout, nil
}

// checkoutSkipReasons are the suffixes the Code provider's checkout appends to
// every skipped repository path, e.g. "public/logo.png (binary)" (see the Code
// provider's backend/github/checkout.go and mcpserver/tools_checkout.go).
var checkoutSkipReasons = []string{" (binary)", " (file too large)", " (file-count cap)", " (total-size cap)"}

// checkoutSkippedPaths extracts the repository paths from checkout skip
// entries, which carry a reason suffix and so never match a workspace path
// as-is. complete is false when some entry names no path — the host truncated
// the tree, the provider capped the list ("(more paths skipped)"), or the entry
// has an unknown shape — so an omitted file may still exist at the commit and
// restore must not delete any.
func checkoutSkippedPaths(skipped []string) (paths []string, complete bool) {
	complete = true
	for _, entry := range skipped {
		path, ok := checkoutSkippedPath(entry)
		if !ok {
			complete = false
			continue
		}
		paths = append(paths, path)
	}
	return paths, complete
}

func checkoutSkippedPath(entry string) (string, bool) {
	for _, reason := range checkoutSkipReasons {
		if path, ok := strings.CutSuffix(entry, reason); ok && strings.TrimSpace(path) != "" {
			return path, true
		}
	}
	return "", false
}

// exactRestoreFiles validates that checkout returned the exact Git object
// requested by History, and decodes its files (base64 binaries included),
// before any workspace mutation is attempted. Paths the checkout skipped are
// not an error: the restore preserves their current workspace copies.
func exactRestoreFiles(requestedSHA string, checkout checkoutToolResult) ([]workspace.File, error) {
	requestedSHA = strings.TrimSpace(requestedSHA)
	returnedSHA := strings.TrimSpace(checkout.CommitSHA)
	if returnedSHA != requestedSHA {
		return nil, fmt.Errorf("checkout returned commit %q instead of requested commit %q", returnedSHA, requestedSHA)
	}
	files := make([]workspace.File, 0, len(checkout.Files))
	for _, file := range checkout.Files {
		data, err := file.bytes()
		if err != nil {
			return nil, fmt.Errorf("checkout file %q: %w", file.Path, err)
		}
		files = append(files, workspace.File{Path: file.Path, Content: string(data)})
	}
	return files, nil
}
