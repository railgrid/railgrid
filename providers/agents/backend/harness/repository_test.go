// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner"

	"github.com/railgrid/provider-agents/backend"
)

const (
	testBase   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testTree   = "cccccccccccccccccccccccccccccccccccccccc"
)

// fakeArtifacts serves artifact bytes by id, the way the runner client does.
type fakeArtifacts struct {
	bodies  map[string][]byte
	fetched []string
}

func (f *fakeArtifacts) Artifact(_ context.Context, _ string, artifactID string, w io.Writer) (runner.Artifact, error) {
	f.fetched = append(f.fetched, artifactID)
	body, ok := f.bodies[artifactID]
	if !ok {
		return runner.Artifact{}, errors.New("no such artifact")
	}
	if _, err := w.Write(body); err != nil {
		return runner.Artifact{}, err
	}
	sum := sha256.Sum256(body)
	return runner.Artifact{ID: artifactID, Digest: "sha256:" + hex.EncodeToString(sum[:]), Length: int64(len(body))}, nil
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// gitResult renders a document the runner would write.
func gitResult(t *testing.T, doc gitResultDocument) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func artifactRef(id, name string, body []byte) runner.Artifact {
	return runner.Artifact{ID: id, Name: name, Digest: "sha256:" + sha(body), Length: int64(len(body))}
}

// repositoryConfig is a repository attempt's Config: the run is its own task,
// no workspace, no session.
func repositoryConfig(f *fakeRunner, artifacts ArtifactReader, repo *Repository) Config {
	cfg := testConfig(f, 1, "")
	cfg.TaskID, cfg.WorkspaceID = "run-1", ""
	cfg.Repository = repo
	cfg.Artifacts = artifacts
	return cfg
}

func testRepository() *Repository {
	return &Repository{
		RepositoryID:         "repo-1",
		BaseCommit:           testBase,
		CommitMessage:        "Fix the thing",
		Source:               &runner.RepositorySource{RemoteURL: "https://git.example/org/repo.git", Token: "ghs_short_lived"},
		RequiredCapabilities: []string{"git-fetch-v1", "git-result-v1"},
		RequiredToolchains:   []string{"git"},
		Verification:         runner.VerificationRequirements{Names: []string{"unit"}},
		ApprovedInput:        json.RawMessage(`{"ticket":"T-1"}`),
	}
}

func completedReceipt(artifacts ...runner.Artifact) runner.Receipt {
	return runner.Receipt{TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1, Phase: runner.PhaseCompleted, Artifacts: artifacts}
}

var completedEvents = []runner.Event{
	event(1, runner.EventProgress, "working", ""),
	event(2, runner.EventCompleted, "", ""),
}

// A repository attempt is the protocol's OTHER shape: repository and commit
// instead of a workspace, a Git result exported, the coordinator's approved
// input verbatim, and the run as its own task at epoch 1.
func TestRepositoryStartRequestIsARepositoryAttempt(t *testing.T) {
	doc := gitResult(t, gitResultDocument{Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1, BaseCommit: testBase, NoChanges: true})
	f := &fakeRunner{receipt: completedReceipt(artifactRef("a-json", GitResultJSONName, doc)), events: completedEvents}
	arts := &fakeArtifacts{bodies: map[string][]byte{"a-json": doc}}
	b := New(repositoryConfig(f, arts, testRepository()))

	out, err := b.Turn(context.Background(), testRun(), backend.Input{
		Messages: []backend.Message{{Role: backend.RoleUser, Content: "implement T-1"}},
	}, &recordingSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if out.Status != backend.StatusCompleted {
		t.Fatalf("status = %s, want completed", out.Status)
	}
	if len(f.starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(f.starts))
	}
	req := f.starts[0]
	switch {
	case req.RepositoryID != "repo-1" || req.BaseCommit != testBase:
		t.Errorf("repositoryID/baseCommit = %q/%q", req.RepositoryID, req.BaseCommit)
	case req.WorkspaceID != "" || req.SessionID != "":
		t.Errorf("a repository attempt names no workspace (%q) and no session (%q)", req.WorkspaceID, req.SessionID)
	case !req.ExportGitResult:
		t.Error("a repository attempt exports a Git result")
	case req.Repository == nil || req.Repository.Token != "ghs_short_lived" || req.Repository.RemoteURL != "https://git.example/org/repo.git":
		t.Errorf("repository source = %+v, want the clone source with its token", req.Repository)
	case req.CommitMessage != "Fix the thing":
		t.Errorf("commitMessage = %q", req.CommitMessage)
	case req.TaskID != "run-1" || req.AttemptID != "run-1" || req.AttemptEpoch != 1:
		t.Errorf("task/attempt/epoch = %q/%q/%d, want run-1/run-1/1", req.TaskID, req.AttemptID, req.AttemptEpoch)
	case req.Limits.MaxTurns != 1:
		t.Errorf("limits.maxTurns = %d, want 1", req.Limits.MaxTurns)
	case req.AskPermission:
		t.Error("a repository attempt must not opt into the permission round-trip; nobody attends it")
	case req.HarnessCredential == nil || req.HarnessCredential.Value == "":
		t.Error("every dispatch carries a harness credential")
	case string(req.ApprovedInput) != `{"ticket":"T-1"}`:
		t.Errorf("approvedInput = %s, want the coordinator's verbatim", req.ApprovedInput)
	case strings.Join(req.RequiredCapabilities, ",") != "git-fetch-v1,git-result-v1" || strings.Join(req.RequiredToolchains, ",") != "git":
		t.Errorf("required capabilities/toolchains = %v/%v", req.RequiredCapabilities, req.RequiredToolchains)
	case len(req.Verification.Names) != 1 || req.Verification.Names[0] != "unit":
		t.Errorf("verification = %+v", req.Verification)
	}
	if out.Result == nil || !out.Result.NoChanges || out.Result.BaseCommit != testBase || out.Result.ResultDigest != sha(doc) {
		t.Fatalf("result = %+v, want a verified no-changes result", out.Result)
	}
	if len(out.Artifacts) != 1 || out.Artifacts[0].Name != GitResultJSONName || string(out.Artifacts[0].Data) != string(doc) {
		t.Fatalf("artifacts = %+v, want the document alone", out.Artifacts)
	}
}

// Without the coordinator's approved input the dispatch carries the provenance
// envelope every conversational turn carries, because the runner refuses an
// empty one.
func TestRepositoryWithoutApprovedInputSendsProvenance(t *testing.T) {
	doc := gitResult(t, gitResultDocument{Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1, BaseCommit: testBase, NoChanges: true})
	f := &fakeRunner{receipt: completedReceipt(artifactRef("a-json", GitResultJSONName, doc)), events: completedEvents}
	repo := testRepository()
	repo.ApprovedInput = nil
	b := New(repositoryConfig(f, &fakeArtifacts{bodies: map[string][]byte{"a-json": doc}}, repo))
	if _, err := b.Turn(context.Background(), testRun(), backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "go"}}}, &recordingSink{}); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(f.starts[0].ApprovedInput, &envelope); err != nil || len(envelope["provenance"]) == 0 {
		t.Fatalf("approvedInput = %s, want a provenance envelope", f.starts[0].ApprovedInput)
	}
}

// The clone token is dispatch data. A fresh start after the process that held
// it is gone has nothing to clone with and must say so, rather than dispatch
// a workspace turn in the repository's place.
func TestRepositoryFreshStartWithoutCloneSourceFails(t *testing.T) {
	f := &fakeRunner{receipt: completedReceipt(), events: completedEvents}
	repo := testRepository()
	repo.Source = nil
	b := New(repositoryConfig(f, &fakeArtifacts{}, repo))
	out, err := b.Turn(context.Background(), testRun(), backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "go"}}}, &recordingSink{})
	if !errors.Is(err, ErrCloneCredentialUnavailable) {
		t.Fatalf("err = %v, want ErrCloneCredentialUnavailable", err)
	}
	if out.Status != backend.StatusFailed || len(f.starts) != 0 {
		t.Fatalf("status = %s, starts = %d; nothing must be dispatched", out.Status, len(f.starts))
	}
}

// A repository attempt cannot also be a workspace one, and is its own task.
func TestRepositoryConfigRefusesWorkspaceShape(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"workspace":     func(c *Config) { c.WorkspaceID = "agent-scout-chat" },
		"session":       func(c *Config) { c.SessionID = "sess-1" },
		"epoch":         func(c *Config) { c.Epoch = 2 },
		"task":          func(c *Config) { c.TaskID = "agent-scout-chat" },
		"bad commit":    func(c *Config) { c.Repository.BaseCommit = "HEAD" },
		"bad repo id":   func(c *Config) { c.Repository.RepositoryID = "../x" },
		"no credential": func(c *Config) { c.Credential.Value = "" },
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRunner{receipt: completedReceipt(), events: completedEvents}
			cfg := repositoryConfig(f, &fakeArtifacts{}, testRepository())
			mutate(&cfg)
			if _, err := New(cfg).Turn(context.Background(), testRun(), backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "go"}}}, &recordingSink{}); err == nil {
				t.Fatal("Turn accepted a repository config it must refuse")
			}
			if len(f.starts) != 0 {
				t.Fatal("nothing must be dispatched")
			}
		})
	}
}

// Verification of what the runner exported, document by document.
func TestVerifyGitResult(t *testing.T) {
	bundle := []byte("not really a bundle but hashed like one")
	good := gitResultDocument{
		Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1,
		BaseCommit: testBase, Commit: testCommit, Tree: testTree, BundleSHA256: "sha256:" + sha(bundle),
	}
	mutate := func(fn func(*gitResultDocument)) gitResultDocument {
		d := good
		fn(&d)
		return d
	}
	cases := []struct {
		name          string
		doc           gitResultDocument
		bundle        []byte
		bundlePresent bool
		wantErr       string
		wantNoChanges bool
	}{
		{name: "good result", doc: good, bundle: bundle, bundlePresent: true},
		{name: "bare digest accepted", doc: mutate(func(d *gitResultDocument) { d.BundleSHA256 = sha(bundle) }), bundle: bundle, bundlePresent: true},
		{name: "no changes", doc: gitResultDocument{Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1, BaseCommit: testBase, NoChanges: true}, wantNoChanges: true},
		{name: "no changes with a bundle", doc: gitResultDocument{Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1, BaseCommit: testBase, NoChanges: true}, bundle: bundle, bundlePresent: true, wantErr: "reports no changes but exports a snapshot"},
		{name: "mismatched base", doc: mutate(func(d *gitResultDocument) { d.BaseCommit = testCommit }), bundle: bundle, bundlePresent: true, wantErr: "base commit"},
		{name: "digest mismatch", doc: good, bundle: []byte("different bytes"), bundlePresent: true, wantErr: "bundle hashes to"},
		{name: "missing bundle", doc: good, bundlePresent: false, wantErr: "git-result.bundle is missing"},
		{name: "wrong version", doc: mutate(func(d *gitResultDocument) { d.Version = "git-result/v2" }), bundle: bundle, bundlePresent: true, wantErr: "version"},
		{name: "another attempt", doc: mutate(func(d *gitResultDocument) { d.AttemptID = "run-2" }), bundle: bundle, bundlePresent: true, wantErr: "not run run-1"},
		{name: "another task", doc: mutate(func(d *gitResultDocument) { d.TaskID = "agent-scout-chat" }), bundle: bundle, bundlePresent: true, wantErr: "not run run-1"},
		{name: "wrong epoch", doc: mutate(func(d *gitResultDocument) { d.AttemptEpoch = 2 }), bundle: bundle, bundlePresent: true, wantErr: "epoch 2"},
		{name: "commit equals base", doc: mutate(func(d *gitResultDocument) { d.Commit = testBase }), bundle: bundle, bundlePresent: true, wantErr: "equals the base"},
		{name: "short commit", doc: mutate(func(d *gitResultDocument) { d.Commit = "abc123" }), bundle: bundle, bundlePresent: true, wantErr: "not a full object id"},
		{name: "short tree", doc: mutate(func(d *gitResultDocument) { d.Tree = "" }), bundle: bundle, bundlePresent: true, wantErr: "not a full object id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := gitResult(t, tc.doc)
			result, err := verifyGitResult("run-1", testBase, raw, tc.bundle, tc.bundlePresent)
			if tc.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), gitResultUnusable) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q prefixed %q", err, tc.wantErr, gitResultUnusable)
				}
				return
			}
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if result.BaseCommit != testBase || result.ResultDigest != sha(raw) || result.NoChanges != tc.wantNoChanges {
				t.Fatalf("result = %+v", result)
			}
			if tc.wantNoChanges {
				if result.Commit != "" || result.BundleDigest != "" || result.BundleSize != 0 {
					t.Fatalf("a no-changes result carries no snapshot: %+v", result)
				}
				return
			}
			if result.Commit != testCommit || result.Tree != testTree || result.BundleDigest != sha(tc.bundle) || result.BundleSize != int64(len(tc.bundle)) {
				t.Fatalf("result = %+v", result)
			}
		})
	}
	t.Run("undecodable", func(t *testing.T) {
		if _, err := verifyGitResult("run-1", testBase, []byte("{"), nil, false); err == nil || !strings.HasPrefix(err.Error(), gitResultUnusable) {
			t.Fatalf("err = %v", err)
		}
	})
}

// A completed receipt is followed by fetching both artifacts, verifying them,
// and handing their bytes over. The result's digests are the bytes', so a
// reader can re-check what it is later served.
func TestRepositoryCompletionFetchesAndVerifiesTheArtifacts(t *testing.T) {
	bundle := []byte("bundle bytes")
	doc := gitResult(t, gitResultDocument{
		Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1,
		BaseCommit: testBase, Commit: testCommit, Tree: testTree, BundleSHA256: "sha256:" + sha(bundle),
	})
	jsonRef := artifactRef("a-json", GitResultJSONName, doc)
	jsonRef.MediaType = "application/json"
	bundleRef := artifactRef("a-bundle", GitResultBundleName, bundle)
	bundleRef.MediaType = "application/x-git-bundle"
	f := &fakeRunner{receipt: completedReceipt(jsonRef, bundleRef), events: completedEvents}
	arts := &fakeArtifacts{bodies: map[string][]byte{"a-json": doc, "a-bundle": bundle}}
	b := New(repositoryConfig(f, arts, testRepository()))

	out, err := b.Turn(context.Background(), testRun(), backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "go"}}}, &recordingSink{})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if out.Status != backend.StatusCompleted || out.Result == nil {
		t.Fatalf("status = %s result = %+v", out.Status, out.Result)
	}
	if out.Result.Commit != testCommit || out.Result.Tree != testTree || out.Result.NoChanges ||
		out.Result.BundleDigest != sha(bundle) || out.Result.BundleSize != int64(len(bundle)) || out.Result.ResultDigest != sha(doc) {
		t.Fatalf("result = %+v", out.Result)
	}
	if len(out.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(out.Artifacts))
	}
	byName := map[string]backend.Artifact{}
	for _, a := range out.Artifacts {
		byName[a.Name] = a
	}
	if a := byName[GitResultBundleName]; string(a.Data) != string(bundle) || a.Digest != sha(bundle) || a.MediaType != "application/x-git-bundle" || a.Size != int64(len(bundle)) {
		t.Fatalf("bundle artifact = %+v", a)
	}
	if a := byName[GitResultJSONName]; string(a.Data) != string(doc) || a.Digest != sha(doc) || a.MediaType != "application/json" {
		t.Fatalf("json artifact = %+v", a)
	}
	if strings.Join(arts.fetched, ",") != "a-json,a-bundle" {
		t.Fatalf("fetched = %v", arts.fetched)
	}
}

// Every way the export can be unusable fails the run with the one prefix a
// reader of its message branches on, and keeps the usage: the work was done.
func TestRepositoryCompletionWithAnUnusableResultFails(t *testing.T) {
	bundle := []byte("bundle bytes")
	goodDoc := gitResult(t, gitResultDocument{
		Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1,
		BaseCommit: testBase, Commit: testCommit, Tree: testTree, BundleSHA256: "sha256:" + sha(bundle),
	})
	for name, tc := range map[string]struct {
		artifacts []runner.Artifact
		bodies    map[string][]byte
		noReader  bool
		want      string
	}{
		"artifact missing": {
			artifacts: nil, bodies: map[string][]byte{},
			want: "git result unusable: artifact missing",
		},
		"bundle bytes differ from the document": {
			artifacts: []runner.Artifact{artifactRef("a-json", GitResultJSONName, goodDoc), artifactRef("a-bundle", GitResultBundleName, []byte("other"))},
			bodies:    map[string][]byte{"a-json": goodDoc, "a-bundle": []byte("other")},
			want:      "bundle hashes to",
		},
		"body does not match the receipt": {
			artifacts: []runner.Artifact{artifactRef("a-json", GitResultJSONName, goodDoc), artifactRef("a-bundle", GitResultBundleName, bundle)},
			bodies:    map[string][]byte{"a-json": goodDoc, "a-bundle": []byte("tampered bytes")},
			want:      "receipt announced",
		},
		"document for another base": {
			artifacts: []runner.Artifact{artifactRef("a-json", GitResultJSONName, gitResult(t, gitResultDocument{Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1, BaseCommit: testCommit, NoChanges: true}))},
			bodies:    map[string][]byte{"a-json": gitResult(t, gitResultDocument{Version: gitResultVersion, TaskID: "run-1", AttemptID: "run-1", AttemptEpoch: 1, BaseCommit: testCommit, NoChanges: true})},
			want:      "base commit",
		},
		"document too large": {
			artifacts: []runner.Artifact{{ID: "a-json", Name: GitResultJSONName, Length: maxGitResultJSONBytes + 1}},
			bodies:    map[string][]byte{},
			want:      "over the",
		},
		"no artifact reader": {
			artifacts: []runner.Artifact{artifactRef("a-json", GitResultJSONName, goodDoc)},
			bodies:    map[string][]byte{"a-json": goodDoc},
			noReader:  true,
			want:      "cannot serve artifacts",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRunner{receipt: completedReceipt(tc.artifacts...), events: completedEvents}
			var reader ArtifactReader = &fakeArtifacts{bodies: tc.bodies}
			if tc.noReader {
				reader = nil
			}
			b := New(repositoryConfig(f, reader, testRepository()))
			out, err := b.Turn(context.Background(), testRun(), backend.Input{Messages: []backend.Message{{Role: backend.RoleUser, Content: "go"}}}, &recordingSink{})
			if err == nil || !strings.HasPrefix(err.Error(), gitResultUnusable) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q under %q", err, tc.want, gitResultUnusable)
			}
			if out.Status != backend.StatusFailed || out.Result != nil || len(out.Artifacts) != 0 {
				t.Fatalf("outcome = %+v, want failed with nothing to store", out)
			}
		})
	}
}

// The reader is bounded on the way in, whatever the receipt announced.
func TestBoundedWriterRefusesOverflow(t *testing.T) {
	var sink strings.Builder
	w := &boundedWriter{w: &sink, remaining: 4}
	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("e")); !errors.Is(err, errArtifactTooLarge) {
		t.Fatalf("err = %v, want errArtifactTooLarge", err)
	}
}
