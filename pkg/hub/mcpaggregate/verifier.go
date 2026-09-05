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

package mcpaggregate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

// BearerVerifier decides whether the bearer presented on an aggregate MCP
// request may act in the tenant cluster and MCPServer named by the request
// path. It runs before any federation, so a rejected bearer never reaches a
// provider.
//
// Implementations return nil to allow, ErrUnauthenticated when the bearer is
// not a credential the hub recognises, ErrForbidden when it is a valid
// credential for a different tenant or identity, and any other error when
// verification itself could not be performed (the handler answers 503).
type BearerVerifier interface {
	Verify(r *http.Request, token, cluster, mcpServerName string) error
}

// BearerVerifierFunc adapts a function to BearerVerifier.
type BearerVerifierFunc func(r *http.Request, token, cluster, mcpServerName string) error

// Verify implements BearerVerifier.
func (f BearerVerifierFunc) Verify(r *http.Request, token, cluster, mcpServerName string) error {
	return f(r, token, cluster, mcpServerName)
}

var (
	// ErrUnauthenticated means the bearer is not a credential the hub or the
	// tenant cluster recognises.
	ErrUnauthenticated = errors.New("bearer is not authenticated")
	// ErrForbidden means the bearer is a valid credential but not for the
	// tenant cluster and MCPServer addressed by the request.
	ErrForbidden = errors.New("bearer is not authorized for this MCPServer")
)

// serviceAccountPrefix is the username prefix kube reports for SA tokens.
const serviceAccountPrefix = "system:serviceaccount:"

// tenantPathRoot is the kcp path every Organization and child Workspace
// lives under (mirrors orgWorkspaceParent in the organization controller and
// workspacePathRoot in the hub's provider tenant resolver).
const tenantPathRoot = "root:faros:tenants:"

// clusterPathTTL bounds how long a resolved cluster ID to workspace path
// mapping is reused. Paths are set once at workspace creation and never
// reassigned, so a long TTL is safe.
const clusterPathTTL = 5 * time.Minute

// logicalClusterGVR addresses the per-workspace LogicalCluster singleton whose
// kcp.io/path annotation carries the workspace path.
var logicalClusterGVR = schema.GroupVersionResource{
	Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
}

// Verifier is the production BearerVerifier. It accepts exactly two kinds of
// bearer:
//
//  1. The MCPServer's own ServiceAccount token, which the mcpserver controller
//     mints and the portal / REST API hand to the user for long-lived MCP
//     client configuration. It is verified online with a TokenReview in the
//     tenant cluster named by the request, and the reviewed username must be
//     ServiceAccountUsername(name) for the MCPServer named by the request.
//  2. A hub user bearer (static token or OIDC id_token) as resolved by the
//     hub's normal identity path; the CLI's `faros mcp` and the e2e suites
//     use these. The user must hold a live Membership covering the tenant
//     the cluster belongs to, per the UserMembershipIndex.
//
// Signatures and claims are never trusted offline; both paths are online
// checks against kcp.
type Verifier struct {
	// clusterConfig returns a rest.Config addressing the named tenant cluster.
	clusterConfig func(cluster string) *rest.Config
	// newKube builds the kube client used for TokenReview (test seam).
	newKube func(*rest.Config) (kubernetes.Interface, error)
	// clusterPath resolves a logical-cluster ID to its kcp workspace path.
	clusterPath func(ctx context.Context, cluster string) (string, error)

	mu         sync.RWMutex
	identify   func(*http.Request) (string, error)
	membership func(ctx context.Context, user string) (*tenancyv1alpha1.UserMembershipIndex, error)

	pathMu sync.RWMutex
	paths  map[string]pathEntry
}

type pathEntry struct {
	path      string
	expiresAt time.Time
}

// VerifierOption customises a Verifier.
type VerifierOption func(*Verifier)

// WithKubeClientFactory overrides how the TokenReview client is built.
func WithKubeClientFactory(f func(*rest.Config) (kubernetes.Interface, error)) VerifierOption {
	return func(v *Verifier) { v.newKube = f }
}

// WithClusterPathResolver overrides how a logical-cluster ID is mapped to its
// workspace path.
func WithClusterPathResolver(f func(ctx context.Context, cluster string) (string, error)) VerifierOption {
	return func(v *Verifier) { v.clusterPath = f }
}

// NewVerifier builds a Verifier. clusterConfig must return a rest.Config whose
// Host addresses the named tenant cluster (apiurl.KCPClusterURL); returning
// nil makes every verification fail closed.
func NewVerifier(clusterConfig func(cluster string) *rest.Config, opts ...VerifierOption) *Verifier {
	v := &Verifier{
		clusterConfig: clusterConfig,
		newKube: func(cfg *rest.Config) (kubernetes.Interface, error) {
			return kubernetes.NewForConfig(cfg)
		},
		paths: make(map[string]pathEntry),
	}
	v.clusterPath = v.lookupClusterPath
	for _, o := range opts {
		o(v)
	}
	return v
}

// SetUserIdentity enables the hub-user branch. identify resolves a request's
// bearer to a User name (KCPProxy.IdentifyUser); membership reads that user's
// UserMembershipIndex. Until both are set only MCPServer ServiceAccount
// tokens are accepted.
func (v *Verifier) SetUserIdentity(
	identify func(*http.Request) (string, error),
	membership func(ctx context.Context, user string) (*tenancyv1alpha1.UserMembershipIndex, error),
) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.identify = identify
	v.membership = membership
}

// Verify implements BearerVerifier.
func (v *Verifier) Verify(r *http.Request, token, cluster, mcpServerName string) error {
	if v == nil || r == nil {
		return errors.New("bearer verifier unavailable")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return ErrUnauthenticated
	}
	ctx := r.Context()

	// Hub user bearers first: identification is an offline JWT / constant-time
	// compare, so a non-user token falls through cheaply to the online path.
	v.mu.RLock()
	identify, membership := v.identify, v.membership
	v.mu.RUnlock()
	if identify != nil && membership != nil {
		user, err := identify(r)
		if err == nil && !strings.HasPrefix(user, serviceAccountPrefix) {
			return v.verifyMember(ctx, user, cluster)
		}
	}

	return v.verifyServiceAccount(ctx, token, cluster, mcpServerName)
}

// verifyServiceAccount runs a TokenReview in the tenant cluster and requires
// the reviewed identity to be the MCPServer's own ServiceAccount. No audience
// is requested: the controller mints legacy Secret-backed tokens, which carry
// the API server's implicit audience only.
func (v *Verifier) verifyServiceAccount(ctx context.Context, token, cluster, mcpServerName string) error {
	cfg := v.clusterConfig(cluster)
	if cfg == nil {
		return fmt.Errorf("no kcp configuration for cluster %q", cluster)
	}
	cs, err := v.newKube(cfg)
	if err != nil {
		return fmt.Errorf("building TokenReview client for cluster %q: %w", cluster, err)
	}
	review, err := cs.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{Token: token},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("reviewing bearer in cluster %q: %w", cluster, err)
	}
	if !review.Status.Authenticated {
		return ErrUnauthenticated
	}
	if review.Status.User.Username != ServiceAccountUsername(mcpServerName) {
		return fmt.Errorf("%w: reviewed identity %q is not the MCPServer service account", ErrForbidden, review.Status.User.Username)
	}
	return nil
}

// verifyMember requires user to hold a live Membership covering the tenant
// the cluster belongs to. An org-scope entry covers every child workspace;
// a workspace-scope entry covers only that workspace.
func (v *Verifier) verifyMember(ctx context.Context, user, cluster string) error {
	v.mu.RLock()
	membership := v.membership
	v.mu.RUnlock()

	path, err := v.resolvePath(ctx, cluster)
	if err != nil {
		return err
	}
	orgUUID, wsUUID, ok := tenantFromPath(path)
	if !ok {
		return fmt.Errorf("%w: cluster %q is not a tenant workspace", ErrForbidden, cluster)
	}
	idx, err := membership(ctx, user)
	if err != nil {
		return fmt.Errorf("reading membership index for %q: %w", user, err)
	}
	if idx != nil {
		for _, e := range idx.Spec.Entries {
			if e.OrgUUID != orgUUID || e.SoftDeletedAt != nil {
				continue
			}
			if e.WorkspaceUUID == "" || e.WorkspaceUUID == wsUUID {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: user %q has no membership in (org=%s, workspace=%s)", ErrForbidden, user, orgUUID, wsUUID)
}

// resolvePath returns the workspace path for cluster. A cluster containing
// ':' is already a path; a bare logical-cluster ID is resolved through the
// cluster's LogicalCluster singleton and cached.
func (v *Verifier) resolvePath(ctx context.Context, cluster string) (string, error) {
	if strings.Contains(cluster, ":") {
		return cluster, nil
	}
	now := time.Now()
	v.pathMu.RLock()
	e, ok := v.paths[cluster]
	v.pathMu.RUnlock()
	if ok && now.Before(e.expiresAt) {
		return e.path, nil
	}
	path, err := v.clusterPath(ctx, cluster)
	if err != nil {
		return "", err
	}
	v.pathMu.Lock()
	v.paths[cluster] = pathEntry{path: path, expiresAt: now.Add(clusterPathTTL)}
	v.pathMu.Unlock()
	return path, nil
}

// lookupClusterPath is the default clusterPath: read kcp.io/path off the
// LogicalCluster singleton in the addressed cluster.
func (v *Verifier) lookupClusterPath(ctx context.Context, cluster string) (string, error) {
	cfg := v.clusterConfig(cluster)
	if cfg == nil {
		return "", fmt.Errorf("no kcp configuration for cluster %q", cluster)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("dynamic client for cluster %q: %w", cluster, err)
	}
	lc, err := dyn.Resource(logicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting LogicalCluster for cluster %q: %w", cluster, err)
	}
	path := lc.GetAnnotations()["kcp.io/path"]
	if path == "" {
		return "", fmt.Errorf("LogicalCluster for cluster %q has no kcp.io/path annotation", cluster)
	}
	return path, nil
}

// tenantFromPath splits a tenant workspace path into its Organization UUID
// and optional child Workspace UUID. Paths outside root:faros:tenants, or
// nested deeper than one child workspace, are not tenant workspaces users
// can hold Memberships in.
func tenantFromPath(path string) (orgUUID, wsUUID string, ok bool) {
	if !strings.HasPrefix(path, tenantPathRoot) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, tenantPathRoot), ":")
	switch {
	case len(parts) == 1 && parts[0] != "":
		return parts[0], "", true
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}
