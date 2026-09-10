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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"

	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

// Workspace file access for the portal's code explorer. The workspace
// FileStore is the live development source — what the assistant edits and
// what development sync pushes into the sandbox — so browsing it shows
// exactly what the dev environment runs.
//
// Reads are token-gated through requireProjectWithClient (the caller must be
// able to GET the Project). Writes use the same gate as the other
// project-mutating workspace routes (hydrate-workspace, restore-workspace):
// requireProjectWithClient plus the project's external-operation reservation,
// so a write never races an active assistant run (409). Every write marks the
// changed paths uncommitted — the project reconciler commits them — and
// schedules a development sync, exactly like an assistant edit.

const (
	// projectFileUploadMaxFiles bounds one multipart upload.
	projectFileUploadMaxFiles = 100
	// projectFileUploadMaxBytes bounds one upload request's file bytes; it
	// matches the total a single repository commit or sync can carry.
	projectFileUploadMaxBytes = 48 << 20
	// projectFileMultipartOverhead leaves room for part headers/boundaries.
	projectFileMultipartOverhead = 1 << 20
	// projectFileUploadMemory is kept in memory while parsing; larger parts
	// spill to temporary files that are removed after the request.
	projectFileUploadMemory = 8 << 20
)

// projectFileContentResponse is GET files/content. Every field is always
// present: a binary file has content "" and a whole-file version.
type projectFileContentResponse struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Version   string `json:"version"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Size      int64  `json:"size"`
}

// projectFileWriteResult describes one written file.
type projectFileWriteResult struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Version string `json:"version"`
	Binary  bool   `json:"binary"`
}

// listProjectFiles is GET /api/projects/{project}/files — the whole workspace
// tree as a flat, sorted path list with sizes. The client builds the tree.
func (s *Server) listProjectFiles(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if s.workspaces == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project workspace store is not configured")
		return
	}
	list, err := s.workspaces.ListFiles(r.Context(), projectWorkspaceScope(id, project), workspace.ListOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// readProjectFile is GET /api/projects/{project}/files/content?path=... — one
// file's bounded content with its opaque version and binary/truncated flags.
func (s *Server) readProjectFile(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if s.workspaces == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project workspace store is not configured")
		return
	}
	filePath, ok := projectFilePathParam(w, r)
	if !ok {
		return
	}
	content, err := s.workspaces.ReadFile(r.Context(), projectWorkspaceScope(id, project), workspace.ReadOptions{
		Path:     filePath,
		MaxBytes: workspace.MaxReadMaxBytes,
	})
	if err != nil {
		writeProjectFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectFileContentResponse{
		Path:      content.Path,
		Content:   content.Content,
		Version:   content.Version,
		Binary:    content.Binary,
		Truncated: content.Truncated,
		Size:      content.Size,
	})
}

// readProjectFileRaw is GET /api/projects/{project}/files/raw?path=... — the
// file's raw bytes for previews and downloads. ETag is the quoted version;
// ?download=1 asks the browser to save rather than render.
func (s *Server) readProjectFileRaw(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	if s.workspaces == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project workspace store is not configured")
		return
	}
	filePath, ok := projectFilePathParam(w, r)
	if !ok {
		return
	}
	scope := projectWorkspaceScope(id, project)
	meta, err := s.workspaces.InspectFile(r.Context(), scope, filePath)
	if err != nil {
		writeProjectFileError(w, err)
		return
	}
	etag := `"` + meta.Version + `"`
	if match := strings.TrimSpace(r.Header.Get("If-None-Match")); match != "" && projectFileETagMatches(match, meta.Version) {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	f, info, err := s.workspaces.OpenFile(r.Context(), scope, filePath)
	if err != nil {
		writeProjectFileError(w, err)
		return
	}
	defer func() { _ = f.Close() }()
	if info.Size() != meta.Size {
		// The file changed between hashing and opening; let the client retry
		// rather than serve bytes that do not match the ETag.
		writeStatus(w, http.StatusConflict, "Conflict", "file changed while it was being read; retry")
		return
	}
	name := path.Base(meta.Path)
	header := w.Header()
	header.Set("Content-Type", projectFileRawContentType(name))
	header.Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	header.Set("ETag", etag)
	header.Set("Cache-Control", "private, no-cache")
	header.Set("X-Content-Type-Options", "nosniff")
	// User content is served from the App Studio origin: never let a
	// navigated-to file (HTML, SVG) run script or load anything.
	header.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	disposition := "inline"
	if download, _ := strconv.ParseBool(r.URL.Query().Get("download")); download {
		disposition = "attachment"
	}
	header.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// projectFileExtensionTypes fills common web-asset types missing from Go's
// built-in table (and from minimal container images' mime.types).
var projectFileExtensionTypes = map[string]string{
	".glb":   "model/gltf-binary",
	".gltf":  "model/gltf+json",
	".obj":   "model/obj",
	".stl":   "model/stl",
	".usdz":  "model/vnd.usdz+zip",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".ico":   "image/vnd.microsoft.icon",
	".mp3":   "audio/mpeg",
	".wav":   "audio/wav",
	".ogg":   "audio/ogg",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
	".zip":   "application/zip",
}

// projectFileTypeByExtension resolves a media type from a file extension.
func projectFileTypeByExtension(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if contentType, ok := projectFileExtensionTypes[ext]; ok {
		return contentType
	}
	return mime.TypeByExtension(ext)
}

// projectFileRawContentType picks a Content-Type from the extension.
// Active document types are served as plain text so a raw URL can never
// become a same-origin page.
func projectFileRawContentType(name string) string {
	contentType := projectFileTypeByExtension(name)
	if contentType == "" {
		return "application/octet-stream"
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "application/octet-stream"
	}
	switch mediaType {
	case "text/html", "application/xhtml+xml", "application/xml", "text/xml":
		return "text/plain; charset=utf-8"
	}
	return contentType
}

// writeProjectFile is PUT /api/projects/{project}/files/content?path=... with
// the raw file bytes as the body. If-None-Match: * creates only; If-Match
// replaces only an unchanged file; neither upserts. 201 created, 200 replaced.
func (s *Server) writeProjectFile(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	filePath, ok := projectFilePathParam(w, r)
	if !ok {
		return
	}
	createOnly := false
	if match := strings.TrimSpace(r.Header.Get("If-None-Match")); match != "" {
		if match != "*" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "If-None-Match supports only * (create only)")
			return
		}
		createOnly = true
	}
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if createOnly && ifMatch != "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "If-Match and If-None-Match cannot be combined")
		return
	}
	release, ok := s.beginProjectFileWrite(w, r, id, project)
	if !ok {
		return
	}
	defer release()

	r.Body = http.MaxBytesReader(w, r.Body, workspace.MaxBinaryWriteBytes+1)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", fmt.Sprintf("file exceeds the %d-byte binary limit", workspace.MaxBinaryWriteBytes))
			return
		}
		writeStatus(w, http.StatusBadRequest, "BadRequest", "read file body: "+err.Error())
		return
	}
	scope := projectWorkspaceScope(id, project)
	opts := workspace.PutOptions{Path: filePath, Data: data, CreateOnly: createOnly}
	if ifMatch == "*" {
		if exists, err := s.workspaces.FileExists(r.Context(), scope, filePath); err != nil {
			writeProjectFileError(w, err)
			return
		} else if !exists {
			writeStatus(w, http.StatusPreconditionFailed, "PreconditionFailed", "file does not exist")
			return
		}
	} else if ifMatch != "" {
		opts.ExpectedVersion = projectFileETagValue(ifMatch)
	}
	result, err := s.workspaces.PutFile(r.Context(), scope, opts)
	if err != nil {
		writeProjectFileWriteError(w, err, true)
		return
	}
	if result.Changed {
		s.recordProjectFileWrites(r.Context(), id, project, []string{result.Path})
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, projectFileWriteResult{Path: result.Path, Size: result.Size, Version: result.Version, Binary: result.Binary})
}

// deleteProjectFile is DELETE /api/projects/{project}/files/content?path=...
// with an optional If-Match version. 204 on success, 404 when missing.
func (s *Server) deleteProjectFile(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	filePath, ok := projectFilePathParam(w, r)
	if !ok {
		return
	}
	release, ok := s.beginProjectFileWrite(w, r, id, project)
	if !ok {
		return
	}
	defer release()
	scope := projectWorkspaceScope(id, project)
	expected := projectFileETagValue(strings.TrimSpace(r.Header.Get("If-Match")))
	if expected == "" || expected == "*" {
		meta, err := s.workspaces.InspectFile(r.Context(), scope, filePath)
		if err != nil {
			writeProjectFileError(w, err)
			return
		}
		expected = meta.Version
	}
	result, err := s.workspaces.DeleteFile(r.Context(), scope, workspace.DeleteOptions{Path: filePath, ExpectedVersion: expected})
	if err != nil {
		var mutationErr *workspace.MutationError
		if errors.As(err, &mutationErr) && mutationErr.Code == workspace.MutationErrorTargetNotFound {
			writeStatus(w, http.StatusNotFound, "NotFound", "file not found")
			return
		}
		writeProjectFileWriteError(w, err, true)
		return
	}
	s.recordProjectFileWrites(r.Context(), id, project, []string{result.Path})
	w.WriteHeader(http.StatusNoContent)
}

// uploadProjectFiles is POST /api/projects/{project}/files/upload: multipart
// with one or more "file" parts, an optional "dir" target directory ("" is
// the workspace root), and optional "overwrite=true". Without overwrite an
// existing target fails the whole upload with 409 before anything is written.
func (s *Server) uploadProjectFiles(w http.ResponseWriter, r *http.Request) {
	_, id, project, ok := s.requireProjectWithClient(w, r)
	if !ok {
		return
	}
	release, ok := s.beginProjectFileWrite(w, r, id, project)
	if !ok {
		return
	}
	defer release()

	r.Body = http.MaxBytesReader(w, r.Body, projectFileUploadMaxBytes+projectFileMultipartOverhead)
	if err := r.ParseMultipartForm(projectFileUploadMemory); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", fmt.Sprintf("upload exceeds the %d-byte request limit", projectFileUploadMaxBytes))
			return
		}
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid multipart upload: "+err.Error())
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "multipart field file is required")
		return
	}
	if len(headers) > projectFileUploadMaxFiles {
		writeStatus(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("upload contains more than %d files", projectFileUploadMaxFiles))
		return
	}
	dir := strings.Trim(strings.TrimSpace(r.FormValue("dir")), "/")
	overwrite := false
	if raw := strings.TrimSpace(r.FormValue("overwrite")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "overwrite must be true or false")
			return
		}
		overwrite = parsed
	}
	scope := projectWorkspaceScope(id, project)

	// Preflight every target before writing so a rejected upload changes
	// nothing: path validity, duplicates, per-file bounds, and existence.
	type plannedUpload struct {
		path   string
		header int
	}
	planned := make([]plannedUpload, 0, len(headers))
	seen := make(map[string]struct{}, len(headers))
	for index, header := range headers {
		name := strings.TrimSpace(strings.ReplaceAll(header.Filename, "\\", "/"))
		if name == "" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "every uploaded file needs a filename")
			return
		}
		target, err := workspace.CleanProjectPath(path.Join(dir, name))
		if err != nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
			return
		}
		if _, duplicate := seen[target]; duplicate {
			writeStatus(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("upload contains %q more than once", target))
			return
		}
		seen[target] = struct{}{}
		if header.Size > workspace.MaxBinaryWriteBytes {
			writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", fmt.Sprintf("file %q exceeds the %d-byte binary limit", target, workspace.MaxBinaryWriteBytes))
			return
		}
		if !overwrite {
			exists, err := s.workspaces.FileExists(r.Context(), scope, target)
			if err != nil {
				writeProjectFileError(w, err)
				return
			}
			if exists {
				writeStatus(w, http.StatusConflict, "Conflict", fmt.Sprintf("file %q already exists; upload with overwrite=true to replace it", target))
				return
			}
		}
		planned = append(planned, plannedUpload{path: target, header: index})
	}
	datas := make([][]byte, len(planned))
	for i, upload := range planned {
		data, err := readProjectUploadPart(headers[upload.header])
		if err != nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("read %q: %v", upload.path, err))
			return
		}
		if tooLarge := workspace.ValidateFileBytes(upload.path, data); tooLarge != nil {
			writeProjectFileWriteError(w, tooLarge, false)
			return
		}
		datas[i] = data
	}

	results := make([]projectFileWriteResult, 0, len(planned))
	changed := make([]string, 0, len(planned))
	var writeErr error
	for i, upload := range planned {
		result, err := s.workspaces.PutFile(r.Context(), scope, workspace.PutOptions{Path: upload.path, Data: datas[i], CreateOnly: !overwrite})
		if err != nil {
			writeErr = err
			break
		}
		if result.Changed {
			changed = append(changed, result.Path)
		}
		results = append(results, projectFileWriteResult{Path: result.Path, Size: result.Size, Version: result.Version, Binary: result.Binary})
	}
	// Files already written stay written (each write is atomic); record them
	// before reporting a later failure so they still commit and sync.
	if len(changed) > 0 {
		s.recordProjectFileWrites(r.Context(), id, project, changed)
	}
	if writeErr != nil {
		writeProjectFileWriteError(w, writeErr, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": results})
}

func readProjectUploadPart(header *multipart.FileHeader) ([]byte, error) {
	file, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, workspace.MaxBinaryWriteBytes+1))
}

// beginProjectFileWrite applies the shared write gate: a configured store,
// a live Project, and the external-operation reservation (409 while an
// assistant run owns the project). The returned release must be deferred.
func (s *Server) beginProjectFileWrite(w http.ResponseWriter, r *http.Request, id identity, project *aiv1alpha1.Project) (func(), bool) {
	if s.workspaces == nil {
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", "project workspace store is not configured")
		return nil, false
	}
	if project.DeletionTimestamp != nil {
		writeStatus(w, http.StatusConflict, "Conflict", "project is being deleted")
		return nil, false
	}
	return s.reserveProjectExternalOperation(w, r.Context(), id, project, "changing project files")
}

// recordProjectFileWrites marks written paths uncommitted and schedules a
// development sync — the same bookkeeping an assistant edit gets.
func (s *Server) recordProjectFileWrites(ctx context.Context, id identity, project *aiv1alpha1.Project, paths []string) {
	if _, err := s.workspaces.AddUncommittedPaths(ctx, projectWorkspaceScope(id, project), paths); err != nil {
		// The bytes are already durable; the next assistant commit repairs
		// the dirty set from its own ledger, and sync still runs below.
		klog.Warningf("app-studio project %s: record uncommitted paths after file write: %v", project.Name, err)
	}
	s.scheduleDevelopmentSyncAfterMutation(id, project, projectActionWorkspaceFileWrite)
}

func projectFilePathParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "path query parameter is required")
		return "", false
	}
	clean, err := workspace.CleanProjectPath(raw)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
		return "", false
	}
	return clean, true
}

// projectFileETagValue strips the weak prefix and quotes from one entity tag.
func projectFileETagValue(raw string) string {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "W/"))
	return strings.Trim(raw, `"`)
}

func projectFileETagMatches(header, version string) bool {
	for _, candidate := range strings.Split(header, ",") {
		value := projectFileETagValue(candidate)
		if value == "*" || value == version {
			return true
		}
	}
	return false
}

func writeProjectFileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeStatus(w, http.StatusNotFound, "NotFound", "file not found")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeStatus(w, http.StatusServiceUnavailable, "Unavailable", err.Error())
	default:
		writeError(w, err)
	}
}

// writeProjectFileWriteError maps workspace write failures. Precondition
// failures (existing target, stale or missing version) are 412 for the
// conditional PUT/DELETE routes and 409 for upload.
func writeProjectFileWriteError(w http.ResponseWriter, err error, conditional bool) {
	var tooLarge *workspace.FileTooLargeError
	if errors.As(err, &tooLarge) {
		writeStatus(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", tooLarge.Error())
		return
	}
	var mutationErr *workspace.MutationError
	if errors.As(err, &mutationErr) {
		precondition := http.StatusConflict
		reason := "Conflict"
		if conditional {
			precondition, reason = http.StatusPreconditionFailed, "PreconditionFailed"
		}
		switch mutationErr.Code {
		case workspace.MutationErrorTargetExists, workspace.MutationErrorStale, workspace.MutationErrorTargetNotFound:
			writeStatus(w, precondition, reason, mutationErr.Message)
		case workspace.MutationErrorConflict:
			writeStatus(w, http.StatusConflict, "Conflict", mutationErr.Message)
		default:
			writeStatus(w, http.StatusBadRequest, "BadRequest", mutationErr.Message)
		}
		return
	}
	writeProjectFileError(w, err)
}
