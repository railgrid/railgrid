/*
Copyright 2026 The Railgrid Authors.

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

// Package projectledger is the control-plane implementation of the project
// working-copy ledger (workspace.Ledger): the dirty-path set, the source
// revision, the commit in flight and the settlement receipt, held in
// `Project.status.workspace` instead of in JSON files beside the tree
// (docs/roadmap/provider-contract-remediation.md §9 Cut D.3).
//
// Writes are merge patches on the status subresource carrying the
// resourceVersion they read, so they are compare-and-swap: two replicas
// editing one project retry against each other's result instead of one
// silently overwriting the other. The retry is the ledger's own, not the
// caller's — workspace.Ledger.Update promises callers an atomic
// read-modify-write and this is where that promise is kept.
//
// The same ledger serves both writers, differing only in the client it is
// built from: the HTTP layer's is the caller's own tenant client (FromProjects),
// the Project reconciler's is the manager's client over this provider's
// APIExport virtual workspace (FromControllerClient).
package projectledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/workspace"
)

// MaxUncommittedPaths mirrors the CRD's MaxItems on
// status.workspace.uncommittedPaths. It is not an arbitrary number: a project
// tree is capped at 500 files, so the largest single transition is a
// whole-tree replacement — up to 500 writes plus up to 500 deletions — and
// this leaves headroom above that. Exceeding it is a bug in a bound
// somewhere, so it is reported rather than silently truncated: a path dropped
// here would be a file that never reaches git.
const MaxUncommittedPaths = 1024

// updateAttempts bounds the compare-and-swap retry. Contention is between a
// handler and the reconciler on one project, so a handful of rounds is
// generous; past that the caller gets the conflict and retries at its own
// cadence rather than spinning here.
const updateAttempts = 6

// ErrProjectGone means the Project the scope names is no longer there, or has
// been recreated under a new UID. Callers treat it the way they treat a
// deleted project: there is nothing left to record.
var ErrProjectGone = errors.New("project is gone; its working-copy ledger went with it")

// Projects is the slice of a control-plane client the ledger needs. Both the
// HTTP layer's provider-scoped typed client and controller-runtime satisfy it
// through the adapters below.
type Projects interface {
	// Get reads one Project by name.
	Get(ctx context.Context, name string) (*aiv1alpha1.Project, error)
	// PatchStatus applies a JSON merge patch to the Project's status
	// subresource. The patch carries metadata.resourceVersion, so the API
	// server answers a stale write with Conflict.
	PatchStatus(ctx context.Context, name string, patch []byte) error
}

// Ledger implements workspace.Ledger over a Project's status subresource.
type Ledger struct {
	projects Projects
	// now is the clock the timestamps come from; tests replace it.
	now func() time.Time
}

// New returns a ledger backed by projects.
func New(projects Projects) *Ledger {
	if projects == nil {
		return nil
	}
	return &Ledger{projects: projects, now: time.Now}
}

var _ workspace.Ledger = (*Ledger)(nil)

// Read implements workspace.Ledger.
func (l *Ledger) Read(ctx context.Context, scope workspace.Scope) (workspace.LedgerRecord, error) {
	if l == nil || l.projects == nil {
		return workspace.LedgerRecord{}, workspace.ErrNoLedger
	}
	project, err := l.project(ctx, scope)
	if err != nil {
		return workspace.LedgerRecord{}, err
	}
	return recordFrom(project.Status.Workspace), nil
}

// Update implements workspace.Ledger: read, apply, compare-and-swap, retry.
func (l *Ledger) Update(ctx context.Context, scope workspace.Scope, mutate func(*workspace.LedgerRecord) (bool, error)) (workspace.LedgerRecord, error) {
	if l == nil || l.projects == nil {
		return workspace.LedgerRecord{}, workspace.ErrNoLedger
	}
	var lastConflict error
	for attempt := 0; attempt < updateAttempts; attempt++ {
		project, err := l.project(ctx, scope)
		if err != nil {
			return workspace.LedgerRecord{}, err
		}
		current := recordFrom(project.Status.Workspace)
		next := current.DeepCopy()
		changed, err := mutate(&next)
		if err != nil {
			return workspace.LedgerRecord{}, err
		}
		if !changed {
			return current, nil
		}
		workspace.NormalizeLedgerRecord(&next)
		if len(next.UncommittedPaths) > MaxUncommittedPaths {
			return workspace.LedgerRecord{}, fmt.Errorf(
				"project %s has %d uncommitted paths, more than the %d the ledger can record",
				scope.ProjectName, len(next.UncommittedPaths), MaxUncommittedPaths)
		}
		patch, err := statusPatch(project.ResourceVersion, statusFrom(next, project.Status.Workspace, l.clock()))
		if err != nil {
			return workspace.LedgerRecord{}, err
		}
		err = l.projects.PatchStatus(ctx, scope.ProjectName, patch)
		if err == nil {
			return next, nil
		}
		if apierrors.IsNotFound(err) {
			return workspace.LedgerRecord{}, fmt.Errorf("%w: %s", ErrProjectGone, scope.ProjectName)
		}
		if !apierrors.IsConflict(err) {
			return workspace.LedgerRecord{}, fmt.Errorf("record project %s working-copy ledger: %w", scope.ProjectName, err)
		}
		lastConflict = err
	}
	return workspace.LedgerRecord{}, fmt.Errorf(
		"record project %s working-copy ledger: gave up after %d conflicting attempts: %w",
		scope.ProjectName, updateAttempts, lastConflict)
}

func (l *Ledger) clock() time.Time {
	if l.now == nil {
		return time.Now()
	}
	return l.now()
}

// project reads the Project the scope names and refuses one that has been
// recreated: a new UID is a new working copy, and inheriting the deleted
// project's dirty set would commit files that are not its own.
func (l *Ledger) project(ctx context.Context, scope workspace.Scope) (*aiv1alpha1.Project, error) {
	name := strings.TrimSpace(scope.ProjectName)
	if name == "" {
		return nil, errors.New("working-copy ledger: scope carries no project name")
	}
	project, err := l.projects.Get(ctx, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s", ErrProjectGone, name)
		}
		return nil, fmt.Errorf("read project %s working-copy ledger: %w", name, err)
	}
	if uid := strings.TrimSpace(scope.ProjectUID); uid != "" && string(project.UID) != uid {
		return nil, fmt.Errorf("%w: %s was recreated (%s, not %s)", ErrProjectGone, name, project.UID, uid)
	}
	return project, nil
}

// recordFrom converts the CR's ledger into the workspace package's.
func recordFrom(status *aiv1alpha1.ProjectWorkspaceStatus) workspace.LedgerRecord {
	if status == nil {
		return workspace.LedgerRecord{}
	}
	record := workspace.LedgerRecord{}
	if status.SourceRevision > 0 {
		record.SourceRevision = uint64(status.SourceRevision)
	}
	if len(status.UncommittedPaths) > 0 {
		record.UncommittedPaths = append([]string(nil), status.UncommittedPaths...)
	}
	if pending := status.PendingCommit; pending != nil {
		record.PendingCommit = &workspace.PendingCommit{
			Name:            pending.Name,
			RepositoryRef:   pending.RepositoryRef,
			WorkspaceDigest: pending.WorkspaceDigest,
			Paths:           append([]string(nil), pending.Paths...),
		}
	}
	if settlement := status.Settlement; settlement != nil {
		record.Settlement = &workspace.CommitSettlement{
			WorkspaceDigest: settlement.WorkspaceDigest,
			Paths:           append([]string(nil), settlement.Paths...),
		}
	}
	return record
}

// statusFrom converts a record back, carrying forward the timestamps of
// sub-records that did not change so a re-recorded pending commit keeps the
// moment it was actually requested.
func statusFrom(record workspace.LedgerRecord, previous *aiv1alpha1.ProjectWorkspaceStatus, now time.Time) *aiv1alpha1.ProjectWorkspaceStatus {
	status := &aiv1alpha1.ProjectWorkspaceStatus{
		SourceRevision:   int64(record.SourceRevision), //nolint:gosec // bounded by the revision counter, not by input
		UncommittedPaths: record.UncommittedPaths,
	}
	if pending := record.PendingCommit; pending != nil {
		requestedAt := &metav1.Time{Time: now}
		if previous != nil && previous.PendingCommit != nil &&
			previous.PendingCommit.Name == pending.Name && previous.PendingCommit.RequestedAt != nil {
			requestedAt = previous.PendingCommit.RequestedAt
		}
		status.PendingCommit = &aiv1alpha1.ProjectPendingCommit{
			Name:            pending.Name,
			RepositoryRef:   pending.RepositoryRef,
			WorkspaceDigest: pending.WorkspaceDigest,
			Paths:           pending.Paths,
			RequestedAt:     requestedAt,
		}
	}
	if settlement := record.Settlement; settlement != nil {
		recordedAt := &metav1.Time{Time: now}
		if previous != nil && previous.Settlement != nil &&
			previous.Settlement.WorkspaceDigest == settlement.WorkspaceDigest && previous.Settlement.RecordedAt != nil {
			recordedAt = previous.Settlement.RecordedAt
		}
		status.Settlement = &aiv1alpha1.ProjectCommitSettlement{
			WorkspaceDigest: settlement.WorkspaceDigest,
			Paths:           settlement.Paths,
			RecordedAt:      recordedAt,
		}
	}
	if status.SourceRevision == 0 && len(status.UncommittedPaths) == 0 &&
		status.PendingCommit == nil && status.Settlement == nil {
		return nil
	}
	return status
}

// statusPatch builds the merge patch. Empty members are explicit nulls rather
// than omissions: a merge patch that leaves a key out keeps what is there, so
// "no pending commit any more" has to be said out loud.
func statusPatch(resourceVersion string, status *aiv1alpha1.ProjectWorkspaceStatus) ([]byte, error) {
	var body any
	if status == nil {
		body = nil
	} else {
		ledger := map[string]any{
			"sourceRevision":   status.SourceRevision,
			"uncommittedPaths": nullIfEmpty(status.UncommittedPaths),
			"pendingCommit":    nil,
			"settlement":       nil,
		}
		if status.PendingCommit != nil {
			ledger["pendingCommit"] = map[string]any{
				"name":            status.PendingCommit.Name,
				"repositoryRef":   status.PendingCommit.RepositoryRef,
				"workspaceDigest": status.PendingCommit.WorkspaceDigest,
				"paths":           nullIfEmpty(status.PendingCommit.Paths),
				"requestedAt":     status.PendingCommit.RequestedAt,
			}
		}
		if status.Settlement != nil {
			ledger["settlement"] = map[string]any{
				"workspaceDigest": status.Settlement.WorkspaceDigest,
				"paths":           nullIfEmpty(status.Settlement.Paths),
				"recordedAt":      status.Settlement.RecordedAt,
			}
		}
		body = ledger
	}
	return json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": resourceVersion},
		"status":   map[string]any{"workspace": body},
	})
}

func nullIfEmpty(paths []string) any {
	if len(paths) == 0 {
		return nil
	}
	return paths
}

// typedProjects is the adapter over the provider-scoped typed client the HTTP
// layer builds per request.
type typedProjects struct {
	get   func(ctx context.Context, name string, opts metav1.GetOptions) (*aiv1alpha1.Project, error)
	patch func(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*aiv1alpha1.Project, error)
}

// TypedProjects is the shape of client.Client's typed Project resource. It is
// declared structurally so this package does not import the api client (which
// would be a cycle for the api package's own tests) and so a fake is a fake of
// two methods.
type TypedProjects interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*aiv1alpha1.Project, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*aiv1alpha1.Project, error)
}

// FromProjects builds a ledger over a typed Project client — the HTTP layer's
// provider-scoped one. Writes are therefore made as the provider through its
// export virtual workspace, exactly like every other tenant write this
// provider makes on the request path.
func FromProjects(projects TypedProjects) *Ledger {
	if projects == nil {
		return nil
	}
	return New(&typedProjects{get: projects.Get, patch: projects.Patch})
}

func (t *typedProjects) Get(ctx context.Context, name string) (*aiv1alpha1.Project, error) {
	return t.get(ctx, name, metav1.GetOptions{})
}

func (t *typedProjects) PatchStatus(ctx context.Context, name string, patch []byte) error {
	_, err := t.patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	return err
}

// controllerProjects is the adapter over a controller-runtime client — the
// Project reconciler's, riding this provider's APIExport virtual workspace.
type controllerProjects struct {
	client ctrlclient.Client
}

// FromControllerClient builds a ledger over a controller-runtime client.
func FromControllerClient(c ctrlclient.Client) *Ledger {
	if c == nil {
		return nil
	}
	return New(&controllerProjects{client: c})
}

func (c *controllerProjects) Get(ctx context.Context, name string) (*aiv1alpha1.Project, error) {
	project := &aiv1alpha1.Project{}
	if err := c.client.Get(ctx, ctrlclient.ObjectKey{Name: name}, project); err != nil {
		return nil, err
	}
	return project, nil
}

func (c *controllerProjects) PatchStatus(ctx context.Context, name string, patch []byte) error {
	project := &aiv1alpha1.Project{}
	project.SetName(name)
	return c.client.Status().Patch(ctx, project, ctrlclient.RawPatch(types.MergePatchType, patch))
}
