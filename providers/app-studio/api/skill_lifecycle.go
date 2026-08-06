// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gorilla/mux"
	"gopkg.in/yaml.v3"

	appskills "github.com/faroshq/provider-app-studio/skills"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectSkillMaxPackageNameBytes = 256
	projectSkillMaxResourceCount    = 64
	projectSkillMaxResourceBytes    = 64 << 10
	projectSkillMaxAggregateBytes   = 4 << 20
	projectSkillMaxRequestBytes     = 5 << 20
)

type projectAssistantSkillResourceInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Digest  string `json:"digest,omitempty"`
	Size    int64  `json:"size,omitempty"`
}

type projectAssistantSkillMutationRequest struct {
	PackageName    string                               `json:"packageName"`
	Name           string                               `json:"name"`
	Description    string                               `json:"description"`
	Instructions   string                               `json:"instructions"`
	Resources      []projectAssistantSkillResourceInput `json:"resources,omitempty"`
	ExpectedDigest string                               `json:"expectedDigest,omitempty"`
	Format         string                               `json:"format,omitempty"`
	Files          []projectAssistantSkillResourceInput `json:"files,omitempty"`
}

type projectAssistantSkillActivationRequest struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

type projectAssistantSkillResourceView struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Digest  string `json:"digest,omitempty"`
	Content string `json:"content,omitempty"`
}

type projectAssistantSkillDetail struct {
	projectAssistantSkillView
	Instructions string                              `json:"instructions"`
	Resources    []projectAssistantSkillResourceView `json:"resources,omitempty"`
}

type projectAssistantSkillExport struct {
	Format      string                              `json:"format"`
	PackageName string                              `json:"packageName"`
	Digest      string                              `json:"digest"`
	Files       []projectAssistantSkillResourceView `json:"files"`
	Filename    string                              `json:"filename"`
	Content     string                              `json:"content"`
	Package     projectAssistantSkillPackage        `json:"package"`
}

type projectAssistantSkillPackage struct {
	PackageName  string                               `json:"packageName"`
	Name         string                               `json:"name"`
	Description  string                               `json:"description"`
	Instructions string                               `json:"instructions"`
	Resources    []projectAssistantSkillResourceInput `json:"resources"`
}

func (s *Server) getProjectAssistantSkillDetail(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if !s.requireProjectSkillWorkspace(w) {
		return
	}
	packageName, err := validateProjectSkillPackageName(mux.Vars(r)["packageName"])
	if err != nil {
		writeProjectError(w, err)
		return
	}
	snapshot, entry, ok := s.projectSkillProjectEntry(w, r, projectWorkspaceScope(id, project), packageName)
	if !ok {
		return
	}
	detail, err := projectAssistantSkillDetailFromSnapshot(r.Context(), snapshot, entry)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	projectAssistantSkillMetric("catalog", "detail")
	writeJSON(w, http.StatusOK, detail)
}

// getProjectAssistantSkillDetailByID serves author-visible instructions for
// either a bundled or project skill. The catalog list intentionally omits
// instruction bodies; this bounded query endpoint resolves only an exact
// canonical qualified ID and returns resource metadata without resource
// contents.
func (s *Server) getProjectAssistantSkillDetailByID(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if !s.requireProjectSkillWorkspace(w) {
		return
	}
	skillID := strings.TrimSpace(r.URL.Query().Get("id"))
	if len([]byte(skillID)) > 512 || skillID == "" {
		projectAssistantSkillMetric("catalog", "invalid")
		writeProjectError(w, newValidationError("id is required and must be bounded"))
		return
	}
	snapshot, err := s.projectAssistantSkillCatalogSnapshot(r.Context(), projectWorkspaceScope(id, project))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	entry, err := snapshot.Get(skillID)
	if err != nil || entry.QualifiedName != skillID {
		projectAssistantSkillMetric("catalog", "not_found")
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant skill qualified ID not found")
		return
	}
	detail := projectAssistantSkillDetailWithoutResourceContents(entry)
	projectAssistantSkillMetric("catalog", "detail")
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) createProjectAssistantSkill(w http.ResponseWriter, r *http.Request) {
	s.createOrImportProjectAssistantSkill(w, r, false)
}

func (s *Server) importProjectAssistantSkill(w http.ResponseWriter, r *http.Request) {
	s.createOrImportProjectAssistantSkill(w, r, true)
}

func (s *Server) createOrImportProjectAssistantSkill(w http.ResponseWriter, r *http.Request, importing bool) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if !s.requireProjectSkillWorkspace(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, projectSkillMaxRequestBytes)
	var request projectAssistantSkillMutationRequest
	if !decodeStrictJSON(w, r, &request) {
		projectAssistantSkillMetric("lifecycle", "invalid")
		return
	}
	if err := normalizeProjectSkillImportRequest(&request); err != nil {
		projectAssistantSkillMetric("lifecycle", "invalid")
		writeProjectError(w, err)
		return
	}
	packageName, skillDocument, resourceFiles, err := validateProjectSkillMutation(request)
	if err != nil {
		projectAssistantSkillMetric("lifecycle", "invalid")
		writeProjectError(w, err)
		return
	}
	scope := projectWorkspaceScope(id, project)
	pathPrefix := path.Join(appskills.ProjectSkillsRoot, packageName)
	changes := make([]workspace.ManagedFileChange, 0, len(resourceFiles)+2)
	changes = append(changes, workspace.ManagedFileChange{Path: path.Join(pathPrefix, "SKILL.md"), Operation: workspace.ManagedFileCreate, Content: string(skillDocument)})
	for _, resource := range resourceFiles {
		changes = append(changes, workspace.ManagedFileChange{Path: path.Join(pathPrefix, resource.Path), Operation: workspace.ManagedFileCreate, Content: resource.Content})
	}
	metadataChange, err := s.projectSkillMetadataChange(r.Context(), scope, packageName, appskills.Activation{Enabled: true}, false)
	if err != nil {
		projectAssistantSkillMetric("lifecycle", "invalid")
		writeProjectSkillError(w, err)
		return
	}
	changes = append(changes, metadataChange)
	if _, err := s.workspaces.ApplyManagedTransaction(r.Context(), scope, changes); err != nil {
		projectAssistantSkillMetric("lifecycle", "rollback")
		writeProjectSkillError(w, err)
		return
	}
	entrySnapshot, entry, entryOK := s.projectSkillProjectEntry(w, r, scope, packageName)
	if !entryOK {
		return
	}
	detail, detailErr := projectAssistantSkillDetailFromSnapshot(r.Context(), entrySnapshot, entry)
	if detailErr != nil {
		writeProjectError(w, detailErr)
		return
	}
	projectAssistantSkillMetric("lifecycle", map[bool]string{true: "import", false: "create"}[importing])
	writeJSON(w, http.StatusCreated, detail)
}

func (s *Server) updateProjectAssistantSkill(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if !s.requireProjectSkillWorkspace(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, projectSkillMaxRequestBytes)
	var request projectAssistantSkillMutationRequest
	if !decodeStrictJSON(w, r, &request) {
		projectAssistantSkillMetric("lifecycle", "invalid")
		return
	}
	if err := normalizeProjectSkillImportRequest(&request); err != nil {
		projectAssistantSkillMetric("lifecycle", "invalid")
		writeProjectError(w, err)
		return
	}
	packageName, skillDocument, resourceFiles, err := validateProjectSkillMutation(request)
	if err != nil {
		projectAssistantSkillMetric("lifecycle", "invalid")
		writeProjectError(w, err)
		return
	}
	routePackageName, routeErr := validateProjectSkillPackageName(mux.Vars(r)["packageName"])
	if routeErr != nil || packageName != routePackageName {
		projectAssistantSkillMetric("lifecycle", "invalid")
		writeProjectError(w, newValidationError("packageName must match the project skill path"))
		return
	}
	scope := projectWorkspaceScope(id, project)
	snapshot, entry, ok := s.projectSkillProjectEntry(w, r, scope, packageName)
	if !ok {
		return
	}
	if strings.TrimSpace(request.ExpectedDigest) == "" || request.ExpectedDigest != entry.Digest {
		projectAssistantSkillMetric("lifecycle", "stale")
		writeStatus(w, http.StatusConflict, "Conflict", "skill package digest is stale; reload before updating")
		return
	}
	pathPrefix := path.Join(appskills.ProjectSkillsRoot, packageName)
	currentSkill, err := s.workspaces.ReadFile(r.Context(), scope, workspace.ReadOptions{Path: path.Join(pathPrefix, "SKILL.md"), MaxBytes: workspace.MaxWriteBytes})
	if err != nil || currentSkill.Version == "" {
		projectAssistantSkillMetric("lifecycle", "not_found")
		writeStatus(w, http.StatusNotFound, "NotFound", "project skill package not found")
		return
	}
	changes := []workspace.ManagedFileChange{{Path: path.Join(pathPrefix, "SKILL.md"), Operation: workspace.ManagedFileReplace, Content: string(skillDocument), ExpectedVersion: currentSkill.Version}}
	for _, resource := range resourceFiles {
		resourcePath := path.Join(pathPrefix, resource.Path)
		current, readErr := s.workspaces.ReadFile(r.Context(), scope, workspace.ReadOptions{Path: resourcePath, MaxBytes: workspace.MaxWriteBytes})
		if errors.Is(readErr, fs.ErrNotExist) {
			changes = append(changes, workspace.ManagedFileChange{Path: resourcePath, Operation: workspace.ManagedFileCreate, Content: resource.Content})
			continue
		}
		if readErr != nil || current.Version == "" {
			if readErr != nil {
				writeProjectSkillError(w, readErr)
			} else {
				writeStatus(w, http.StatusBadRequest, "BadRequest", "project skill resource is binary or too large")
			}
			return
		}
		changes = append(changes, workspace.ManagedFileChange{Path: resourcePath, Operation: workspace.ManagedFileReplace, Content: resource.Content, ExpectedVersion: current.Version})
	}
	wantedResources := make(map[string]struct{}, len(resourceFiles))
	for _, resource := range resourceFiles {
		wantedResources[resource.Path] = struct{}{}
	}
	for _, resource := range entry.Resources {
		if _, keep := wantedResources[resource.Path]; keep {
			continue
		}
		resourcePath := path.Join(pathPrefix, resource.Path)
		current, readErr := s.workspaces.ReadFile(r.Context(), scope, workspace.ReadOptions{Path: resourcePath, MaxBytes: workspace.MaxWriteBytes})
		if errors.Is(readErr, fs.ErrNotExist) {
			projectAssistantSkillMetric("lifecycle", "not_found")
			writeProjectSkillError(w, readErr)
			return
		}
		if readErr != nil || current.Version == "" {
			projectAssistantSkillMetric("lifecycle", "stale")
			if readErr != nil {
				writeProjectSkillError(w, readErr)
			} else {
				writeStatus(w, http.StatusNotFound, "NotFound", "project skill resource is not readable")
			}
			return
		}
		changes = append(changes, workspace.ManagedFileChange{Path: resourcePath, Operation: workspace.ManagedFileDelete, ExpectedVersion: current.Version})
	}
	metadataChange, err := s.projectSkillMetadataChange(r.Context(), scope, packageName, appskills.Activation{Enabled: entry.Enabled, Version: entry.Version, Digest: entry.Digest}, false)
	if err != nil {
		projectAssistantSkillMetric("lifecycle", "invalid")
		writeProjectSkillError(w, err)
		return
	}
	changes = append(changes, metadataChange)
	if _, err := s.workspaces.ApplyManagedTransaction(r.Context(), scope, changes); err != nil {
		projectAssistantSkillMetric("lifecycle", "rollback")
		writeProjectSkillError(w, err)
		return
	}
	newSnapshot, newEntry, ok := s.projectSkillProjectEntry(w, r, scope, packageName)
	if !ok {
		return
	}
	detail, err := projectAssistantSkillDetailFromSnapshot(r.Context(), newSnapshot, newEntry)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	projectAssistantSkillMetric("lifecycle", "update")
	writeJSON(w, http.StatusOK, detail)
	_ = snapshot
}

func (s *Server) setProjectAssistantSkillActivation(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if !s.requireProjectSkillWorkspace(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var request projectAssistantSkillActivationRequest
	if !decodeStrictJSON(w, r, &request) {
		projectAssistantSkillMetric("lifecycle", "invalid")
		return
	}
	if len([]byte(strings.TrimSpace(request.ID))) > 512 || strings.TrimSpace(request.ID) == "" {
		projectAssistantSkillMetric("lifecycle", "invalid")
		writeProjectError(w, newValidationError("id is required and must be bounded"))
		return
	}
	skillID := strings.TrimSpace(request.ID)
	scope := projectWorkspaceScope(id, project)
	snapshot, err := s.projectAssistantSkillCatalogSnapshot(r.Context(), scope)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	entry, err := snapshot.Get(skillID)
	if err != nil || entry.QualifiedName != skillID || (entry.Scope != appskills.ScopeSystem && entry.Scope != appskills.ScopeProject) || (entry.Scope == appskills.ScopeProject && !entry.Editable) {
		projectAssistantSkillMetric("lifecycle", "forbidden")
		writeStatus(w, http.StatusForbidden, "Forbidden", "skill activation requires an exact system or project qualified ID")
		return
	}
	if err := s.updateProjectSkillActivationForScope(r.Context(), scope, entry.Scope, entry.PackagePath, appskills.Activation{Enabled: request.Enabled, Version: entry.Version, Digest: entry.Digest}); err != nil {
		projectAssistantSkillMetric("lifecycle", "conflict")
		writeProjectSkillError(w, err)
		return
	}
	updated, detailErr := s.projectAssistantSkillCatalogSnapshot(r.Context(), scope)
	if detailErr != nil {
		writeProjectError(w, detailErr)
		return
	}
	updatedEntry, detailErr := updated.Get(skillID)
	if detailErr != nil || updatedEntry.QualifiedName != skillID {
		writeStatus(w, http.StatusNotFound, "NotFound", "assistant skill qualified ID not found after activation")
		return
	}
	var detail projectAssistantSkillDetail
	if updatedEntry.Scope == appskills.ScopeSystem {
		detail = projectAssistantSkillDetailWithoutResourceContents(updatedEntry)
	} else {
		detail, detailErr = projectAssistantSkillDetailFromSnapshot(r.Context(), updated, updatedEntry)
		if detailErr != nil {
			writeProjectError(w, detailErr)
			return
		}
	}
	projectAssistantSkillMetric("lifecycle", "activate")
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) deleteProjectAssistantSkill(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if !s.requireProjectSkillWorkspace(w) {
		return
	}
	packageName, err := validateProjectSkillPackageName(mux.Vars(r)["packageName"])
	if err != nil {
		writeProjectError(w, err)
		return
	}
	expectedDigest := strings.TrimSpace(r.URL.Query().Get("expectedDigest"))
	if expectedDigest == "" {
		projectAssistantSkillMetric("lifecycle", "invalid")
		writeProjectError(w, newValidationError("expectedDigest is required"))
		return
	}
	scope := projectWorkspaceScope(id, project)
	snapshot, entry, ok := s.projectSkillProjectEntry(w, r, scope, packageName)
	if !ok {
		return
	}
	if !entry.Editable {
		projectAssistantSkillMetric("lifecycle", "forbidden")
		writeStatus(w, http.StatusForbidden, "Forbidden", "bundled skills are read-only")
		return
	}
	if expectedDigest != entry.Digest {
		projectAssistantSkillMetric("lifecycle", "stale")
		writeStatus(w, http.StatusConflict, "Conflict", "skill package digest is stale; reload before deleting")
		return
	}
	files := projectSkillPackageFiles(snapshot, entry)
	changes := make([]workspace.ManagedFileChange, 0, len(files)+1)
	for _, filePath := range files {
		file, readErr := s.workspaces.ReadFile(r.Context(), scope, workspace.ReadOptions{Path: filePath, MaxBytes: workspace.MaxWriteBytes})
		if readErr != nil || file.Version == "" {
			if readErr != nil {
				writeProjectSkillError(w, readErr)
			} else {
				writeStatus(w, http.StatusNotFound, "NotFound", "project skill package file is not readable")
			}
			return
		}
		changes = append(changes, workspace.ManagedFileChange{Path: filePath, Operation: workspace.ManagedFileDelete, ExpectedVersion: file.Version})
	}
	metadataChange, err := s.projectSkillMetadataChange(r.Context(), scope, packageName, appskills.Activation{}, true)
	if err != nil {
		writeProjectSkillError(w, err)
		return
	}
	changes = append(changes, metadataChange)
	if _, err := s.workspaces.ApplyManagedTransaction(r.Context(), scope, changes); err != nil {
		projectAssistantSkillMetric("lifecycle", "rollback")
		writeProjectSkillError(w, err)
		return
	}
	projectAssistantSkillMetric("lifecycle", "delete")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) exportProjectAssistantSkill(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if !s.requireProjectSkillWorkspace(w) {
		return
	}
	packageName, err := validateProjectSkillPackageName(mux.Vars(r)["packageName"])
	if err != nil {
		writeProjectError(w, err)
		return
	}
	snapshot, entry, ok := s.projectSkillProjectEntry(w, r, projectWorkspaceScope(id, project), packageName)
	if !ok {
		return
	}
	document, err := renderProjectSkillDocument(entry.Name, entry.Description, entry.Content)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	files := []projectAssistantSkillResourceView{{Path: "SKILL.md", Size: int64(len(document)), Content: string(document)}}
	for _, resource := range entry.Resources {
		read, readErr := snapshot.ReadResource(r.Context(), entry.QualifiedName, resource.Path, appskills.ResourceReadOptions{})
		if readErr != nil {
			writeProjectError(w, readErr)
			return
		}
		files = append(files, projectAssistantSkillResourceView{Path: resource.Path, Size: read.Size, Digest: resource.Digest, Content: string(read.Content)})
	}
	packageValue := projectAssistantSkillPackage{PackageName: entry.PackagePath, Name: entry.Name, Description: entry.Description, Instructions: entry.Content}
	for _, file := range files {
		if file.Path == "SKILL.md" {
			continue
		}
		packageValue.Resources = append(packageValue.Resources, projectAssistantSkillResourceInput{Path: file.Path, Content: file.Content})
	}
	content, marshalErr := json.MarshalIndent(packageValue, "", "  ")
	if marshalErr != nil {
		writeProjectError(w, marshalErr)
		return
	}
	projectAssistantSkillMetric("lifecycle", "export")
	filename := strings.ReplaceAll(entry.PackagePath, "/", "-")
	writeJSON(w, http.StatusOK, projectAssistantSkillExport{Format: "kedge.skill.v1", PackageName: entry.PackagePath, Digest: entry.Digest, Files: files, Filename: filename + ".json", Content: string(content), Package: packageValue})
}

func (s *Server) projectSkillProjectEntry(w http.ResponseWriter, r *http.Request, scope workspace.Scope, packageName string) (appskills.Snapshot, appskills.Entry, bool) {
	snapshot, err := s.projectAssistantSkillCatalogSnapshot(r.Context(), scope)
	if err != nil {
		writeProjectError(w, err)
		return appskills.Snapshot{}, appskills.Entry{}, false
	}
	for _, entry := range snapshot.Entries {
		if entry.Scope == appskills.ScopeProject && entry.PackagePath == packageName {
			return snapshot, entry, true
		}
	}
	writeStatus(w, http.StatusNotFound, "NotFound", "project skill package not found")
	projectAssistantSkillMetric("catalog", "not_found")
	return appskills.Snapshot{}, appskills.Entry{}, false
}

func projectAssistantSkillDetailFromSnapshot(ctx context.Context, snapshot appskills.Snapshot, entry appskills.Entry) (projectAssistantSkillDetail, error) {
	resourceViews := make([]projectAssistantSkillResourceView, 0, len(entry.Resources))
	for _, resource := range entry.Resources {
		resourceViews = append(resourceViews, projectAssistantSkillResourceView{Path: resource.Path, Size: resource.Size, Digest: resource.Digest})
	}
	detail := projectAssistantSkillDetail{projectAssistantSkillView: projectAssistantSkillView{ID: entry.QualifiedName, Name: entry.Name, Description: entry.Description, Scope: entry.Scope, PackagePath: entry.PackagePath, PackageName: entry.PackagePath, Enabled: entry.Enabled, Editable: entry.Editable, Version: entry.Version, Digest: entry.Digest, ContentDigest: entry.ContentDigest, Resources: resourceViews}, Instructions: entry.Content}
	for _, resource := range entry.Resources {
		read, err := snapshot.ReadResource(ctx, entry.QualifiedName, resource.Path, appskills.ResourceReadOptions{})
		if err != nil {
			return projectAssistantSkillDetail{}, err
		}
		detail.Resources = append(detail.Resources, projectAssistantSkillResourceView{Path: resource.Path, Size: read.Size, Digest: resource.Digest, Content: string(read.Content)})
	}
	return detail, nil
}

func projectAssistantSkillDetailWithoutResourceContents(entry appskills.Entry) projectAssistantSkillDetail {
	resourceViews := make([]projectAssistantSkillResourceView, 0, len(entry.Resources))
	for _, resource := range entry.Resources {
		resourceViews = append(resourceViews, projectAssistantSkillResourceView{Path: resource.Path, Size: resource.Size, Digest: resource.Digest})
	}
	return projectAssistantSkillDetail{
		projectAssistantSkillView: projectAssistantSkillView{
			ID:            entry.QualifiedName,
			Name:          entry.Name,
			Description:   entry.Description,
			Scope:         entry.Scope,
			PackagePath:   entry.PackagePath,
			PackageName:   entry.PackagePath,
			Enabled:       entry.Enabled,
			Editable:      entry.Editable,
			Version:       entry.Version,
			Digest:        entry.Digest,
			ContentDigest: entry.ContentDigest,
			Resources:     resourceViews,
		},
		Instructions: entry.Content,
		Resources:    resourceViews,
	}
}

func validateProjectSkillPackageName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len([]byte(raw)) > projectSkillMaxPackageNameBytes {
		return "", newValidationError("packageName is too large")
	}
	clean, err := appskills.ValidatePackagePath(raw)
	if err != nil || clean != raw || !projectSkillPathSafe(clean) || clean == "SKILL.md" || strings.Contains(clean, "/SKILL.md") || strings.Contains(clean, "/.kedge-") || strings.HasPrefix(clean, ".kedge-") {
		return "", newValidationError("packageName must be a clean project-relative package identity")
	}
	return clean, nil
}

func validateProjectSkillMutation(request projectAssistantSkillMutationRequest) (string, []byte, []projectAssistantSkillResourceInput, error) {
	packageName, err := validateProjectSkillPackageName(request.PackageName)
	if err != nil {
		return "", nil, nil, err
	}
	if len(request.Resources) > projectSkillMaxResourceCount {
		return "", nil, nil, newValidationError("too many supporting resources")
	}
	if len([]byte(request.Instructions)) > appskills.DefaultMaxSkillBytes {
		return "", nil, nil, newValidationError("instructions are too large")
	}
	if !utf8.ValidString(request.Instructions) || strings.ContainsRune(request.Instructions, '\x00') {
		return "", nil, nil, newValidationError("instructions must be valid UTF-8 text without NUL bytes")
	}
	document, err := renderProjectSkillDocument(request.Name, request.Description, request.Instructions)
	if err != nil {
		return "", nil, nil, newValidationError("skill frontmatter is invalid")
	}
	if _, err := appskills.ParseSkill(document, appskills.DefaultLimits()); err != nil {
		return "", nil, nil, newValidationError("skill frontmatter or instructions are invalid: " + err.Error())
	}
	total := len(document)
	seen := make(map[string]struct{}, len(request.Resources))
	resources := make([]projectAssistantSkillResourceInput, 0, len(request.Resources))
	for _, resource := range request.Resources {
		resourcePath, pathErr := appskills.ValidateResourcePath(resource.Path)
		if pathErr != nil || !projectSkillPathSafe(resourcePath) || resourcePath == "SKILL.md" {
			return "", nil, nil, newValidationError("supporting resource path is invalid")
		}
		if _, exists := seen[resourcePath]; exists {
			return "", nil, nil, newValidationError("supporting resource paths must be unique")
		}
		seen[resourcePath] = struct{}{}
		if len([]byte(resource.Content)) > projectSkillMaxResourceBytes || !utf8.ValidString(resource.Content) || strings.ContainsRune(resource.Content, '\x00') {
			return "", nil, nil, newValidationError("supporting resource content is too large or invalid")
		}
		total += len([]byte(resource.Content))
		if total > projectSkillMaxAggregateBytes {
			return "", nil, nil, newValidationError("skill package exceeds the aggregate size bound")
		}
		resources = append(resources, projectAssistantSkillResourceInput{Path: resourcePath, Content: resource.Content})
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].Path < resources[j].Path })
	return packageName, document, resources, nil
}

func normalizeProjectSkillImportRequest(request *projectAssistantSkillMutationRequest) error {
	if request == nil || len(request.Files) == 0 {
		return nil
	}
	if request.Format != "" && request.Format != "kedge.skill.v1" {
		return newValidationError("unsupported skill export format")
	}
	var document string
	resources := make([]projectAssistantSkillResourceInput, 0, len(request.Files))
	for _, file := range request.Files {
		if file.Path == "SKILL.md" {
			if document != "" {
				return newValidationError("skill import contains duplicate SKILL.md files")
			}
			document = file.Content
			continue
		}
		resources = append(resources, file)
	}
	if document == "" {
		return newValidationError("skill import requires SKILL.md")
	}
	parsed, err := appskills.ParseSkill([]byte(document), appskills.DefaultLimits())
	if err != nil {
		return newValidationError("skill import SKILL.md is invalid: " + err.Error())
	}
	request.Name = parsed.Name
	request.Description = parsed.Description
	request.Instructions = parsed.Content
	request.Resources = resources
	return nil
}

func projectSkillPathSafe(clean string) bool {
	for _, segment := range strings.Split(clean, "/") {
		lower := strings.ToLower(segment)
		if lower == ".git" || lower == "node_modules" || lower == ".workspace-snapshots" || strings.HasPrefix(lower, ".workspace-write-") {
			return false
		}
	}
	return true
}

func renderProjectSkillDocument(name, description, instructions string) ([]byte, error) {
	frontmatter, err := yaml.Marshal(struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}{Name: name, Description: description})
	if err != nil {
		return nil, err
	}
	document := append([]byte("---\n"), frontmatter...)
	document = append(document, []byte("---\n")...)
	document = append(document, []byte(instructions)...)
	return document, nil
}

func projectSkillPackageFiles(snapshot appskills.Snapshot, entry appskills.Entry) []string {
	paths := make([]string, 0, len(entry.Resources)+1)
	paths = append(paths, path.Join(appskills.ProjectSkillsRoot, entry.PackagePath, "SKILL.md"))
	for _, resource := range entry.Resources {
		paths = append(paths, path.Join(appskills.ProjectSkillsRoot, entry.PackagePath, resource.Path))
	}
	sort.Strings(paths)
	_ = snapshot
	return paths
}

func (s *Server) projectSkillMetadataChange(ctx context.Context, scope workspace.Scope, packageName string, activation appskills.Activation, remove bool) (workspace.ManagedFileChange, error) {
	return s.projectSkillMetadataChangeForScope(ctx, scope, appskills.ScopeProject, packageName, activation, remove)
}

func (s *Server) projectSkillMetadataChangeForScope(ctx context.Context, scope workspace.Scope, skillScope appskills.Scope, packageName string, activation appskills.Activation, remove bool) (workspace.ManagedFileChange, error) {
	if skillScope != appskills.ScopeProject && skillScope != appskills.ScopeSystem {
		return workspace.ManagedFileChange{}, newValidationError("unsupported skill activation scope")
	}
	metadata, version, err := appskills.ReadProjectMetadata(ctx, s.workspaces, scope)
	if err != nil {
		return workspace.ManagedFileChange{}, err
	}
	activations := metadata.Packages
	if skillScope == appskills.ScopeSystem {
		activations = metadata.System
	}
	if remove {
		delete(activations, packageName)
	} else {
		activations[packageName] = activation
	}
	raw, err := appskills.EncodeProjectMetadata(metadata)
	if err != nil {
		return workspace.ManagedFileChange{}, err
	}
	change := workspace.ManagedFileChange{Path: appskills.ProjectMetadataPath, Content: raw}
	if version == "" {
		change.Operation = workspace.ManagedFileCreate
	} else {
		change.Operation = workspace.ManagedFileReplace
		change.ExpectedVersion = version
	}
	return change, nil
}

func (s *Server) updateProjectSkillActivation(ctx context.Context, scope workspace.Scope, packageName string, activation appskills.Activation, remove ...bool) error {
	metadataChange, err := s.projectSkillMetadataChange(ctx, scope, packageName, activation, len(remove) > 0 && remove[0])
	if err != nil {
		return err
	}
	_, err = s.workspaces.ApplyManagedTransaction(ctx, scope, []workspace.ManagedFileChange{metadataChange})
	return err
}

func (s *Server) updateProjectSkillActivationForScope(ctx context.Context, scope workspace.Scope, skillScope appskills.Scope, packageName string, activation appskills.Activation) error {
	metadataChange, err := s.projectSkillMetadataChangeForScope(ctx, scope, skillScope, packageName, activation, false)
	if err != nil {
		return err
	}
	_, err = s.workspaces.ApplyManagedTransaction(ctx, scope, []workspace.ManagedFileChange{metadataChange})
	return err
}

func (s *Server) requireProjectSkillWorkspace(w http.ResponseWriter) bool {
	if s == nil || s.workspaces == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project skill workspace is not configured")
		return false
	}
	return true
}

func writeProjectSkillError(w http.ResponseWriter, err error) {
	var mutationErr *workspace.MutationError
	if errors.As(err, &mutationErr) {
		switch mutationErr.Code {
		case workspace.MutationErrorStale, workspace.MutationErrorConflict, workspace.MutationErrorTargetExists:
			writeStatus(w, http.StatusConflict, "Conflict", "project skill package changed; reload and retry")
			return
		case workspace.MutationErrorTargetNotFound:
			writeStatus(w, http.StatusNotFound, "NotFound", "project skill package not found")
			return
		case workspace.MutationErrorInvalid, workspace.MutationErrorVersionRequired:
			writeStatus(w, http.StatusBadRequest, "BadRequest", "project skill package mutation is invalid")
			return
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		writeStatus(w, http.StatusNotFound, "NotFound", "project skill package not found")
		return
	}
	writeProjectError(w, err)
}
