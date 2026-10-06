// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/dispatch"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/store"
)

const (
	lifecycleBase   = "0123456789abcdef0123456789abcdef01234567"
	lifecycleCommit = "89abcdef0123456789abcdef0123456789abcdef"
	lifecycleTree   = "456789abcdef0123456789abcdef0123456789ab"
)

// repositoryLifecycleDispatcher completes every attempt at once and serves the
// artifacts it announced, the way a real runner connection does.
type repositoryLifecycleDispatcher struct {
	mu        sync.Mutex
	starts    []runner.StartRequest
	artifacts []runner.Artifact
	bodies    map[string][]byte
	fetched   []string
}

func (d *repositoryLifecycleDispatcher) Start(_ context.Context, req runner.StartRequest) (runner.Receipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.starts = append(d.starts, req)
	return d.receipt(req.TaskID, req.AttemptID, req.AttemptEpoch), nil
}

func (d *repositoryLifecycleDispatcher) receipt(taskID, attemptID string, epoch uint64) runner.Receipt {
	return runner.Receipt{
		TaskID: taskID, AttemptID: attemptID, AttemptEpoch: epoch, Phase: runner.PhaseCompleted,
		SessionID: "harness-thread", Cursor: 2, Artifacts: d.artifacts,
	}
}

func (*repositoryLifecycleDispatcher) Resume(context.Context, runner.ResumeRequest) (runner.Receipt, error) {
	return runner.Receipt{}, errors.New("unexpected harness resume")
}

func (*repositoryLifecycleDispatcher) Cancel(context.Context, runner.CancelRequest) (runner.Receipt, error) {
	return runner.Receipt{}, errors.New("unexpected harness cancel")
}

func (d *repositoryLifecycleDispatcher) Inspect(_ context.Context, attemptID string) (runner.Receipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.receipt(attemptID, attemptID, 1), nil
}

func (d *repositoryLifecycleDispatcher) Events(_ context.Context, _ string, after uint64) (dispatch.Stream, error) {
	events := []runner.Event{
		{Cursor: 1, Type: runner.EventProgress, Message: "working", AttemptEpoch: 1},
		{Cursor: 2, Type: runner.EventCompleted, AttemptEpoch: 1},
	}
	var out []runner.Event
	for _, e := range events {
		if e.Cursor > after {
			out = append(out, e)
		}
	}
	return &lifecycleEventSlice{events: out}, nil
}

func (d *repositoryLifecycleDispatcher) Artifact(_ context.Context, _ string, artifactID string, w io.Writer) (runner.Artifact, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fetched = append(d.fetched, artifactID)
	body, ok := d.bodies[artifactID]
	if !ok {
		return runner.Artifact{}, errors.New("no such artifact")
	}
	if _, err := w.Write(body); err != nil {
		return runner.Artifact{}, err
	}
	return runner.Artifact{ID: artifactID, Digest: "sha256:" + hexSHA(body), Length: int64(len(body))}, nil
}

type lifecycleEventSlice struct {
	events []runner.Event
	i      int
}

func (s *lifecycleEventSlice) Next(ctx context.Context) (runner.Event, error) {
	if err := ctx.Err(); err != nil {
		return runner.Event{}, err
	}
	if s.i >= len(s.events) {
		return runner.Event{}, io.EOF
	}
	e := s.events[s.i]
	s.i++
	return e, nil
}

func (*lifecycleEventSlice) Close() error { return nil }

func hexSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// gitResultArtifacts renders a runner's export: the document, and the bundle
// unless noChanges.
func gitResultArtifacts(t *testing.T, runID string, noChanges bool) (artifacts []runner.Artifact, bodies map[string][]byte) {
	t.Helper()
	bundle := []byte("bundle-bytes-for-" + runID)
	doc := map[string]any{
		"version": "git-result/v1", "taskID": runID, "attemptID": runID, "attemptEpoch": 1,
		"baseCommit": lifecycleBase, "noChanges": noChanges,
	}
	if !noChanges {
		doc["commit"], doc["tree"], doc["bundleSHA256"] = lifecycleCommit, lifecycleTree, "sha256:"+hexSHA(bundle)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	artifacts = []runner.Artifact{{ID: "art-json", Name: backendharness.GitResultJSONName, Digest: "sha256:" + hexSHA(raw), Length: int64(len(raw)), MediaType: "application/json"}}
	bodies = map[string][]byte{"art-json": raw}
	if !noChanges {
		artifacts = append(artifacts, runner.Artifact{ID: "art-bundle", Name: backendharness.GitResultBundleName, Digest: "sha256:" + hexSHA(bundle), Length: int64(len(bundle)), MediaType: "application/x-git-bundle"})
		bodies["art-bundle"] = bundle
	}
	return artifacts, bodies
}

func repositoryLifecycleRun(t *testing.T, dispatcher dispatch.Runner) (*Server, taskRun) {
	t.Helper()
	s, run := newHarnessRunLifecycleFixture(t, dispatcher, 0)
	run.Trigger = agentsv1alpha1.RunTriggerAPI
	run.SessionID = ""
	run.Task = "implement T-1"
	run.Agent.Spec.SystemPrompt = "You are the coder."
	run.Repository = &repositoryAttempt{
		Repository: backendharness.Repository{
			RepositoryID: "railgrid", BaseCommit: lifecycleBase, CommitMessage: "Implement T-1",
			Source:               &runner.RepositorySource{RemoteURL: "https://github.com/railgrid/railgrid.git", Token: "ghs_short_lived"},
			RequiredCapabilities: []string{"git-fetch-v1", "git-result-v1"},
			RequiredToolchains:   []string{"git"},
			ApprovedInput:        json.RawMessage(`{"ticket":"T-1"}`),
		},
		MaxDurationSeconds: 900,
	}
	// The record a detached starter writes before the turn runs.
	now := metav1.Now().UTC()
	if err := s.store.SaveRun(t.Context(), run.Scope, store.Run{
		ID: run.RunID, AgentName: run.Agent.Name, Trigger: run.Trigger, Phase: store.RunPhasePending,
		Input: run.Task, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return s, run
}

// The whole lifecycle of a repository run: dispatched as a repository attempt
// with the token, completed, its export fetched and verified, the artifacts
// stored under the run, the result on the record — and the token nowhere.
func TestRepositoryRunLifecycleStoresTheVerifiedResult(t *testing.T) {
	artifacts, bodies := gitResultArtifacts(t, "run-lifecycle", false)
	dispatcher := &repositoryLifecycleDispatcher{artifacts: artifacts, bodies: bodies}
	s, run := repositoryLifecycleRun(t, dispatcher)

	res, err := s.runTurn(t.Context(), run, nil)
	if err != nil {
		t.Fatalf("runTurn: %v", err)
	}
	if res.Phase != store.RunPhaseSucceeded {
		t.Fatalf("phase = %s, want Succeeded", res.Phase)
	}

	// The dispatch: a repository attempt, the run its own task, the token on
	// the start and the coordinator's input verbatim.
	if len(dispatcher.starts) != 1 {
		t.Fatalf("starts = %d", len(dispatcher.starts))
	}
	req := dispatcher.starts[0]
	switch {
	case req.RepositoryID != "railgrid" || req.BaseCommit != lifecycleBase || !req.ExportGitResult:
		t.Errorf("start = %+v, want a repository attempt", req)
	case req.Repository == nil || req.Repository.Token != "ghs_short_lived":
		t.Error("the clone source with its token goes on the start")
	case req.WorkspaceID != "" || req.SessionID != "":
		t.Errorf("workspace %q session %q, want neither", req.WorkspaceID, req.SessionID)
	case req.TaskID != run.RunID || req.AttemptID != run.RunID || req.AttemptEpoch != 1:
		t.Errorf("task/attempt/epoch = %q/%q/%d", req.TaskID, req.AttemptID, req.AttemptEpoch)
	case req.CommitMessage != "Implement T-1":
		t.Errorf("commitMessage = %q", req.CommitMessage)
	case string(req.ApprovedInput) != `{"ticket":"T-1"}`:
		t.Errorf("approvedInput = %s", req.ApprovedInput)
	case req.Limits.MaxDurationSeconds != 900:
		t.Errorf("maxDurationSeconds = %d", req.Limits.MaxDurationSeconds)
	}
	// The prompt is the persona and the approved task, nothing from a session.
	if !strings.Contains(req.Instructions, "You are the coder.") || !strings.Contains(req.Instructions, "implement T-1") {
		t.Errorf("instructions = %q", req.Instructions)
	}
	if strings.Contains(req.Instructions, "you said earlier") {
		t.Error("a repository attempt carries no history")
	}

	// The record: Succeeded, the request minus the credential, the result.
	stored, err := s.store.GetRun(t.Context(), run.Scope, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != store.RunPhaseSucceeded || stored.Backend != agentsv1alpha1.AgentBackendHarness || stored.AttemptID != run.RunID {
		t.Fatalf("stored = %+v", stored)
	}
	if stored.Repository == nil || stored.Repository.RepositoryID != "railgrid" || stored.Repository.BaseCommit != lifecycleBase || stored.Repository.CommitMessage != "Implement T-1" {
		t.Fatalf("repository = %+v", stored.Repository)
	}
	if stored.Result == nil || stored.Result.Commit != lifecycleCommit || stored.Result.Tree != lifecycleTree || stored.Result.BaseCommit != lifecycleBase || stored.Result.NoChanges {
		t.Fatalf("result = %+v", stored.Result)
	}
	if stored.Result.BundleDigest != hexSHA(bodies["art-bundle"]) || stored.Result.ResultDigest != hexSHA(bodies["art-json"]) || stored.Result.BundleSize != int64(len(bodies["art-bundle"])) {
		t.Fatalf("result digests = %+v", stored.Result)
	}
	if stored.Input != "implement T-1" {
		t.Fatalf("input = %q, want the task text only", stored.Input)
	}
	if raw, _ := json.Marshal(stored); strings.Contains(string(raw), "ghs_short_lived") {
		t.Fatal("the run record carries the clone token")
	}

	// The artifacts, stored under the run with the digests the result names.
	for name, id := range map[string]string{backendharness.GitResultJSONName: "art-json", backendharness.GitResultBundleName: "art-bundle"} {
		a, found, err := s.store.GetRunArtifact(t.Context(), run.Scope, run.RunID, name)
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", name, found, err)
		}
		if string(a.Data) != string(bodies[id]) || a.Digest != hexSHA(bodies[id]) || a.Size != int64(len(bodies[id])) {
			t.Fatalf("%s = %+v", name, a)
		}
	}

	// No harness session was written: a repository attempt chains onto nothing
	// and must not leave a row a later chat turn would resume from.
	if _, found, _ := s.store.GetHarnessSession(t.Context(), run.Scope, effectiveSessionID("", run.Trigger)); found {
		t.Fatal("a repository attempt must not record a harness session")
	}
	// The transcript holds the task and the answer; never the token.
	rows, err := s.store.LoadRecentMessages(t.Context(), run.Scope, effectiveSessionID("", run.Trigger), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Content, "ghs_short_lived") {
			t.Fatal("a transcript row carries the clone token")
		}
		if raw, _ := json.Marshal(row.Metadata); strings.Contains(string(raw), "ghs_short_lived") {
			t.Fatal("transcript metadata carries the clone token")
		}
	}

	// The projection: spec.repository and status.result in the contract's shape.
	status := runStatusFor(stored)
	if status.Result == nil || status.Result.Commit != lifecycleCommit || status.Result.BundleDigest != stored.Result.BundleDigest {
		t.Fatalf("status.result = %+v", status.Result)
	}
}

// A completed attempt that reports no usable export is a FAILED run, with the
// prefix a coordinator branches on, and stores nothing.
func TestRepositoryRunWithoutAnExportFails(t *testing.T) {
	dispatcher := &repositoryLifecycleDispatcher{bodies: map[string][]byte{}}
	s, run := repositoryLifecycleRun(t, dispatcher)

	res, err := s.runTurn(t.Context(), run, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "git result unusable: artifact missing") {
		t.Fatalf("err = %v", err)
	}
	if res.Phase != store.RunPhaseFailed {
		t.Fatalf("phase = %s", res.Phase)
	}
	stored, err := s.store.GetRun(t.Context(), run.Scope, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != store.RunPhaseFailed || !strings.HasPrefix(stored.Message, "git result unusable: ") || stored.Result != nil {
		t.Fatalf("stored = %+v", stored)
	}
	if _, found, _ := s.store.GetRunArtifact(t.Context(), run.Scope, run.RunID, backendharness.GitResultJSONName); found {
		t.Fatal("nothing must be stored for an unusable result")
	}
}

// A no-changes export succeeds with no bundle and no commit.
func TestRepositoryRunWithNoChangesSucceedsWithoutABundle(t *testing.T) {
	artifacts, bodies := gitResultArtifacts(t, "run-lifecycle", true)
	dispatcher := &repositoryLifecycleDispatcher{artifacts: artifacts, bodies: bodies}
	s, run := repositoryLifecycleRun(t, dispatcher)

	if res, err := s.runTurn(t.Context(), run, nil); err != nil || res.Phase != store.RunPhaseSucceeded {
		t.Fatalf("runTurn: %v %s", err, res.Phase)
	}
	stored, _ := s.store.GetRun(t.Context(), run.Scope, run.RunID)
	if stored.Result == nil || !stored.Result.NoChanges || stored.Result.Commit != "" || stored.Result.BundleDigest != "" {
		t.Fatalf("result = %+v", stored.Result)
	}
	if _, found, _ := s.store.GetRunArtifact(t.Context(), run.Scope, run.RunID, backendharness.GitResultBundleName); found {
		t.Fatal("a no-changes result has no bundle")
	}
	if _, found, _ := s.store.GetRunArtifact(t.Context(), run.Scope, run.RunID, backendharness.GitResultJSONName); !found {
		t.Fatal("the document is still stored")
	}
}

// A repository run whose Start was never issued before the provider restarted
// has lost its clone token with the process. It fails with the message the
// coordinator retries on, and nothing is dispatched.
func TestRepositoryRunRestartedBeforeStartFailsForRetry(t *testing.T) {
	dispatcher := &repositoryLifecycleDispatcher{bodies: map[string][]byte{}}
	s, run := repositoryLifecycleRun(t, dispatcher)
	// Rebuilt from the record, as a restarted process would: no source.
	run.Repository = repositoryAttemptFromStored(run.Repository.persisted())

	res, err := s.runTurn(t.Context(), run, nil)
	if !errors.Is(err, backendharness.ErrCloneCredentialUnavailable) {
		t.Fatalf("err = %v, want ErrCloneCredentialUnavailable", err)
	}
	if res.Phase != store.RunPhaseFailed {
		t.Fatalf("phase = %s", res.Phase)
	}
	stored, _ := s.store.GetRun(t.Context(), run.Scope, run.RunID)
	if stored.Message != "clone credential not available after restart; retry" {
		t.Fatalf("message = %q", stored.Message)
	}
	if len(dispatcher.starts) != 0 {
		t.Fatal("nothing must be dispatched without the clone source")
	}
}

// The KRM types spell the contract's field names.
func TestRunRepositoryAndResultWireShapes(t *testing.T) {
	raw, _ := json.Marshal(agentsv1alpha1.RunRepository{RepositoryID: "r", BaseCommit: lifecycleBase, CommitMessage: "m"})
	if string(raw) != `{"repositoryID":"r","baseCommit":"`+lifecycleBase+`","commitMessage":"m"}` {
		t.Fatalf("spec.repository = %s", raw)
	}
	raw, _ = json.Marshal(agentsv1alpha1.RunResult{BaseCommit: lifecycleBase, NoChanges: true, ResultDigest: "d"})
	if string(raw) != `{"baseCommit":"`+lifecycleBase+`","noChanges":true,"resultDigest":"d"}` {
		t.Fatalf("status.result = %s", raw)
	}
	raw, _ = json.Marshal(runArtifactResponse{Name: "git-result.bundle", Digest: "d", Size: 3, MediaType: "application/x-git-bundle", Data: "YWJj"})
	if string(raw) != `{"name":"git-result.bundle","digest":"d","size":3,"mediaType":"application/x-git-bundle","data":"YWJj"}` {
		t.Fatalf("artifact result = %s", raw)
	}
}
