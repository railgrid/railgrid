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
// owns the scoped bytes until the controller commits them to the host repository.
package commitbundle

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

const (
	// EnvDir overrides the local filesystem directory used for bundles.
	EnvDir = "CODE_COMMIT_BUNDLE_DIR"

	// Limits keep the tool useful for generated apps while preventing the
	// provider from being used as an unbounded object store. Every size is
	// counted in decoded bytes, whatever the wire encoding.
	MaxFiles = 500
	// MaxFileBytes caps one UTF-8 text file.
	MaxFileBytes = 2 * 1024 * 1024
	// MaxBinaryFileBytes caps one base64-encoded (binary) file.
	MaxBinaryFileBytes = 25 * 1024 * 1024
	// MaxTotalBytes caps all files in one bundle.
	MaxTotalBytes = 48 * 1024 * 1024
	MaxPathLength = 1024

	// EncodingUTF8 marks content carried verbatim as UTF-8 text (the default).
	EncodingUTF8 = "utf-8"
	// EncodingBase64 marks content carried as RFC 4648 standard base64 with
	// padding; the file's bytes are the decoded content.
	EncodingBase64 = "base64"

	// DefaultSweepMaxAge is how long an unclaimed bundle may sit on disk. A
	// live bundle is claimed within minutes (commit wait, rate-limit window,
	// checkout read), so anything this old was orphaned by a crash.
	DefaultSweepMaxAge = 24 * time.Hour
	// DefaultSweepInterval is how often RunSweeper looks for orphans.
	DefaultSweepInterval = time.Hour
)

var errBundleNotFound = errors.New("bundle not found")

// IsNotFound reports whether err means the requested bundle is not present in
// the addressed scope.
func IsNotFound(err error) bool {
	return errors.Is(err, errBundleNotFound)
}

// File is one file from an MCP commit_files call or a repository checkout.
// Encoding is "" or EncodingUTF8 for text, EncodingBase64 for bytes carried
// as base64; Content stays in that encoding.
type File struct {
	Path     string
	Content  string
	Encoding string
	Delete   bool
}

// NormalizeEncoding validates a file encoding and returns its canonical form:
// "" for UTF-8 text (omitted or "utf-8") and EncodingBase64 for base64.
func NormalizeEncoding(encoding string) (string, error) {
	switch encoding {
	case "", EncodingUTF8:
		return "", nil
	case EncodingBase64:
		return EncodingBase64, nil
	}
	return "", fmt.Errorf("unsupported encoding %q: use %q or %q", encoding, EncodingUTF8, EncodingBase64)
}

// DecodeBase64 strictly decodes standard padded base64. Line breaks and
// non-canonical padding bits are rejected so every accepted string maps to
// exactly one byte sequence, whichever decoder later reads it.
func DecodeBase64(content string) ([]byte, error) {
	if strings.ContainsAny(content, "\r\n") {
		return nil, errors.New("base64 content must not contain line breaks")
	}
	return base64.StdEncoding.Strict().DecodeString(content)
}

// FileMeta is file metadata safe to expose in status.
type FileMeta struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	Delete bool   `json:"delete,omitempty"`
}

// BundleRef is returned after storing a bundle.
type BundleRef struct {
	Name      string
	Digest    string
	Scope     string
	Size      int64
	FileCount int
	Files     []FileMeta
}

// Bundle is the stored source payload and its metadata.
type Bundle struct {
	Name   string       `json:"name"`
	Digest string       `json:"digest"`
	Scope  string       `json:"scope"`
	Size   int64        `json:"size"`
	Files  []BundleFile `json:"files"`
}

// BundleFile is one file entry inside a bundle. Content keeps the encoding it
// arrived in; Size and Digest describe the decoded bytes.
type BundleFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
	Size     int64  `json:"size"`
	Digest   string `json:"digest"`
	Delete   bool   `json:"delete,omitempty"`
}

// Store persists and fetches immutable commit bundles.
type Store interface {
	Put(ctx context.Context, scope string, files []File) (BundleRef, error)
	Get(ctx context.Context, scope, name, digest string) (*Bundle, error)
	Delete(ctx context.Context, scope, name, digest string) error
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
		dir = filepath.Join(os.TempDir(), "faros-code-commit-bundles")
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
func (s *FileStore) Put(ctx context.Context, scope string, files []File) (BundleRef, error) {
	if s == nil {
		return BundleRef{}, errors.New("bundle store is nil")
	}
	scope = strings.TrimSpace(scope)
	key, err := scopeKey(scope)
	if err != nil {
		return BundleRef{}, err
	}
	bundle, ref, err := buildBundle(files)
	if err != nil {
		return BundleRef{}, err
	}
	bundle.Scope = scope
	ref.Scope = scope
	if err := ctx.Err(); err != nil {
		return BundleRef{}, err
	}
	dir := s.scopeDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return BundleRef{}, fmt.Errorf("create scoped bundle directory: %w", err)
	}
	path := s.path(key, bundle.Name)
	if _, err := os.Stat(path); err == nil {
		// Reusing an identical bundle: refresh its age so the orphan sweeper
		// does not reclaim it out from under this new request.
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return ref, nil
	} else if !os.IsNotExist(err) {
		return BundleRef{}, fmt.Errorf("stat bundle: %w", err)
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return BundleRef{}, fmt.Errorf("marshal bundle: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+bundle.Name+"-*.tmp")
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
func (s *FileStore) Get(ctx context.Context, scope, name, digest string) (*Bundle, error) {
	if s == nil {
		return nil, errors.New("bundle store is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := scopeKey(scope)
	if err != nil {
		return nil, err
	}
	if err := validateBundleName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path(key, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", errBundleNotFound, name)
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
	if bundle.Scope != strings.TrimSpace(scope) {
		return nil, fmt.Errorf("bundle %q scope mismatch", name)
	}
	if strings.TrimSpace(digest) != "" && bundle.Digest != digest {
		return nil, fmt.Errorf("bundle %q digest mismatch: got %s want %s", name, bundle.Digest, digest)
	}
	return &bundle, nil
}

// Delete removes a bundle after its RepositoryCommit has reached terminal
// status. A missing bundle is already gone and is treated as success.
func (s *FileStore) Delete(ctx context.Context, scope, name, digest string) error {
	if s == nil {
		return errors.New("bundle store is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := scopeKey(scope)
	if err != nil {
		return err
	}
	if err := validateBundleName(name); err != nil {
		return err
	}
	if strings.TrimSpace(digest) != "" {
		bundle, err := s.Get(ctx, scope, name, digest)
		if err != nil {
			if errors.Is(err, errBundleNotFound) {
				return nil
			}
			return err
		}
		if bundle.Digest != digest {
			return fmt.Errorf("bundle %q digest mismatch: got %s want %s", name, bundle.Digest, digest)
		}
	}
	if err := os.Remove(s.path(key, name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete bundle %q: %w", name, err)
	}
	return nil
}

// Sweep removes bundles (and abandoned temp files) last written more than
// maxAge before now. Consumers delete bundles once they reach a terminal
// state; this only reclaims what a crash or an abandoned request left behind.
// Scope directories are kept: removing one could race a concurrent Put.
func (s *FileStore) Sweep(now time.Time, maxAge time.Duration) (int, error) {
	if s == nil {
		return 0, errors.New("bundle store is nil")
	}
	scopes, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, fmt.Errorf("list bundle directory: %w", err)
	}
	cutoff := now.Add(-maxAge)
	removed := 0
	var errs []error
	for _, scope := range scopes {
		if !scope.IsDir() || !strings.HasPrefix(scope.Name(), "scope-") {
			continue
		}
		dir := filepath.Join(s.dir, scope.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("list %s: %w", scope.Name(), err))
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || (!strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".tmp")) {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove %s/%s: %w", scope.Name(), name, err))
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

// RunSweeper sweeps orphaned bundles once at startup and then every interval
// until ctx is done.
func (s *FileStore) RunSweeper(ctx context.Context, interval, maxAge time.Duration) {
	logger := klog.FromContext(ctx).WithName("commitbundle-sweeper")
	sweep := func() {
		removed, err := s.Sweep(time.Now(), maxAge)
		if err != nil {
			logger.Error(err, "sweep orphaned bundles", "dir", s.dir)
		}
		if removed > 0 {
			logger.Info("removed orphaned bundles", "count", removed, "maxAge", maxAge)
		}
	}
	sweep()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

func (s *FileStore) scopeDir(scopeKey string) string {
	return filepath.Join(s.dir, scopeKey)
}

func (s *FileStore) path(scopeKey, name string) string {
	return filepath.Join(s.scopeDir(scopeKey), name+".json")
}

func scopeKey(scope string) (string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "", errors.New("bundle scope is required")
	}
	sum := sha256.Sum256([]byte(scope))
	return "scope-" + hex.EncodeToString(sum[:16]), nil
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
	hasDelete := false
	for _, f := range files {
		path, err := cleanPath(f.Path)
		if err != nil {
			return Bundle{}, BundleRef{}, err
		}
		if _, ok := seen[path]; ok {
			return Bundle{}, BundleRef{}, fmt.Errorf("conflicting commit operation for path %q", path)
		}
		seen[path] = struct{}{}
		encoding, err := NormalizeEncoding(f.Encoding)
		if err != nil {
			return Bundle{}, BundleRef{}, fmt.Errorf("file %q: %w", path, err)
		}
		if f.Delete {
			if f.Content != "" {
				return Bundle{}, BundleRef{}, fmt.Errorf("deleted file %q cannot include content", path)
			}
			encoding = ""
			hasDelete = true
		}
		bundleFiles = append(bundleFiles, BundleFile{Path: path, Content: f.Content, Encoding: encoding, Delete: f.Delete})
	}
	sort.Slice(bundleFiles, func(i, j int) bool {
		return bundleFiles[i].Path < bundleFiles[j].Path
	})

	// Sizes and digests cover the decoded bytes. Files are decoded one at a
	// time, in digest order, so at most one decoded file is held at once.
	digester := newBundleDigester(hasDelete)
	var total int64
	for i := range bundleFiles {
		f := &bundleFiles[i]
		data, err := decodedContent(*f)
		if err != nil {
			return Bundle{}, BundleRef{}, err
		}
		f.Size = int64(len(data))
		total += f.Size
		if total > MaxTotalBytes {
			return Bundle{}, BundleRef{}, fmt.Errorf("bundle is too large: %d > %d bytes", total, MaxTotalBytes)
		}
		if !f.Delete {
			f.Digest = digestBytes(data)
		}
		digester.add(f.Path, f.Delete, data)
	}
	digest := digester.sum()
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
		ref.Files = append(ref.Files, FileMeta{Path: f.Path, Size: f.Size, Digest: f.Digest, Delete: f.Delete})
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

// decodedContent returns a file's bytes, enforcing the per-file cap for its
// class: MaxFileBytes for UTF-8 text, MaxBinaryFileBytes for base64.
func decodedContent(f BundleFile) ([]byte, error) {
	if f.Encoding != EncodingBase64 {
		if len(f.Content) > MaxFileBytes {
			return nil, fmt.Errorf("file %q is too large: %d > %d bytes", f.Path, len(f.Content), MaxFileBytes)
		}
		return []byte(f.Content), nil
	}
	// Reject an oversized payload before allocating its decoded copy.
	if len(f.Content) > base64.StdEncoding.EncodedLen(MaxBinaryFileBytes) {
		return nil, fmt.Errorf("file %q is too large: more than %d bytes", f.Path, MaxBinaryFileBytes)
	}
	data, err := DecodeBase64(f.Content)
	if err != nil {
		return nil, fmt.Errorf("file %q has invalid base64 content: %w", f.Path, err)
	}
	if len(data) > MaxBinaryFileBytes {
		return nil, fmt.Errorf("file %q is too large: %d > %d bytes", f.Path, len(data), MaxBinaryFileBytes)
	}
	return data, nil
}

// bundleDigester hashes a bundle's files, in path order, over their decoded
// bytes. Upsert-only bundles keep the original path/content framing; bundles
// with deletions add an operation flag and a length prefix so a deletion and
// an empty file cannot collide.
type bundleDigester struct {
	h         hash.Hash
	hasDelete bool
}

func newBundleDigester(hasDelete bool) *bundleDigester {
	return &bundleDigester{h: sha256.New(), hasDelete: hasDelete}
}

func (d *bundleDigester) add(path string, deleted bool, data []byte) {
	_, _ = d.h.Write([]byte(path))
	_, _ = d.h.Write([]byte{0})
	if d.hasDelete {
		if deleted {
			_, _ = d.h.Write([]byte{0})
		} else {
			_, _ = d.h.Write([]byte{1})
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(data)))
		_, _ = d.h.Write(size[:])
	}
	_, _ = d.h.Write(data)
	_, _ = d.h.Write([]byte{0})
}

func (d *bundleDigester) sum() string {
	return "sha256:" + hex.EncodeToString(d.h.Sum(nil))
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
