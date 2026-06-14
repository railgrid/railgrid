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

// Package commitbundle stores generated source bundles outside Kubernetes API
// objects. RepositoryCommit CRs carry only a bundle name and digest; this store
// owns the bytes until the controller commits them to the host repository.
package commitbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// EnvDir overrides the local filesystem directory used for bundles.
	EnvDir = "CODE_COMMIT_BUNDLE_DIR"

	// Limits keep the tool useful for generated apps while preventing the
	// provider from being used as an unbounded object store.
	MaxFiles      = 500
	MaxFileBytes  = 2 * 1024 * 1024
	MaxTotalBytes = 16 * 1024 * 1024
	MaxPathLength = 1024
)

// File is one UTF-8 text file from an MCP commit_files call.
type File struct {
	Path    string
	Content string
}

// FileMeta is file metadata safe to expose in status.
type FileMeta struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// BundleRef is returned after storing a bundle.
type BundleRef struct {
	Name      string
	Digest    string
	Size      int64
	FileCount int
	Files     []FileMeta
}

// Bundle is the stored source payload and its metadata.
type Bundle struct {
	Name   string       `json:"name"`
	Digest string       `json:"digest"`
	Size   int64        `json:"size"`
	Files  []BundleFile `json:"files"`
}

// BundleFile is one file entry inside a bundle.
type BundleFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
	Digest  string `json:"digest"`
}

// Store persists and fetches immutable commit bundles.
type Store interface {
	Put(ctx context.Context, files []File) (BundleRef, error)
	Get(ctx context.Context, name, digest string) (*Bundle, error)
}

// FileStore stores bundles as JSON files in a local directory.
type FileStore struct {
	dir string
}

// NewFileStoreFromEnv builds a filesystem store. CODE_COMMIT_BUNDLE_DIR can be
// mounted to persistent storage in production; local dev falls back to /tmp.
func NewFileStoreFromEnv() (*FileStore, error) {
	dir := strings.TrimSpace(os.Getenv(EnvDir))
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "kedge-code-commit-bundles")
	}
	return NewFileStore(dir)
}

// NewFileStore builds a filesystem store rooted at dir.
func NewFileStore(dir string) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("bundle directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create bundle directory: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

// Dir returns the filesystem directory backing this store.
func (s *FileStore) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Put validates, canonicalizes, and writes an immutable bundle.
func (s *FileStore) Put(ctx context.Context, files []File) (BundleRef, error) {
	if s == nil {
		return BundleRef{}, errors.New("bundle store is nil")
	}
	bundle, ref, err := buildBundle(files)
	if err != nil {
		return BundleRef{}, err
	}
	if err := ctx.Err(); err != nil {
		return BundleRef{}, err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return BundleRef{}, fmt.Errorf("marshal bundle: %w", err)
	}
	path := s.path(bundle.Name)
	if _, err := os.Stat(path); err == nil {
		return ref, nil
	} else if !os.IsNotExist(err) {
		return BundleRef{}, fmt.Errorf("stat bundle: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, "."+bundle.Name+"-*.tmp")
	if err != nil {
		return BundleRef{}, fmt.Errorf("create temp bundle: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return BundleRef{}, fmt.Errorf("write bundle: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return BundleRef{}, fmt.Errorf("close bundle: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return BundleRef{}, fmt.Errorf("publish bundle: %w", err)
	}
	return ref, nil
}

// Get loads a bundle and verifies its digest when digest is provided.
func (s *FileStore) Get(ctx context.Context, name, digest string) (*Bundle, error) {
	if s == nil {
		return nil, errors.New("bundle store is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateBundleName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("bundle %q not found", name)
		}
		return nil, fmt.Errorf("read bundle %q: %w", name, err)
	}
	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("decode bundle %q: %w", name, err)
	}
	if bundle.Name != name {
		return nil, fmt.Errorf("bundle %q has stored name %q", name, bundle.Name)
	}
	if strings.TrimSpace(digest) != "" && bundle.Digest != digest {
		return nil, fmt.Errorf("bundle %q digest mismatch: got %s want %s", name, bundle.Digest, digest)
	}
	return &bundle, nil
}

func (s *FileStore) path(name string) string {
	return filepath.Join(s.dir, name+".json")
}

func buildBundle(files []File) (Bundle, BundleRef, error) {
	if len(files) == 0 {
		return Bundle{}, BundleRef{}, errors.New("at least one file is required")
	}
	if len(files) > MaxFiles {
		return Bundle{}, BundleRef{}, fmt.Errorf("too many files: %d > %d", len(files), MaxFiles)
	}

	seen := map[string]struct{}{}
	bundleFiles := make([]BundleFile, 0, len(files))
	var total int64
	for _, f := range files {
		path, err := cleanPath(f.Path)
		if err != nil {
			return Bundle{}, BundleRef{}, err
		}
		if _, ok := seen[path]; ok {
			return Bundle{}, BundleRef{}, fmt.Errorf("duplicate file path %q", path)
		}
		seen[path] = struct{}{}
		size := int64(len([]byte(f.Content)))
		if size > MaxFileBytes {
			return Bundle{}, BundleRef{}, fmt.Errorf("file %q is too large: %d > %d bytes", path, size, MaxFileBytes)
		}
		total += size
		if total > MaxTotalBytes {
			return Bundle{}, BundleRef{}, fmt.Errorf("bundle is too large: %d > %d bytes", total, MaxTotalBytes)
		}
		bundleFiles = append(bundleFiles, BundleFile{
			Path:    path,
			Content: f.Content,
			Size:    size,
			Digest:  digestBytes([]byte(f.Content)),
		})
	}
	sort.Slice(bundleFiles, func(i, j int) bool {
		return bundleFiles[i].Path < bundleFiles[j].Path
	})
	digest := bundleDigest(bundleFiles)
	name := "bundle-" + strings.TrimPrefix(digest, "sha256:")[:24]
	bundle := Bundle{Name: name, Digest: digest, Size: total, Files: bundleFiles}
	ref := BundleRef{
		Name:      name,
		Digest:    digest,
		Size:      total,
		FileCount: len(bundleFiles),
		Files:     make([]FileMeta, 0, len(bundleFiles)),
	}
	for _, f := range bundleFiles {
		ref.Files = append(ref.Files, FileMeta{Path: f.Path, Size: f.Size, Digest: f.Digest})
	}
	return bundle, ref, nil
}

func cleanPath(raw string) (string, error) {
	path := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if path == "" {
		return "", errors.New("file path is required")
	}
	if len(path) > MaxPathLength {
		return "", fmt.Errorf("file path %q is too long", path)
	}
	if strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("file path %q contains a null byte", path)
	}
	if strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("file path %q must be relative", path)
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("file path %q escapes the repository", path)
	}
	return cleaned, nil
}

func validateBundleName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("bundle name is required")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return fmt.Errorf("invalid bundle name %q", name)
	}
	return nil
}

func bundleDigest(files []BundleFile) string {
	h := sha256.New()
	for _, f := range files {
		_, _ = h.Write([]byte(f.Path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(f.Content))
		_, _ = h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
