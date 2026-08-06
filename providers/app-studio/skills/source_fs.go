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

package skills

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

type filesystemSource struct {
	filesystem fs.FS
	root       string
	scope      Scope
	limits     Limits
}

// NewFilesystemSource creates a source rooted at a package-relative fs.FS
// directory. It is useful for embedded system skills and tests. Filesystem
// paths are never returned; only paths relative to root are exposed.
func NewFilesystemSource(scope Scope, filesystem fs.FS, root string) (Source, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("filesystem is required")
	}
	if scope != ScopeSystem && scope != ScopeProject {
		return nil, fmt.Errorf("unsupported skill source scope %q", scope)
	}
	if root == "" {
		root = "."
	}
	root = strings.TrimPrefix(strings.ReplaceAll(root, "\\", "/"), "./")
	if root == "" {
		root = "."
	}
	if root != "." {
		if _, err := cleanPublicPackagePath(root); err != nil {
			return nil, err
		}
	}
	return &filesystemSource{filesystem: filesystem, root: root, scope: scope, limits: DefaultLimits()}, nil
}

// NewBundledSource is an explicit alias for a system-scoped filesystem source.
func NewBundledSource(filesystem fs.FS) (Source, error) {
	return NewFilesystemSource(ScopeSystem, filesystem, ".")
}

func (s *filesystemSource) Scope() Scope { return s.scope }

func (s *filesystemSource) List(ctx context.Context, maxPackages int) (PackageList, error) {
	if err := ctx.Err(); err != nil {
		return PackageList{}, err
	}
	limit := maxPackages
	if limit <= 0 || limit > s.limits.MaxPackages {
		limit = s.limits.MaxPackages
	}
	entries, err := fs.ReadDir(s.filesystem, s.root)
	if err != nil {
		if err == fs.ErrNotExist {
			return PackageList{}, nil
		}
		return PackageList{}, fmt.Errorf("list skill source: %w", err)
	}
	packages := make([]Package, 0, len(entries))
	warnings := make([]Warning, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return PackageList{}, err
		}
		if !entry.IsDir() {
			continue
		}
		packagePath := entry.Name()
		if s.root != "." {
			packagePath = path.Join(s.root, packagePath)
		}
		packagePath, err = cleanPublicPackagePath(packagePath)
		if err != nil {
			warnings = appendBoundedWarning(warnings, Warning{Scope: s.scope, PackagePath: entry.Name(), Code: "invalid_package_path", Message: "skill package path is invalid"}, s.limits.MaxWarnings)
			continue
		}
		skillPath := path.Join(packagePath, "SKILL.md")
		skill, readErr := readFSFileBounded(ctx, s.filesystem, skillPath, s.limits.MaxSkillBytes)
		if readErr != nil {
			warnings = appendBoundedWarning(warnings, Warning{Scope: s.scope, PackagePath: packagePath, Code: "skill_read_failed", Message: "SKILL.md could not be read"}, s.limits.MaxWarnings)
			continue
		}
		resourceRefs, resourceWarnings := s.listFSResources(ctx, packagePath, limit)
		warnings = appendBoundedWarnings(warnings, resourceWarnings, s.limits.MaxWarnings)
		packages = append(packages, Package{Path: packagePath, SkillContent: skill, Resources: resourceRefs})
		if len(packages) >= limit {
			return PackageList{Packages: packages, Warnings: warnings, Truncated: len(entries) > len(packages)}, nil
		}
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Path < packages[j].Path })
	return PackageList{Packages: packages, Warnings: warnings}, nil
}

func (s *filesystemSource) listFSResources(ctx context.Context, packagePath string, max int) ([]ResourceRef, []Warning) {
	resources := make([]ResourceRef, 0)
	warnings := make([]Warning, 0)
	err := fs.WalkDir(s.filesystem, packagePath, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if filePath == path.Join(packagePath, "SKILL.md") {
			return nil
		}
		if len(resources) >= s.limits.MaxResourceCount {
			return fs.SkipAll
		}
		prefix := packagePath + "/"
		if !strings.HasPrefix(filePath, prefix) {
			return nil
		}
		rel := strings.TrimPrefix(filePath, prefix)
		var relErr error
		rel, relErr = cleanPublicResourcePath(rel)
		if relErr != nil {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		resources = append(resources, ResourceRef{Path: rel, Size: info.Size()})
		return nil
	})
	if err != nil && err != fs.SkipAll && err != context.Canceled && err != context.DeadlineExceeded {
		warnings = appendBoundedWarning(warnings, Warning{Scope: s.scope, PackagePath: packagePath, Code: "resource_list_failed", Message: "supporting resources could not be listed"}, s.limits.MaxWarnings)
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].Path < resources[j].Path })
	return resources, warnings
}

func (s *filesystemSource) ReadResource(ctx context.Context, packagePath, resourcePath string, opts ResourceReadOptions) (ResourceReadResult, error) {
	packagePath, err := cleanPublicPackagePath(packagePath)
	if err != nil {
		return ResourceReadResult{}, err
	}
	resourcePath, err = cleanPublicResourcePath(resourcePath)
	if err != nil {
		return ResourceReadResult{}, err
	}
	if resourcePath == "SKILL.md" {
		return ResourceReadResult{}, fmt.Errorf("SKILL.md is not a supporting resource")
	}
	limit, err := boundedReadOptions(opts, s.limits.MaxResourceRead)
	if err != nil {
		return ResourceReadResult{}, err
	}
	fullPath := path.Join(packagePath, resourcePath)
	return readFSResource(ctx, s.filesystem, fullPath, limit)
}

func readFSFileBounded(ctx context.Context, filesystem fs.FS, filePath string, max int) ([]byte, error) {
	result, err := readFSResource(ctx, filesystem, filePath, ResourceReadOptions{Limit: max})
	if err != nil {
		return nil, err
	}
	if result.Truncated {
		return nil, fmt.Errorf("file exceeds bounded size")
	}
	return result.Content, nil
}

func readFSResource(ctx context.Context, filesystem fs.FS, filePath string, opts ResourceReadOptions) (ResourceReadResult, error) {
	if err := ctx.Err(); err != nil {
		return ResourceReadResult{}, err
	}
	info, err := fs.Stat(filesystem, filePath)
	if err != nil {
		return ResourceReadResult{}, ErrResourceNotFound
	}
	if info.IsDir() {
		return ResourceReadResult{}, fmt.Errorf("resource is a directory")
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return ResourceReadResult{}, fmt.Errorf("symlink resources are not allowed")
	}
	if opts.Offset < 0 {
		return ResourceReadResult{}, fmt.Errorf("resource offset cannot be negative")
	}
	if opts.Offset > info.Size() {
		return ResourceReadResult{Path: filePath, Size: info.Size(), Offset: opts.Offset}, nil
	}
	file, err := filesystem.Open(filePath)
	if err != nil {
		return ResourceReadResult{}, ErrResourceNotFound
	}
	defer func() { _ = file.Close() }()
	if seeker, ok := file.(io.Seeker); ok {
		if _, err := seeker.Seek(opts.Offset, io.SeekStart); err != nil {
			return ResourceReadResult{}, fmt.Errorf("seek resource: %w", err)
		}
	} else if opts.Offset > 0 {
		if _, err := io.CopyN(io.Discard, file, opts.Offset); err != nil {
			return ResourceReadResult{}, fmt.Errorf("seek resource: %w", err)
		}
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(opts.Limit)+1))
	if err != nil {
		return ResourceReadResult{}, fmt.Errorf("read resource: %w", err)
	}
	truncated := int64(len(content)) > int64(opts.Limit) || opts.Offset+int64(len(content)) < info.Size()
	if len(content) > opts.Limit {
		content = content[:opts.Limit]
	}
	result := ResourceReadResult{Path: filePath, Content: append([]byte(nil), content...), Size: info.Size(), Offset: opts.Offset, Truncated: truncated}
	if truncated {
		result.NextOffset = opts.Offset + int64(len(content))
	}
	return result, nil
}
