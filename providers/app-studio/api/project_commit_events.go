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
}

type projectCommittedEventPayload struct {
	CommitSHA     string   `json:"commitSHA"`
	CommitURL     string   `json:"commitURL,omitempty"`
	Branch        string   `json:"branch,omitempty"`
	RepositoryRef string   `json:"repositoryRef"`
	Files         []string `json:"files"`
}

// ProjectCommitted records a reconciler auto-commit as a project.committed
// event on the turn of the project's latest assistant run, so the
// conversation shows that its edits reached git. Projects without an
// assistant thread are skipped silently; failures are logged and never block
// commit convergence.
func (s *Server) ProjectCommitted(ctx context.Context, scope workspace.Scope, commit ProjectCommit) {
	if s == nil || s.store == nil || strings.TrimSpace(commit.CommitSHA) == "" {
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
	turn, ok, err := s.latestProjectAssistantThreadTurn(ctx, storeScope)
	if err != nil {
		logger.Error(err, "resolving assistant thread for project commit event")
		return
	}
	if !ok {
		return
	}
	files := commit.Files
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
		logger.Error(err, "encoding project commit event")
		return
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

// latestProjectAssistantThreadTurn resolves the thread turn of the project's
// latest assistant run. ok is false when the project has no run, or the run
// has no thread turn (legacy runs).
func (s *Server) latestProjectAssistantThreadTurn(ctx context.Context, scope store.Scope) (store.AssistantTurn, bool, error) {
	run, err := s.store.LatestAssistantRun(ctx, scope)
	if errors.Is(err, store.ErrAssistantRunNotFound) {
		return store.AssistantTurn{}, false, nil
	}
	if err != nil {
		return store.AssistantTurn{}, false, err
	}
	if run.UserMessageID == "" || run.ClientRequestID == "" {
		return store.AssistantTurn{}, false, nil
	}
	// Threads are listed per actor; the run's user message names its actor.
	messages, err := s.store.GetMessagesByIDs(ctx, scope, []string{run.UserMessageID})
	if err != nil {
		return store.AssistantTurn{}, false, err
	}
	actor := ""
	for _, message := range messages {
		if message.ID == run.UserMessageID {
			actor = message.ActorID
		}
	}
	if actor == "" {
		return store.AssistantTurn{}, false, nil
	}
	if run.ThreadID != "" {
		thread, err := s.store.GetAssistantThread(ctx, scope, run.ThreadID)
		if errors.Is(err, store.ErrAssistantThreadNotFound) {
			return store.AssistantTurn{}, false, nil
		}
		if err != nil {
			return store.AssistantTurn{}, false, err
		}
		if thread.ActorID != actor {
			return store.AssistantTurn{}, false, nil
		}
		turn, err := s.store.GetAssistantTurn(ctx, scope, run.ThreadID, run.ID)
		if errors.Is(err, store.ErrAssistantTurnNotFound) {
			return store.AssistantTurn{}, false, nil
		}
		if err != nil {
			return store.AssistantTurn{}, false, err
		}
		return turn, turn.ThreadID == run.ThreadID && turn.ID == run.ID, nil
	}
	// Unresolved legacy ownership must have exactly one matching canonical
	// turn. Choosing the first thread would manufacture ownership for the
	// ambiguous records the migration deliberately leaves unresolved.
	var match store.AssistantTurn
	found := false
	cursor := ""
	for {
		page, err := s.store.ListAssistantThreads(ctx, scope, actor, true, 100, cursor)
		if err != nil {
			return store.AssistantTurn{}, false, err
		}
		for _, thread := range page.Items {
			turn, err := s.store.GetAssistantTurn(ctx, scope, thread.ID, run.ID)
			if errors.Is(err, store.ErrAssistantTurnNotFound) {
				continue
			}
			if err != nil {
				return store.AssistantTurn{}, false, err
			}
			if found {
				return store.AssistantTurn{}, false, nil
			}
			match, found = turn, true
		}
		if page.NextCursor == "" {
			return match, found, nil
		}
		if page.NextCursor == cursor {
			return store.AssistantTurn{}, false, nil
		}
		cursor = page.NextCursor
	}
}
