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
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/commitbundle"
)

var (
	deploykeysGVR    = codev1alpha1.SchemeGroupVersion.WithResource("deploykeys")
	collaboratorsGVR = codev1alpha1.SchemeGroupVersion.WithResource("collaborators")
)

const (
	// maxCommitMessageLength matches RepositoryCommit spec.message MaxLength.
	maxCommitMessageLength = 512
	// commitWaitTimeout bounds how long commit_files waits for a terminal phase.
	commitWaitTimeout = 75 * time.Second
)

// Write tools are CRD-native: they create or delete a CR in the caller's tenant
// workspace AS THE CALLER, and controllers do the reconciled host work. Pasting
// a PAT remains a portal-only action (create_connection references an
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
	AutoInit      *bool  `json:"autoInit,omitempty" jsonschema:"Create an initial commit so the default branch exists; defaults to true"`
}

type commitFileInput struct {
	Path     string `json:"path" jsonschema:"Repository-relative file path"`
	Content  string `json:"content" jsonschema:"Complete file content: the UTF-8 text itself, or the base64 of the file bytes when encoding is base64"`
	Encoding string `json:"encoding,omitempty" jsonschema:"Content encoding: utf-8 (default) for text, or base64 (RFC 4648 standard alphabet with padding, no line breaks) for binary files such as images. Limits apply to decoded bytes: 2 MiB per utf-8 file, 25 MiB per base64 file, 48 MiB and 500 files per commit"`
}

type commitFilesInput struct {
	RepositoryRef string            `json:"repositoryRef" jsonschema:"Name of the managed Repository CR to commit into"`
	Message       string            `json:"message,omitempty" jsonschema:"Commit message of at most 512 characters including the body; defaults to a generated update message"`
	Branch        string            `json:"branch,omitempty" jsonschema:"Branch name; defaults to the Repository defaultBranch, then main"`
	Files         []commitFileInput `json:"files,omitempty" jsonschema:"Files to write in this commit"`
	DeletePaths   []string          `json:"deletePaths,omitempty" jsonschema:"Repository-relative file paths to delete in this commit"`
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

type commitFilesOutput struct {
	RepositoryRef string   `json:"repositoryRef"`
	Name          string   `json:"name,omitempty"`
	Phase         string   `json:"phase,omitempty"`
	BundleRef     string   `json:"bundleRef,omitempty"`
	BundleDigest  string   `json:"bundleDigest,omitempty"`
	CommitSHA     string   `json:"commitSHA,omitempty"`
	CommitURL     string   `json:"commitURL,omitempty"`
	Branch        string   `json:"branch,omitempty"`
	Files         []string `json:"files,omitempty"`
	DeletedPaths  []string `json:"deletedPaths,omitempty"`
}

func registerWriteTools(srv *mcp.Server, deps Deps, ident identity) {
	no := false
	yes := true
	mutating := &mcp.ToolAnnotations{IdempotentHint: false, DestructiveHint: &no, OpenWorldHint: &yes}
	commitMutating := &mcp.ToolAnnotations{IdempotentHint: false, DestructiveHint: &yes, OpenWorldHint: &yes}
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
		return createCR(ctx, dyn, repositoriesGVR, "Repository", in.Name, repositorySpec(in, repoName))
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
		Name:        "commit_files",
		Title:       "Commit files to a repository",
		Description: "Atomically write and delete files in a managed Repository. Text files are sent as UTF-8; binary files (images, fonts, archives) as base64 with encoding=base64. The tool stores a provider-owned source bundle, creates a RepositoryCommit request in your workspace, and reports the resulting commit status.",
		Annotations: commitMutating,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commitFilesInput) (*mcp.CallToolResult, commitFilesOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, commitFilesOutput{}, err
		}
		return commitFiles(ctx, dyn, deps.Bundles, ident.tenantPath, in)
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

// createCR creates a cluster-scoped CR in group code.faros.sh.
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

func commitFiles(ctx context.Context, dyn dynamic.Interface, bundles commitbundle.Store, tenantScope string, in commitFilesInput) (*mcp.CallToolResult, commitFilesOutput, error) {
	if bundles == nil {
		return nil, commitFilesOutput{}, fmt.Errorf("commit bundle store is unavailable")
	}
	tenantScope = strings.TrimSpace(tenantScope)
	if tenantScope == "" {
		return nil, commitFilesOutput{}, fmt.Errorf("tenant identity is required")
	}
	in.RepositoryRef = strings.TrimSpace(in.RepositoryRef)
	if in.RepositoryRef == "" {
		return nil, commitFilesOutput{}, fmt.Errorf("repositoryRef is required")
	}
	if len(in.Files) == 0 && len(in.DeletePaths) == 0 {
		return nil, commitFilesOutput{}, fmt.Errorf("at least one file or delete path is required")
	}
	// Mirror the RepositoryCommit spec.message limit, which counts characters,
	// so an over-long message fails here instead of after the bundle is written.
	in.Message = strings.TrimSpace(in.Message)
	if n := utf8.RuneCountInString(in.Message); n > maxCommitMessageLength {
		return nil, commitFilesOutput{}, fmt.Errorf("commit message is %d characters; the limit is %d — shorten the body", n, maxCommitMessageLength)
	}
	// Reject an unknown encoding before any lookup; the bundle store then
	// strictly decodes base64 and enforces the size limits on decoded bytes.
	for _, f := range in.Files {
		if _, err := commitbundle.NormalizeEncoding(f.Encoding); err != nil {
			return nil, commitFilesOutput{}, fmt.Errorf("file %q: %w", f.Path, err)
		}
	}
	repo, err := getRepository(ctx, dyn, in.RepositoryRef)
	if err != nil {
		return nil, commitFilesOutput{}, err
	}
	files := make([]commitbundle.File, 0, len(in.Files)+len(in.DeletePaths))
	for _, f := range in.Files {
		files = append(files, commitbundle.File{Path: f.Path, Content: f.Content, Encoding: f.Encoding})
	}
	for _, path := range in.DeletePaths {
		files = append(files, commitbundle.File{Path: path, Delete: true})
	}
	bundle, err := bundles.Put(ctx, tenantScope, files)
	if err != nil {
		return nil, commitFilesOutput{}, err
	}
	obj := repositoryCommitObject(repo, bundle, in)
	created, err := dyn.Resource(repositoryCommitsGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		_ = bundles.Delete(ctx, tenantScope, bundle.Name, bundle.Digest)
		if apierrors.IsNotFound(err) {
			return nil, commitFilesOutput{}, fmt.Errorf("create RepositoryCommit: RepositoryCommit API is not available in this workspace; enable or re-register the Code provider so repositorycommits.code.faros.sh is published: %w", err)
		}
		return nil, commitFilesOutput{}, fmt.Errorf("create RepositoryCommit: %w", err)
	}
	storageScope := repositoryCommitBundleStorageScope(tenantScope, created)
	if storageScope == "" {
		_ = dyn.Resource(repositoryCommitsGVR).Delete(ctx, created.GetName(), metav1.DeleteOptions{})
		_ = bundles.Delete(ctx, tenantScope, bundle.Name, bundle.Digest)
		return nil, commitFilesOutput{}, fmt.Errorf("created RepositoryCommit %q did not include kcp.io/cluster", created.GetName())
	}
	if _, err := bundles.Put(ctx, storageScope, files); err != nil {
		_ = dyn.Resource(repositoryCommitsGVR).Delete(ctx, created.GetName(), metav1.DeleteOptions{})
		_ = bundles.Delete(ctx, tenantScope, bundle.Name, bundle.Digest)
		return nil, commitFilesOutput{}, fmt.Errorf("store RepositoryCommit bundle: %w", err)
	}
	// Remove the staging copy only when it lives under a different scope
	// than the controller will read. The RepositoryCommit controller reads
	// the bundle under storageScope (the commit's kcp.io/cluster ID). When
	// X-Faros-Tenant and X-Faros-Cluster resolve to the same scope — e.g.
	// the MCP federation forwards the logical-cluster ID as both — then
	// tenantScope == storageScope, and deleting here would remove the only
	// bundle copy, leaving the controller to fail with "bundle not found".
	if tenantScope != storageScope {
		_ = bundles.Delete(ctx, tenantScope, bundle.Name, bundle.Digest)
	}
	out := commitFilesOutput{
		RepositoryRef: in.RepositoryRef,
		Name:          created.GetName(),
		Phase:         string(codev1alpha1.RepositoryCommitPhasePending),
		BundleRef:     bundle.Name,
		BundleDigest:  bundle.Digest,
		Files:         bundleFilePaths(bundle.Files),
		DeletedPaths:  bundleDeletedPaths(bundle.Files),
	}
	waited, err := waitRepositoryCommit(ctx, dyn, created.GetName(), commitWaitTimeout)
	if err != nil {
		return nil, out, err
	}
	if waited != nil {
		out = repositoryCommitOutput(waited, out)
	}
	switch out.Phase {
	case string(codev1alpha1.RepositoryCommitPhaseSucceeded):
		return nil, out, nil
	case string(codev1alpha1.RepositoryCommitPhaseFailed):
		return nil, out, fmt.Errorf("RepositoryCommit %q failed: %s", out.Name, repositoryCommitConditionMessage(waited))
	}
	return nil, out, unfinishedCommitError(waited, out)
}

// unfinishedCommitError reports a commit still in progress when the wait ends.
// It is a tool error so callers do not build or promote files that have not
// landed; the RepositoryCommit keeps running and names what to watch.
func unfinishedCommitError(obj *unstructured.Unstructured, out commitFilesOutput) error {
	if cond := repositoryCommitReadyCondition(obj); cond != nil && cond["reason"] == codev1alpha1.ReasonRateLimited {
		detail, _ := cond["message"].(string)
		return fmt.Errorf("RepositoryCommit %q is queued behind a GitHub rate limit (%s); the provider retries it until %s, then marks it Failed. The files are not committed yet: watch RepositoryCommit %q for phase Succeeded before relying on them",
			out.Name, detail, rateLimitDeadline(obj).UTC().Format(time.RFC3339), out.Name)
	}
	return fmt.Errorf("RepositoryCommit %q did not finish within the %s wait (phase %s); the files may not be committed yet: watch RepositoryCommit %q for phase Succeeded or Failed",
		out.Name, commitWaitTimeout, out.Phase, out.Name)
}

// rateLimitDeadline is when the controller stops retrying a rate-limited
// commit: RepositoryCommitRateLimitWindow after status.startedAt.
func rateLimitDeadline(obj *unstructured.Unstructured) time.Time {
	start := obj.GetCreationTimestamp().Time
	if raw, _, _ := unstructured.NestedString(obj.Object, "status", "startedAt"); raw != "" {
		if startedAt, err := time.Parse(time.RFC3339, raw); err == nil {
			start = startedAt
		}
	}
	if start.IsZero() {
		start = time.Now()
	}
	return start.Add(codev1alpha1.RepositoryCommitRateLimitWindow)
}

func repositoryCommitBundleStorageScope(tenantScope string, created metav1.Object) string {
	if created != nil {
		if cluster := strings.TrimSpace(created.GetAnnotations()["kcp.io/cluster"]); cluster != "" {
			return cluster
		}
	}
	return ""
}

func repositoryCommitObject(repo *codev1alpha1.Repository, bundle commitbundle.BundleRef, in commitFilesInput) *unstructured.Unstructured {
	labels := map[string]string{}
	for k, v := range repo.Labels {
		labels[k] = v
	}
	labels[codev1alpha1.LabelRepository] = in.RepositoryRef
	labelValues := make(map[string]any, len(labels))
	for k, v := range labels {
		labelValues[k] = v
	}
	metadata := map[string]any{
		"name":   commitObjectName(in.RepositoryRef, bundle.Digest, time.Now()),
		"labels": labelValues,
	}
	if repo.UID != "" {
		metadata["ownerReferences"] = []map[string]any{{
			"apiVersion":         codev1alpha1.SchemeGroupVersion.String(),
			"kind":               "Repository",
			"name":               repo.Name,
			"uid":                string(repo.UID),
			"controller":         false,
			"blockOwnerDeletion": false,
		}}
	}
	bundleRef := map[string]any{
		"name":   bundle.Name,
		"digest": bundle.Digest,
	}
	spec := map[string]any{
		"repositoryRef": in.RepositoryRef,
		"source": map[string]any{
			"bundleRef": bundleRef,
		},
	}
	putIf(spec, "message", in.Message)
	putIf(spec, "branch", in.Branch)
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       "RepositoryCommit",
		"metadata":   metadata,
		"spec":       spec,
	}}
}

func repositorySpec(in createRepositoryInput, repoName string) map[string]any {
	spec := map[string]any{
		"connectionRef": in.ConnectionRef,
		"name":          repoName,
	}
	putIf(spec, "owner", in.Owner)
	putIf(spec, "visibility", in.Visibility)
	putIf(spec, "description", in.Description)
	putIf(spec, "defaultBranch", in.DefaultBranch)
	if in.AutoInit == nil || *in.AutoInit {
		spec["autoInit"] = true
	}
	return spec
}

func waitRepositoryCommit(ctx context.Context, dyn dynamic.Interface, name string, timeout time.Duration) (*unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		obj, err := dyn.Resource(repositoryCommitsGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil
			}
			return nil, fmt.Errorf("get RepositoryCommit %q: %w", name, err)
		}
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		if phase == string(codev1alpha1.RepositoryCommitPhaseSucceeded) || phase == string(codev1alpha1.RepositoryCommitPhaseFailed) {
			return obj, nil
		}
		select {
		case <-ctx.Done():
			return obj, nil
		case <-ticker.C:
		}
	}
}

func repositoryCommitOutput(obj *unstructured.Unstructured, fallback commitFilesOutput) commitFilesOutput {
	out := fallback
	if obj.GetName() != "" {
		out.Name = obj.GetName()
	}
	if phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase"); phase != "" {
		out.Phase = phase
	}
	if branch, _, _ := unstructured.NestedString(obj.Object, "status", "branch"); branch != "" {
		out.Branch = branch
	}
	if sha, _, _ := unstructured.NestedString(obj.Object, "status", "commitSHA"); sha != "" {
		out.CommitSHA = sha
	}
	if url, _, _ := unstructured.NestedString(obj.Object, "status", "commitURL"); url != "" {
		out.CommitURL = url
	}
	if files := repositoryCommitFilePaths(obj); len(files) > 0 {
		out.Files = files
	}
	return out
}

func repositoryCommitFilePaths(obj *unstructured.Unstructured) []string {
	items, found, _ := unstructured.NestedSlice(obj.Object, "status", "files")
	if !found {
		return nil
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func bundleFilePaths(files []commitbundle.FileMeta) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

func bundleDeletedPaths(files []commitbundle.FileMeta) []string {
	paths := make([]string, 0)
	for _, file := range files {
		if file.Delete {
			paths = append(paths, file.Path)
		}
	}
	return paths
}

func repositoryCommitConditionMessage(obj *unstructured.Unstructured) string {
	if msg, ok := repositoryCommitReadyCondition(obj)["message"].(string); ok && msg != "" {
		return msg
	}
	return "unknown error"
}

// repositoryCommitReadyCondition returns the Ready condition, or nil.
func repositoryCommitReadyCondition(obj *unstructured.Unstructured) map[string]any {
	if obj == nil {
		return nil
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conds {
		if cond, ok := raw.(map[string]any); ok && cond["type"] == codev1alpha1.ConditionReady {
			return cond
		}
	}
	return nil
}

func commitObjectName(repositoryRef, digest string, now time.Time) string {
	base := strings.Trim(repositoryRef, "-")
	if base == "" {
		base = "repository"
	}
	sum := strings.TrimPrefix(digest, "sha256:")
	if len(sum) > 12 {
		sum = sum[:12]
	}
	if sum == "" {
		sum = "bundle"
	}
	suffix := fmt.Sprintf("%s-%x", sum, now.UnixNano())
	maxBase := 253 - len("-commit-") - len(suffix)
	if maxBase < 1 {
		maxBase = 1
	}
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	if base == "" {
		base = "repository"
	}
	return base + "-commit-" + suffix
}

func getRepository(ctx context.Context, dyn dynamic.Interface, name string) (*codev1alpha1.Repository, error) {
	u, err := dyn.Resource(repositoriesGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("repository %q not found", name)
		}
		return nil, fmt.Errorf("get repository %q: %w", name, err)
	}
	var repo codev1alpha1.Repository
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &repo); err != nil {
		return nil, fmt.Errorf("decode repository %q: %w", name, err)
	}
	return &repo, nil
}

func putIf(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}
