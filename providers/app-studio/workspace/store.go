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

// Package workspace stores App Studio project files in a provider-owned
// checkout/workspace directory.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	MaxProjectPathBytes = 1024
	DefaultListLimit    = 200
	MaxListLimit        = 500
	DefaultReadMaxBytes = 64 << 10
	MaxReadMaxBytes     = 256 << 10
	MaxWriteBytes       = MaxReadMaxBytes
	// MaxUnifiedPatchBytes bounds the complete model-authored patch envelope.
	// Individual resulting files remain bounded by MaxWriteBytes.
	MaxUnifiedPatchBytes      = MaxWriteBytes
	DefaultSearchLimit        = 50
	MaxSearchLimit            = 100
	MaxSearchFragmentBytes    = 240
	defaultSearchFragmentHits = 3
	workspaceTempFilePrefix   = ".workspace-write-"
)

var errStopWalk = errors.New("stop workspace walk")

// ErrMutationConflict means the workspace changed after the mutation baseline
// was read, so the requested write was not applied.
var ErrMutationConflict = errors.New("workspace file changed during edit")

// Scope identifies one App Studio project workspace incarnation. ProjectUID
// prevents a recreated Project from inheriting the deleted Project's files.
type Scope struct {
	OrgUUID       string
	WorkspaceUUID string
	ProjectName   string
	ProjectUID    string
}

// File is one UTF-8 text file to materialize into a project workspace.
type File struct {
	Path    string
	Content string
}

// MutationResult describes one workspace mutation.
type MutationResult struct {
	Operation    string           `json:"operation"`
	Path         string           `json:"path,omitempty"`
	PreviousPath string           `json:"previousPath,omitempty"`
	Paths        []string         `json:"paths,omitempty"`
	Size         int64            `json:"size,omitempty"`
	Replacements int              `json:"replacements,omitempty"`
	Additions    int              `json:"additions,omitempty"`
	Deletions    int              `json:"deletions,omitempty"`
	Patch        string           `json:"patch,omitempty"`
	Files        []MutationResult `json:"files,omitempty"`
}

// FileInfo describes one file in a project workspace.
type FileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size,omitempty"`
}

// FileList is a bounded list of files in a project workspace.
type FileList struct {
	Files     []FileInfo `json:"files"`
	Truncated bool       `json:"truncated,omitempty"`
	Limit     int        `json:"limit,omitempty"`
}

// ListOptions configures workspace file listing.
type ListOptions struct {
	Limit int
}

// ReadOptions configures a bounded file read.
type ReadOptions struct {
	Path     string
	MaxBytes int
}

// WriteOptions configures a whole-file workspace write.
type WriteOptions struct {
	Path    string
	Content string
}

// PatchOptions configures a contextual multi-file workspace patch.
type PatchOptions struct {
	Patch      string
	SnapshotID string
}

// FileContent is a bounded text-file read response.
type FileContent struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// SearchOptions configures text search over a project workspace.
type SearchOptions struct {
	Query      string
	MaxResults int
}

// SearchMatch is one workspace file search hit.
type SearchMatch struct {
	Path      string   `json:"path"`
	Fragments []string `json:"fragments,omitempty"`
}

// SearchResult reports bounded search hits.
type SearchResult struct {
	Query      string        `json:"query"`
	TotalCount int           `json:"totalCount"`
	Results    []SearchMatch `json:"results"`
	Truncated  bool          `json:"truncated,omitempty"`
	Limit      int           `json:"limit,omitempty"`
}

// FileStore stores workspaces under a root directory.
type FileStore struct {
	root       string
	mutationMu sync.Mutex
	// migrationMu serializes the one-time compatibility transition from the
	// pre-ProjectUID layout. It is intentionally separate from mutationMu so
	// path resolution can safely migrate data before a caller takes the
	// workspace mutation lock.
	migrationMu sync.Mutex
}

// NewFileStore returns a filesystem-backed project workspace store.
func NewFileStore(root string) *FileStore {
	return &FileStore{root: strings.TrimSpace(root)}
}

// CleanProjectPath returns the canonical workspace-relative path accepted by
// FileStore APIs.
func CleanProjectPath(raw string) (string, error) {
	return cleanProjectPath(raw)
}

// Root returns the filesystem root used by the store.
func (s *FileStore) Root() string {
	return s.root
}

// ApplyFiles writes files into the scoped project workspace.
func (s *FileStore) ApplyFiles(ctx context.Context, scope Scope, files []File) error {
	if s == nil {
		return errors.New("project workspace store is not configured")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.applyFiles(ctx, scope, files)
}

func (s *FileStore) applyFiles(ctx context.Context, scope Scope, files []File) error {
	dir, err := s.scopeDir(scope)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		clean, err := cleanProjectPath(f.Path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(clean))
		if err := ensureWithin(dir, target); err != nil {
			return err
		}
		if err := mkdirAllForFile(dir, clean); err != nil {
			return fmt.Errorf("create parent directory for %q: %w", clean, err)
		}
		if err := rejectSymlink(target, clean); err != nil {
			return err
		}
		if err := writeFileAtomically(filepath.Dir(target), target, []byte(f.Content), 0o644, false); err != nil {
			return fmt.Errorf("write %q: %w", clean, err)
		}
	}
	return nil
}

// WriteFile writes one bounded UTF-8 text file into the project workspace.
func (s *FileStore) WriteFile(ctx context.Context, scope Scope, opts WriteOptions) (MutationResult, error) {
	if s == nil {
		return MutationResult{}, errors.New("project workspace store is not configured")
	}
	if err := validateMutationContent(opts.Path, opts.Content); err != nil {
		return MutationResult{}, err
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	clean, _ := cleanProjectPath(opts.Path)
	before, _, err := s.readMutationTarget(ctx, scope, clean)
	if err != nil {
		return MutationResult{}, err
	}
	if err := s.applyFiles(ctx, scope, []File{{Path: clean, Content: opts.Content}}); err != nil {
		return MutationResult{}, err
	}
	return mutationResult("write_file", clean, before, opts.Content, 0), nil
}

// ApplyPatch applies one contextual patch to the project workspace.
func (s *FileStore) ApplyPatch(ctx context.Context, scope Scope, opts PatchOptions) (MutationResult, error) {
	return s.applyUnifiedPatch(ctx, scope, opts)
}

func writeFileAtomically(dir, target string, content []byte, mode fs.FileMode, createOnly bool) error {
	if !createOnly {
		info, err := os.Lstat(target)
		switch {
		case err == nil:
			mode = info.Mode().Perm()
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	file, err := os.CreateTemp(dir, workspaceTempFilePrefix+"*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer func() { _ = os.Remove(temp) }()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if createOnly {
		return os.Link(temp, target)
	}
	return os.Rename(temp, target)
}

func mutationResult(operation, filePath string, before []byte, after string, replacements int) MutationResult {
	beforeLines := difflib.SplitLines(string(before))
	afterLines := difflib.SplitLines(after)
	additions, deletions := 0, 0
	for _, op := range difflib.NewMatcher(beforeLines, afterLines).GetOpCodes() {
		switch op.Tag {
		case 'i':
			additions += op.J2 - op.J1
		case 'd':
			deletions += op.I2 - op.I1
		case 'r':
			additions += op.J2 - op.J1
			deletions += op.I2 - op.I1
		}
	}
	patch, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        beforeLines,
		B:        afterLines,
		FromFile: "a/" + filePath,
		ToFile:   "b/" + filePath,
		Context:  3,
	})
	return MutationResult{
		Operation:    operation,
		Path:         filePath,
		Size:         int64(len([]byte(after))),
		Replacements: replacements,
		Additions:    additions,
		Deletions:    deletions,
		Patch:        patch,
	}
}

// ListFiles lists regular files in the scoped project workspace.
func (s *FileStore) ListFiles(ctx context.Context, scope Scope, opts ListOptions) (FileList, error) {
	dir, err := s.scopeDir(scope)
	if err != nil {
		return FileList{}, err
	}
	limit := boundedPositive(opts.Limit, DefaultListLimit, MaxListLimit)
	files, err := s.allFiles(ctx, dir, limit+1)
	if err != nil {
		return FileList{}, err
	}
	truncated := len(files) > limit
	if truncated {
		files = files[:limit]
	}
	return FileList{Files: files, Truncated: truncated, Limit: limit}, nil
}

// ReadFile reads a bounded file from the scoped project workspace.
func (s *FileStore) ReadFile(ctx context.Context, scope Scope, opts ReadOptions) (FileContent, error) {
	dir, err := s.scopeDir(scope)
	if err != nil {
		return FileContent{}, err
	}
	clean, err := cleanProjectPath(opts.Path)
	if err != nil {
		return FileContent{}, err
	}
	target := filepath.Join(dir, filepath.FromSlash(clean))
	if err := ensureWithin(dir, target); err != nil {
		return FileContent{}, err
	}
	if err := rejectSymlinkComponents(dir, clean, true); err != nil {
		return FileContent{}, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return FileContent{}, fmt.Errorf("read %q: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return FileContent{}, fmt.Errorf("read %q: symlink paths are not allowed", clean)
	}
	if info.IsDir() {
		return FileContent{}, fmt.Errorf("%q is a directory", clean)
	}
	maxBytes := boundedPositive(opts.MaxBytes, DefaultReadMaxBytes, MaxReadMaxBytes)
	f, err := os.Open(target)
	if err != nil {
		return FileContent{}, fmt.Errorf("open %q: %w", clean, err)
	}
	defer func() { _ = f.Close() }()
	if err := ctx.Err(); err != nil {
		return FileContent{}, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return FileContent{}, fmt.Errorf("read %q: %w", clean, err)
	}
	truncated := len(buf) > maxBytes
	if truncated {
		buf = trimValidUTF8(buf[:maxBytes])
	}
	if isBinary(buf) {
		return FileContent{Path: clean, Size: info.Size(), Binary: true, Truncated: truncated}, nil
	}
	return FileContent{Path: clean, Content: string(buf), Size: info.Size(), Truncated: truncated}, nil
}

// SearchFiles searches text files in the scoped project workspace.
func (s *FileStore) SearchFiles(ctx context.Context, scope Scope, opts SearchOptions) (SearchResult, error) {
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return SearchResult{}, errors.New("query is required")
	}
	dir, err := s.scopeDir(scope)
	if err != nil {
		return SearchResult{}, err
	}
	limit := boundedPositive(opts.MaxResults, DefaultSearchLimit, MaxSearchLimit)
	result := SearchResult{Query: query, Limit: limit}
	err = s.walkFiles(ctx, dir, func(file FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		content, err := s.ReadFile(ctx, scope, ReadOptions{Path: file.Path, MaxBytes: DefaultReadMaxBytes})
		if err != nil || content.Binary {
			return nil
		}
		fragments := matchingFragments(content.Content, query, defaultSearchFragmentHits)
		if len(fragments) == 0 {
			return nil
		}
		result.TotalCount++
		if len(result.Results) < limit {
			result.Results = append(result.Results, SearchMatch{Path: file.Path, Fragments: fragments})
		} else {
			result.Truncated = true
			return errStopWalk
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return SearchResult{}, err
	}
	return result, nil
}

func (s *FileStore) allFiles(ctx context.Context, dir string, maxFiles int) ([]FileInfo, error) {
	files := []FileInfo{}
	err := s.walkFiles(ctx, dir, func(file FileInfo) error {
		files = append(files, file)
		if maxFiles > 0 && len(files) >= maxFiles {
			return errStopWalk
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func (s *FileStore) walkFiles(ctx context.Context, dir string, visit func(FileInfo) error) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat workspace: %w", err)
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() && (name == ".git" || name == "node_modules" || strings.EqualFold(name, workspaceSnapshotDirectory)) {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasPrefix(name, workspaceTempFilePrefix) {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return visit(FileInfo{Path: filepath.ToSlash(rel), Size: info.Size()})
	})
	if err != nil {
		return fmt.Errorf("list workspace files: %w", err)
	}
	return nil
}

func (s *FileStore) scopeDir(scope Scope) (string, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return "", errors.New("project workspace store is not configured")
	}
	parts := []string{scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID}
	for _, part := range parts {
		if err := validateScopeSegment(part); err != nil {
			return "", err
		}
	}
	if err := s.migrateLegacyWorkspace(scope); err != nil {
		return "", err
	}
	return filepath.Join(s.root, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID), nil
}

func validateScopeSegment(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("workspace scope is incomplete")
	}
	if strings.ContainsAny(value, `/\`+"\x00") || value == "." || value == ".." {
		return fmt.Errorf("invalid workspace scope segment %q", value)
	}
	return nil
}

func cleanProjectPath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" {
		return "", errors.New("file path cannot be empty")
	}
	if len([]byte(raw)) > MaxProjectPathBytes {
		return "", fmt.Errorf("file path %q is too long", raw)
	}
	if strings.HasPrefix(raw, "/") || path.IsAbs(raw) {
		return "", fmt.Errorf("file path %q must be relative", raw)
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", fmt.Errorf("file path %q cannot contain ..", raw)
		}
		if isReservedPathSegment(part) {
			return "", fmt.Errorf("file path %q contains reserved segment %q", raw, part)
		}
		if strings.ContainsRune(part, '\x00') {
			return "", fmt.Errorf("file path %q cannot contain NUL", raw)
		}
	}
	clean := path.Clean(raw)
	if clean == "." || clean == "" {
		return "", errors.New("file path cannot be empty")
	}
	return clean, nil
}

func ensureWithin(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return fmt.Errorf("path %q escapes workspace", target)
	}
	return nil
}

func isReservedPathSegment(part string) bool {
	part = strings.ToLower(strings.TrimSpace(part))
	if strings.HasPrefix(part, workspaceTempFilePrefix) {
		return true
	}
	switch part {
	case ".git", "node_modules", workspaceSnapshotDirectory:
		return true
	default:
		return false
	}
}

func mkdirAllForFile(root, clean string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	parent := path.Dir(clean)
	if parent == "." {
		return nil
	}
	current := root
	seen := []string{}
	for _, part := range strings.Split(parent, "/") {
		if part == "" || part == "." {
			continue
		}
		seen = append(seen, part)
		relPath := strings.Join(seen, "/")
		next := filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(next)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("path %q contains a symlink", relPath)
			}
			if !info.IsDir() {
				return fmt.Errorf("path %q is not a directory", relPath)
			}
			current = next
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.Mkdir(next, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		current = next
	}
	return nil
}

func rejectSymlinkComponents(root, clean string, includeTarget bool) error {
	current := root
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stat %q: %w", strings.Join(parts[:i+1], "/"), err)
		}
		if !includeTarget && i == len(parts)-1 {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q contains a symlink", strings.Join(parts[:i+1], "/"))
		}
		if !info.IsDir() && i < len(parts)-1 {
			return fmt.Errorf("path %q is not a directory", strings.Join(parts[:i+1], "/"))
		}
	}
	return nil
}

func rejectSymlink(target, clean string) error {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %q: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is a symlink", clean)
	}
	return nil
}

func validateMutationContent(path, content string) error {
	if _, err := cleanProjectPath(path); err != nil {
		return err
	}
	if len([]byte(content)) > MaxWriteBytes {
		return fmt.Errorf("file %q is too large: %d > %d bytes", path, len([]byte(content)), MaxWriteBytes)
	}
	if !validTextContent(content) {
		return fmt.Errorf("file %q must be UTF-8 text without NUL bytes", path)
	}
	return nil
}

func validTextContent(content string) bool {
	return utf8.ValidString(content) && !strings.ContainsRune(content, '\x00')
}

func boundedPositive(value, fallback, maximum int) int {
	if value > maximum {
		return maximum
	}
	if value > 0 {
		return value
	}
	return fallback
}

func trimValidUTF8(buf []byte) []byte {
	for len(buf) > 0 && !utf8.Valid(buf) {
		buf = buf[:len(buf)-1]
	}
	return buf
}

func isBinary(buf []byte) bool {
	return bytes.IndexByte(buf, 0) >= 0 || !utf8.Valid(buf)
}

func matchingFragments(content, query string, limit int) []string {
	lines := strings.Split(content, "\n")
	matches := []string{}
	lowerQuery := strings.ToLower(query)
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), lowerQuery) {
			matches = append(matches, truncateFragment(strings.TrimSpace(line)))
			if len(matches) >= limit {
				break
			}
		}
	}
	return matches
}

func truncateFragment(fragment string) string {
	if len(fragment) <= MaxSearchFragmentBytes {
		return fragment
	}
	buf := trimValidUTF8([]byte(fragment[:MaxSearchFragmentBytes]))
	return string(buf) + "..."
}
