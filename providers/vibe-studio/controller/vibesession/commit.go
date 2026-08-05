// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package vibesession

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/provision"
	"github.com/faroshq/provider-vibe-studio/session"
	"github.com/faroshq/provider-vibe-studio/sessionlog"
	"github.com/faroshq/provider-vibe-studio/store"
)

// Keeping git in step with the sandbox.
//
// Edits land in the workspace and stream into the running sandbox
// immediately, because that is what makes the preview feel live. Git is the
// durable copy, and it converges on the same reconcile loop: the store keeps
// a monotonic workspace revision, the Session records the revision it last
// pushed, and when they differ the controller commits. No change feed, no
// post-write hook — a burst of edits during one turn collapses into a single
// commit, and a commit that fails is simply retried next pass.
//
// Commits wait for the turn to finish: committing mid-turn would capture a
// half-written app and produce a history nobody wants to read.

// commitWorkspace pushes the workspace to git when it has moved on since the
// last commit. Returns true when it committed (so the caller refreshes
// status), false when there was nothing to do.
func (r *Reconciler) commitWorkspace(
	ctx context.Context,
	c client.Client,
	sess *vibev1alpha1.Session,
	scope store.Scope,
	state session.SessionState,
) (bool, error) {
	if sess.Spec.ProjectRef == nil || sess.Spec.ProjectRef.Name == "" {
		return false, nil
	}
	rev, err := r.Store.WorkspaceRevision(ctx, scope, sess.Name)
	if err != nil || rev == 0 || rev == sess.Status.CommittedRevision {
		return false, nil //nolint:nilerr // a missing store row is not a controller error
	}

	var p vibev1alpha1.Project
	if err := c.Get(ctx, types.NamespacedName{Name: sess.Spec.ProjectRef.Name}, &p); err != nil {
		return false, nil //nolint:nilerr // project may be mid-creation; retry next pass
	}
	if p.Spec.Repository == nil || p.Spec.Repository.RepositoryRef == "" ||
		p.Status.Repository == nil || p.Status.Repository.Phase != "Ready" {
		return false, nil // no repository to push to (yet)
	}

	contents, err := r.Store.ListWorkspaceContents(ctx, scope, sess.Name)
	if err != nil || len(contents) == 0 {
		return false, nil //nolint:nilerr
	}

	token, err := r.ensureIdentity(ctx, c, sess, ownerRefOf(sess), sess.Name)
	if err != nil || token == "" {
		return false, err
	}
	pc := provision.NewClient(r.HubBase, string(clusterOf(&p)), token, r.HubInsecure)
	if !pc.Ready() {
		return false, nil
	}

	payload := make([]map[string]string, 0, len(contents))
	for _, f := range contents {
		payload = append(payload, map[string]string{"path": f.Path, "content": f.Content})
	}
	// Describe the change from the events since the last commit.
	since, err := r.Store.ListEvents(ctx, scope, sess.Name, sess.Status.CommittedOrdinal, 0)
	if err != nil {
		since = nil // fall back to a generic message rather than skipping the commit
	}
	result, err := pc.CallCodeTool(ctx, "code__commit_files", map[string]any{
		"repositoryRef": p.Spec.Repository.RepositoryRef,
		"message":       commitMessage(since),
		"files":         payload,
	})
	if err != nil {
		// Report it where the user is looking, and retry on the next pass.
		return false, sessionlog.SetCheckpoint(ctx, r.Store, scope, sess.Name, session.Checkpoint{
			Name: session.CheckpointGit, State: session.CheckpointError,
			Reason: "committing the workspace failed: " + explainGitError(err),
		})
	}
	var commit struct {
		CommitSHA string `json:"commitSHA"`
	}
	_ = json.Unmarshal(result, &commit)

	sess.Status.CommittedRevision = rev
	sess.Status.WorkspaceRevision = rev
	sess.Status.CommittedOrdinal = state.LastOrdinal
	sess.Status.LastCommitSHA = commit.CommitSHA
	if err := c.Status().Update(ctx, sess); err != nil {
		// The commit landed; status will catch up next pass rather than
		// committing twice (git dedups identical trees anyway).
		return true, err
	}

	reason := fmt.Sprintf("%d files committed", len(contents))
	if len(commit.CommitSHA) >= 7 {
		reason += " @ " + commit.CommitSHA[:7]
	}
	return true, sessionlog.SetCheckpoint(ctx, r.Store, scope, sess.Name, session.Checkpoint{
		Name: session.CheckpointGit, State: session.CheckpointDone, Reason: reason,
	})
}

// commitMessage describes the change from the events since the last commit:
// the request that drove it as the subject, the files it touched as the body.
// The folded state cannot supply this — pending input is consumed the moment
// a turn starts, which is why every commit used to read "sync workspace".
func commitMessage(events []session.Event) string {
	var (
		request string
		touched []string
		seen    = map[string]bool{}
	)
	for _, e := range events {
		switch e.Type {
		case session.EventUserMessage:
			var d session.MessageData
			if session.DecodeData(e, &d) == nil && strings.TrimSpace(d.Text) != "" {
				request = d.Text // keep the latest
			}
		case session.EventToolActivity:
			var a session.ToolActivityData
			if session.DecodeData(e, &a) != nil || !a.OK || a.Detail == "" {
				continue
			}
			verb := ""
			switch a.Tool {
			case "write_file":
				verb = ""
			case "delete_file":
				verb = "delete "
			default:
				continue
			}
			if line := verb + a.Detail; !seen[line] {
				seen[line] = true
				touched = append(touched, line)
			}
		}
	}

	subject := oneLine(request)
	switch {
	case subject == "" && len(touched) == 1:
		subject = "update " + strings.TrimPrefix(touched[0], "delete ")
	case subject == "" && len(touched) > 1:
		subject = fmt.Sprintf("update %d files", len(touched))
	case subject == "":
		subject = "sync workspace"
	}
	if len(subject) > 68 {
		subject = strings.TrimSpace(subject[:68]) + "…"
	}

	msg := "vibe: " + subject
	if len(touched) > 0 {
		sort.Strings(touched)
		const maxListed = 20
		listed := touched
		extra := 0
		if len(listed) > maxListed {
			extra = len(listed) - maxListed
			listed = listed[:maxListed]
		}
		msg += "\n\n" + strings.Join(prefixEach(listed, "- "), "\n")
		if extra > 0 {
			msg += fmt.Sprintf("\n- …and %d more", extra)
		}
	}
	return msg
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func prefixEach(in []string, prefix string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, prefix+s)
	}
	return out
}
