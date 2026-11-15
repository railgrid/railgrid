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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// This runs through the runner/v1 wire handler and exercises a real local app
// from the pinned repository checkout. The adapter owns the local test process;
// Runner owns the checkout, artifact validation, resource lease and retention.
func TestRunnerV1LocalAppAcceptanceEvidenceAndCancellationRecovery(t *testing.T) {
	source, _ := testGitSource(t)
	if err := os.WriteFile(filepath.Join(source, ".gitignore"), []byte("artifacts/\napp-server.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("<title>Local app acceptance fixture</title><main>checkout is pinned</main>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", ".gitignore", "index.html")
	runGit(t, source, "-c", "user.name=Runner Test", "-c", "user.email=runner-test@example.invalid", "commit", "-m", "local app fixture")
	commit := strings.TrimSpace(string(runGit(t, source, "rev-parse", "HEAD")))
	port, err := freeTCPPort()
	if err != nil {
		t.Fatal(err)
	}
	cancelStarted := make(chan *exec.Cmd, 1)
	adapter := &fakeAdapter{name: "local-app", run: func(ctx context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if launch.AttemptID == "attempt-cancel" {
			cmd := exec.CommandContext(ctx, "python3", "-m", "http.server", fmt.Sprint(port), "--bind", "127.0.0.1")
			cmd.Dir = launch.Workdir
			if err := cmd.Start(); err != nil {
				return harness.Result{}, err
			}
			cancelStarted <- cmd
			<-ctx.Done()
			_ = cmd.Wait()
			return harness.Result{Phase: string(PhaseCancelled), SessionID: launch.SessionID}, nil
		}
		cmd := exec.CommandContext(ctx, "python3", "-m", "http.server", fmt.Sprint(port), "--bind", "127.0.0.1")
		cmd.Dir = launch.Workdir
		logFile, err := os.Create(filepath.Join(launch.Workdir, "app-server.log"))
		if err != nil {
			return harness.Result{}, err
		}
		cmd.Stdout, cmd.Stderr = logFile, logFile
		if err := cmd.Start(); err != nil {
			_ = logFile.Close()
			return harness.Result{}, err
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = logFile.Close() }()
		url := fmt.Sprintf("http://127.0.0.1:%d/", port)
		var response *http.Response
		client := &http.Client{Timeout: 500 * time.Millisecond}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			response, err = client.Do(request)
			if err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if err != nil {
			return harness.Result{}, fmt.Errorf("local app readiness: %w", err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			return harness.Result{}, readErr
		}
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "checkout is pinned") {
			return harness.Result{}, fmt.Errorf("acceptance failed: status=%d body=%q", response.StatusCode, body)
		}
		if err := emit(harness.Event{Type: EventProgress, Message: "local app returned the expected pinned checkout page"}); err != nil {
			return harness.Result{}, err
		}
		resultPath := filepath.Join(launch.Workdir, "artifacts", "acceptance.txt")
		if err := os.MkdirAll(filepath.Dir(resultPath), 0o700); err != nil {
			return harness.Result{}, err
		}
		if err := os.WriteFile(resultPath, []byte("PASS GET / returned the pinned checkout content\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	stateDir := t.TempDir()
	r, err := New(Config{RunnerID: "local-app", StateDir: stateDir, Token: "test-token", Listen: "127.0.0.1:0", Repositories: map[string]RepositoryConfig{"repo": {Source: source, BaseCommit: commit}}, Resources: map[string]ResourceConfig{"local-app": {Capacity: 1}}}, adapter)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	server := httptest.NewServer(http.HandlerFunc(r.serveHTTP))
	defer server.Close()
	start := testStartRequest(commit)
	start.TaskID, start.AttemptID, start.RequestID = "task-local", "attempt-local", "start-local"
	start.Artifacts = []ArtifactSpec{{Name: "acceptance", Path: "artifacts/acceptance.txt", MediaType: "text/plain", CollectOnCompletion: true}}
	start.Resources = []ResourceRequest{{Name: "local-app"}}
	start.ExportGitResult = true
	receipt := postRunnerV1[Receipt](t, server.URL+"/runner/v1/attempts", start)
	waitForPhase(t, r, receipt.AttemptID, PhaseCompleted)
	completed := getRunnerV1[Receipt](t, server.URL+"/runner/v1/attempts/"+receipt.AttemptID)
	if completed.Workdir == "" {
		t.Fatal("receipt omitted its scoped checkout")
	}
	var evidence *Artifact
	for i := range completed.Artifacts {
		if completed.Artifacts[i].Name == "acceptance" {
			evidence = &completed.Artifacts[i]
		}
	}
	if evidence == nil {
		t.Fatalf("acceptance artifact missing: %+v", completed.Artifacts)
	}
	body := getRunnerV1Bytes(t, server.URL+"/runner/v1/attempts/"+receipt.AttemptID+"/artifacts/"+evidence.ID)
	if !strings.Contains(string(body), "PASS GET /") {
		t.Fatalf("test evidence = %q", body)
	}
	var gitEvidence gitResultDocument
	for _, artifact := range completed.Artifacts {
		if artifact.Name == gitResultJSONName {
			data := getRunnerV1Bytes(t, server.URL+"/runner/v1/attempts/"+receipt.AttemptID+"/artifacts/"+artifact.ID)
			if err := json.Unmarshal(data, &gitEvidence); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !gitEvidence.NoChanges || gitEvidence.BaseCommit != commit {
		t.Fatalf("host-observed final source state = %+v", gitEvidence)
	}
	r.mu.Lock()
	_, reserved := r.state.Reservations["local-app/"+receipt.AttemptID]
	r.mu.Unlock()
	if reserved {
		t.Fatal("completed attempt retained its local app resource reservation")
	}

	// A second attempt starts a real local server but is cancelled while it is
	// running. The process stops and the attempt's own checkout remains retained
	// for recovery/inspection; its scoped resource lease is released.
	cancel := testStartRequest(commit)
	cancel.TaskID, cancel.AttemptID, cancel.RequestID = "task-local", "attempt-cancel", "start-cancel"
	cancel.AttemptEpoch = 2
	cancel.Resources = []ResourceRequest{{Name: "local-app"}}
	cancelRec := postRunnerV1[Receipt](t, server.URL+"/runner/v1/attempts", cancel)
	select {
	case <-cancelStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel fixture server did not start")
	}
	deadline := time.Now().Add(5 * time.Second)
	serverReady := false
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			serverReady = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !serverReady {
		t.Fatal("cancel fixture server did not become reachable")
	}
	cancelReq := CancelRequest{ProtocolVersion: ProtocolVersion, RequestID: "cancel-local", TaskID: cancel.TaskID, AttemptID: cancel.AttemptID, AttemptEpoch: cancel.AttemptEpoch}
	_ = postRunnerV1[Receipt](t, server.URL+"/runner/v1/attempts/"+cancel.AttemptID+"/cancel", cancelReq)
	waitForPhase(t, r, cancelRec.AttemptID, PhaseCancelled)
	if _, err := os.Stat(cancelRec.Workdir); err != nil {
		t.Fatalf("cancelled checkout was not retained: %v", err)
	}
	r.mu.Lock()
	_, reserved = r.state.Reservations["local-app/"+cancelRec.AttemptID]
	r.mu.Unlock()
	if reserved {
		t.Fatal("cancelled attempt retained its local app resource reservation")
	}
	connection, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		t.Fatal("cancelled local app still accepts connections")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = New(Config{RunnerID: "local-app", StateDir: stateDir, Token: "test-token", Listen: "127.0.0.1:0", Repositories: map[string]RepositoryConfig{"repo": {Source: source, BaseCommit: commit}}, Resources: map[string]ResourceConfig{"local-app": {Capacity: 1}}}, adapter)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	recovered, err := r.Inspect(context.Background(), cancelRec.AttemptID)
	if err != nil || recovered.Phase != PhaseCancelled {
		t.Fatalf("cancel receipt recovery = %+v, %v", recovered, err)
	}
}

func freeTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func postRunnerV1[T any](t *testing.T, url string, value any) T {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		content, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s: status=%d body=%s", url, response.StatusCode, content)
	}
	var result T
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func getRunnerV1[T any](t *testing.T, url string) T {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		content, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s: status=%d body=%s", url, response.StatusCode, content)
	}
	var result T
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func getRunnerV1Bytes(t *testing.T, url string) []byte {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		content, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s: status=%d body=%s", url, response.StatusCode, content)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
