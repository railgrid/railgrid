/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

// Asking the Code provider to commit, on the data-plane grammar.
//
// The request itself lives in internal/codecommit, because this reconciler is
// not the only caller: the assistant's commit_project_files tool makes the
// same call with the human's bearer instead of the project identity's
// (api/llm.go). One request, two credentials, the same two gates on the
// serving side — which is the point. What stays here is the one thing that is
// this reconciler's own: which provider to send it to, read from the tenant
// workspace's APIBinding as the project identity.
//
// This used to be an MCP tool call: the reconciler reached the tenant's MCP
// aggregate, held `use` on an MCPServer to get in, invoked
// `code__commit_files`, and — when the tool did not finish inside its own 75 s
// wait — recovered the name of the RepositoryCommit it had just created by
// REGULAR-EXPRESSING the tool's prose error. Every part of that was
// load-bearing and none of it was the contract.

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/railgrid/provider-app-studio/internal/codecommit"
	"github.com/railgrid/provider-app-studio/internal/crossprovider"
)

// apiBindingGVK is kcp's APIBinding, read by name to learn which provider
// serves the Code APIExport in this workspace.
var apiBindingGVK = crossprovider.APIBindingsGVR.GroupVersion().WithKind("APIBinding")

// requestCommit asks the Code provider for one commit and returns the
// RepositoryCommit it created.
func (r *Reconciler) requestCommit(ctx context.Context, tc client.Client, token string, cluster, repositoryRef, repositoryUID string, bundle commitBundle, message string) (codecommit.Ref, error) {
	provider, err := r.codeProviderName(ctx, tc, cluster)
	if err != nil {
		return codecommit.Ref{}, err
	}
	return (&codecommit.Client{HubBase: r.HubBase, Insecure: r.HubInsecure}).Commit(ctx, codecommit.Request{
		Provider:      provider,
		Token:         token,
		Cluster:       cluster,
		RepositoryRef: repositoryRef,
		RepositoryUID: repositoryUID,
		Message:       message,
		Files:         bundle.wireFiles(),
	})
}

// codeProviderName resolves which provider serves the Code APIExport in this
// workspace, from the workspace's own APIBinding — never from configuration.
// The project identity holds a named `get` on exactly this binding
// (identity.go, clause D).
func (r *Reconciler) codeProviderName(ctx context.Context, tc client.Client, cluster string) (string, error) {
	if tc == nil {
		return "", fmt.Errorf("no workspace client to read this workspace's APIBindings with")
	}
	candidate := crossprovider.ProviderNameForExport(crossprovider.CodeAPIExport)
	binding := &unstructured.Unstructured{}
	binding.SetGroupVersionKind(apiBindingGVK)
	if err := tc.Get(ctx, types.NamespacedName{Name: candidate}, binding); err != nil {
		return "", fmt.Errorf("workspace %s binds no provider serving %s: %w", cluster, crossprovider.CodeAPIExport, err)
	}
	bound, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
	if bound != crossprovider.CodeAPIExport {
		return "", fmt.Errorf("APIBinding %q in workspace %s serves %q, not %s", binding.GetName(), cluster, bound, crossprovider.CodeAPIExport)
	}
	if name := strings.TrimSpace(binding.GetName()); name != "" {
		return name, nil
	}
	return "", fmt.Errorf("the APIBinding serving %s in workspace %s has no name", crossprovider.CodeAPIExport, cluster)
}
