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

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railgrid/provider-app-studio/workspace"
)

type failWorkspaceLedgerReadAt struct {
	workspace.Ledger
	failAt int32
	reads  atomic.Int32
	err    error
}

func (l *failWorkspaceLedgerReadAt) Read(ctx context.Context, scope workspace.Scope) (workspace.LedgerRecord, error) {
	if l.reads.Add(1) == l.failAt {
		return workspace.LedgerRecord{}, l.err
	}
	return l.Ledger.Read(ctx, scope)
}

type failWorkspaceLedgerUpdate struct {
	workspace.Ledger
	updates atomic.Int32
	err     error
}

func (l *failWorkspaceLedgerUpdate) Update(context.Context, workspace.Scope, func(*workspace.LedgerRecord) (bool, error)) (workspace.LedgerRecord, error) {
	l.updates.Add(1)
	return workspace.LedgerRecord{}, l.err
}

func TestDevelopmentSyncFailsOnSourceProvenanceReadBeforeSandboxPost(t *testing.T) {
	var requests atomic.Int32
	f := newDevelopmentSyncReadyFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			next.ServeHTTP(w, r)
		})
	})
	injectedErr := errors.New("injected source provenance read failure")
	ledger := &failWorkspaceLedgerReadAt{
		Ledger: workspace.NewMemoryLedger(),
		// Sync reads a baseline and snapshot revision, dirty paths, then two
		// revision fences before asking for LastSourceChange.
		failAt: 6,
		err:    injectedErr,
	}
	f.server.workspaces.SetLedger(ledger)

	err := f.sync()
	if !errors.Is(err, injectedErr) || !strings.Contains(err.Error(), "read workspace source provenance") {
		t.Fatalf("sync error = %v, want the LastSourceChange error with provenance context", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("data-plane requests before provenance failure = %d, want none", got)
	}
	if got := ledger.reads.Load(); got != ledger.failAt {
		t.Fatalf("ledger reads = %d, want failure on LastSourceChange read %d", got, ledger.failAt)
	}
}

func TestDevelopmentSyncCheckpointFailureIsPartialSuccessWithoutReplay(t *testing.T) {
	var syncPosts atomic.Int32
	f := newDevelopmentSyncReadyFixture(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
				syncPosts.Add(1)
			}
			next.ServeHTTP(w, r)
		})
	})
	injectedErr := &projectDevelopmentSyncHTTPError{component: "web", status: http.StatusServiceUnavailable, detail: "component is starting"}
	if !projectDevelopmentSyncProvisioning(injectedErr) {
		t.Fatal("injected 503 is not classified as a provisioning retry, so it would not exercise the partial-success guard")
	}
	ledger := &failWorkspaceLedgerUpdate{Ledger: workspace.NewMemoryLedger(), err: injectedErr}
	f.server.workspaces.SetLedger(ledger)
	f.server.developmentSyncReadyTimeout = 100 * time.Millisecond

	err := f.sync()
	var partial *projectDevelopmentSyncPartialError
	if !errors.As(err, &partial) || !partial.Checkpoint || partial.Stage != "record preview checkpoint" {
		t.Fatalf("sync error = %v, want typed preview-checkpoint partial-success error", err)
	}
	if !errors.Is(err, injectedErr) {
		t.Fatalf("sync error = %v, want injected checkpoint error wrapped", err)
	}
	if len(partial.AppliedComponents) != 1 || partial.AppliedComponents[0] != "web" {
		t.Fatalf("applied components = %v, want [web]", partial.AppliedComponents)
	}
	if got := syncPosts.Load(); got != 1 {
		t.Fatalf("component sync posts = %d, want one post and no automatic replay after checkpoint failure", got)
	}
	if got := ledger.updates.Load(); got != 1 {
		t.Fatalf("ledger updates = %d, want one failed preview checkpoint write", got)
	}
	if got := f.agent.received["web"]; len(got) != 1 || got[0].Path != "index.html" {
		t.Fatalf("component accepted files = %+v, want the workspace file before checkpoint failure", got)
	}

	message := developmentSyncFailureMessage("create_file", err)
	lowerMessage := strings.ToLower(message)
	if !strings.Contains(lowerMessage, "preview checkpoint") || !strings.Contains(lowerMessage, "accepted the workspace revision") {
		t.Fatalf("failure message = %q, want checkpoint failure after an accepted sync", message)
	}
	for _, misleading := range []string{"runtime may not match the workspace", "still running old code", "previous code"} {
		if strings.Contains(lowerMessage, misleading) {
			t.Fatalf("failure message = %q, incorrectly suggests %q after the sandbox accepted the workspace", message, misleading)
		}
	}
	f.server.recordDevelopmentSyncFailure(f.id, f.project, message)
	if got := f.server.lastDevelopmentSyncFailure(f.id, f.project); got != message {
		t.Fatalf("recorded sync failure = %q, want checkpoint-specific message %q", got, message)
	}
}
