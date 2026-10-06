// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane/conformance"

	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
)

const (
	validBase  = "0123456789abcdef0123456789abcdef01234567"
	validRemot = "https://github.com/railgrid/railgrid.git"
)

func validRepositoryInput() invokeRepository {
	return invokeRepository{
		RepositoryID:         "railgrid",
		BaseCommit:           strings.ToUpper(validBase),
		CloneSource:          &invokeCloneSource{RemoteURL: validRemot, Token: "ghs_token"},
		CommitMessage:        "  Implement T-1  ",
		RequiredCapabilities: []string{"git-fetch-v1", "git-result-v1"},
		RequiredToolchains:   []string{"git"},
		Verification:         &invokeVerification{Names: []string{"unit"}, Commands: []string{"go test ./..."}},
		ApprovedInput:        json.RawMessage(`{"ticket":"T-1"}`),
		MaxDurationSeconds:   900,
	}
}

// A valid block is normalized: the commit lower-cased, the message trimmed,
// and the clone source carried whole for the dispatch.
func TestRepositoryInputValidates(t *testing.T) {
	in := validRepositoryInput()
	got, err := in.validate()
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	switch {
	case got.RepositoryID != "railgrid":
		t.Errorf("repositoryID = %q", got.RepositoryID)
	case got.BaseCommit != validBase:
		t.Errorf("baseCommit = %q, want lower-cased", got.BaseCommit)
	case got.CommitMessage != "Implement T-1":
		t.Errorf("commitMessage = %q, want trimmed", got.CommitMessage)
	case got.Source == nil || got.Source.Token != "ghs_token" || got.Source.RemoteURL != validRemot:
		t.Errorf("source = %+v", got.Source)
	case string(got.ApprovedInput) != `{"ticket":"T-1"}`:
		t.Errorf("approvedInput = %s", got.ApprovedInput)
	case got.MaxDurationSeconds != 900:
		t.Errorf("maxDurationSeconds = %d", got.MaxDurationSeconds)
	case len(got.Verification.Commands) != 1 || got.Verification.Commands[0] != "go test ./...":
		t.Errorf("verification = %+v", got.Verification)
	}
	// What the run record keeps: the coordinates, never the credential.
	kept := got.persisted()
	if kept.RepositoryID != "railgrid" || kept.BaseCommit != validBase || kept.CommitMessage != "Implement T-1" {
		t.Fatalf("persisted = %+v", kept)
	}
	if raw, _ := json.Marshal(kept); strings.Contains(string(raw), "ghs_token") {
		t.Fatalf("the persisted block carries the clone token: %s", raw)
	}
	// Absent is absent.
	var none *invokeRepository
	if got, err := none.validate(); err != nil || got != nil {
		t.Fatalf("nil block: %v %v", got, err)
	}
}

// Every refusal names the field the way the input spells it.
func TestRepositoryInputRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*invokeRepository)
		want   string
	}{
		"missing repository id":      {func(r *invokeRepository) { r.RepositoryID = "" }, "repository.repositoryID"},
		"repository id with a slash": {func(r *invokeRepository) { r.RepositoryID = "org/repo" }, "repository.repositoryID"},
		"repository id too long":     {func(r *invokeRepository) { r.RepositoryID = strings.Repeat("a", 129) }, "repository.repositoryID"},
		"short commit":               {func(r *invokeRepository) { r.BaseCommit = "abc123" }, "repository.baseCommit"},
		"symbolic commit":            {func(r *invokeRepository) { r.BaseCommit = "main" }, "repository.baseCommit"},
		"no clone source":            {func(r *invokeRepository) { r.CloneSource = nil }, "repository.cloneSource is required"},
		"no remote":                  {func(r *invokeRepository) { r.CloneSource.RemoteURL = "" }, "repository.cloneSource.remoteURL is required"},
		"http remote":                {func(r *invokeRepository) { r.CloneSource.RemoteURL = "http://git.example/x.git" }, "must be an https:// or ssh://"},
		"file remote":                {func(r *invokeRepository) { r.CloneSource.RemoteURL = "file:///srv/git/x" }, "must be an https:// or ssh://"},
		"local path":                 {func(r *invokeRepository) { r.CloneSource.RemoteURL = "/srv/git/x" }, "must be an https:// or ssh://"},
		"embedded password": {
			func(r *invokeRepository) { r.CloneSource.RemoteURL = "https://user:pw@git.example/x.git" }, "embedded credentials",
		},
		"embedded user on https": {
			func(r *invokeRepository) { r.CloneSource.RemoteURL = "https://token@git.example/x.git" }, "embedded credentials",
		},
		"ssh password": {
			func(r *invokeRepository) {
				r.CloneSource.RemoteURL = "ssh://git:pw@git.example/x.git"
				r.CloneSource.Token = ""
			}, "embedded credentials",
		},
		"option as remote":  {func(r *invokeRepository) { r.CloneSource.RemoteURL = "--upload-pack=x" }, "must not begin with an option"},
		"padded remote":     {func(r *invokeRepository) { r.CloneSource.RemoteURL = " " + validRemot }, "surrounding whitespace"},
		"query on remote":   {func(r *invokeRepository) { r.CloneSource.RemoteURL = validRemot + "?x=1" }, "unsupported options"},
		"no path on remote": {func(r *invokeRepository) { r.CloneSource.RemoteURL = "https://git.example/" }, "no repository path"},
		"control character": {func(r *invokeRepository) { r.CloneSource.RemoteURL = "https://git.example/x\n.git" }, "control character"},
		"token on ssh remote": {
			func(r *invokeRepository) { r.CloneSource.RemoteURL = "ssh://git@git.example/x.git" }, "only used with an https remote",
		},
		"token with newline": {func(r *invokeRepository) { r.CloneSource.Token = "abc\ndef" }, "repository.cloneSource.token"},
		"username with colon": {
			func(r *invokeRepository) { r.CloneSource.Username = "a:b" }, "repository.cloneSource.username",
		},
		"two-line message":     {func(r *invokeRepository) { r.CommitMessage = "one\ntwo" }, "repository.commitMessage must be one line"},
		"long message":         {func(r *invokeRepository) { r.CommitMessage = strings.Repeat("x", 201) }, "at most 200 bytes"},
		"control in message":   {func(r *invokeRepository) { r.CommitMessage = "a\x07b" }, "control characters"},
		"empty capability":     {func(r *invokeRepository) { r.RequiredCapabilities = []string{"git-fetch-v1", " "} }, "repository.requiredCapabilities"},
		"too many toolchains":  {func(r *invokeRepository) { r.RequiredToolchains = make([]string, 33) }, "repository.requiredToolchains must have at most"},
		"long command":         {func(r *invokeRepository) { r.Verification.Commands = []string{strings.Repeat("x", 4097)} }, "repository.verification.commands"},
		"approved input array": {func(r *invokeRepository) { r.ApprovedInput = json.RawMessage(`[1]`) }, "repository.approvedInput must be a JSON object"},
		"approved input null":  {func(r *invokeRepository) { r.ApprovedInput = json.RawMessage(`null`) }, "repository.approvedInput must be a JSON object"},
		"approved input large": {
			func(r *invokeRepository) {
				r.ApprovedInput = json.RawMessage(`{"pad":"` + strings.Repeat("x", 64<<10) + `"}`)
			}, "repository.approvedInput must be at most",
		},
		"negative duration":   {func(r *invokeRepository) { r.MaxDurationSeconds = -1 }, "repository.maxDurationSeconds"},
		"duration over a day": {func(r *invokeRepository) { r.MaxDurationSeconds = 86401 }, "repository.maxDurationSeconds"},
	} {
		t.Run(name, func(t *testing.T) {
			in := validRepositoryInput()
			tc.mutate(&in)
			got, err := in.validate()
			if err == nil {
				t.Fatalf("accepted %+v", in)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want it to mention %q", err, tc.want)
			}
			if got != nil {
				t.Fatal("a refusal returns no attempt")
			}
		})
	}
}

// The remotes the runner takes, and the provider forwards.
func TestRepositoryRemoteForms(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/railgrid/railgrid.git",
		"https://git.example:8443/org/repo",
		"ssh://git@github.com/railgrid/railgrid.git",
		"git@github.com:railgrid/railgrid.git",
	} {
		if err := validateRemoteURL(remote); err != nil {
			t.Errorf("%q refused: %v", remote, err)
		}
	}
	for _, remote := range []string{
		"ext::sh -c id",
		"-oProxyCommand=x",
		"git@github.com:",
		"github.com",
	} {
		if err := validateRemoteURL(remote); err == nil {
			t.Errorf("%q accepted", remote)
		}
	}
}

// A run rebuilt from its record has the coordinates and no clone source.
func TestRepositoryAttemptFromStoredCarriesNoCredential(t *testing.T) {
	got := repositoryAttemptFromStored(&store.RunRepository{RepositoryID: "r", BaseCommit: validBase, CommitMessage: "m"})
	if got == nil || got.Source != nil || got.RepositoryID != "r" || got.BaseCommit != validBase || got.CommitMessage != "m" {
		t.Fatalf("got %+v", got)
	}
	if repositoryAttemptFromStored(nil) != nil {
		t.Fatal("a conversational run stays one")
	}
}

// ---- the verbs, through the real gated handler --------------------------------

// A model-backed agent has no checkout to run in: the block is refused before
// a run record exists. A malformed block is refused the same way, naming the
// field, and a block with a session is a contradiction.
func TestRunVerbRefusesRepositoryOnAModelBackedAgent(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster:   conformanceCluster,
		User:      conformanceUser,
		Objects:   []*unstructured.Unstructured{agentObject(conformanceAgent)},
		ListKinds: map[schema.GroupVersionResource]string{agentsclient.AgentGVR: "AgentList"},
		Allow:     func(a conformance.Attributes) bool { return a.Verb == "get" },
	}
	s := newGatedTestServer(t, callers)
	handler := gatedHandler(t, s)
	path := verbBase(conformanceCluster, "agents", conformanceAgent) + "/run"

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, stamped(http.MethodPost, path, strings.NewReader(body), conformanceUser))
		return rec
	}
	repo := `{"repositoryID":"r","baseCommit":"` + validBase + `","cloneSource":{"remoteURL":"` + validRemot + `","token":"t"}}`

	rec := post(`{"task":"do it","repository":` + repo + `}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "this agent cannot run against a repository") {
		t.Fatalf("model-backed agent → %d %s", rec.Code, rec.Body.String())
	}
	rec = post(`{"task":"do it","repository":{"repositoryID":"r","baseCommit":"nope","cloneSource":{"remoteURL":"` + validRemot + `"}}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "repository.baseCommit") {
		t.Fatalf("bad block → %d %s", rec.Code, rec.Body.String())
	}
	rec = post(`{"task":"do it","sessionId":"chat","repository":` + repo + `}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cannot continue a session") {
		t.Fatalf("session + repository → %d %s", rec.Code, rec.Body.String())
	}
	// None of the refusals recorded a run.
	scope := store.Scope{OrgUUID: unmappedOrg, WorkspaceUUID: conformanceCluster, AgentName: conformanceAgent}
	if runs, _ := s.store.ListRuns(t.Context(), scope, 10); len(runs) != 0 {
		t.Fatalf("a refused run was recorded: %+v", runs)
	}
}

// The artifact verb serves what the provider stored for THAT run, gated on
// the Run object like trace, and nothing else.
func TestArtifactVerbServesStoredArtifacts(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		User:    conformanceUser,
		Objects: []*unstructured.Unstructured{
			agentObject(conformanceAgent),
			runObject("r1", conformanceAgent),
			runObject("r2", conformanceAgent),
		},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList",
			agentsclient.RunGVR:   "RunList",
		},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "runs" && (a.Name == "r1" || a.Name == "r2")
		},
	}
	s := newGatedTestServer(t, callers)
	handler := gatedHandler(t, s)

	// The scope a verb resolves for this cluster until a tenant mapping is
	// learned (resolveClusterScope): the unmapped org, keyed by cluster.
	scope := store.Scope{OrgUUID: unmappedOrg, WorkspaceUUID: conformanceCluster, AgentName: conformanceAgent}
	now := time.Now().UTC()
	for _, id := range []string{"r1", "r2"} {
		if err := s.store.SaveRun(t.Context(), scope, store.Run{
			ID: id, AgentName: conformanceAgent, Trigger: "api", Phase: store.RunPhaseSucceeded,
			Backend: "harness", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	bundle := []byte{0x00, 0x01, 0xff, 'b', 'u', 'n', 'd', 'l', 'e'}
	if err := s.store.SaveRunArtifact(t.Context(), scope, store.RunArtifact{
		RunID: "r1", Name: "git-result.bundle", Digest: "ab12", MediaType: "application/x-git-bundle",
		Size: int64(len(bundle)), Data: bundle, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	base := verbBase(conformanceCluster, "runs", "")
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, stamped(http.MethodPost, path, strings.NewReader(body), conformanceUser))
		return rec
	}

	rec := post(base+"r1/artifact", `{"name":"git-result.bundle"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("stored artifact → %d %s", rec.Code, rec.Body.String())
	}
	var got runArtifactResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(got.Data)
	if err != nil || string(data) != string(bundle) {
		t.Fatalf("data = %q (%v), want the stored bytes", got.Data, err)
	}
	if got.Name != "git-result.bundle" || got.Digest != "ab12" || got.Size != int64(len(bundle)) || got.MediaType != "application/x-git-bundle" {
		t.Fatalf("response = %+v", got)
	}

	for _, tc := range []struct {
		path, body string
		want       int
		why        string
	}{
		{base + "r1/artifact", `{"name":"git-result.json"}`, 404, "r1 stored no document"},
		{base + "r2/artifact", `{"name":"git-result.bundle"}`, 404, "r2 is visible but stored nothing"},
		{base + "r3/artifact", `{"name":"git-result.bundle"}`, 404, "no such run, and a denial does not say so"},
		{base + "r1/artifact", `{}`, 400, "a name is required"},
		{base + "r1/artifact", `nope`, 400, "the body is JSON"},
		{base + "r1/artifact/x", `{"name":"git-result.bundle"}`, 400, "artifact takes no tail"},
	} {
		rec := post(tc.path, tc.body)
		if rec.Code != tc.want {
			t.Errorf("POST %s %s → %d, want %d (%s): %s", tc.path, tc.body, rec.Code, tc.want, tc.why, rec.Body.String())
		}
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, stamped(http.MethodGet, base+"r1/artifact", nil, conformanceUser))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET artifact → %d, want 405", rec.Code)
	}
}
