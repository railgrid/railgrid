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

// Package commitexec is the one executor behind every way of asking this
// provider to commit files: the repositories/commit/v1 action
// (actions/commit.go) and the commit_files MCP tool
// (mcpserver/tools_write.go), which is a projection of it.
//
// A RepositoryCommit is a POINTER at a source bundle held in this provider's
// own store (commitbundle): the CR carries a name and a digest, never file
// contents, so a generated app does not land in etcd. Writing the bundle and
// creating the CR that names it therefore have to happen together, in the
// provider, and that pairing is what lives here. Callers differ only in how
// they were authorized (an action's kcp grant and gate, or the MCP bearer) and in what
// they do afterwards (the action returns the commit's coordinates; the tool
// waits for a terminal phase).
package commitexec

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
	"k8s.io/client-go/dynamic"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/commitbundle"
)

// The cluster-scoped kinds this package addresses.
var (
	RepositoriesGVR      = codev1alpha1.SchemeGroupVersion.WithResource("repositories")
	RepositoryCommitsGVR = codev1alpha1.SchemeGroupVersion.WithResource("repositorycommits")
)

// MaxCommitMessageLength mirrors RepositoryCommit spec.message MaxLength, so
// an over-long message fails before a bundle is written rather than at
// admission after it.
const MaxCommitMessageLength = 512

// Request is one commit to ask for. Exactly one source is given: Files
// (contents that still have to be stored) or Bundle (a bundle a large-upload
// verb already stored under the same scope).
type Request struct {
	// RepositoryRef names the managed Repository to commit into.
	RepositoryRef string
	// Message is the commit message; empty lets the backend choose one.
	Message string
	// Branch overrides the Repository's default branch.
	Branch string
	// Files are the writes and deletions of this commit.
	Files []commitbundle.File
	// Bundle names an already-stored bundle instead of Files. Its digest is
	// verified on read, so a staged handle cannot be pointed at other bytes.
	Bundle *commitbundle.BundleRef
}

// Result is the created RepositoryCommit and the bundle it points at.
type Result struct {
	Name   string
	UID    string
	Bundle commitbundle.BundleRef
}

// Validate checks everything that can be checked without touching kcp or the
// bundle store. It is exported so a caller can refuse a malformed request
// with its own error shape (an MCP error, an action's invalid_action_input)
// before any work starts.
func (req *Request) Validate() error {
	req.RepositoryRef = strings.TrimSpace(req.RepositoryRef)
	if req.RepositoryRef == "" {
		return fmt.Errorf("repositoryRef is required")
	}
	req.Message = strings.TrimSpace(req.Message)
	if n := utf8.RuneCountInString(req.Message); n > MaxCommitMessageLength {
		return fmt.Errorf("commit message is %d characters; the limit is %d — shorten the body", n, MaxCommitMessageLength)
	}
	if req.Bundle != nil {
		if len(req.Files) > 0 {
			return fmt.Errorf("a commit names either files or a bundleRef, not both")
		}
		if strings.TrimSpace(req.Bundle.Name) == "" {
			return fmt.Errorf("bundleRef.name is required")
		}
		return nil
	}
	if len(req.Files) == 0 {
		return fmt.Errorf("at least one file or delete path is required")
	}
	// Reject an unknown encoding before any lookup; the bundle store then
	// strictly decodes base64 and enforces the size limits on decoded bytes.
	for _, f := range req.Files {
		if _, err := commitbundle.NormalizeEncoding(f.Encoding); err != nil {
			return fmt.Errorf("file %q: %w", f.Path, err)
		}
	}
	return nil
}

// Create stores the bundle and creates the RepositoryCommit that names it.
//
// dyn is whatever client the caller was authorized to act with: the caller's
// own for the MCP tool, the provider's export client for an action whose two
// gates have already passed. tenantScope is the workspace's logical-cluster
// ID, which is both the staging scope and — barring a disagreement the
// function repairs — the scope the RepositoryCommit controller reads under.
//
// Every failure leaves nothing behind: a bundle written for a CR that could
// not be created is deleted again, and a CR whose bundle could not be moved
// into the controller's scope is deleted with it.
func Create(ctx context.Context, dyn dynamic.Interface, bundles commitbundle.Store, tenantScope string, req Request) (Result, error) {
	if bundles == nil {
		return Result{}, fmt.Errorf("commit bundle store is unavailable")
	}
	if dyn == nil {
		return Result{}, fmt.Errorf("no workspace client to create the RepositoryCommit with")
	}
	tenantScope = strings.TrimSpace(tenantScope)
	if tenantScope == "" {
		return Result{}, fmt.Errorf("tenant identity is required")
	}
	if err := req.Validate(); err != nil {
		return Result{}, err
	}

	// A staged bundle is read back (digest-verified) into the same file list
	// an inline request carries, so both sources meet here and everything
	// below runs once.
	files := req.Files
	if req.Bundle != nil {
		stored, err := bundles.Get(ctx, tenantScope, req.Bundle.Name, req.Bundle.Digest)
		if err != nil {
			return Result{}, fmt.Errorf("read staged bundle %q: %w", req.Bundle.Name, err)
		}
		files = make([]commitbundle.File, 0, len(stored.Files))
		for _, f := range stored.Files {
			files = append(files, commitbundle.File{Path: f.Path, Content: f.Content, Encoding: f.Encoding, Delete: f.Delete})
		}
		if len(files) == 0 {
			return Result{}, fmt.Errorf("staged bundle %q is empty", req.Bundle.Name)
		}
	}

	repo, err := getRepository(ctx, dyn, req.RepositoryRef)
	if err != nil {
		return Result{}, err
	}

	bundle, err := bundles.Put(ctx, tenantScope, files)
	if err != nil {
		return Result{}, err
	}
	obj := commitObject(repo, bundle, req)
	created, err := dyn.Resource(RepositoryCommitsGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		_ = bundles.Delete(ctx, tenantScope, bundle.Name, bundle.Digest)
		if apierrors.IsNotFound(err) {
			return Result{}, fmt.Errorf("create RepositoryCommit: RepositoryCommit API is not available in this workspace; enable or re-register the Code provider so repositorycommits.code.railgrid.ai is published: %w", err)
		}
		return Result{}, fmt.Errorf("create RepositoryCommit: %w", err)
	}
	storageScope := bundleStorageScope(created)
	if storageScope == "" {
		_ = dyn.Resource(RepositoryCommitsGVR).Delete(ctx, created.GetName(), metav1.DeleteOptions{})
		_ = bundles.Delete(ctx, tenantScope, bundle.Name, bundle.Digest)
		return Result{}, fmt.Errorf("created RepositoryCommit %q did not include kcp.io/cluster", created.GetName())
	}
	if _, err := bundles.Put(ctx, storageScope, files); err != nil {
		_ = dyn.Resource(RepositoryCommitsGVR).Delete(ctx, created.GetName(), metav1.DeleteOptions{})
		_ = bundles.Delete(ctx, tenantScope, bundle.Name, bundle.Digest)
		return Result{}, fmt.Errorf("store RepositoryCommit bundle: %w", err)
	}
	// Remove the staging copy only when it lives under a different scope
	// than the controller will read. The RepositoryCommit controller reads
	// the bundle under storageScope (the commit's kcp.io/cluster ID), and the
	// staging scope is the request's cluster — the same logical-cluster ID —
	// so the two normally agree and deleting here would remove the only copy,
	// leaving the controller to fail with "bundle not found".
	if tenantScope != storageScope {
		_ = bundles.Delete(ctx, tenantScope, bundle.Name, bundle.Digest)
	}
	return Result{Name: created.GetName(), UID: string(created.GetUID()), Bundle: bundle}, nil
}

// bundleStorageScope is the logical cluster the created RepositoryCommit
// lives in, which is the scope its controller reads the bundle under.
func bundleStorageScope(created metav1.Object) string {
	if created == nil {
		return ""
	}
	return strings.TrimSpace(created.GetAnnotations()["kcp.io/cluster"])
}

// commitObject builds the RepositoryCommit that points at bundle.
func commitObject(repo *codev1alpha1.Repository, bundle commitbundle.BundleRef, req Request) *unstructured.Unstructured {
	labels := map[string]any{}
	for k, v := range repo.Labels {
		labels[k] = v
	}
	labels[codev1alpha1.LabelRepository] = req.RepositoryRef
	metadata := map[string]any{
		"name":   ObjectName(req.RepositoryRef, bundle.Digest, time.Now()),
		"labels": labels,
	}
	if repo.UID != "" {
		// []any, not []map[string]any: an unstructured object has to be
		// deep-copyable JSON, and a typed slice is not.
		metadata["ownerReferences"] = []any{map[string]any{
			"apiVersion":         codev1alpha1.SchemeGroupVersion.String(),
			"kind":               "Repository",
			"name":               repo.Name,
			"uid":                string(repo.UID),
			"controller":         false,
			"blockOwnerDeletion": false,
		}}
	}
	spec := map[string]any{
		"repositoryRef": req.RepositoryRef,
		"source": map[string]any{
			"bundleRef": map[string]any{
				"name":   bundle.Name,
				"digest": bundle.Digest,
			},
		},
	}
	putIf(spec, "message", req.Message)
	putIf(spec, "branch", req.Branch)
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       "RepositoryCommit",
		"metadata":   metadata,
		"spec":       spec,
	}}
}

// ObjectName mints the RepositoryCommit's name from the repository it targets
// and the bundle it carries, so two concurrent commits of the same content
// never collide and the name still reads as what it is.
func ObjectName(repositoryRef, digest string, now time.Time) string {
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

// GetRepository reads one managed Repository through dyn.
func GetRepository(ctx context.Context, dyn dynamic.Interface, name string) (*codev1alpha1.Repository, error) {
	return getRepository(ctx, dyn, name)
}

func getRepository(ctx context.Context, dyn dynamic.Interface, name string) (*codev1alpha1.Repository, error) {
	u, err := dyn.Resource(RepositoriesGVR).Get(ctx, name, metav1.GetOptions{})
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

// BundleFilePaths lists every path a bundle carries.
func BundleFilePaths(files []commitbundle.FileMeta) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

// BundleDeletedPaths lists the paths a bundle removes.
func BundleDeletedPaths(files []commitbundle.FileMeta) []string {
	paths := make([]string, 0)
	for _, f := range files {
		if f.Delete {
			paths = append(paths, f.Path)
		}
	}
	return paths
}

func putIf(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}
