/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"strings"

	"github.com/railgrid/provider-app-studio/internal/reconcilesignal"
	"github.com/railgrid/provider-app-studio/store"
)

// Reconcile signals: the HTTP/assistant layer tells the controller manager
// when something the store or the workspace holds — invisible to any kcp
// watch — has changed, so the Session and Project reconcilers converge on
// the event instead of on a poll.
//
// A signal names its target as (workspace cluster, object name). The
// assistant layer works in store scopes (org/workspace UUIDs), so the
// Server keeps a small index from workspace UUID to the kcp cluster ID it
// last saw a request from; every project request refreshes it. A scope the
// index has not seen (a turn resumed after restart before any request for
// its workspace arrived) simply publishes nothing, and the reconciler's
// safety resync covers it.

// SessionSignals is the bus the Session reconciler subscribes to; every
// thread/turn transition publishes the Session's key on it.
func (s *Server) SessionSignals() *reconcilesignal.Bus {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionSignals == nil {
		s.sessionSignals = reconcilesignal.NewBus()
	}
	return s.sessionSignals
}

// ProjectSignals is the bus the Project reconciler subscribes to; a turn
// ending or files changing publishes the Project's key on it so commit
// convergence runs as soon as the workspace is idle and dirty.
func (s *Server) ProjectSignals() *reconcilesignal.Bus {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projectSignals == nil {
		s.projectSignals = reconcilesignal.NewBus()
	}
	return s.projectSignals
}

// noteWorkspaceCluster records which kcp cluster serves a workspace UUID.
func (s *Server) noteWorkspaceCluster(id identity) {
	if s == nil {
		return
	}
	workspaceUUID := strings.TrimSpace(id.workspaceUUID)
	clusterID := strings.TrimSpace(id.clusterID)
	if workspaceUUID == "" || clusterID == "" {
		return
	}
	s.workspaceClusters.Store(workspaceUUID, clusterID)
}

// clusterForWorkspace resolves a workspace UUID to its cluster, or "".
func (s *Server) clusterForWorkspace(workspaceUUID string) string {
	if s == nil {
		return ""
	}
	clusterID, ok := s.workspaceClusters.Load(strings.TrimSpace(workspaceUUID))
	if !ok {
		return ""
	}
	id, _ := clusterID.(string)
	return id
}

// signalSession wakes the Session reconciler for a thread. The Session CR is
// named after its thread (ensureSessionCR), so the thread ID is the name.
func (s *Server) signalSession(scope store.Scope, threadID string) {
	if s == nil {
		return
	}
	s.SessionSignals().Publish(reconcilesignal.Key{
		Cluster: s.clusterForWorkspace(scope.WorkspaceUUID),
		Name:    strings.TrimSpace(threadID),
	})
}

// signalProject wakes the Project reconciler for a project whose workspace
// may have new uncommitted work or just went idle.
func (s *Server) signalProject(workspaceUUID, projectName string) {
	if s == nil {
		return
	}
	s.ProjectSignals().Publish(reconcilesignal.Key{
		Cluster: s.clusterForWorkspace(workspaceUUID),
		Name:    strings.TrimSpace(projectName),
	})
}
