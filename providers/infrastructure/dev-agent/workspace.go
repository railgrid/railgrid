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

package main

// workspace.go is the small, coordinator-owned workspace API used by coding
// sandboxes.  It deliberately sits beside /sync rather than beside /exec:
// every mutation updates the same source manifest and therefore produces the
// revision/digest evidence that the stateless executor verifies immediately
// before a command starts.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	workspaceRequestMaxBytes    = 8 << 20
	workspaceMaxFiles           = 512
	workspaceMaxFileBytes       = 1 << 20
	workspaceMaxReadBytes       = 4 << 20
	workspaceMaxListEntries     = 4096
	workspaceMaxOperations      = 256
	workspaceMaxCheckpoints     = 64
	workspaceMaxCheckpointBytes = 8 << 20
	workspaceCheckpointDir      = "workspace-checkpoints"
)

type workspaceSeedRequest struct {
	Files          []syncFile `json:"files"`
	Restart        string     `json:"restart,omitempty"`
	SourceRevision uint64     `json:"sourceRevision,omitempty"`
	SourceDigest   string     `json:"sourceDigest,omitempty"`
}

type workspaceListRequest struct {
	Path       string `json:"path,omitempty"`
	Recursive  bool   `json:"recursive,omitempty"`
	MaxEntries int    `json:"maxEntries,omitempty"`
}

type workspaceEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
	Mode uint32 `json:"mode,omitempty"`
}

type workspaceListResponse struct {
	Path           string           `json:"path"`
	Entries        []workspaceEntry `json:"entries"`
	SourceRevision uint64           `json:"sourceRevision,omitempty"`
	SourceDigest   string           `json:"sourceDigest,omitempty"`
}

type workspaceReadRequest struct {
	Paths    []string `json:"paths"`
	MaxBytes int      `json:"maxBytes,omitempty"`
}

type workspaceReadFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Bytes   int    `json:"bytes"`
	Digest  string `json:"digest"`
}

type workspaceReadResponse struct {
	Files          []workspaceReadFile `json:"files"`
	SourceRevision uint64              `json:"sourceRevision,omitempty"`
	SourceDigest   string              `json:"sourceDigest,omitempty"`
}

type workspaceMutation struct {
	Operation string `json:"op"`
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
}

type workspaceMutateRequest struct {
	ExpectedRevision uint64              `json:"expectedRevision"`
	ExpectedDigest   string              `json:"expectedDigest"`
	Operations       []workspaceMutation `json:"operations"`
	Restart          string              `json:"restart,omitempty"`
}

type workspaceMutateResponse struct {
	Phase          string   `json:"phase"`
	Changed        []string `json:"changed,omitempty"`
	Deleted        []string `json:"deleted,omitempty"`
	Restarted      bool     `json:"restarted,omitempty"`
	SourceRevision uint64   `json:"sourceRevision"`
	SourceDigest   string   `json:"sourceDigest"`
}

type workspaceChange struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	BeforeDigest string `json:"beforeDigest,omitempty"`
	AfterDigest  string `json:"afterDigest,omitempty"`
	BeforeBytes  int    `json:"beforeBytes,omitempty"`
	AfterBytes   int    `json:"afterBytes,omitempty"`
}

type workspaceDiffRequest struct {
	CheckpointID     string     `json:"checkpointID,omitempty"`
	Files            []syncFile `json:"files,omitempty"`
	DeletePaths      []string   `json:"deletePaths,omitempty"`
	ExpectedRevision uint64     `json:"expectedRevision,omitempty"`
	ExpectedDigest   string     `json:"expectedDigest,omitempty"`
}

type workspaceDiffResponse struct {
	BaseRevision   uint64            `json:"baseRevision,omitempty"`
	BaseDigest     string            `json:"baseDigest,omitempty"`
	SourceRevision uint64            `json:"sourceRevision"`
	SourceDigest   string            `json:"sourceDigest"`
	Changes        []workspaceChange `json:"changes"`
}

type workspaceCheckpointRequest struct {
	Action           string `json:"action,omitempty"`
	ID               string `json:"id,omitempty"`
	Label            string `json:"label,omitempty"`
	ExpectedRevision uint64 `json:"expectedRevision,omitempty"`
	ExpectedDigest   string `json:"expectedDigest,omitempty"`
}

type workspaceCheckpoint struct {
	ID             string     `json:"id"`
	Label          string     `json:"label,omitempty"`
	SourceRevision uint64     `json:"sourceRevision"`
	SourceDigest   string     `json:"sourceDigest"`
	Files          []syncFile `json:"files"`
	// OpaqueFiles are binary or large managed files, recorded by digest and
	// size only: their bytes are never copied into checkpoint state. Restore
	// leaves them untouched when the workspace copy still matches, and fails
	// otherwise rather than inventing content.
	OpaqueFiles []workspaceOpaqueFile `json:"opaqueFiles,omitempty"`
	// EntriesDigest seals Files and OpaqueFiles when OpaqueFiles is non-empty,
	// because SourceDigest then covers bytes the checkpoint does not hold.
	EntriesDigest string    `json:"entriesDigest,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

type workspaceOpaqueFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

// workspaceFile is one managed file as the workspace verbs see it. Text
// files (valid UTF-8, no NUL, at most workspaceMaxFileBytes) carry their
// content. Every other file is opaque: represented by size and sha256 only,
// and never loaded into a JSON body. An opaque file with nil Content has its
// bytes on disk at Path; a diff candidate may carry opaque bytes in memory.
type workspaceFile struct {
	Path    string
	Content []byte
	Opaque  bool
	Size    int64
	Digest  string
}

func (f workspaceFile) onDisk() bool { return f.Opaque && f.Content == nil }

// isWorkspaceText reports whether content can travel as a text body in the
// workspace API.
func isWorkspaceText(content []byte) bool {
	return len(content) <= workspaceMaxFileBytes && utf8.Valid(content) && bytes.IndexByte(content, 0) < 0
}

// classifyWorkspaceBytes builds the in-memory workspace file for content.
// Text keeps its bytes; opaque content keeps them only when keepOpaque is set.
func classifyWorkspaceBytes(clean string, content []byte, keepOpaque bool) workspaceFile {
	if content == nil {
		content = []byte{}
	}
	file := workspaceFile{Path: clean, Size: int64(len(content)), Digest: digestBytes(content)}
	if isWorkspaceText(content) {
		file.Content = content
		return file
	}
	file.Opaque = true
	if keepOpaque {
		file.Content = content
	}
	return file
}

type workspaceCheckpointSummary struct {
	ID             string    `json:"id"`
	Label          string    `json:"label,omitempty"`
	SourceRevision uint64    `json:"sourceRevision"`
	SourceDigest   string    `json:"sourceDigest"`
	FileCount      int       `json:"fileCount"`
	CreatedAt      time.Time `json:"createdAt"`
}

type workspaceCheckpointResponse struct {
	Action         string                       `json:"action"`
	Checkpoint     *workspaceCheckpointSummary  `json:"checkpoint,omitempty"`
	Checkpoints    []workspaceCheckpointSummary `json:"checkpoints,omitempty"`
	SourceRevision uint64                       `json:"sourceRevision,omitempty"`
	SourceDigest   string                       `json:"sourceDigest,omitempty"`
	Changed        []string                     `json:"changed,omitempty"`
	Deleted        []string                     `json:"deleted,omitempty"`
}

// handleWorkspace dispatches a bounded, token-authenticated workspace
// operation.  The workspace prefix is also the only path exposed by the
// universal coding-sandbox data-plane contract; unknown operations fail
// closed instead of becoming an open filesystem proxy.
func (s *agentServer) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeControl(w, r) {
		return
	}
	op := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/workspace"), "/")
	if op == "" {
		http.Error(w, "workspace operation is required", http.StatusNotFound)
		return
	}
	if op == "seed" {
		// Reuse the established authoritative-sync implementation so seed and
		// exec share exactly one manifest/digest policy.
		s.handleWorkspaceSeed(w, r)
		return
	}
	if s.mutationMu != nil {
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
	}
	switch op {
	case "list":
		s.handleWorkspaceList(w, r)
	case "read":
		s.handleWorkspaceRead(w, r)
	case "mutate":
		s.handleWorkspaceMutate(w, r)
	case "diff":
		s.handleWorkspaceDiff(w, r)
	case "checkpoint":
		s.handleWorkspaceCheckpoint(w, r)
	default:
		http.Error(w, "unknown workspace operation", http.StatusNotFound)
	}
}

func (s *agentServer) handleWorkspaceSeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req workspaceSeedRequest
	if err := decodeBoundedJSON(w, r, workspaceRequestMaxBytes, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Files) > workspaceMaxFiles {
		http.Error(w, fmt.Sprintf("seed may contain at most %d files", workspaceMaxFiles), http.StatusRequestEntityTooLarge)
		return
	}
	if _, err := validateWorkspaceSeedFiles(req.Files); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errSyncTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return
	}
	if req.SourceRevision == 0 && strings.TrimSpace(req.SourceDigest) == "" {
		root, err := openWorkspaceRoot(s.config.WorkDir)
		if err == nil {
			previous, found, readErr := readWorkspaceManifest(root)
			_ = root.Close()
			if readErr == nil && found {
				req.SourceRevision = previous.SourceRevision + 1
			}
		}
		if req.SourceRevision == 0 {
			req.SourceRevision = 1
		}
	}
	digest, err := digestSyncFiles(req.Files)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SourceDigest) == "" {
		req.SourceDigest = digest
	}
	raw, err := json.Marshal(syncRequest{
		Files: req.Files, Restart: req.Restart,
		SourceRevision: req.SourceRevision, SourceDigest: req.SourceDigest,
	})
	if err != nil {
		http.Error(w, "encode seed request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	clone := r.Clone(r.Context())
	clone.Body = &ioNopCloser{Reader: bytes.NewReader(raw)}
	clone.Method = http.MethodPost
	clone.URL.Path = "/sync"
	s.handleSync(w, clone)
}

// ioNopCloser is kept local to avoid allocating a second request body helper
// in the main coordinator file.
type ioNopCloser struct{ *bytes.Reader }

func (ioNopCloser) Close() error { return nil }

func (s *agentServer) handleWorkspaceList(w http.ResponseWriter, r *http.Request) {
	var req workspaceListRequest
	switch r.Method {
	case http.MethodGet:
		req.Path = r.URL.Query().Get("path")
		req.Recursive, _ = strconv.ParseBool(r.URL.Query().Get("recursive"))
		if raw := r.URL.Query().Get("maxEntries"); raw != "" {
			req.MaxEntries, _ = strconv.Atoi(raw)
		}
	case http.MethodPost:
		if err := decodeBoundedJSON(w, r, 64<<10, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	maxEntries, err := boundedWorkspaceListEntries(req.MaxEntries)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	base, err := cleanWorkspaceDirectory(req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root, err := openWorkspaceRoot(s.config.WorkDir)
	if err != nil {
		http.Error(w, "open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()
	entries, err := listWorkspaceEntries(root, base, req.Recursive, maxEntries)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, fs.ErrNotExist) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	response := workspaceListResponse{Path: base, Entries: entries}
	if manifest, found, readErr := readWorkspaceManifest(root); readErr == nil && found {
		response.SourceRevision, response.SourceDigest = manifest.SourceRevision, manifest.SourceDigest
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *agentServer) handleWorkspaceRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req workspaceReadRequest
	if err := decodeBoundedJSON(w, r, 64<<10, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > workspaceMaxFiles {
		http.Error(w, fmt.Sprintf("paths must contain between 1 and %d entries", workspaceMaxFiles), http.StatusBadRequest)
		return
	}
	maxBytes, err := boundedWorkspaceReadBytes(req.MaxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths, err := normalizeWorkspacePaths(req.Paths)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root, err := openWorkspaceRoot(s.config.WorkDir)
	if err != nil {
		http.Error(w, "open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()
	response := workspaceReadResponse{Files: make([]workspaceReadFile, 0, len(paths))}
	remaining := maxBytes
	for _, clean := range paths {
		if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, fs.ErrNotExist) {
				status = http.StatusNotFound
			}
			http.Error(w, fmt.Sprintf("read %q: %v", clean, err), status)
			return
		}
		info, err := root.Lstat(clean)
		if err != nil {
			http.Error(w, fmt.Sprintf("read %q: %v", clean, err), http.StatusNotFound)
			return
		}
		if !info.Mode().IsRegular() {
			http.Error(w, fmt.Sprintf("read %q: path is not a regular file", clean), http.StatusBadRequest)
			return
		}
		// Large and binary files are opaque to the workspace API: the read
		// fails with their size and digest instead of loading them.
		if info.Size() > workspaceMaxFileBytes {
			digest, size, err := hashWorkspaceFile(root, clean)
			if err != nil {
				http.Error(w, fmt.Sprintf("read %q: %v", clean, err), http.StatusInternalServerError)
				return
			}
			http.Error(w, fmt.Sprintf("read %q is %d bytes (sha256 %s), above the %d-byte text limit; large and binary files are opaque to the workspace API", clean, size, digest, workspaceMaxFileBytes), http.StatusRequestEntityTooLarge)
			return
		}
		content, err := root.ReadFile(clean)
		if err != nil {
			http.Error(w, fmt.Sprintf("read %q: %v", clean, err), http.StatusInternalServerError)
			return
		}
		if len(content) > workspaceMaxFileBytes || len(content) > remaining {
			http.Error(w, fmt.Sprintf("read %q exceeds the remaining %d-byte response limit", clean, remaining), http.StatusRequestEntityTooLarge)
			return
		}
		if !isWorkspaceText(content) {
			http.Error(w, fmt.Sprintf("read %q is not UTF-8 text: it is a binary file (%d bytes, sha256 %s); binary files are opaque to the workspace API", clean, len(content), digestBytes(content)), http.StatusUnprocessableEntity)
			return
		}
		response.Files = append(response.Files, workspaceReadFile{Path: clean, Content: string(content), Bytes: len(content), Digest: digestBytes(content)})
		remaining -= len(content)
	}
	if manifest, found, readErr := readWorkspaceManifest(root); readErr == nil && found {
		response.SourceRevision, response.SourceDigest = manifest.SourceRevision, manifest.SourceDigest
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *agentServer) handleWorkspaceMutate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req workspaceMutateRequest
	if err := decodeBoundedJSON(w, r, workspaceRequestMaxBytes, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ExpectedRevision == 0 || normalizeSourceDigest(req.ExpectedDigest) == "" {
		http.Error(w, "expectedRevision and expectedDigest are required", http.StatusBadRequest)
		return
	}
	if len(req.Operations) == 0 || len(req.Operations) > workspaceMaxOperations {
		http.Error(w, fmt.Sprintf("operations must contain between 1 and %d entries", workspaceMaxOperations), http.StatusBadRequest)
		return
	}
	root, err := openWorkspaceRoot(s.config.WorkDir)
	if err != nil {
		http.Error(w, "open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()
	manifest, found, err := readWorkspaceManifest(root)
	if err != nil || !found {
		http.Error(w, "workspace is not seeded", http.StatusConflict)
		return
	}
	if err := verifyWorkspaceManifest(root, manifest); err != nil {
		http.Error(w, "workspace manifest verification failed: "+err.Error(), http.StatusConflict)
		return
	}
	if manifest.SourceRevision != req.ExpectedRevision || normalizeSourceDigest(manifest.SourceDigest) != normalizeSourceDigest(req.ExpectedDigest) {
		http.Error(w, "workspace revision or digest no longer matches expected evidence", http.StatusConflict)
		return
	}
	files, err := readManagedFiles(root, manifest.Files)
	if err != nil {
		http.Error(w, "read managed workspace: "+err.Error(), http.StatusConflict)
		return
	}
	// Opaque (binary or large) managed files ride through unchanged unless an
	// operation overwrites them with text or deletes them.
	filesByPath := make(map[string]workspaceFile, len(files))
	for _, file := range files {
		filesByPath[file.Path] = file
	}
	seen := make(map[string]struct{}, len(req.Operations))
	for i, operation := range req.Operations {
		clean, err := cleanWorkspacePath(operation.Path)
		if err != nil {
			http.Error(w, fmt.Sprintf("operations[%d]: %v", i, err), http.StatusBadRequest)
			return
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			http.Error(w, fmt.Sprintf("operations[%d]: %v", i, err), http.StatusBadRequest)
			return
		}
		if _, duplicate := seen[clean]; duplicate {
			http.Error(w, fmt.Sprintf("duplicate mutation path %q", clean), http.StatusBadRequest)
			return
		}
		seen[clean] = struct{}{}
		switch strings.ToLower(strings.TrimSpace(operation.Operation)) {
		case "write", "upsert":
			if len([]byte(operation.Content)) > workspaceMaxFileBytes {
				http.Error(w, fmt.Sprintf("mutation %q exceeds %d bytes", clean, workspaceMaxFileBytes), http.StatusRequestEntityTooLarge)
				return
			}
			if !utf8.ValidString(operation.Content) || strings.ContainsRune(operation.Content, '\x00') {
				http.Error(w, fmt.Sprintf("mutation %q must be UTF-8 text without NUL bytes", clean), http.StatusBadRequest)
				return
			}
			filesByPath[clean] = classifyWorkspaceBytes(clean, []byte(operation.Content), true)
		case "delete", "remove":
			delete(filesByPath, clean)
		default:
			http.Error(w, fmt.Sprintf("operations[%d].op must be write or delete", i), http.StatusBadRequest)
			return
		}
	}
	if len(filesByPath) > workspaceMaxFiles {
		http.Error(w, fmt.Sprintf("mutation would exceed %d managed files", workspaceMaxFiles), http.StatusRequestEntityTooLarge)
		return
	}
	updated := workspaceFilesFromMap(filesByPath)
	newDigest, err := digestWorkspaceFiles(root, updated)
	if err != nil {
		http.Error(w, "digest mutated workspace: "+err.Error(), http.StatusConflict)
		return
	}
	if newDigest == normalizeSourceDigest(manifest.SourceDigest) {
		writeJSON(w, http.StatusOK, workspaceMutateResponse{Phase: "Unchanged", SourceRevision: manifest.SourceRevision, SourceDigest: manifest.SourceDigest})
		return
	}
	changed, deleted, err := applyManagedContent(root, updated, manifest.Files)
	if err != nil {
		http.Error(w, "apply workspace mutation: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if manifest.SourceRevision == ^uint64(0) {
		http.Error(w, "workspace revision exhausted", http.StatusConflict)
		return
	}
	manifest.SourceRevision++
	manifest.SourceDigest = newDigest
	manifest.Files = workspaceFilePaths(updated)
	manifest.PendingReloadCommands = nil
	if err := writeWorkspaceManifest(root, manifest); err != nil {
		http.Error(w, "write workspace manifest: "+err.Error(), http.StatusInternalServerError)
		return
	}
	response := workspaceMutateResponse{Phase: "Mutated", Changed: changed, Deleted: deleted, SourceRevision: manifest.SourceRevision, SourceDigest: manifest.SourceDigest}
	if strings.EqualFold(strings.TrimSpace(req.Restart), "always") {
		if err := s.runtime.Restart(r.Context()); err != nil {
			http.Error(w, "restart: "+err.Error(), http.StatusBadGateway)
			return
		}
		response.Restarted = true
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *agentServer) handleWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req workspaceDiffRequest
	if err := decodeBoundedJSON(w, r, workspaceRequestMaxBytes, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root, err := openWorkspaceRoot(s.config.WorkDir)
	if err != nil {
		http.Error(w, "open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()
	currentManifest, found, err := readWorkspaceManifest(root)
	if err != nil || !found {
		http.Error(w, "workspace is not seeded", http.StatusConflict)
		return
	}
	if err := verifyWorkspaceManifest(root, currentManifest); err != nil {
		http.Error(w, "workspace manifest verification failed: "+err.Error(), http.StatusConflict)
		return
	}
	currentFiles, err := readManagedFiles(root, currentManifest.Files)
	if err != nil {
		http.Error(w, "read managed workspace: "+err.Error(), http.StatusConflict)
		return
	}
	baseline := currentFiles
	baseRevision, baseDigest := currentManifest.SourceRevision, currentManifest.SourceDigest
	if strings.TrimSpace(req.CheckpointID) != "" {
		checkpoint, err := s.loadWorkspaceCheckpoint(req.CheckpointID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		baseline = checkpointWorkspaceFiles(checkpoint)
		baseRevision, baseDigest = checkpoint.SourceRevision, checkpoint.SourceDigest
	} else if req.Files != nil || req.DeletePaths != nil {
		candidate, err := candidateWorkspaceFiles(currentFiles, req.Files, req.DeletePaths)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		baseline, currentFiles = currentFiles, candidate
		baseRevision, baseDigest = currentManifest.SourceRevision, currentManifest.SourceDigest
	}
	if req.ExpectedRevision != 0 && req.ExpectedRevision != currentManifest.SourceRevision {
		http.Error(w, "workspace revision no longer matches expected evidence", http.StatusConflict)
		return
	}
	if strings.TrimSpace(req.ExpectedDigest) != "" && normalizeSourceDigest(req.ExpectedDigest) != normalizeSourceDigest(currentManifest.SourceDigest) {
		http.Error(w, "workspace digest no longer matches expected evidence", http.StatusConflict)
		return
	}
	currentDigest, err := digestWorkspaceFiles(root, currentFiles)
	if err != nil {
		http.Error(w, "digest workspace: "+err.Error(), http.StatusConflict)
		return
	}
	changes, err := diffWorkspaceFiles(baseline, currentFiles)
	if err != nil {
		http.Error(w, "diff workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, workspaceDiffResponse{BaseRevision: baseRevision, BaseDigest: baseDigest, SourceRevision: currentManifest.SourceRevision, SourceDigest: currentDigest, Changes: changes})
}

func (s *agentServer) handleWorkspaceCheckpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req workspaceCheckpointRequest
	if err := decodeBoundedJSON(w, r, 64<<10, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "create"
	}
	switch action {
	case "list":
		checkpoints, err := s.listWorkspaceCheckpoints()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, workspaceCheckpointResponse{Action: action, Checkpoints: checkpoints})
		return
	case "delete":
		if strings.TrimSpace(req.ID) == "" {
			http.Error(w, "checkpoint id is required", http.StatusBadRequest)
			return
		}
		if err := s.deleteWorkspaceCheckpoint(req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, workspaceCheckpointResponse{Action: action})
		return
	case "restore":
		s.restoreWorkspaceCheckpoint(w, r, req)
		return
	case "create":
		// continue below
	default:
		http.Error(w, "checkpoint action must be create, list, restore, or delete", http.StatusBadRequest)
		return
	}
	root, err := openWorkspaceRoot(s.config.WorkDir)
	if err != nil {
		http.Error(w, "open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()
	manifest, found, err := readWorkspaceManifest(root)
	if err != nil || !found {
		http.Error(w, "workspace is not seeded", http.StatusConflict)
		return
	}
	if err := verifyWorkspaceManifest(root, manifest); err != nil {
		http.Error(w, "workspace manifest verification failed: "+err.Error(), http.StatusConflict)
		return
	}
	if req.ExpectedRevision != 0 && req.ExpectedRevision != manifest.SourceRevision || strings.TrimSpace(req.ExpectedDigest) != "" && normalizeSourceDigest(req.ExpectedDigest) != normalizeSourceDigest(manifest.SourceDigest) {
		http.Error(w, "workspace revision or digest no longer matches expected evidence", http.StatusConflict)
		return
	}
	files, err := readManagedFiles(root, manifest.Files)
	if err != nil {
		http.Error(w, "read managed workspace: "+err.Error(), http.StatusConflict)
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = checkpointID(manifest)
	}
	if err := validateCheckpointID(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	checkpoint, err := newWorkspaceCheckpoint(id, strings.TrimSpace(req.Label), manifest, files)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.saveWorkspaceCheckpoint(checkpoint); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	summary := checkpointSummary(checkpoint)
	writeJSON(w, http.StatusOK, workspaceCheckpointResponse{Action: action, Checkpoint: &summary, SourceRevision: manifest.SourceRevision, SourceDigest: manifest.SourceDigest})
}

func (s *agentServer) restoreWorkspaceCheckpoint(w http.ResponseWriter, r *http.Request, req workspaceCheckpointRequest) {
	if strings.TrimSpace(req.ID) == "" {
		http.Error(w, "checkpoint id is required", http.StatusBadRequest)
		return
	}
	checkpoint, err := s.loadWorkspaceCheckpoint(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	root, err := openWorkspaceRoot(s.config.WorkDir)
	if err != nil {
		http.Error(w, "open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = root.Close() }()
	manifest, found, err := readWorkspaceManifest(root)
	if err != nil || !found {
		http.Error(w, "workspace is not seeded", http.StatusConflict)
		return
	}
	if err := verifyWorkspaceManifest(root, manifest); err != nil {
		http.Error(w, "workspace manifest verification failed: "+err.Error(), http.StatusConflict)
		return
	}
	if req.ExpectedRevision != 0 && req.ExpectedRevision != manifest.SourceRevision || strings.TrimSpace(req.ExpectedDigest) != "" && normalizeSourceDigest(req.ExpectedDigest) != normalizeSourceDigest(manifest.SourceDigest) {
		http.Error(w, "workspace revision or digest no longer matches expected evidence", http.StatusConflict)
		return
	}
	target := checkpointWorkspaceFiles(checkpoint)
	// A checkpoint holds no bytes for opaque files: restore can only keep the
	// workspace copy, so it must still be exactly the recorded bytes. Every
	// opaque file is checked before anything is written.
	for _, file := range target {
		if !file.onDisk() {
			continue
		}
		if err := verifyOpaqueWorkspaceFile(root, file); err != nil {
			http.Error(w, fmt.Sprintf("checkpoint %q cannot be restored: binary file %q (%d bytes, sha256 %s) is recorded by digest only and the workspace copy %v; sync that file back first", checkpoint.ID, file.Path, file.Size, file.Digest, err), http.StatusConflict)
			return
		}
	}
	restoredDigest, err := digestWorkspaceFiles(root, target)
	if err != nil {
		http.Error(w, "restore workspace checkpoint: "+err.Error(), http.StatusConflict)
		return
	}
	if restoredDigest != normalizeSourceDigest(checkpoint.SourceDigest) {
		http.Error(w, "checkpoint digest does not match its files", http.StatusConflict)
		return
	}
	changed, deleted, err := applyManagedContent(root, target, manifest.Files)
	if err != nil {
		http.Error(w, "restore workspace checkpoint: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if manifest.SourceRevision == ^uint64(0) {
		http.Error(w, "workspace revision exhausted", http.StatusConflict)
		return
	}
	manifest.SourceRevision++
	manifest.SourceDigest = checkpoint.SourceDigest
	manifest.Files = workspaceFilePaths(target)
	manifest.PendingReloadCommands = nil
	if err := writeWorkspaceManifest(root, manifest); err != nil {
		http.Error(w, "write workspace manifest: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, workspaceCheckpointResponse{Action: "restore", Checkpoint: func() *workspaceCheckpointSummary { summary := checkpointSummary(checkpoint); return &summary }(), SourceRevision: manifest.SourceRevision, SourceDigest: manifest.SourceDigest, Changed: changed, Deleted: deleted})
}

func boundedWorkspaceListEntries(value int) (int, error) {
	if value <= 0 {
		return 256, nil
	}
	if value > workspaceMaxListEntries {
		return 0, fmt.Errorf("maxEntries must be at most %d", workspaceMaxListEntries)
	}
	return value, nil
}

func boundedWorkspaceReadBytes(value int) (int, error) {
	if value <= 0 {
		return workspaceMaxReadBytes, nil
	}
	if value > workspaceMaxReadBytes {
		return 0, fmt.Errorf("maxBytes must be at most %d", workspaceMaxReadBytes)
	}
	return value, nil
}

func cleanWorkspaceDirectory(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "." {
		return ".", nil
	}
	return cleanWorkspacePath(raw)
}

func normalizeWorkspacePaths(raw []string) ([]string, error) {
	paths := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		clean, err := cleanWorkspacePath(value)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[clean]; ok {
			return nil, fmt.Errorf("duplicate workspace path %q", clean)
		}
		seen[clean] = struct{}{}
		paths = append(paths, clean)
	}
	slicesSortStrings(paths)
	return paths, nil
}

func listWorkspaceEntries(root *os.Root, base string, recursive bool, maxEntries int) ([]workspaceEntry, error) {
	entries := make([]workspaceEntry, 0)
	queue := []string{base}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		directory, err := root.Open(dir)
		if err != nil {
			return nil, err
		}
		children, err := directory.ReadDir(-1)
		_ = directory.Close()
		if err != nil {
			return nil, err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if dir == "." && (child.Name() == workspaceManifestName || child.Name() == workspaceManifestName+".tmp") {
				continue
			}
			childPath := child.Name()
			if dir != "." {
				childPath = path.Join(dir, child.Name())
			}
			info, err := root.Lstat(childPath)
			if err != nil {
				return nil, err
			}
			kind := "file"
			switch {
			case info.IsDir():
				kind = "directory"
			case info.Mode()&os.ModeSymlink != 0:
				kind = "symlink"
			case !info.Mode().IsRegular():
				kind = "special"
			}
			entries = append(entries, workspaceEntry{Path: childPath, Type: kind, Size: info.Size(), Mode: uint32(info.Mode().Perm())})
			if len(entries) > maxEntries {
				return nil, fmt.Errorf("workspace listing exceeds maxEntries %d", maxEntries)
			}
			if recursive && info.IsDir() {
				queue = append(queue, childPath)
			}
		}
	}
	return entries, nil
}

// readManagedFiles loads the managed workspace. Text files are loaded;
// binary files and files above workspaceMaxFileBytes are opaque: hashed
// from disk and represented by size and digest only, so a large asset never
// makes mutate, diff, or checkpoint fail.
func readManagedFiles(root *os.Root, paths []string) ([]workspaceFile, error) {
	cleaned, err := normalizeWorkspacePaths(paths)
	if err != nil {
		return nil, err
	}
	files := make([]workspaceFile, 0, len(cleaned))
	for _, clean := range cleaned {
		if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
			return nil, err
		}
		info, err := root.Lstat(clean)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("managed path %q is not a regular file", clean)
		}
		if info.Size() > workspaceMaxFileBytes {
			digest, size, err := hashWorkspaceFile(root, clean)
			if err != nil {
				return nil, err
			}
			files = append(files, workspaceFile{Path: clean, Opaque: true, Size: size, Digest: digest})
			continue
		}
		content, err := root.ReadFile(clean)
		if err != nil {
			return nil, err
		}
		files = append(files, classifyWorkspaceBytes(clean, content, false))
	}
	return files, nil
}

// hashWorkspaceFile streams one workspace file and returns its sha256 (hex)
// and size.
func hashWorkspaceFile(root *os.Root, clean string) (string, int64, error) {
	file, err := root.Open(clean)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

// verifyOpaqueWorkspaceFile checks that an opaque file's recorded bytes are
// still exactly what the workspace holds at its path.
func verifyOpaqueWorkspaceFile(root *os.Root, file workspaceFile) error {
	if err := ensureExecPathNoSymlink(root, file.Path, true); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("is missing")
		}
		return err
	}
	info, err := root.Lstat(file.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("is missing")
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("is not a regular file")
	}
	digest, size, err := hashWorkspaceFile(root, file.Path)
	if err != nil {
		return err
	}
	if digest != file.Digest || size != file.Size {
		return fmt.Errorf("has changed (now %d bytes, sha256 %s)", size, digest)
	}
	return nil
}

// digestWorkspaceFiles computes the workspace source digest of files, the
// same digest /sync and exec verify. In-memory bytes are hashed directly;
// opaque files are streamed from disk and must still match their recorded
// digest, so a concurrent change fails the operation instead of being
// silently adopted.
func digestWorkspaceFiles(root *os.Root, files []workspaceFile) (string, error) {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b workspaceFile) int { return strings.Compare(a.Path, b.Path) })
	digester := newSyncDigester()
	for _, file := range sorted {
		clean, err := cleanWorkspacePath(file.Path)
		if err != nil {
			return "", err
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return "", err
		}
		if !file.onDisk() {
			digester.add(clean, file.Content)
			continue
		}
		if err := ensureExecPathNoSymlink(root, clean, true); err != nil {
			return "", fmt.Errorf("read binary file %q: %w", clean, err)
		}
		digest, size, err := digester.addFile(root, clean)
		if err != nil {
			return "", fmt.Errorf("read binary file %q: %w", clean, err)
		}
		if digest != file.Digest || size != file.Size {
			return "", fmt.Errorf("binary file %q changed while the workspace operation ran", clean)
		}
	}
	return digester.sum(), nil
}

func workspaceFilesFromMap(files map[string]workspaceFile) []workspaceFile {
	out := make([]workspaceFile, 0, len(files))
	for _, file := range files {
		out = append(out, file)
	}
	slices.SortFunc(out, func(a, b workspaceFile) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func workspaceFilePaths(files []workspaceFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	slicesSortStrings(paths)
	return paths
}

// applyManagedContent converges the managed workspace to files: in-memory
// content is written when it differs, opaque files whose bytes live on disk
// are left untouched, and previously managed paths absent from files are
// removed.
func applyManagedContent(root *os.Root, files []workspaceFile, previous []string) (changed, deleted []string, err error) {
	previousSet := make(map[string]struct{}, len(previous))
	for _, raw := range previous {
		clean, cleanErr := cleanWorkspacePath(raw)
		if cleanErr != nil {
			return nil, nil, cleanErr
		}
		previousSet[clean] = struct{}{}
	}
	keep := make(map[string]struct{}, len(files))
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b workspaceFile) int { return strings.Compare(a.Path, b.Path) })
	for _, file := range sorted {
		clean := file.Path
		keep[clean] = struct{}{}
		if file.onDisk() {
			continue
		}
		if err := ensureExecPathNoSymlink(root, clean, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
		if err := ensureExecPathNoSymlink(root, clean, true); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
		if workspaceFileContentChanged(root, clean, file.Content) {
			if err := writeWorkspaceFile(root, clean, file.Content); err != nil {
				return nil, nil, err
			}
			changed = append(changed, clean)
		}
	}
	for _, clean := range sortedMapKeys(previousSet) {
		if _, ok := keep[clean]; ok {
			continue
		}
		removed, err := removeManagedWorkspaceFile(root, clean)
		if err != nil {
			return nil, nil, err
		}
		if removed {
			deleted = append(deleted, clean)
		}
	}
	return changed, deleted, nil
}

func workspaceFileIndex(files []workspaceFile) map[string]workspaceFile {
	index := make(map[string]workspaceFile, len(files))
	for _, file := range files {
		index[file.Path] = file
	}
	return index
}

// candidateWorkspaceFiles overlays proposed writes and deletes on the current
// workspace for a diff preview. utf-8 candidates keep the text rules; base64
// candidates may be binary up to the sync per-file limit and are compared by
// digest.
func candidateWorkspaceFiles(current []workspaceFile, writes []syncFile, deletes []string) ([]workspaceFile, error) {
	files := workspaceFileIndex(current)
	for _, file := range writes {
		clean, err := cleanWorkspacePath(file.Path)
		if err != nil {
			return nil, err
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return nil, err
		}
		content, binary, err := decodeSyncContent(file)
		if err != nil {
			return nil, err
		}
		switch {
		case binary && len(content) > syncMaxFileBytes:
			return nil, fmt.Errorf("candidate file %q is %d bytes, above the %d-byte limit", clean, len(content), syncMaxFileBytes)
		case !binary && !isWorkspaceText(content):
			return nil, fmt.Errorf("candidate file %q is invalid or too large", clean)
		}
		files[clean] = classifyWorkspaceBytes(clean, content, true)
	}
	for _, raw := range deletes {
		clean, err := cleanWorkspacePath(raw)
		if err != nil {
			return nil, err
		}
		delete(files, clean)
	}
	if len(files) > workspaceMaxFiles {
		return nil, fmt.Errorf("candidate workspace exceeds %d files", workspaceMaxFiles)
	}
	return workspaceFilesFromMap(files), nil
}

// diffWorkspaceFiles compares two workspace views by per-file digest, so
// opaque files take part without their bytes.
func diffWorkspaceFiles(before, after []workspaceFile) ([]workspaceChange, error) {
	left := make(map[string]workspaceFile, len(before))
	right := make(map[string]workspaceFile, len(after))
	for _, file := range before {
		clean, err := cleanWorkspacePath(file.Path)
		if err != nil {
			return nil, err
		}
		left[clean] = file
	}
	for _, file := range after {
		clean, err := cleanWorkspacePath(file.Path)
		if err != nil {
			return nil, err
		}
		right[clean] = file
	}
	all := make(map[string]struct{}, len(left)+len(right))
	for clean := range left {
		all[clean] = struct{}{}
	}
	for clean := range right {
		all[clean] = struct{}{}
	}
	paths := sortedMapKeys(all)
	changes := make([]workspaceChange, 0)
	for _, clean := range paths {
		beforeFile, beforeOK := left[clean]
		afterFile, afterOK := right[clean]
		if beforeOK && afterOK && beforeFile.Digest == afterFile.Digest && beforeFile.Size == afterFile.Size {
			continue
		}
		change := workspaceChange{Path: clean}
		switch {
		case !beforeOK:
			change.Kind = "added"
		case !afterOK:
			change.Kind = "deleted"
		default:
			change.Kind = "modified"
		}
		if beforeOK {
			change.BeforeDigest, change.BeforeBytes = beforeFile.Digest, int(beforeFile.Size)
		}
		if afterOK {
			change.AfterDigest, change.AfterBytes = afterFile.Digest, int(afterFile.Size)
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func digestBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func checkpointID(manifest workspaceManifest) string {
	digest := normalizeSourceDigest(manifest.SourceDigest)
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return fmt.Sprintf("ckpt-%d-%s", manifest.SourceRevision, digest)
}

func validateCheckpointID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\\x00") || id == "." || id == ".." {
		return errors.New("checkpoint id is invalid")
	}
	return nil
}

func checkpointSummary(checkpoint workspaceCheckpoint) workspaceCheckpointSummary {
	return workspaceCheckpointSummary{ID: checkpoint.ID, Label: checkpoint.Label, SourceRevision: checkpoint.SourceRevision, SourceDigest: checkpoint.SourceDigest, FileCount: len(checkpoint.Files) + len(checkpoint.OpaqueFiles), CreatedAt: checkpoint.CreatedAt}
}

// newWorkspaceCheckpoint records text files by content and opaque files by
// digest and size only.
func newWorkspaceCheckpoint(id, label string, manifest workspaceManifest, files []workspaceFile) (workspaceCheckpoint, error) {
	checkpoint := workspaceCheckpoint{ID: id, Label: label, SourceRevision: manifest.SourceRevision, SourceDigest: manifest.SourceDigest, Files: make([]syncFile, 0, len(files)), CreatedAt: time.Now().UTC()}
	for _, file := range workspaceFilesFromMap(workspaceFileIndex(files)) {
		if file.Opaque {
			checkpoint.OpaqueFiles = append(checkpoint.OpaqueFiles, workspaceOpaqueFile{Path: file.Path, Digest: file.Digest, Bytes: file.Size})
			continue
		}
		checkpoint.Files = append(checkpoint.Files, syncFile{Path: file.Path, Content: string(file.Content)})
	}
	if len(checkpoint.OpaqueFiles) > 0 {
		sealed, err := checkpointEntriesDigest(checkpoint)
		if err != nil {
			return workspaceCheckpoint{}, err
		}
		checkpoint.EntriesDigest = sealed
	}
	return checkpoint, nil
}

// checkpointWorkspaceFiles is the workspace view a checkpoint describes;
// opaque entries point at bytes that must still be on disk.
func checkpointWorkspaceFiles(checkpoint workspaceCheckpoint) []workspaceFile {
	files := make([]workspaceFile, 0, len(checkpoint.Files)+len(checkpoint.OpaqueFiles))
	for _, file := range checkpoint.Files {
		files = append(files, classifyWorkspaceBytes(file.Path, []byte(file.Content), true))
	}
	for _, file := range checkpoint.OpaqueFiles {
		files = append(files, workspaceFile{Path: file.Path, Opaque: true, Size: file.Bytes, Digest: file.Digest})
	}
	return workspaceFilesFromMap(workspaceFileIndex(files))
}

// checkpointEntriesDigest seals a checkpoint's recorded entries (text content
// and opaque digests) so a corrupt checkpoint is detected even though its
// SourceDigest covers bytes it does not hold.
func checkpointEntriesDigest(checkpoint workspaceCheckpoint) (string, error) {
	raw, err := json.Marshal(struct {
		Files       []syncFile            `json:"files"`
		OpaqueFiles []workspaceOpaqueFile `json:"opaqueFiles"`
	}{Files: checkpoint.Files, OpaqueFiles: checkpoint.OpaqueFiles})
	if err != nil {
		return "", err
	}
	return digestBytes(raw), nil
}

// validateWorkspaceSeedFiles applies the authoritative /sync rules to a seed:
// utf-8 entries must be text, base64 entries may be binary, and the sync
// per-file and total decoded limits apply (within the seed's request cap).
func validateWorkspaceSeedFiles(files []syncFile) (map[string]struct{}, error) {
	decoded, err := decodeSyncFiles(files, true)
	if err != nil {
		return nil, err
	}
	return validateAuthoritativeSyncFiles(decoded)
}

func validateWorkspaceCheckpoint(checkpoint workspaceCheckpoint) error {
	if len(checkpoint.Files)+len(checkpoint.OpaqueFiles) > workspaceMaxFiles {
		return fmt.Errorf("checkpoint has more than %d files", workspaceMaxFiles)
	}
	paths := make(map[string]struct{}, len(checkpoint.Files)+len(checkpoint.OpaqueFiles))
	claim := func(raw string) error {
		clean, err := cleanWorkspacePath(raw)
		if err != nil {
			return err
		}
		if clean != raw {
			return fmt.Errorf("checkpoint path %q is not clean", raw)
		}
		if err := validateManagedWorkspacePath(clean); err != nil {
			return err
		}
		if _, exists := paths[clean]; exists {
			return fmt.Errorf("checkpoint duplicates path %q", clean)
		}
		paths[clean] = struct{}{}
		return nil
	}
	for _, file := range checkpoint.Files {
		if err := claim(file.Path); err != nil {
			return err
		}
		if file.Encoding != "" || !isWorkspaceText([]byte(file.Content)) {
			return fmt.Errorf("checkpoint file %q must be UTF-8 text of at most %d bytes", file.Path, workspaceMaxFileBytes)
		}
	}
	for _, file := range checkpoint.OpaqueFiles {
		if err := claim(file.Path); err != nil {
			return err
		}
		if raw, err := hex.DecodeString(file.Digest); err != nil || len(raw) != sha256.Size || file.Bytes < 0 {
			return fmt.Errorf("checkpoint binary file %q has an invalid digest or size", file.Path)
		}
	}
	if len(checkpoint.OpaqueFiles) > 0 {
		// SourceDigest covers opaque bytes held only on disk; restore checks
		// it against the workspace. Here the recorded entries are verified.
		sealed, err := checkpointEntriesDigest(checkpoint)
		if err != nil {
			return err
		}
		if sealed != checkpoint.EntriesDigest {
			return errors.New("checkpoint digest does not match its files")
		}
		return nil
	}
	digest, err := digestSyncFiles(checkpoint.Files)
	if err != nil {
		return err
	}
	if normalizeSourceDigest(digest) != normalizeSourceDigest(checkpoint.SourceDigest) {
		return errors.New("checkpoint digest does not match its files")
	}
	return nil
}

func (s *agentServer) checkpointStorePath(id string) (string, error) {
	if err := validateCheckpointID(id); err != nil {
		return "", err
	}
	if strings.TrimSpace(s.config.StateDir) == "" {
		return "", errors.New("checkpoint persistence requires coordinator state")
	}
	return path.Join(s.config.StateDir, workspaceCheckpointDir, id+".json"), nil
}

func (s *agentServer) saveWorkspaceCheckpoint(checkpoint workspaceCheckpoint) error {
	if err := validateCheckpointID(checkpoint.ID); err != nil {
		return err
	}
	if err := validateWorkspaceCheckpoint(checkpoint); err != nil {
		return err
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	if len(raw) > workspaceMaxCheckpointBytes {
		return fmt.Errorf("checkpoint exceeds %d bytes", workspaceMaxCheckpointBytes)
	}
	if strings.TrimSpace(s.config.StateDir) == "" {
		if s.checkpoints == nil {
			s.checkpoints = map[string]workspaceCheckpoint{}
		}
		if existing, ok := s.checkpoints[checkpoint.ID]; ok {
			if existing.SourceRevision != checkpoint.SourceRevision || normalizeSourceDigest(existing.SourceDigest) != normalizeSourceDigest(checkpoint.SourceDigest) {
				return errors.New("checkpoint id already exists for different workspace evidence")
			}
			return nil
		}
		if len(s.checkpoints) >= workspaceMaxCheckpoints {
			return fmt.Errorf("at most %d checkpoints may be retained", workspaceMaxCheckpoints)
		}
		s.checkpoints[checkpoint.ID] = checkpoint
		return nil
	}
	dir := path.Join(s.config.StateDir, workspaceCheckpointDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create checkpoint state: %w", err)
	}
	file, err := s.checkpointStorePath(checkpoint.ID)
	if err != nil {
		return err
	}
	if existingRaw, readErr := os.ReadFile(file); readErr == nil {
		var existing workspaceCheckpoint
		if json.Unmarshal(existingRaw, &existing) == nil && existing.SourceRevision == checkpoint.SourceRevision && normalizeSourceDigest(existing.SourceDigest) == normalizeSourceDigest(checkpoint.SourceDigest) {
			return nil
		}
		return errors.New("checkpoint id already exists for different workspace evidence")
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) >= workspaceMaxCheckpoints {
		return fmt.Errorf("at most %d checkpoints may be retained", workspaceMaxCheckpoints)
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *agentServer) loadWorkspaceCheckpoint(id string) (workspaceCheckpoint, error) {
	if err := validateCheckpointID(id); err != nil {
		return workspaceCheckpoint{}, err
	}
	if strings.TrimSpace(s.config.StateDir) == "" {
		checkpoint, ok := s.checkpoints[id]
		if !ok {
			return workspaceCheckpoint{}, errors.New("checkpoint not found")
		}
		if err := validateWorkspaceCheckpoint(checkpoint); err != nil {
			return workspaceCheckpoint{}, err
		}
		return checkpoint, nil
	}
	file, err := s.checkpointStorePath(id)
	if err != nil {
		return workspaceCheckpoint{}, err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return workspaceCheckpoint{}, errors.New("checkpoint not found")
	}
	var checkpoint workspaceCheckpoint
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return workspaceCheckpoint{}, errors.New("checkpoint is corrupt")
	}
	if err := validateWorkspaceCheckpoint(checkpoint); err != nil {
		return workspaceCheckpoint{}, err
	}
	return checkpoint, nil
}

func (s *agentServer) listWorkspaceCheckpoints() ([]workspaceCheckpointSummary, error) {
	if strings.TrimSpace(s.config.StateDir) == "" {
		out := make([]workspaceCheckpointSummary, 0, len(s.checkpoints))
		for _, checkpoint := range s.checkpoints {
			out = append(out, checkpointSummary(checkpoint))
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	dir := path.Join(s.config.StateDir, workspaceCheckpointDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []workspaceCheckpointSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]workspaceCheckpointSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		checkpoint, err := s.loadWorkspaceCheckpoint(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, checkpointSummary(checkpoint))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *agentServer) deleteWorkspaceCheckpoint(id string) error {
	if err := validateCheckpointID(id); err != nil {
		return err
	}
	if strings.TrimSpace(s.config.StateDir) == "" {
		if _, ok := s.checkpoints[id]; !ok {
			return errors.New("checkpoint not found")
		}
		delete(s.checkpoints, id)
		return nil
	}
	file, err := s.checkpointStorePath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil {
		return errors.New("checkpoint not found")
	}
	return nil
}

func sortedMapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slicesSortStrings(keys)
	return keys
}

func slicesSortStrings(values []string) { sort.Strings(values) }
