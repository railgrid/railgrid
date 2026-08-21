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

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverEventDirsIncludesEveryProviderRoot(t *testing.T) {
	root := t.TempDir()
	platform := filepath.Join(root, "telemetry", "events")
	providers := filepath.Join(root, "providers")
	for _, path := range []string{
		platform,
		filepath.Join(providers, "agents", "telemetry", "events"),
		filepath.Join(providers, "app-studio", "telemetry", "events"),
		filepath.Join(providers, "edges", "telemetry", "events"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(providers, "without-telemetry"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := discoverEventDirs(platform, providers)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		platform,
		filepath.Join(providers, "agents", "telemetry", "events"),
		filepath.Join(providers, "app-studio", "telemetry", "events"),
		filepath.Join(providers, "edges", "telemetry", "events"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event roots = %v, want %v", got, want)
	}
}
