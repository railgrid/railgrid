/*
Copyright 2026 The Faros Authors.

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

package api

import (
	"fmt"
	"strings"
	"time"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

// workspaceRebuildNotice records that a replica rebuilt a project's workspace
// from git after taking the project over. When the claim carried a source
// revision above zero, edits from earlier assistant turns existed that were
// never committed; after the rebuild they are gone from disk while the
// conversation history still describes them. Left unsaid, the assistant keeps
// reasoning about files that no longer exist and the user is told the deck is
// "still present in the workspace" while the sandbox serves an empty tree.
type workspaceRebuildNotice struct {
	CommitSHA       string
	Files           int
	DroppedRevision uint64
	At              time.Time
	PreviousOwner   string
}

// Blocker renders the notice as a verification blocker. It names the commit
// the tree now matches, how many files that produced, and the workspace
// revision that was dropped, so both the model and the user can see that
// prior edits must be re-read or redone rather than assumed.
func (n workspaceRebuildNotice) Blocker() string {
	commit := strings.TrimSpace(n.CommitSHA)
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit == "" {
		commit = "the repository head"
	}
	at := ""
	if !n.At.IsZero() {
		at = " at " + n.At.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(
		"the project workspace was rebuilt from git commit %s (%d files)%s after a replica change; "+
			"workspace revision %d and every uncommitted edit before it are no longer on disk. "+
			"Re-read files before relying on earlier edits, and redo or recover work that was never committed.",
		commit, n.Files, at, n.DroppedRevision,
	)
}

// recordWorkspaceRebuild stores the most recent rebuild for a project. The
// entry lives until the next workspace mutation: once the assistant writes to
// the rebuilt tree it is working against what is actually on disk.
func (s *Server) recordWorkspaceRebuild(id identity, project *aiv1alpha1.Project, notice workspaceRebuildNotice) {
	if s == nil || project == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workspaceRebuilds == nil {
		s.workspaceRebuilds = map[string]workspaceRebuildNotice{}
	}
	s.workspaceRebuilds[developmentSyncFailureKey(id, project)] = notice
}

// clearWorkspaceRebuild drops the notice once a mutation lands on the rebuilt
// tree, so it never outlives the state it described.
func (s *Server) clearWorkspaceRebuild(id identity, project *aiv1alpha1.Project) {
	if s == nil || project == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.workspaceRebuilds, developmentSyncFailureKey(id, project))
}

// lastWorkspaceRebuild returns the recorded rebuild for a project, if any.
func (s *Server) lastWorkspaceRebuild(id identity, project *aiv1alpha1.Project) (workspaceRebuildNotice, bool) {
	if s == nil || project == nil {
		return workspaceRebuildNotice{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	notice, ok := s.workspaceRebuilds[developmentSyncFailureKey(id, project)]
	return notice, ok
}

// projectAssistantWorkspaceRebuild reports the pending rebuild notice for the
// run's project as a blocker string, or "" when the tree has not been rebuilt
// since the last mutation. Safe on a run context missing a server or project.
func projectAssistantWorkspaceRebuild(runCtx projectAssistantWorkflowRunContext) string {
	if runCtx.Server == nil || runCtx.Project == nil {
		return ""
	}
	notice, ok := runCtx.Server.lastWorkspaceRebuild(runCtx.Identity, runCtx.Project)
	if !ok {
		return ""
	}
	return notice.Blocker()
}
