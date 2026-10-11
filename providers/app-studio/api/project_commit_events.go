/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

// projectCommitEventTimeout bounds the thread lookup and append so a slow
// store never stalls the Project reconciler that reports the commit.
const projectCommitEventTimeout = 10 * time.Second

// ProjectCommit is a workspace commit the Project reconciler settled.
type ProjectCommit struct {
	RepositoryRef string
	CommitSHA     string
	CommitURL     string
	Branch        string
	// Files are the committed workspace paths, deletions included.
	Files []string
	// Attributions is the immutable set of producing turns captured when the
	// pending commit was created. Paths omitted from it have no provable owner.
	Attributions []workspace.SourceAttribution
}

type projectCommittedEventPayload struct {
	CommitSHA     string   `json:"commitSHA"`
	CommitURL     string   `json:"commitURL,omitempty"`
	Branch        string   `json:"branch,omitempty"`
	RepositoryRef string   `json:"repositoryRef"`
	Files         []string `json:"files"`
}

// ProjectCommitted records a reconciler commit on each producing assistant
// turn captured with the pending commit. Unattributed paths and commits with
// no known assistant owner are skipped; failures never block commit
// convergence.
func (s *Server) ProjectCommitted(ctx context.Context, scope workspace.Scope, commit ProjectCommit) {
	if s == nil || s.store == nil || strings.TrimSpace(commit.CommitSHA) == "" || len(commit.Attributions) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, projectCommitEventTimeout)
	defer cancel()
	storeScope := store.Scope{
		OrgUUID:       scope.OrgUUID,
		WorkspaceUUID: scope.WorkspaceUUID,
		ProjectName:   scope.ProjectName,
		ProjectUID:    scope.ProjectUID,
	}
	logger := klog.FromContext(ctx).WithValues("org", scope.OrgUUID, "workspace", scope.WorkspaceUUID, "project", scope.ProjectName, "commit", commit.CommitSHA)
	for _, attribution := range workspace.NormalizeSourceAttributions(commit.Attributions, commit.Files) {
		turn, err := s.store.GetAssistantTurn(ctx, storeScope, attribution.ThreadID, attribution.RunID)
		if errors.Is(err, store.ErrAssistantTurnNotFound) {
			continue
		}
		if err != nil {
			logger.Error(err, "resolving assistant turn for project commit event", "thread", attribution.ThreadID, "turn", attribution.RunID)
			continue
		}
		files := attribution.Paths
		if files == nil {
			files = []string{}
		}
		payload, err := json.Marshal(projectCommittedEventPayload{
			CommitSHA:     commit.CommitSHA,
			CommitURL:     commit.CommitURL,
			Branch:        commit.Branch,
			RepositoryRef: commit.RepositoryRef,
			Files:         files,
		})
		if err != nil {
			logger.Error(err, "encoding project commit event", "thread", turn.ThreadID, "turn", turn.ID)
			continue
		}
		if _, err := s.appendAssistantThreadEvent(ctx, storeScope, store.AssistantThreadEvent{
			ThreadID: turn.ThreadID,
			TurnID:   turn.ID,
			Type:     assistantThreadEventProjectCommitted,
			Payload:  payload,
		}); err != nil {
			logger.Error(err, "appending project commit event", "thread", turn.ThreadID, "turn", turn.ID)
		}
	}
}
