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

package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/railgrid/provider-app-studio/workspace"
)

// projectAssistantPreviewSourceFence is captured after checkpointing and before
// waiting for synchronization. It remains immutable through every observation:
// rereading mutable sandbox metadata later must not certify a newer snapshot.
type projectAssistantPreviewSourceFence struct {
	server                          *Server
	sandbox                         *projectAssistantRunSandbox
	runState                        *projectEinoAssistantRunState
	scope                           workspace.Scope
	revision                        uint64
	digest                          string
	requiresSharedPreviewCheckpoint bool
}

type projectAssistantSharedPreviewSourceStaleError struct {
	workspaceRevision uint64
	previewRevision   uint64
}

func (e *projectAssistantSharedPreviewSourceStaleError) Error() string {
	if e == nil {
		return "shared preview has not applied the current workspace source"
	}
	return fmt.Sprintf("shared preview has not applied the current workspace source (workspace revision %d, last applied revision %d)", e.workspaceRevision, e.previewRevision)
}

func (*projectAssistantSharedPreviewSourceStaleError) Unwrap() error {
	return errProjectAssistantRunSandboxConflict
}

func projectAssistantPreviewWorkspaceScope(req projectAssistantToolCallRequest) workspace.Scope {
	if req.WorkspaceScope.ProjectName != "" {
		return req.WorkspaceScope
	}
	return projectWorkspaceScope(req.Identity, req.Project)
}

func (s *Server) captureProjectAssistantPreviewSourceFence(ctx context.Context, runState *projectEinoAssistantRunState, scope workspace.Scope) (*projectAssistantPreviewSourceFence, error) {
	if s == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runState != nil {
		if sandbox := runState.Sandbox(); sandbox != nil {
			metadata := sandbox.metadataSnapshot()
			return &projectAssistantPreviewSourceFence{
				server: s, sandbox: sandbox, runState: runState, scope: sandbox.scope,
				revision: metadata.SourceRevision, digest: metadata.SourceDigest,
				requiresSharedPreviewCheckpoint: true,
			}, nil
		}
	}
	// Read-only and lazily initialized runs still observe the shared preview.
	// A FileStore revision alone only proves which files are on disk; the
	// durable preview checkpoint proves that the shared runtime applied them.
	// Requiring both prevents an inspection started while an async sync is
	// queued from certifying the previous preview.
	if s.workspaces == nil {
		return nil, nil
	}
	revision, err := s.workspaces.SourceRevision(ctx, scope)
	if err != nil {
		return nil, err
	}
	checkpoint, err := s.workspaces.LastPreviewCheckpoint(ctx, scope)
	if err != nil {
		return nil, err
	}
	previewRevision := uint64(0)
	if checkpoint != nil {
		previewRevision = checkpoint.SourceRevision
	}
	if revision == 0 || previewRevision == 0 || previewRevision != revision {
		return nil, &projectAssistantSharedPreviewSourceStaleError{
			workspaceRevision: revision,
			previewRevision:   previewRevision,
		}
	}
	return &projectAssistantPreviewSourceFence{
		server: s, scope: scope, runState: runState, revision: revision,
		requiresSharedPreviewCheckpoint: true,
	}, nil
}

func (f *projectAssistantPreviewSourceFence) validate(ctx context.Context) error {
	if f == nil {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.server.workspaces != nil {
		current, err := f.server.workspaces.SourceRevision(ctx, f.scope)
		if err != nil {
			return err
		}
		if current != f.revision {
			return fmt.Errorf("%w: shared source changed during preview verification", errProjectAssistantRunSandboxConflict)
		}
		if f.requiresSharedPreviewCheckpoint {
			checkpoint, err := f.server.workspaces.LastPreviewCheckpoint(ctx, f.scope)
			if err != nil {
				return err
			}
			previewRevision := uint64(0)
			if checkpoint != nil {
				previewRevision = checkpoint.SourceRevision
			}
			if previewRevision != f.revision {
				return &projectAssistantSharedPreviewSourceStaleError{
					workspaceRevision: f.revision,
					previewRevision:   previewRevision,
				}
			}
		}
	}
	sandbox := f.sandbox
	if f.runState != nil {
		current := f.runState.Sandbox()
		if sandbox != nil && current != sandbox {
			return fmt.Errorf("%w: private sandbox changed during preview verification", errProjectAssistantRunSandboxConflict)
		}
		sandbox = current
	}
	if sandbox != nil {
		metadata := sandbox.metadataSnapshot()
		if metadata.ExecutionUnresolved || metadata.RemoteFenceNeedsRefresh {
			return fmt.Errorf("%w: private command execution has not established current source evidence", errProjectAssistantRunSandboxConflict)
		}
		if metadata.RemoteRevision > 0 && strings.TrimSpace(metadata.RemoteDigest) == "" {
			return fmt.Errorf("%w: private source has a remote revision without a digest", errProjectAssistantRunSandboxConflict)
		}
		_, remoteDigest := projectAssistantSandboxRemoteFence(metadata)
		if metadata.SourceRevision != f.revision || !sandboxDigestEqual(metadata.SourceDigest, remoteDigest) ||
			(f.sandbox != nil && !sandboxDigestEqual(metadata.SourceDigest, f.digest)) {
			return fmt.Errorf("%w: private source changed during preview verification", errProjectAssistantRunSandboxConflict)
		}
	}
	return ctx.Err()
}
