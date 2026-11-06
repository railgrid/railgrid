/*
Copyright 2026 The Railgrid Authors.

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

// Package skills contains the framework-neutral App Studio skill catalog.
//
// The package deliberately models only bounded, declarative skill metadata and
// content. Eino-specific authority fields (context, agent, and model) are
// rejected while loading instead of being interpreted by this package.
package skills

import (
	"context"
	"errors"
	"testing"

	"github.com/railgrid/provider-app-studio/workspace"
)

func TestProjectSourceUsesScopedFileStoreAndListsResources(t *testing.T) {
	store := workspace.NewFileStore(t.TempDir())
	scope := workspace.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "project", ProjectUID: "uid"}
	if err := store.ApplyFiles(context.Background(), scope, []workspace.File{
		{Path: "skills/demo/SKILL.md", Content: "---\nname: demo\ndescription: project skill\n---\nbody"},
		{Path: "skills/demo/scripts/check.sh", Content: "echo ok\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles error = %v", err)
	}
	source, err := NewProjectSourceWithRoot(store, scope, "skills")
	if err != nil {
		t.Fatalf("NewProjectSource error = %v", err)
	}
	list, err := source.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(list.Packages) != 1 || len(list.Packages[0].Resources) != 1 || list.Packages[0].Resources[0].Path != "scripts/check.sh" {
		t.Fatalf("project packages = %#v", list.Packages)
	}
	catalog, err := NewCatalog(CatalogOptions{Sources: []Source{source}})
	if err != nil {
		t.Fatalf("NewCatalog error = %v", err)
	}
	snapshot, err := catalog.Load(context.Background())
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	resource, err := snapshot.ReadResource(context.Background(), "demo", "scripts/check.sh", ResourceReadOptions{})
	if err != nil || string(resource.Content) != "echo ok\n" {
		t.Fatalf("project resource = %#v, %v", resource, err)
	}
	if _, err := source.ReadResource(context.Background(), "demo", "../outside", ResourceReadOptions{}); err == nil {
		t.Fatal("project source accepted resource traversal")
	}
}

type memorySource struct {
	scope     Scope
	packages  []Package
	resources map[string][]byte
}

func newMemorySource(scope Scope, packages ...Package) *memorySource {
	return &memorySource{scope: scope, packages: packages, resources: make(map[string][]byte)}
}

func (s *memorySource) Scope() Scope { return s.scope }

func (s *memorySource) List(context.Context, int) (PackageList, error) {
	packages := make([]Package, len(s.packages))
	copy(packages, s.packages)
	return PackageList{Packages: packages}, nil
}

func (s *memorySource) ReadResource(_ context.Context, packagePath, resourcePath string, opts ResourceReadOptions) (ResourceReadResult, error) {
	data, ok := s.resources[packagePath+"/"+resourcePath]
	if !ok {
		return ResourceReadResult{}, ErrResourceNotFound
	}
	if opts.Limit <= 0 {
		opts.Limit = len(data)
	}
	if opts.Offset < 0 || opts.Offset > int64(len(data)) {
		return ResourceReadResult{}, errors.New("bad offset")
	}
	end := opts.Offset + int64(opts.Limit)
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return ResourceReadResult{Path: resourcePath, Content: append([]byte(nil), data[opts.Offset:end]...), Size: int64(len(data)), Offset: opts.Offset, Truncated: end < int64(len(data)), NextOffset: end}, nil
}
