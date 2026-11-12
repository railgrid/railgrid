// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tools"
)

// Reuse the native repository attempt: the runner performs private checkout and
// exports the canonical snapshot, rather than asking the model to clone/commit.
func (s *Server) delegatedRepository(ctx context.Context, d tools.Deps, child *agentsv1alpha1.Agent) (*repositoryAttempt, error) {
	if d.CodeScope == nil {
		return nil, nil
	}
	cfg := child.Spec.Harness()
	if cfg == nil || cfg.GitHubConnectionRef == "" {
		return nil, fmt.Errorf("workflow delegate requires a read-only App checkout connection")
	}
	conn, err := d.CR.GetConnection(ctx, cfg.GitHubConnectionRef)
	if err != nil {
		return nil, err
	}
	if conn.Spec.Type != agentsv1alpha1.ConnectionTypeGitHub || conn.Spec.Auth != "secret" || conn.Spec.Config["credentialType"] != "github-app" || conn.Spec.Config["githubRole"] != "checkout" {
		return nil, fmt.Errorf("workflow delegate requires a GitHub App connection with checkout role")
	}
	raw, _ := json.Marshal(map[string]any{"pullRequest": d.CodeScope.PullRequest, "input": map[string]any{"op": "observe", "pullRequestUID": d.CodeScope.PullRequestUID}})
	result, err := tools.Code(d, false)[0].Exec(ctx, string(raw))
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			Repository   string `json:"repository"`
			RepositoryID int64  `json:"repositoryID"`
			Observation  struct {
				Head string `json:"head"`
			} `json:"observation"`
			Event struct {
				RepositoryID int64 `json:"repositoryID"`
			} `json:"event"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		return nil, err
	}
	observed := response.Result
	repositoryNumericID := observed.RepositoryID
	if repositoryNumericID == 0 {
		repositoryNumericID = observed.Event.RepositoryID
	}
	repositoryID := strconv.FormatInt(repositoryNumericID, 10)
	if repositoryNumericID <= 0 || observed.Repository == "" || observed.Observation.Head == "" || conn.Spec.Config["repositoryID"] != repositoryID || conn.Spec.Config["repository"] != observed.Repository ||
		(d.CodeScope.Mode == "task" && (d.CodeScope.Repository != observed.Repository || d.CodeScope.RepositoryUID == "" || d.CodeScope.ConnectionUID == "")) {
		return nil, fmt.Errorf("worker checkout does not match admitted repository")
	}
	return (&invokeRepository{RepositoryID: repositoryID, BaseCommit: observed.Observation.Head, CloneSource: &invokeCloneSource{RemoteURL: "https://github.com/" + observed.Repository + ".git", Username: "x-access-token"}, CommitMessage: "Apply scoped PR fixes"}).validate()
}

func (s *Server) delegationOutput(ctx context.Context, scope store.Scope, child store.Run) (string, error) {
	if child.Repository == nil {
		return child.Output, nil
	}
	if child.Result == nil || child.Result.BaseCommit == "" || child.Result.BaseCommit != child.Repository.BaseCommit {
		return "", fmt.Errorf("verified delegated repository result unavailable")
	}
	result := map[string]any{
		"runId": child.ID, "report": safeTruncate(child.Output, 16<<10),
		"baseCommit": child.Result.BaseCommit, "noChanges": child.Result.NoChanges,
	}
	if child.Result.NoChanges {
		raw, err := json.Marshal(result)
		return string(raw), err
	}
	artifact, found, err := s.store.GetRunArtifact(ctx, scope, child.ID, "git-result.bundle")
	if err != nil || !found {
		return "", fmt.Errorf("verified delegated snapshot bundle unavailable")
	}
	sum := sha256.Sum256(artifact.Data)
	if hex.EncodeToString(sum[:]) != child.Result.BundleDigest {
		return "", fmt.Errorf("delegated snapshot bundle digest changed")
	}
	if len(artifact.Data) > 64<<10 {
		return "", fmt.Errorf("delegated snapshot exceeds the supported inline publication limit (64 KiB of bundle data)")
	}
	result["snapshot"] = map[string]any{"baseCommit": child.Result.BaseCommit, "commit": child.Result.Commit, "tree": child.Result.Tree, "bundle": artifact.Data, "message": child.Repository.CommitMessage}
	raw, err := json.Marshal(result)
	return string(raw), err
}
