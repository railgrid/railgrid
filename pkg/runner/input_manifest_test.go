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

package runner

import (
	"strings"
	"testing"
)

func TestValidateInputFilesRejectsFileDirectoryCollisions(t *testing.T) {
	file := func(id, groupID, relativePath string) InputFile {
		name := relativePath
		if slash := strings.LastIndex(relativePath, "/"); slash >= 0 {
			name = relativePath[slash+1:]
		}
		ref := inputFileRef(id, name, "text/plain", []byte(id))
		ref.GroupID = groupID
		ref.RelativePath = relativePath
		return ref
	}

	tests := []struct {
		name  string
		files []InputFile
	}{
		{
			name: "file before child path",
			files: []InputFile{
				file("input-a", "group-a", "assets/logo.png"),
				file("input-b", "group-a", "assets/logo.png/metadata.json"),
			},
		},
		{
			name: "child path before file",
			files: []InputFile{
				file("input-b", "group-a", "assets/logo.png/metadata.json"),
				file("input-a", "group-a", "assets/logo.png"),
			},
		},
		{
			name: "case-insensitive prefix",
			files: []InputFile{
				file("input-a", "group-a", "Docs/Readme.md"),
				file("input-b", "group-a", "docs/readme.md/child.txt"),
			},
		},
		{
			name: "group and private ID namespace prefix",
			files: []InputFile{
				file("input-a", "g", "Readme.md"),
				file("group-g", "", "readme.md/child.txt"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateInputFiles(tt.files); err == nil {
				t.Fatalf("ValidateInputFiles accepted colliding destinations: %+v", tt.files)
			}
		})
	}
}

func TestValidateInputFilesAllowsSiblingFilesAndDirectories(t *testing.T) {
	file := func(id, relativePath string) InputFile {
		name := relativePath
		if slash := strings.LastIndex(relativePath, "/"); slash >= 0 {
			name = relativePath[slash+1:]
		}
		ref := inputFileRef(id, name, "text/plain", []byte(id))
		ref.GroupID = "group-a"
		ref.RelativePath = relativePath
		return ref
	}

	files := []InputFile{
		file("input-readme", "docs/README.md"),
		file("input-notes", "docs/notes.txt"),
		file("input-logo", "docs/images/logo.png"),
		file("input-diagram", "docs/images/diagram.png"),
		file("input-a", "a"),
		file("input-ab-child", "ab/child.txt"),
	}
	if err := ValidateInputFiles(files); err != nil {
		t.Fatalf("ValidateInputFiles rejected distinct sibling paths: %v", err)
	}
}
