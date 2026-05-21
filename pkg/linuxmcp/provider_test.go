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

package linuxmcp

import (
	"context"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func TestProvider_DefaultsAndPolicy(t *testing.T) {
	p := NewProvider(Config{
		Cluster:   "root",
		EdgeNames: []string{"a", "b"},
	})
	if got := p.CommandTimeout(); got != 30*time.Second {
		t.Errorf("default CommandTimeout = %s, want 30s", got)
	}
	if got := p.MaxOutputBytes(); got != 1<<20 {
		t.Errorf("default MaxOutputBytes = %d, want %d", got, 1<<20)
	}
	if p.ReadOnly() {
		t.Errorf("ReadOnly: default should be false")
	}
	if p.Cluster() != "root" {
		t.Errorf("Cluster: got %q, want %q", p.Cluster(), "root")
	}
}

func TestProvider_DefaultTarget(t *testing.T) {
	empty := NewProvider(Config{Cluster: "root"})
	if got := empty.DefaultTarget(); got != "" {
		t.Errorf("DefaultTarget with empty edges: got %q, want \"\"", got)
	}
	populated := NewProvider(Config{Cluster: "root", EdgeNames: []string{"first", "second"}})
	if got := populated.DefaultTarget(); got != "first" {
		t.Errorf("DefaultTarget: got %q, want %q", got, "first")
	}
}

func TestProvider_HasTarget(t *testing.T) {
	p := NewProvider(Config{EdgeNames: []string{"alpha", "beta"}})
	if !p.HasTarget("alpha") {
		t.Errorf("HasTarget(alpha) should be true")
	}
	if p.HasTarget("gamma") {
		t.Errorf("HasTarget(gamma) should be false")
	}
}

func TestProvider_OpenSession_RejectsUnknownEdge(t *testing.T) {
	p := NewProvider(Config{
		Cluster:   "root",
		EdgeNames: []string{"only-edge"},
		OpenSession: func(_ context.Context, _ string) (*gossh.Client, error) {
			t.Fatalf("OpenSession callback should not be invoked for an unknown edge")
			return nil, nil
		},
	})
	_, err := p.OpenSession(context.Background(), "other-edge")
	if err == nil {
		t.Fatalf("expected error for unknown edge")
	}
	if !strings.Contains(err.Error(), "not in this LinuxMCP's resolved set") {
		t.Errorf("error: got %v, want unknown-edge message", err)
	}
}

func TestProvider_OpenSession_NoEdgesAvailable(t *testing.T) {
	p := NewProvider(Config{Cluster: "root"})
	_, err := p.OpenSession(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error when no edges connected")
	}
	if !strings.Contains(err.Error(), "no connected edges") {
		t.Errorf("error: got %v, want no-connected-edges message", err)
	}
}
