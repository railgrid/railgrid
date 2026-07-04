/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
)

var (
	connectionsGVR       = codev1alpha1.SchemeGroupVersion.WithResource("connections")
	repositoriesGVR      = codev1alpha1.SchemeGroupVersion.WithResource("repositories")
	repositoryCommitsGVR = codev1alpha1.SchemeGroupVersion.WithResource("repositorycommits")
)

// tenantClient resolves a tenant-scoped dynamic client that acts AS THE CALLER.
func tenantClient(deps Deps, ident identity) (dynamic.Interface, error) {
	if ident.tenantPath == "" {
		return nil, errors.New("no tenant identity on this request — bearer token did not resolve to a workspace")
	}
	if ident.clusterID == "" {
		return nil, errors.New("no workspace cluster on this request (X-Kedge-Cluster missing) — cannot address the tenant workspace by ID")
	}
	if ident.token == "" {
		return nil, errors.New("no bearer token on this request — the MCP request must carry the caller's credentials")
	}
	if deps.Tenant == nil {
		return nil, errors.New("tenant client unavailable (provider kubeconfig not set)")
	}
	return deps.Tenant.For(ident.clusterID, ident.token)
}

type connectionSummary struct {
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Owner     string `json:"owner"`
	Login     string `json:"login,omitempty"`
	Validated bool   `json:"validated"`
}

type listConnectionsOutput struct {
	Connections []connectionSummary `json:"connections"`
}

type repositorySummary struct {
	Name       string `json:"name"`
	Connection string `json:"connection"`
	Repo       string `json:"repo"`
	Visibility string `json:"visibility,omitempty"`
	HTMLURL    string `json:"htmlURL,omitempty"`
	Ready      bool   `json:"ready"`
}

type listRepositoriesOutput struct {
	Repositories []repositorySummary `json:"repositories"`
}

// registerTools wires the read-only list tools. Write tools (create/delete
// repository, deploy keys, collaborators) land in PR C.
func registerTools(srv *mcp.Server, deps Deps, ident identity) {
	yes := true
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &yes}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_connections",
		Title:       "List git connections",
		Description: "List the git account connections configured in your workspace, with their validation status. Call this to discover which connection a repository can use.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listConnectionsOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, listConnectionsOutput{}, err
		}
		items, err := list(ctx, dyn, connectionsGVR)
		if err != nil {
			return nil, listConnectionsOutput{}, fmt.Errorf("list connections: %w", err)
		}
		out := listConnectionsOutput{Connections: make([]connectionSummary, 0, len(items))}
		for _, u := range items {
			provider, _, _ := unstructured.NestedString(u.Object, "spec", "provider")
			owner, _, _ := unstructured.NestedString(u.Object, "spec", "owner")
			login, _, _ := unstructured.NestedString(u.Object, "status", "login")
			out.Connections = append(out.Connections, connectionSummary{
				Name:      u.GetName(),
				Provider:  provider,
				Owner:     owner,
				Login:     login,
				Validated: conditionTrue(u, codev1alpha1.ConditionValidated),
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_repositories",
		Title:       "List managed repositories",
		Description: "List the git repositories managed in your workspace, with their URLs and readiness. Each repository references a connection (see list_connections).",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listRepositoriesOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, listRepositoriesOutput{}, err
		}
		items, err := list(ctx, dyn, repositoriesGVR)
		if err != nil {
			return nil, listRepositoriesOutput{}, fmt.Errorf("list repositories: %w", err)
		}
		out := listRepositoriesOutput{Repositories: make([]repositorySummary, 0, len(items))}
		for _, u := range items {
			connRef, _, _ := unstructured.NestedString(u.Object, "spec", "connectionRef")
			repoName, _, _ := unstructured.NestedString(u.Object, "spec", "name")
			vis, _, _ := unstructured.NestedString(u.Object, "spec", "visibility")
			htmlURL, _, _ := unstructured.NestedString(u.Object, "status", "htmlURL")
			out.Repositories = append(out.Repositories, repositorySummary{
				Name:       u.GetName(),
				Connection: connRef,
				Repo:       repoName,
				Visibility: vis,
				HTMLURL:    htmlURL,
				Ready:      conditionTrue(u, codev1alpha1.ConditionReady),
			})
		}
		return nil, out, nil
	})

	registerWriteTools(srv, deps, ident)
	registerCheckoutTools(srv, deps, ident)
}

func list(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource) ([]unstructured.Unstructured, error) {
	l, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return l.Items, nil
}

// conditionTrue reports whether the object's status.conditions has condType=True.
func conditionTrue(u unstructured.Unstructured, condType string) bool {
	conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found {
		return false
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == condType && m["status"] == string(metav1.ConditionTrue) {
			return true
		}
	}
	return false
}
