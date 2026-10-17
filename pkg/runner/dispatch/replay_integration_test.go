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

package dispatch

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/client"
	"github.com/railgrid/railgrid/pkg/runner/harness"
)

type replayAdapter struct {
	runs  atomic.Int32
	final string
}

func (a *replayAdapter) Probe(context.Context) (harness.Info, error) {
	return harness.Info{Name: "fake", Ready: true}, nil
}
func (a *replayAdapter) Run(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
	a.runs.Add(1)
	data, _ := json.Marshal(map[string]any{"item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": a.final}})
	if err := emit(harness.Event{Type: runner.EventProgress, Data: data}); err != nil {
		return harness.Result{}, err
	}
	// Evict the final answer from the hot cache before the dispatcher connects.
	for i := 0; i < 20; i++ {
		if err := emit(harness.Event{Type: runner.EventProgress, Message: "working"}); err != nil {
			return harness.Result{}, err
		}
	}
	return harness.Result{Phase: string(runner.PhaseCompleted), SessionID: launch.SessionID}, nil
}

func TestHTTPReplayPreservesFinalScreenshotDescription(t *testing.T) {
	source := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--initial-branch=main")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial")
	commit := git("rev-parse", "HEAD")
	adapter := &replayAdapter{final: "Verified against the live backend.\n\n![Changed UI](artifacts/screenshots/planner.png)"}
	local, err := runner.New(runner.Config{RunnerID: "replay", StateDir: t.TempDir(), Token: "test-token", Listen: "127.0.0.1:0", MaxEvents: 2, Repositories: map[string]runner.RepositoryConfig{"repo": {Source: source, BaseCommit: commit}}}, adapter)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = local.Close() }()
	const base = "/clusters/tenant/apis/edges.railgrid.ai/v1alpha1/services/runner/proxy"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == strings.TrimSuffix(base, "/proxy") {
			_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "runner"}, "spec": map[string]any{"edgeRef": map[string]any{"name": "edge", "kind": "LinuxServer"}}, "status": map[string]any{"url": base}})
			return
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, base)
		local.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	remote, err := client.New(&rest.Config{Host: server.URL + "/clusters/tenant", BearerToken: "test-token", TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}}, client.ServiceRef{Cluster: "tenant", Service: "runner", EdgeKind: "LinuxServer", EdgeName: "edge", RunnerID: "replay"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	receipt, err := remote.Start(ctx, runner.StartRequest{RequestID: "start", TaskID: "task", AttemptID: "attempt", AttemptEpoch: 1, RepositoryID: "repo", BaseCommit: commit, Instructions: "change copy", ApprovedInput: json.RawMessage(`{"provenance":{"source":"test"}}`), HarnessCredential: &runner.HarnessCredential{Kind: string(harness.CredentialClaudeOAuth), Value: "test-credential"}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		current, err := remote.Inspect(ctx, receipt.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Phase.IsTerminal() {
			if current.Phase != runner.PhaseCompleted {
				t.Fatalf("failed: %+v", current)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, err := FollowSnapshot(ctx, Wrap(remote), receipt, Snapshot{}, Noop{})
	if err != nil || !out.Completed() {
		t.Fatalf("follow: %+v %v", out, err)
	}
	if out.Summary.Final != adapter.final {
		t.Fatalf("lost final PR description: %q", out.Summary.Final)
	}
	if adapter.runs.Load() != 1 {
		t.Fatal("replay dispatched duplicate work")
	}
}
