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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	codev1alpha1 "github.com/faroshq/faros-kedge/providers/code/apis/v1alpha1"
)

var (
	deploykeysGVR    = codev1alpha1.SchemeGroupVersion.WithResource("deploykeys")
	collaboratorsGVR = codev1alpha1.SchemeGroupVersion.WithResource("collaborators")
)

// The write tools are CRD-native: each creates or deletes a CR in the caller's
// tenant workspace AS THE CALLER. The controllers do the actual host work, so
// these tools never carry a credential and never call GitHub directly —
// pasting a PAT remains a portal-only action (create_connection references an
// already-stored Secret by name).

type createConnectionInput struct {
	Name            string `json:"name" jsonschema:"Object name for the Connection CR (cluster-scoped)"`
	Provider        string `json:"provider,omitempty" jsonschema:"Git provider; defaults to github"`
	Owner           string `json:"owner" jsonschema:"Org or user that repositories are created under"`
	SecretName      string `json:"secretName" jsonschema:"Name of an existing Secret in your workspace holding the token"`
	SecretNamespace string `json:"secretNamespace,omitempty" jsonschema:"Namespace of the Secret; defaults to the provider convention namespace"`
	SecretKey       string `json:"secretKey,omitempty" jsonschema:"Data key within the Secret holding the token; defaults to token"`
	BaseURL         string `json:"baseURL,omitempty" jsonschema:"Optional API base URL for GitHub Enterprise Server"`
}

type createRepositoryInput struct {
	Name          string `json:"name" jsonschema:"Object name for the Repository CR (cluster-scoped)"`
	ConnectionRef string `json:"connectionRef" jsonschema:"Name of the Connection to create the repo under"`
	Repo          string `json:"repo,omitempty" jsonschema:"Repository name on the host; defaults to name"`
	Owner         string `json:"owner,omitempty" jsonschema:"Override the connection owner for this repo"`
	Visibility    string `json:"visibility,omitempty" jsonschema:"private|public|internal; defaults to private"`
	Description   string `json:"description,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	AutoInit      bool   `json:"autoInit,omitempty" jsonschema:"Create an initial commit so the default branch exists"`
}

type addDeployKeyInput struct {
	Name          string `json:"name" jsonschema:"Object name for the DeployKey CR (cluster-scoped)"`
	RepositoryRef string `json:"repositoryRef" jsonschema:"Name of the Repository to install the key on"`
	Title         string `json:"title,omitempty"`
	PublicKey     string `json:"publicKey,omitempty" jsonschema:"OpenSSH public key to register; omit to have one generated"`
	ReadOnly      bool   `json:"readOnly,omitempty"`
}

type addCollaboratorInput struct {
	Name          string `json:"name" jsonschema:"Object name for the Collaborator CR (cluster-scoped)"`
	RepositoryRef string `json:"repositoryRef" jsonschema:"Name of the Repository to grant access on"`
	Username      string `json:"username" jsonschema:"Host login to grant access to"`
	Permission    string `json:"permission,omitempty" jsonschema:"pull|push|admin; defaults to pull"`
}

type nameInput struct {
	Name string `json:"name" jsonschema:"Object name of the CR to delete"`
}

type createOutput struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Created bool   `json:"created"`
}

type deleteOutput struct {
	Deleted bool `json:"deleted"`
}

func registerWriteTools(srv *mcp.Server, deps Deps, ident identity) {
	no := false
	yes := true
	mutating := &mcp.ToolAnnotations{IdempotentHint: false, DestructiveHint: &no, OpenWorldHint: &yes}
	destructive := &mcp.ToolAnnotations{DestructiveHint: &yes}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_connection",
		Title:       "Create a git connection",
		Description: "Create a Connection that binds your workspace to a git account, referencing an existing Secret that holds the token. Paste the token into the portal first; this tool never transports it. The provider validates the credential and reports the login.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createConnectionInput) (*mcp.CallToolResult, createOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, createOutput{}, err
		}
		provider := in.Provider
		if provider == "" {
			provider = string(codev1alpha1.ProviderGitHub)
		}
		secretRef := map[string]any{"name": in.SecretName}
		if in.SecretNamespace != "" {
			secretRef["namespace"] = in.SecretNamespace
		}
		if in.SecretKey != "" {
			secretRef["key"] = in.SecretKey
		}
		spec := map[string]any{
			"provider":  provider,
			"type":      string(codev1alpha1.CredentialTypePAT),
			"owner":     in.Owner,
			"secretRef": secretRef,
		}
		if in.BaseURL != "" {
			spec["baseURL"] = in.BaseURL
		}
		return createCR(ctx, dyn, connectionsGVR, "Connection", in.Name, spec)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_repository",
		Title:       "Create a repository",
		Description: "Create a Repository under a connection; the provider creates it on the git host and reports its URLs. Identity is taken from your bearer token.",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createRepositoryInput) (*mcp.CallToolResult, createOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, createOutput{}, err
		}
		repoName := in.Repo
		if repoName == "" {
			repoName = in.Name
		}
		spec := map[string]any{
			"connectionRef": in.ConnectionRef,
			"name":          repoName,
		}
		putIf(spec, "owner", in.Owner)
		putIf(spec, "visibility", in.Visibility)
		putIf(spec, "description", in.Description)
		putIf(spec, "defaultBranch", in.DefaultBranch)
		if in.AutoInit {
			spec["autoInit"] = true
		}
		return createCR(ctx, dyn, repositoriesGVR, "Repository", in.Name, spec)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_repository",
		Title:       "Delete a repository",
		Description: "Delete a Repository CR; the provider removes it from the git host. Idempotent: returns deleted=true even if already gone.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, deleteOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, deleteOutput{}, err
		}
		return deleteCR(ctx, dyn, repositoriesGVR, in.Name)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_deploy_key",
		Title:       "Add a deploy key",
		Description: "Install a deploy key on a repository. Omit publicKey to have an ed25519 keypair generated; the private half is stored in a Secret in your workspace (status.secretRef on the DeployKey).",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addDeployKeyInput) (*mcp.CallToolResult, createOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, createOutput{}, err
		}
		spec := map[string]any{"repositoryRef": in.RepositoryRef}
		putIf(spec, "title", in.Title)
		putIf(spec, "publicKey", in.PublicKey)
		if in.ReadOnly {
			spec["readOnly"] = true
		}
		return createCR(ctx, dyn, deploykeysGVR, "DeployKey", in.Name, spec)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_collaborator",
		Title:       "Add a collaborator",
		Description: "Grant a host user a permission level on a repository. The user may receive an invitation to accept (tracked via the Collaborator's InvitationPending status).",
		Annotations: mutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addCollaboratorInput) (*mcp.CallToolResult, createOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, createOutput{}, err
		}
		spec := map[string]any{
			"repositoryRef": in.RepositoryRef,
			"username":      in.Username,
		}
		putIf(spec, "permission", in.Permission)
		return createCR(ctx, dyn, collaboratorsGVR, "Collaborator", in.Name, spec)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "remove_collaborator",
		Title:       "Remove a collaborator",
		Description: "Delete a Collaborator CR; the provider revokes the grant (and cancels any pending invitation). Idempotent.",
		Annotations: destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nameInput) (*mcp.CallToolResult, deleteOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, deleteOutput{}, err
		}
		return deleteCR(ctx, dyn, collaboratorsGVR, in.Name)
	})
}

// createCR creates a cluster-scoped CR in group code.kedge.faros.sh.
func createCR(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, kind, name string, spec map[string]any) (*mcp.CallToolResult, createOutput, error) {
	if name == "" {
		return nil, createOutput{}, fmt.Errorf("name is required")
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
	if _, err := dyn.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, createOutput{}, fmt.Errorf("%s %q already exists", kind, name)
		}
		return nil, createOutput{}, fmt.Errorf("create %s: %w", kind, err)
	}
	return nil, createOutput{Name: name, Kind: kind, Created: true}, nil
}

// deleteCR deletes a cluster-scoped CR; a missing object is success.
func deleteCR(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, name string) (*mcp.CallToolResult, deleteOutput, error) {
	if name == "" {
		return nil, deleteOutput{}, fmt.Errorf("name is required")
	}
	err := dyn.Resource(gvr).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, deleteOutput{}, fmt.Errorf("delete %s: %w", name, err)
	}
	return nil, deleteOutput{Deleted: true}, nil
}

func putIf(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}
