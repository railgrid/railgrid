// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"testing"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/tools"
)

func TestDelegatedEnvironmentFollowsCommandAndWorkspacePolicy(t *testing.T) {
	scope := &tools.CodeScope{PullRequestUID: "pr-uid", EventUID: "command-uid", Proof: "first-invocation"}
	cfg := &agentsv1alpha1.AgentHarnessBackend{Workspace: agentsv1alpha1.HarnessWorkspacePersistent}
	key := delegatedEnvironmentID(scope, cfg)
	if key == "" || !runnerIdentifier.MatchString(key) {
		t.Fatal("persistent worker has no valid environment identity")
	}
	scope.Proof = "next-invocation"
	if delegatedEnvironmentID(scope, cfg) != key {
		t.Fatal("continuation changed environment identity")
	}
	cfg.Workspace = ""
	if delegatedEnvironmentID(scope, cfg) != key {
		t.Fatal("default persistent policy changed environment identity")
	}
	for _, other := range []*tools.CodeScope{
		{PullRequestUID: "other-pr", EventUID: scope.EventUID},
		{PullRequestUID: scope.PullRequestUID, EventUID: "other-command"},
	} {
		if delegatedEnvironmentID(other, cfg) == key {
			t.Fatal("different command or PR shared an environment")
		}
	}
	cfg.Workspace = agentsv1alpha1.HarnessWorkspaceEphemeral
	if delegatedEnvironmentID(scope, cfg) != "" || delegatedEnvironmentID(nil, cfg) != "" || delegatedEnvironmentID(scope, nil) != "" {
		t.Fatal("ephemeral or unbound worker retained an environment")
	}
}

func TestDelegatedEnvironmentUsesTaskUIDAndDigestAcrossContinuations(t *testing.T) {
	scope := &tools.CodeScope{
		Mode: "task", PullRequestUID: "pr-uid", SourceUID: "task-uid", SourceDigest: "request-digest",
		GrantRef: "grant-one", Proof: "source-proof-one", ScopeProof: "invocation-proof-one",
	}
	cfg := &agentsv1alpha1.AgentHarnessBackend{Workspace: agentsv1alpha1.HarnessWorkspacePersistent}
	key := delegatedEnvironmentID(scope, cfg)
	if key == "" || !runnerIdentifier.MatchString(key) {
		t.Fatal("persistent task worker has no valid environment identity")
	}
	scope.GrantRef, scope.Proof, scope.ScopeProof = "grant-two", "source-proof-two", "invocation-proof-two"
	if delegatedEnvironmentID(scope, cfg) != key {
		t.Fatal("a task continuation changed its retained checkout")
	}
	for _, changed := range []*tools.CodeScope{
		{Mode: "task", PullRequestUID: "other-pr", SourceUID: scope.SourceUID, SourceDigest: scope.SourceDigest},
		{Mode: "task", PullRequestUID: scope.PullRequestUID, SourceUID: "other-task", SourceDigest: scope.SourceDigest},
		{Mode: "task", PullRequestUID: scope.PullRequestUID, SourceUID: scope.SourceUID, SourceDigest: "other-digest"},
	} {
		if delegatedEnvironmentID(changed, cfg) == key {
			t.Fatal("another task or PR shared the task checkout")
		}
	}
	if delegatedEnvironmentID(&tools.CodeScope{Mode: "task", PullRequestUID: scope.PullRequestUID, SourceUID: scope.SourceUID}, cfg) != "" {
		t.Fatal("an unbound task scope selected a retained checkout")
	}
}

func TestDelegatedEnvironmentPersistsWithoutWeakeningWorkerIsolation(t *testing.T) {
	key := delegatedEnvironmentID(&tools.CodeScope{PullRequestUID: "pr", EventUID: "event"}, &agentsv1alpha1.AgentHarnessBackend{})
	repository := &repositoryAttempt{Repository: backendharness.Repository{EnvironmentID: key, RepositoryID: "42", BaseCommit: lifecycleBase}}
	restored := repositoryAttemptFromStored(repository.persisted())
	if restored.EnvironmentID != key || restored.BaseCommit != lifecycleBase {
		t.Fatal("recovery lost environment or pinned base")
	}
	first := scopedRepositoryEnvironment(restored.Repository, "tenant-a", "reviewer-uid").EnvironmentID
	for _, other := range []string{
		scopedRepositoryEnvironment(restored.Repository, "tenant-b", "reviewer-uid").EnvironmentID,
		scopedRepositoryEnvironment(restored.Repository, "tenant-a", "fixer-uid").EnvironmentID,
		scopedRepositoryEnvironment(backendharness.Repository{RepositoryID: "43", EnvironmentID: key}, "tenant-a", "reviewer-uid").EnvironmentID,
	} {
		if first == other {
			t.Fatal("tenant, repository or worker Agent isolation lost")
		}
	}
}
