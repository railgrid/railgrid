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

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	einofs "github.com/cloudwego/eino/adk/filesystem"
)

const (
	maxEinoBackendAggregateBytes = 16 << 20
	maxEinoBackendMatches        = 1000
	maxEinoBackendRawMatches     = 10000
	einoBackendCandidateMarker   = "__app_studio_eino_candidate__"
)

var errEinoReadOnlyWorkspace = errors.New("App Studio project filesystem backend is read-only")

// EinoReadOnlyBackend exposes one App Studio project through Eino's filesystem
// interface without granting filesystem mutation capabilities.
type EinoReadOnlyBackend struct {
	store *FileStore
	scope Scope
}

// NewEinoReadOnlyBackend returns an Eino filesystem backend scoped to exactly
// one configured App Studio project.
func NewEinoReadOnlyBackend(store *FileStore, scope Scope) (*EinoReadOnlyBackend, error) {
	if store == nil {
		return nil, errors.New("project workspace store is not configured")
	}
	if _, err := store.scopeDir(scope); err != nil {
		return nil, err
	}
	if err := validateEinoScopeDirectories(store, scope); err != nil {
		return nil, err
	}
	return &EinoReadOnlyBackend{store: store, scope: scope}, nil
}

func (b *EinoReadOnlyBackend) projectFilesMatching(
	ctx context.Context,
	match func(context.Context, FileInfo) (bool, error),
) ([]FileInfo, error) {
	if err := validateEinoScopeDirectories(b.store, b.scope); err != nil {
		return nil, err
	}
	dir, err := b.store.scopeDir(b.scope)
	if err != nil {
		return nil, err
	}
	files := make([]FileInfo, 0)
	err = b.store.walkFiles(ctx, dir, func(file FileInfo) error {
		matched, err := match(ctx, file)
		if err != nil {
			return err
		}
		if !matched {
			return nil
		}
		if len(files) >= MaxListLimit {
			return fmt.Errorf(
				"request matches more than %d files; narrow path or glob (or file type for grep)",
				MaxListLimit,
			)
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func validateEinoScopeDirectories(store *FileStore, scope Scope) error {
	current := filepath.Clean(store.Root())
	info, err := os.Lstat(current)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat workspace root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("project workspace root is a symlink")
	}
	if !info.IsDir() {
		return errors.New("project workspace root is not a directory")
	}
	for _, component := range []string{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName} {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stat workspace scope %q: %w", component, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace scope component %q is a symlink", component)
		}
		if !info.IsDir() {
			return fmt.Errorf("workspace scope component %q is not a directory", component)
		}
	}
	return nil
}

func cleanEinoDirectoryPath(raw string) (string, error) {
	if raw == "" || raw == "." {
		return "/", nil
	}
	clean, err := cleanProjectPath(raw)
	if err != nil {
		return "", err
	}
	return "/" + clean, nil
}

func cleanEinoGlobPattern(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("glob pattern cannot be empty")
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("glob pattern %q must be relative", raw)
	}
	if _, err := cleanProjectPath(raw); err != nil {
		return "", err
	}
	return raw, nil
}

func einoMetadataSnapshot(ctx context.Context, files []FileInfo) (*einofs.InMemoryBackend, error) {
	snapshot := einofs.NewInMemoryBackend()
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := snapshot.Write(ctx, &einofs.WriteRequest{
			FilePath: "/" + file.Path,
			Content:  einoBackendCandidateMarker,
		}); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func einoPathContainsFile(basePath, filePath string) bool {
	absoluteFilePath := "/" + filePath
	return basePath == "/" ||
		absoluteFilePath == basePath ||
		strings.HasPrefix(absoluteFilePath, basePath+"/")
}

func validateEinoGlobPattern(ctx context.Context, pattern string) error {
	snapshot, err := einoMetadataSnapshot(ctx, []FileInfo{{Path: einoBackendCandidateMarker}})
	if err != nil {
		return err
	}
	_, err = snapshot.GlobInfo(ctx, &einofs.GlobInfoRequest{
		Path:    "/",
		Pattern: pattern,
	})
	return err
}

func einoGlobMatchesFile(
	ctx context.Context,
	file FileInfo,
	basePath string,
	pattern string,
) (bool, error) {
	snapshot, err := einoMetadataSnapshot(ctx, []FileInfo{file})
	if err != nil {
		return false, err
	}
	infos, err := snapshot.GlobInfo(ctx, &einofs.GlobInfoRequest{
		Path:    basePath,
		Pattern: pattern,
	})
	if err != nil {
		return false, err
	}
	return len(infos) > 0, nil
}

func einoGrepMatchesFile(
	ctx context.Context,
	file FileInfo,
	req *einofs.GrepRequest,
) (bool, error) {
	snapshot, err := einoMetadataSnapshot(ctx, []FileInfo{file})
	if err != nil {
		return false, err
	}
	matches, err := snapshot.GrepRaw(ctx, req)
	if err != nil {
		return false, err
	}
	return len(matches) > 0, nil
}

// validateEinoGrepGlob delegates glob syntax validation to Eino even when the
// project has no files (or none below the requested path).
func validateEinoGrepGlob(ctx context.Context, glob string) error {
	if glob == "" {
		return nil
	}
	snapshot := einofs.NewInMemoryBackend()
	if err := snapshot.Write(ctx, &einofs.WriteRequest{
		FilePath: "/" + einoBackendCandidateMarker,
		Content:  einoBackendCandidateMarker,
	}); err != nil {
		return err
	}
	_, err := snapshot.GrepRaw(ctx, &einofs.GrepRequest{
		Pattern: regexp.QuoteMeta(einoBackendCandidateMarker),
		Glob:    glob,
	})
	return err
}

func (b *EinoReadOnlyBackend) GlobInfo(ctx context.Context, req *einofs.GlobInfoRequest) ([]einofs.FileInfo, error) {
	if req == nil {
		return nil, errors.New("glob request is required")
	}
	basePath, err := cleanEinoDirectoryPath(req.Path)
	if err != nil {
		return nil, err
	}
	pattern, err := cleanEinoGlobPattern(req.Pattern)
	if err != nil {
		return nil, err
	}
	if err := validateEinoGlobPattern(ctx, pattern); err != nil {
		return nil, err
	}
	files, err := b.projectFilesMatching(ctx, func(ctx context.Context, file FileInfo) (bool, error) {
		return einoGlobMatchesFile(ctx, file, basePath, pattern)
	})
	if err != nil {
		return nil, err
	}
	snapshot, err := einoMetadataSnapshot(ctx, files)
	if err != nil {
		return nil, err
	}
	infos, err := snapshot.GlobInfo(ctx, &einofs.GlobInfoRequest{Path: basePath, Pattern: pattern})
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(files))
	for _, file := range files {
		sizes[file.Path] = file.Size
	}
	for i := range infos {
		if basePath != "/" {
			infos[i].Path = strings.TrimPrefix(basePath, "/") + "/" + infos[i].Path
		}
		infos[i].Path = strings.TrimPrefix(infos[i].Path, "/")
		infos[i].Size = sizes[infos[i].Path]
		infos[i].ModifiedAt = ""
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
	return infos, nil
}

func (b *EinoReadOnlyBackend) LsInfo(ctx context.Context, req *einofs.LsInfoRequest) ([]einofs.FileInfo, error) {
	if req == nil {
		return nil, errors.New("ls request is required")
	}
	basePath, err := cleanEinoDirectoryPath(req.Path)
	if err != nil {
		return nil, err
	}
	files, err := b.projectFilesMatching(ctx, func(_ context.Context, file FileInfo) (bool, error) {
		return einoPathContainsFile(basePath, file.Path), nil
	})
	if err != nil {
		return nil, err
	}
	snapshot, err := einoMetadataSnapshot(ctx, files)
	if err != nil {
		return nil, err
	}
	infos, err := snapshot.LsInfo(ctx, &einofs.LsInfoRequest{Path: basePath})
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(files))
	for _, file := range files {
		sizes[file.Path] = file.Size
	}
	for i := range infos {
		if !infos[i].IsDir {
			fullPath := infos[i].Path
			if basePath != "/" {
				fullPath = strings.TrimPrefix(basePath, "/") + "/" + fullPath
			}
			infos[i].Size = sizes[fullPath]
		}
		infos[i].ModifiedAt = ""
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
	return infos, nil
}

func (b *EinoReadOnlyBackend) Read(ctx context.Context, req *einofs.ReadRequest) (*einofs.FileContent, error) {
	if req == nil {
		return nil, errors.New("read request is required")
	}
	clean, err := cleanProjectPath(req.FilePath)
	if err != nil {
		return nil, err
	}
	if err := validateEinoScopeDirectories(b.store, b.scope); err != nil {
		return nil, err
	}
	file, err := b.store.ReadFile(ctx, b.scope, ReadOptions{Path: clean, MaxBytes: MaxReadMaxBytes})
	if err != nil {
		return nil, err
	}
	if file.Binary {
		return nil, fmt.Errorf("file %q is binary", file.Path)
	}
	if file.Truncated {
		return nil, fmt.Errorf("file %q is too large to read", file.Path)
	}
	snapshot := einofs.NewInMemoryBackend()
	if err := snapshot.Write(ctx, &einofs.WriteRequest{FilePath: "/" + clean, Content: file.Content}); err != nil {
		return nil, err
	}
	return snapshot.Read(ctx, &einofs.ReadRequest{FilePath: "/" + clean, Offset: req.Offset, Limit: req.Limit})
}

func (b *EinoReadOnlyBackend) GrepRaw(ctx context.Context, req *einofs.GrepRequest) ([]einofs.GrepMatch, error) {
	if req == nil {
		return nil, errors.New("grep request is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	basePath, err := cleanEinoDirectoryPath(req.Path)
	if err != nil {
		return nil, err
	}
	if _, err := compileEinoGrepPattern(req); err != nil {
		return nil, err
	}
	if err := validateEinoGrepGlob(ctx, req.Glob); err != nil {
		return nil, err
	}
	candidateReq := *req
	candidateReq.Path = basePath
	candidateReq.Pattern = regexp.QuoteMeta(einoBackendCandidateMarker)
	candidateReq.CaseInsensitive = false
	candidateReq.EnableMultiline = false
	candidateReq.BeforeLines = 0
	candidateReq.AfterLines = 0
	files, err := b.projectFilesMatching(ctx, func(ctx context.Context, file FileInfo) (bool, error) {
		return einoGrepMatchesFile(ctx, file, &candidateReq)
	})
	if err != nil {
		return nil, err
	}

	totalBytes := 0
	matches := make([]einofs.GrepMatch, 0)
	for _, candidate := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidatePath := candidate.Path
		file, err := b.store.ReadFile(ctx, b.scope, ReadOptions{Path: candidatePath, MaxBytes: MaxReadMaxBytes})
		if err != nil {
			return nil, err
		}
		if file.Binary {
			continue
		}
		if file.Truncated {
			return nil, fmt.Errorf("file %q is too large to search; narrow request", file.Path)
		}
		totalBytes += len([]byte(file.Content))
		if totalBytes > maxEinoBackendAggregateBytes {
			return nil, fmt.Errorf("search exceeds %d bytes; narrow request", maxEinoBackendAggregateBytes)
		}
		fileMatches, err := boundedEinoGrepFileLimit(ctx, file.Path, file.Content, req, maxEinoBackendMatches-len(matches))
		if err != nil {
			return nil, err
		}
		matches = append(matches, fileMatches...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		if matches[i].Line != matches[j].Line {
			return matches[i].Line < matches[j].Line
		}
		return matches[i].Content < matches[j].Content
	})
	return matches, nil
}

func boundedEinoGrepFile(ctx context.Context, path, content string, req *einofs.GrepRequest) ([]einofs.GrepMatch, error) {
	return boundedEinoGrepFileLimit(ctx, path, content, req, maxEinoBackendMatches)
}

func boundedEinoGrepFileLimit(ctx context.Context, path, content string, req *einofs.GrepRequest, limit int) ([]einofs.GrepMatch, error) {
	re, err := compileEinoGrepPattern(req)
	if err != nil {
		return nil, err
	}
	return boundedEinoGrepFileWithRegexp(ctx, path, content, req, re, limit)
}

func compileEinoGrepPattern(req *einofs.GrepRequest) (*regexp.Regexp, error) {
	if req.Pattern == "" {
		return nil, errors.New("pattern cannot be empty")
	}
	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}
	return re, nil
}

type einoLineIndex []int

func newEinoLineIndex(content string) einoLineIndex {
	var index einoLineIndex
	for offset := 0; offset < len(content); offset++ {
		if content[offset] == '\n' {
			index = append(index, offset)
		}
	}
	return index
}

func (i einoLineIndex) lineAtByteOffset(offset int) int {
	return sort.SearchInts(i, offset) + 1
}

func boundedEinoGrepFileWithRegexp(ctx context.Context, path, content string, req *einofs.GrepRequest, re *regexp.Regexp, limit int) ([]einofs.GrepMatch, error) {
	lines := strings.Split(content, "\n")
	results := make([]einofs.GrepMatch, 0)
	seenLines := make(map[int]struct{})
	appendMatchLine := func(line int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		before, after := 0, 0
		if req.BeforeLines > 0 {
			before = req.BeforeLines
		}
		if req.AfterLines > 0 {
			after = req.AfterLines
		}
		start, end := line, line
		if before > 0 || after > 0 {
			start -= before
			if start < 1 {
				start = 1
			}
			end += after
			if end > len(lines) {
				end = len(lines)
			}
		}
		for lineNumber := start; lineNumber <= end; lineNumber++ {
			if before > 0 || after > 0 {
				if _, ok := seenLines[lineNumber]; ok {
					continue
				}
				seenLines[lineNumber] = struct{}{}
			}
			if len(results) >= limit {
				return fmt.Errorf("search produced more than %d matches; narrow request", maxEinoBackendMatches)
			}
			results = append(results, einofs.GrepMatch{Path: path, Line: lineNumber, Content: lines[lineNumber-1]})
		}
		return nil
	}
	if req.EnableMultiline {
		indices := re.FindAllStringIndex(content, maxEinoBackendRawMatches+1)
		if len(indices) > maxEinoBackendRawMatches {
			return nil, fmt.Errorf("search produced more than %d raw matches; narrow request", maxEinoBackendRawMatches)
		}
		lineIndex := newEinoLineIndex(content)
		for _, index := range indices {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			startLine := lineIndex.lineAtByteOffset(index[0])
			endLine := lineIndex.lineAtByteOffset(index[1])
			for line := startLine; line <= endLine && line <= len(lines); line++ {
				if err := appendMatchLine(line); err != nil {
					return nil, err
				}
			}
		}
		return results, nil
	}
	for line, value := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if re.MatchString(value) {
			if err := appendMatchLine(line + 1); err != nil {
				return nil, err
			}
		}
	}
	return results, nil
}

func (b *EinoReadOnlyBackend) Write(context.Context, *einofs.WriteRequest) error {
	return errEinoReadOnlyWorkspace
}

func (b *EinoReadOnlyBackend) Edit(context.Context, *einofs.EditRequest) error {
	return errEinoReadOnlyWorkspace
}
