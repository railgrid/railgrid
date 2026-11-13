//go:build !windows

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
	"encoding/json"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStreamedArtifactRejectsFIFO(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	attempt := &attemptRecord{ArtifactSpecs: []ArtifactSpec{{Name: "report", Path: "pipe"}}}
	attempt.Receipt.Workdir = root
	result := make(chan error, 1)
	go func() {
		result <- (&Runner{}).recordArtifactLocked(attempt, json.RawMessage(`{"name":"report","path":"pipe"}`))
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("expected regular-file validation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("opening the artifact FIFO blocked")
	}
}
