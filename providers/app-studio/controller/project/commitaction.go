/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

// Asking the Code provider to commit, as App Studio.
//
// The request itself lives in internal/codecommit, because this reconciler is
// not the only caller: the assistant's commit_project_files tool makes the
// same call (api/llm.go). One request, one credential — this provider's own,
// through its APIExport virtual workspace, under the repositories/commit and
// repositories/stage-commit-bundle claims the tenant accepted — and the same
// gate on the serving side. Which provider serves Code in the workspace is
// kcp's to resolve from the claim; nothing here reads an APIBinding to find
// out any more.
//
// This used to be an MCP tool call: the reconciler reached the tenant's MCP
// aggregate, held `use` on an MCPServer to get in, invoked
// `code__commit_files`, and — when the tool did not finish inside its own 75 s
// wait — recovered the name of the RepositoryCommit it had just created by
// REGULAR-EXPRESSING the tool's prose error. Every part of that was
// load-bearing and none of it was the contract.

import (
	"context"

	"github.com/railgrid/provider-app-studio/internal/codecommit"
)

// requestCommit asks the Code provider for one commit and returns the
// RepositoryCommit it created.
func (r *Reconciler) requestCommit(ctx context.Context, cluster, repositoryRef, repositoryUID string, bundle commitBundle, message string) (codecommit.Ref, error) {
	return (&codecommit.Client{Callers: r.Callers}).Commit(ctx, codecommit.Request{
		Cluster:       cluster,
		RepositoryRef: repositoryRef,
		RepositoryUID: repositoryUID,
		Message:       message,
		Files:         bundle.wireFiles(),
	})
}
