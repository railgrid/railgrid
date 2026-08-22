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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantSandboxWorkspaceChange struct {
	Path            string `json:"path"`
	Operation       string `json:"operation"`
	Content         string `json:"content,omitempty"`
	ExpectedVersion string `json:"expectedVersion,omitempty"`
}

type projectAssistantSandboxWorkspaceRequest struct {
	Action           string                                   `json:"action"`
	Files            []projectSandboxSyncFile                 `json:"files,omitempty"`
	Path             string                                   `json:"path,omitempty"`
	SourcePath       string                                   `json:"sourcePath,omitempty"`
	DestinationPath  string                                   `json:"destinationPath,omitempty"`
	Pattern          string                                   `json:"pattern,omitempty"`
	GrepPattern      string                                   `json:"grepPattern,omitempty"`
	Glob             string                                   `json:"glob,omitempty"`
	Offset           int                                      `json:"offset,omitempty"`
	Limit            int                                      `json:"limit,omitempty"`
	FileType         string                                   `json:"fileType,omitempty"`
	Content          string                                   `json:"content,omitempty"`
	OldString        string                                   `json:"oldString,omitempty"`
	NewString        string                                   `json:"newString,omitempty"`
	ReplaceAll       bool                                     `json:"replaceAll,omitempty"`
	ExpectedVersion  string                                   `json:"expectedVersion,omitempty"`
	Changes          []projectAssistantSandboxWorkspaceChange `json:"changes,omitempty"`
	SourceRevision   uint64                                   `json:"sourceRevision,omitempty"`
	SourceDigest     string                                   `json:"sourceDigest,omitempty"`
	BaselineFiles    []projectAssistantSandboxBaselineFile    `json:"baselineFiles,omitempty"`
	CheckpointID     string                                   `json:"checkpointID,omitempty"`
	CheckpointAction string                                   `json:"checkpointAction,omitempty"`
}

type projectAssistantSandboxBaselineFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type projectAssistantSandboxWorkspaceResponse struct {
	Status         string                                   `json:"status,omitempty"`
	File           workspace.FileContent                    `json:"file,omitempty"`
	Files          workspace.FileList                       `json:"files,omitempty"`
	Matches        []projectAssistantSandboxGrepMatch       `json:"matches,omitempty"`
	Mutation       workspace.MutationResult                 `json:"mutation,omitempty"`
	Changes        []projectAssistantSandboxWorkspaceChange `json:"changes,omitempty"`
	SourceRevision uint64                                   `json:"sourceRevision,omitempty"`
	SourceDigest   string                                   `json:"sourceDigest,omitempty"`
	Conflict       string                                   `json:"conflict,omitempty"`
	Entries        []projectAssistantSandboxListEntry       `json:"entries,omitempty"`
	CheckpointID   string                                   `json:"checkpointID,omitempty"`
}

type projectAssistantSandboxListEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

type projectAssistantSandboxGrepMatch struct {
	Content string `json:"content"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
}

// These wire types intentionally mirror the Infrastructure dev-agent
// workspace contract without importing provider-infrastructure code.  The
// internal request/response above remains App Studio's stable seam for fakes
// and for the tool registry.
type projectAssistantSandboxListWireRequest struct {
	Path       string `json:"path,omitempty"`
	Recursive  bool   `json:"recursive,omitempty"`
	MaxEntries int    `json:"maxEntries,omitempty"`
}

type projectAssistantSandboxListWireEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
	Mode uint32 `json:"mode,omitempty"`
}

type projectAssistantSandboxListWireResponse struct {
	Path           string                                 `json:"path"`
	Entries        []projectAssistantSandboxListWireEntry `json:"entries"`
	SourceRevision uint64                                 `json:"sourceRevision,omitempty"`
	SourceDigest   string                                 `json:"sourceDigest,omitempty"`
}

type projectAssistantSandboxSeedWireResponse struct {
	Phase          string `json:"phase"`
	SourceRevision uint64 `json:"sourceRevision"`
	SourceDigest   string `json:"sourceDigest"`
}

type projectAssistantSandboxReadWireRequest struct {
	Paths    []string `json:"paths"`
	MaxBytes int      `json:"maxBytes,omitempty"`
}

type projectAssistantSandboxReadWireFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Bytes   int    `json:"bytes"`
	Digest  string `json:"digest"`
}

type projectAssistantSandboxReadWireResponse struct {
	Files          []projectAssistantSandboxReadWireFile `json:"files"`
	SourceRevision uint64                                `json:"sourceRevision,omitempty"`
	SourceDigest   string                                `json:"sourceDigest,omitempty"`
}

type projectAssistantSandboxMutateWireOperation struct {
	Operation string `json:"op"`
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
}

type projectAssistantSandboxMutateWireRequest struct {
	ExpectedRevision uint64                                       `json:"expectedRevision"`
	ExpectedDigest   string                                       `json:"expectedDigest"`
	Operations       []projectAssistantSandboxMutateWireOperation `json:"operations"`
	Restart          string                                       `json:"restart,omitempty"`
}

type projectAssistantSandboxMutateWireResponse struct {
	Phase          string   `json:"phase"`
	Changed        []string `json:"changed,omitempty"`
	Deleted        []string `json:"deleted,omitempty"`
	Restarted      bool     `json:"restarted,omitempty"`
	SourceRevision uint64   `json:"sourceRevision"`
	SourceDigest   string   `json:"sourceDigest"`
}

type projectAssistantSandboxDiffWireRequest struct {
	CheckpointID     string `json:"checkpointID,omitempty"`
	ExpectedRevision uint64 `json:"expectedRevision,omitempty"`
	ExpectedDigest   string `json:"expectedDigest,omitempty"`
}

type projectAssistantSandboxDiffWireChange struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	BeforeDigest string `json:"beforeDigest,omitempty"`
	AfterDigest  string `json:"afterDigest,omitempty"`
	BeforeBytes  int    `json:"beforeBytes,omitempty"`
	AfterBytes   int    `json:"afterBytes,omitempty"`
}

type projectAssistantSandboxDiffWireResponse struct {
	BaseRevision   uint64                                  `json:"baseRevision,omitempty"`
	BaseDigest     string                                  `json:"baseDigest,omitempty"`
	SourceRevision uint64                                  `json:"sourceRevision"`
	SourceDigest   string                                  `json:"sourceDigest"`
	Changes        []projectAssistantSandboxDiffWireChange `json:"changes"`
}

type projectAssistantSandboxCheckpointWireRequest struct {
	Action           string `json:"action,omitempty"`
	ID               string `json:"id,omitempty"`
	Label            string `json:"label,omitempty"`
	ExpectedRevision uint64 `json:"expectedRevision,omitempty"`
	ExpectedDigest   string `json:"expectedDigest,omitempty"`
}

type projectAssistantSandboxCheckpointWireSummary struct {
	ID             string `json:"id"`
	Label          string `json:"label,omitempty"`
	SourceRevision uint64 `json:"sourceRevision"`
	SourceDigest   string `json:"sourceDigest"`
	FileCount      int    `json:"fileCount"`
}

type projectAssistantSandboxCheckpointWireResponse struct {
	Action         string                                        `json:"action"`
	Checkpoint     *projectAssistantSandboxCheckpointWireSummary `json:"checkpoint,omitempty"`
	SourceRevision uint64                                        `json:"sourceRevision,omitempty"`
	SourceDigest   string                                        `json:"sourceDigest,omitempty"`
}

// projectAssistantSandboxClient is the only worker-facing abstraction.  The
// implementation uses the existing infrastructure data-plane client, while
// tests and a future alternate transport can provide a focused fake.
type projectAssistantSandboxClient interface {
	Workspace(context.Context, identity, dataPlaneRef, projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error)
	Exec(context.Context, identity, dataPlaneRef, projectSandboxExecRequest) (projectSandboxExecResponse, error)
}

type projectAssistantDataPlaneSandboxClient struct{ server *Server }

func (c projectAssistantDataPlaneSandboxClient) Workspace(ctx context.Context, id identity, ref dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	if c.server == nil {
		return projectAssistantSandboxWorkspaceResponse{}, errors.New("assistant sandbox server is not configured")
	}
	switch strings.ToLower(strings.TrimSpace(request.Action)) {
	case "seed":
		return c.workspaceSeed(ctx, id, ref, request)
	case "list":
		return c.workspaceList(ctx, id, ref, request)
	case "read":
		return c.workspaceRead(ctx, id, ref, request)
	case "glob":
		return c.workspaceGlob(ctx, id, ref, request)
	case "grep":
		return c.workspaceGrep(ctx, id, ref, request)
	case "create", "replace", "edit", "delete", "move":
		return c.workspaceMutate(ctx, id, ref, request)
	case "checkpoint":
		if strings.EqualFold(strings.TrimSpace(request.CheckpointAction), "create") {
			return c.workspaceCheckpointCreate(ctx, id, ref, request)
		}
		return c.workspaceCheckpointDiff(ctx, id, ref, request)
	default:
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: unsupported workspace action %q", errProjectAssistantRunSandboxConflict, request.Action)
	}
}

func (c projectAssistantDataPlaneSandboxClient) workspaceSeed(ctx context.Context, id identity, ref dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	// Deliberately omit sourceRevision/sourceDigest. The worker owns its
	// monotonic revision domain and advances the currently applied manifest
	// while recomputing the digest from this complete authoritative snapshot.
	body, status, err := c.workspaceCall(ctx, id, ref, "seed", struct {
		Files   []projectSandboxSyncFile `json:"files"`
		Restart string                   `json:"restart,omitempty"`
	}{Files: request.Files, Restart: "auto"}, 1<<20)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return projectAssistantSandboxWorkspaceResponse{}, sandboxWorkspaceHTTPError("seed", status, body)
	}
	var wire projectAssistantSandboxSeedWireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("decode sandbox workspace seed response: %w", err)
	}
	return projectAssistantSandboxWorkspaceResponse{
		Status:         strings.ToLower(strings.TrimSpace(wire.Phase)),
		SourceRevision: wire.SourceRevision,
		SourceDigest:   sandboxSourceDigest(wire.SourceDigest),
	}, nil
}

func (c projectAssistantDataPlaneSandboxClient) workspaceCall(ctx context.Context, id identity, ref dataPlaneRef, operation string, payload any, maxBytes int64) ([]byte, int, error) {
	if c.server == nil {
		return nil, 0, errors.New("assistant sandbox server is not configured")
	}
	if strings.TrimSpace(operation) == "" {
		return nil, 0, errors.New("assistant sandbox workspace operation is required")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("encode sandbox workspace %s request: %w", operation, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, dataPlaneCallTimeout)
	defer cancel()
	req, err := c.server.newDataPlaneRequest(callCtx, http.MethodPost, id, ref, projectAssistantRunSandboxWorkspaceVerb, "/"+operation, bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.server.sandboxDataPlaneClient(dataPlaneCallTimeout).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("sandbox workspace %s: %w", operation, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if maxBytes <= 0 {
		maxBytes = 16 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read sandbox workspace %s response: %w", operation, err)
	}
	if int64(len(body)) > maxBytes {
		return nil, resp.StatusCode, fmt.Errorf("sandbox workspace %s response exceeds %d bytes", operation, maxBytes)
	}
	return body, resp.StatusCode, nil
}

func sandboxWorkspaceHTTPError(operation string, status int, body []byte) error {
	message := strings.TrimSpace(truncateProjectToolInfo(string(body)))
	if status == http.StatusConflict {
		if message == "" {
			message = "remote workspace revision or digest no longer matches"
		}
		return fmt.Errorf("%w: %s", errProjectAssistantRunSandboxConflict, message)
	}
	if status == http.StatusBadGateway || status == http.StatusServiceUnavailable {
		return &projectDevelopmentSyncHTTPError{component: operation, status: status, detail: message}
	}
	return fmt.Errorf("sandbox workspace %s endpoint returned %d: %s", operation, status, truncateProjectToolInfo(string(body)))
}

func sandboxSourceDigest(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return "sha256:" + strings.TrimPrefix(raw, "sha256:")
}

func sandboxDigestEqual(left, right string) bool {
	return strings.TrimPrefix(strings.TrimSpace(left), "sha256:") == strings.TrimPrefix(strings.TrimSpace(right), "sha256:")
}

func sandboxFileVersion(rawDigest string) string {
	if strings.TrimSpace(rawDigest) == "" {
		return ""
	}
	return sandboxSourceDigest(rawDigest)
}

func sandboxWorkspacePath(raw string, directory bool) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if directory && (raw == "" || raw == "." || raw == "/") {
		return ".", nil
	}
	raw = strings.TrimPrefix(raw, "/")
	if raw == "" {
		return "", errors.New("workspace path cannot be empty")
	}
	return workspace.CleanProjectPath(raw)
}

func (c projectAssistantDataPlaneSandboxClient) workspaceList(ctx context.Context, id identity, ref dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	base, err := sandboxWorkspacePath(request.Path, true)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	limit := request.Limit
	if limit <= 0 || limit > workspace.MaxListLimit {
		limit = workspace.MaxListLimit
	}
	body, status, err := c.workspaceCall(ctx, id, ref, "list", projectAssistantSandboxListWireRequest{Path: base, Recursive: true, MaxEntries: limit}, 1<<20)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return projectAssistantSandboxWorkspaceResponse{}, sandboxWorkspaceHTTPError("list", status, body)
	}
	var wire projectAssistantSandboxListWireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("decode sandbox workspace list response: %w", err)
	}
	response := projectAssistantSandboxWorkspaceResponse{
		Status:         "ok",
		SourceRevision: wire.SourceRevision,
		SourceDigest:   sandboxSourceDigest(wire.SourceDigest),
		Entries:        make([]projectAssistantSandboxListEntry, 0, len(wire.Entries)),
		Files:          workspace.FileList{Files: make([]workspace.FileInfo, 0, len(wire.Entries)), Limit: limit},
	}
	for _, entry := range wire.Entries {
		response.Entries = append(response.Entries, projectAssistantSandboxListEntry{Path: entry.Path, Type: entry.Type, Size: entry.Size})
		if strings.EqualFold(entry.Type, "file") {
			response.Files.Files = append(response.Files.Files, workspace.FileInfo{Path: entry.Path, Size: entry.Size})
		}
	}
	response.Files.Truncated = len(wire.Entries) >= limit
	return response, nil
}

func (c projectAssistantDataPlaneSandboxClient) workspaceReadFiles(ctx context.Context, id identity, ref dataPlaneRef, paths []string) (map[string]projectAssistantSandboxReadWireFile, uint64, string, int, error) {
	if len(paths) == 0 {
		return map[string]projectAssistantSandboxReadWireFile{}, 0, "", http.StatusOK, nil
	}
	cleanPaths := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, raw := range paths {
		clean, err := sandboxWorkspacePath(raw, false)
		if err != nil {
			return nil, 0, "", 0, err
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		cleanPaths = append(cleanPaths, clean)
	}
	sort.Strings(cleanPaths)
	body, status, err := c.workspaceCall(ctx, id, ref, "read", projectAssistantSandboxReadWireRequest{Paths: cleanPaths, MaxBytes: 4 << 20}, 5<<20)
	if err != nil {
		return nil, 0, "", status, err
	}
	if status == http.StatusNotFound {
		return nil, 0, "", status, nil
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, 0, "", status, sandboxWorkspaceHTTPError("read", status, body)
	}
	var wire projectAssistantSandboxReadWireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, 0, "", status, fmt.Errorf("decode sandbox workspace read response: %w", err)
	}
	files := make(map[string]projectAssistantSandboxReadWireFile, len(wire.Files))
	for _, file := range wire.Files {
		clean, err := sandboxWorkspacePath(file.Path, false)
		if err != nil {
			return nil, 0, "", status, err
		}
		files[clean] = file
	}
	return files, wire.SourceRevision, sandboxSourceDigest(wire.SourceDigest), status, nil
}

func (c projectAssistantDataPlaneSandboxClient) workspaceRead(ctx context.Context, id identity, ref dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	clean, err := sandboxWorkspacePath(request.Path, false)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	files, revision, digest, status, err := c.workspaceReadFiles(ctx, id, ref, []string{clean})
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	if status == http.StatusNotFound || len(files) == 0 {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("sandbox workspace file %q was not found", clean)
	}
	file, ok := files[clean]
	if !ok {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("sandbox workspace read omitted %q", clean)
	}
	return projectAssistantSandboxWorkspaceResponse{
		Status: "ok", SourceRevision: revision, SourceDigest: digest,
		File: workspace.FileContent{Path: clean, Content: file.Content, Size: int64(file.Bytes), Version: sandboxFileVersion(file.Digest)},
	}, nil
}

func sandboxGlobMatch(pattern, candidate string) bool {
	pattern = strings.TrimPrefix(strings.TrimSpace(pattern), "/")
	candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "/")
	if pattern == "" {
		return true
	}
	var expression strings.Builder
	expression.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					expression.WriteString("(?:.*/)?")
				} else {
					expression.WriteString(".*")
				}
			} else {
				expression.WriteString("[^/]*")
			}
		case '?':
			expression.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				return false
			}
			expression.WriteByte('[')
			expression.WriteString(pattern[i+1 : i+1+end])
			expression.WriteByte(']')
			i += end + 1
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	expression.WriteString("$")
	matched, err := regexp.MatchString(expression.String(), candidate)
	return err == nil && matched
}

func (c projectAssistantDataPlaneSandboxClient) workspaceGlob(ctx context.Context, id identity, ref dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	listed, err := c.workspaceList(ctx, id, ref, projectAssistantSandboxWorkspaceRequest{Path: request.Path, Limit: workspace.MaxListLimit})
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	filtered := projectAssistantSandboxWorkspaceResponse{Status: "ok", SourceRevision: listed.SourceRevision, SourceDigest: listed.SourceDigest, Files: workspace.FileList{Limit: workspace.MaxListLimit}}
	for _, entry := range listed.Entries {
		if !strings.EqualFold(entry.Type, "file") || !sandboxGlobMatch(request.Pattern, entry.Path) {
			continue
		}
		filtered.Entries = append(filtered.Entries, entry)
		filtered.Files.Files = append(filtered.Files.Files, workspace.FileInfo{Path: entry.Path, Size: entry.Size})
	}
	return filtered, nil
}

func sandboxFileTypeMatch(filePath, fileType string) bool {
	fileType = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileType)), ".")
	if fileType == "" {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(filePath)), ".")
	return ext == fileType
}

func (c projectAssistantDataPlaneSandboxClient) workspaceGrep(ctx context.Context, id identity, ref dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	pattern := request.GrepPattern
	if strings.TrimSpace(pattern) == "" {
		return projectAssistantSandboxWorkspaceResponse{}, errors.New("grep pattern is required")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("invalid grep pattern: %w", err)
	}
	listed, err := c.workspaceList(ctx, id, ref, projectAssistantSandboxWorkspaceRequest{Path: request.Path, Limit: workspace.MaxListLimit})
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	paths := make([]string, 0, len(listed.Entries))
	for _, entry := range listed.Entries {
		if strings.EqualFold(entry.Type, "file") && sandboxFileTypeMatch(entry.Path, request.FileType) && sandboxGlobMatch(request.Glob, entry.Path) {
			paths = append(paths, entry.Path)
		}
	}
	files, revision, digest, _, err := c.workspaceReadFiles(ctx, id, ref, paths)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	response := projectAssistantSandboxWorkspaceResponse{Status: "ok", SourceRevision: revision, SourceDigest: digest}
	for _, filePath := range paths {
		file, ok := files[filePath]
		if !ok {
			continue
		}
		for lineNumber, line := range strings.Split(file.Content, "\n") {
			if !re.MatchString(line) {
				continue
			}
			response.Matches = append(response.Matches, projectAssistantSandboxGrepMatch{Content: line, Path: filePath, Line: lineNumber + 1})
			if len(response.Matches) >= projectAssistantRunSandboxMaxChanges*8 {
				return response, nil
			}
		}
	}
	return response, nil
}

func (c projectAssistantDataPlaneSandboxClient) workspaceReadForMutation(ctx context.Context, id identity, ref dataPlaneRef, rawPath string) (projectAssistantSandboxReadWireFile, bool, error) {
	path, err := sandboxWorkspacePath(rawPath, false)
	if err != nil {
		return projectAssistantSandboxReadWireFile{}, false, err
	}
	files, _, _, status, err := c.workspaceReadFiles(ctx, id, ref, []string{path})
	if err != nil {
		return projectAssistantSandboxReadWireFile{}, false, err
	}
	if status == http.StatusNotFound {
		return projectAssistantSandboxReadWireFile{}, false, nil
	}
	file, ok := files[path]
	return file, ok, nil
}

func (c projectAssistantDataPlaneSandboxClient) workspaceMutate(ctx context.Context, id identity, ref dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	if request.SourceRevision == 0 || strings.TrimSpace(request.SourceDigest) == "" {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: remote source revision and digest are required", errProjectAssistantRunSandboxConflict)
	}
	operations := make([]projectAssistantSandboxMutateWireOperation, 0, 2)
	mutation := workspace.MutationResult{Operation: strings.TrimSpace(request.Action) + "_file", Changed: true}
	pathForResult := request.Path
	if request.Action == "move" {
		pathForResult = request.DestinationPath
	}
	cleanPath, err := sandboxWorkspacePath(pathForResult, false)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	mutation.Path = cleanPath
	if request.Action == "move" {
		source, sourceExists, err := c.workspaceReadForMutation(ctx, id, ref, request.SourcePath)
		if err != nil {
			return projectAssistantSandboxWorkspaceResponse{}, err
		}
		if !sourceExists {
			return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("workspace source %q does not exist", request.SourcePath)
		}
		if request.ExpectedVersion != "" && !sandboxDigestEqual(request.ExpectedVersion, source.Digest) {
			return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: workspace source %q changed", errProjectAssistantRunSandboxConflict, request.SourcePath)
		}
		_, destinationExists, err := c.workspaceReadForMutation(ctx, id, ref, request.DestinationPath)
		if err != nil {
			return projectAssistantSandboxWorkspaceResponse{}, err
		}
		if destinationExists {
			return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("workspace destination %q already exists", request.DestinationPath)
		}
		destination, err := sandboxWorkspacePath(request.DestinationPath, false)
		if err != nil {
			return projectAssistantSandboxWorkspaceResponse{}, err
		}
		operations = append(operations,
			projectAssistantSandboxMutateWireOperation{Operation: "write", Path: destination, Content: source.Content},
			projectAssistantSandboxMutateWireOperation{Operation: "delete", Path: source.Path},
		)
		mutation.PreviousPath = source.Path
		mutation.Paths = []string{source.Path, destination}
	} else {
		current, exists, err := c.workspaceReadForMutation(ctx, id, ref, request.Path)
		if err != nil {
			return projectAssistantSandboxWorkspaceResponse{}, err
		}
		if request.Action == "create" && exists {
			return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("workspace file %q already exists", request.Path)
		}
		if request.Action != "create" && !exists {
			return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("workspace file %q does not exist", request.Path)
		}
		if request.ExpectedVersion != "" && (!exists || !sandboxDigestEqual(request.ExpectedVersion, current.Digest)) {
			return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: workspace file %q changed", errProjectAssistantRunSandboxConflict, request.Path)
		}
		cleanPath, err := sandboxWorkspacePath(request.Path, false)
		if err != nil {
			return projectAssistantSandboxWorkspaceResponse{}, err
		}
		content := request.Content
		if request.Action == "edit" {
			if request.OldString == "" {
				return projectAssistantSandboxWorkspaceResponse{}, errors.New("oldString cannot be empty")
			}
			count := strings.Count(current.Content, request.OldString)
			if count == 0 {
				return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("workspace oldString was not found in %q", cleanPath)
			}
			if count > 1 && !request.ReplaceAll {
				return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("workspace oldString matched %d times in %q", count, cleanPath)
			}
			if request.ReplaceAll {
				content = strings.ReplaceAll(current.Content, request.OldString, request.NewString)
			} else {
				content = strings.Replace(current.Content, request.OldString, request.NewString, 1)
			}
			mutation.Replacements = count
		}
		if request.Action == "delete" {
			operations = append(operations, projectAssistantSandboxMutateWireOperation{Operation: "delete", Path: cleanPath})
		} else {
			operations = append(operations, projectAssistantSandboxMutateWireOperation{Operation: "write", Path: cleanPath, Content: content})
		}
		mutation.Paths = []string{cleanPath}
		if request.Action == "delete" {
			mutation.Size = 0
		} else {
			mutation.Size = int64(len([]byte(content)))
		}
	}
	payload := projectAssistantSandboxMutateWireRequest{ExpectedRevision: request.SourceRevision, ExpectedDigest: request.SourceDigest, Operations: operations}
	body, status, err := c.workspaceCall(ctx, id, ref, "mutate", payload, 2<<20)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return projectAssistantSandboxWorkspaceResponse{}, sandboxWorkspaceHTTPError("mutate", status, body)
	}
	var wire projectAssistantSandboxMutateWireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("decode sandbox workspace mutate response: %w", err)
	}
	mutation.Changed = len(wire.Changed) > 0 || len(wire.Deleted) > 0
	return projectAssistantSandboxWorkspaceResponse{
		Status: "ok", Mutation: mutation, SourceRevision: wire.SourceRevision, SourceDigest: sandboxSourceDigest(wire.SourceDigest),
	}, nil
}

func (c projectAssistantDataPlaneSandboxClient) workspaceCheckpointCreate(ctx context.Context, id identity, ref dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	payload := projectAssistantSandboxCheckpointWireRequest{Action: "create", ID: request.CheckpointID, Label: "app-studio-run-sandbox", ExpectedRevision: request.SourceRevision, ExpectedDigest: request.SourceDigest}
	body, status, err := c.workspaceCall(ctx, id, ref, "checkpoint", payload, 1<<20)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return projectAssistantSandboxWorkspaceResponse{}, sandboxWorkspaceHTTPError("checkpoint", status, body)
	}
	var wire projectAssistantSandboxCheckpointWireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("decode sandbox workspace checkpoint response: %w", err)
	}
	if wire.Checkpoint == nil || strings.TrimSpace(wire.Checkpoint.ID) == "" {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: checkpoint endpoint returned no durable checkpoint ID", errProjectAssistantRunSandboxConflict)
	}
	revision := wire.SourceRevision
	if revision == 0 {
		revision = wire.Checkpoint.SourceRevision
	}
	digest := sandboxSourceDigest(wire.SourceDigest)
	if digest == "" {
		digest = sandboxSourceDigest(wire.Checkpoint.SourceDigest)
	}
	return projectAssistantSandboxWorkspaceResponse{Status: "ok", CheckpointID: wire.Checkpoint.ID, SourceRevision: revision, SourceDigest: digest}, nil
}

func (c projectAssistantDataPlaneSandboxClient) workspaceCheckpointDiff(ctx context.Context, id identity, ref dataPlaneRef, request projectAssistantSandboxWorkspaceRequest) (projectAssistantSandboxWorkspaceResponse, error) {
	if strings.TrimSpace(request.CheckpointID) == "" {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: remote baseline checkpoint is missing", errProjectAssistantRunSandboxConflict)
	}
	body, status, err := c.workspaceCall(ctx, id, ref, "diff", projectAssistantSandboxDiffWireRequest{CheckpointID: request.CheckpointID, ExpectedRevision: request.SourceRevision, ExpectedDigest: request.SourceDigest}, 2<<20)
	if err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return projectAssistantSandboxWorkspaceResponse{}, sandboxWorkspaceHTTPError("diff", status, body)
	}
	var wire projectAssistantSandboxDiffWireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("decode sandbox workspace diff response: %w", err)
	}
	if len(wire.Changes) > projectAssistantRunSandboxMaxChanges {
		return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: checkpoint contains too many files", errProjectAssistantRunSandboxConflict)
	}
	readPaths := make([]string, 0, len(wire.Changes))
	for _, change := range wire.Changes {
		if change.Kind == "added" || change.Kind == "modified" {
			readPaths = append(readPaths, change.Path)
		}
	}
	files := map[string]projectAssistantSandboxReadWireFile{}
	if len(readPaths) > 0 {
		var readRevision uint64
		var readDigest string
		files, readRevision, readDigest, _, err = c.workspaceReadFiles(ctx, id, ref, readPaths)
		if err != nil {
			return projectAssistantSandboxWorkspaceResponse{}, err
		}
		// Diff and content reads are separate worker calls. Import only content
		// proven to belong to the exact diff fence; never replace the first call's
		// evidence with a later read that may have observed another mutation.
		if readRevision != wire.SourceRevision || !sandboxDigestEqual(readDigest, wire.SourceDigest) {
			return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: workspace changed between checkpoint diff and content read", errProjectAssistantRunSandboxConflict)
		}
	}
	response := projectAssistantSandboxWorkspaceResponse{Status: "ok", CheckpointID: request.CheckpointID, SourceRevision: wire.SourceRevision, SourceDigest: sandboxSourceDigest(wire.SourceDigest)}
	for _, change := range wire.Changes {
		operation := string(workspace.ManagedFileReplace)
		content := ""
		expectedVersion := sandboxFileVersion(change.BeforeDigest)
		switch change.Kind {
		case "added":
			operation = string(workspace.ManagedFileCreate)
			file, ok := files[change.Path]
			if !ok || strings.TrimSpace(change.AfterDigest) == "" || !sandboxDigestEqual(file.Digest, change.AfterDigest) {
				return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: diff added file %q was not returned with the proven content digest", errProjectAssistantRunSandboxConflict, change.Path)
			}
			content = file.Content
		case "modified":
			file, ok := files[change.Path]
			if !ok || expectedVersion == "" || strings.TrimSpace(change.AfterDigest) == "" || !sandboxDigestEqual(file.Digest, change.AfterDigest) {
				return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: diff modified file %q is missing proven content or baseline evidence", errProjectAssistantRunSandboxConflict, change.Path)
			}
			content = file.Content
		case "deleted":
			operation = string(workspace.ManagedFileDelete)
			if expectedVersion == "" {
				return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: diff deleted file %q is missing baseline version", errProjectAssistantRunSandboxConflict, change.Path)
			}
		default:
			return projectAssistantSandboxWorkspaceResponse{}, fmt.Errorf("%w: unsupported remote diff kind %q", errProjectAssistantRunSandboxConflict, change.Kind)
		}
		response.Changes = append(response.Changes, projectAssistantSandboxWorkspaceChange{Path: change.Path, Operation: operation, Content: content, ExpectedVersion: expectedVersion})
	}
	return response, nil
}

func (c projectAssistantDataPlaneSandboxClient) Exec(ctx context.Context, id identity, ref dataPlaneRef, request projectSandboxExecRequest) (projectSandboxExecResponse, error) {
	if c.server == nil {
		return projectSandboxExecResponse{}, errors.New("assistant sandbox server is not configured")
	}
	return projectAssistantExecCall(ctx, c.server, id, ref, request)
}
