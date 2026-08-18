// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

// Project affinity: the workspace file tree is pod-local, so every
// workspace-touching request for a project must execute on the ONE replica
// that owns it (docs/app-studio-replica-awareness.md). Ownership is a durable
// project claim carrying the owner's pod address; the middleware here claims
// unowned projects lazily, serves owned ones, and forwards the rest to the
// owner over the internal listener — one intra-cluster hop, invisible to the
// hub and the browser. Store/CR-backed reads (SSE streams, listings) are
// served by any replica without touching claims.
//
// Single-replica deployments run this unchanged: every claim resolves to the
// local replica and forwarding never triggers.

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	// replicaForwardedHeader marks a request already forwarded by a peer —
	// the loop guard. Only the internal listener may set it; the public
	// listener strips it.
	replicaForwardedHeader = "X-Faros-AppStudio-Forwarded"
	// replicaInternalTokenHeader authenticates peer-forwarded requests on the
	// internal listener. Deliberately not Authorization: that header carries
	// the caller's bearer and is forwarded untouched.
	replicaInternalTokenHeader = "X-Faros-AppStudio-Internal-Token"

	// projectClaimTTL is how stale a project pin may go before any replica
	// may take the project over (workspace re-hydration is Phase C; until
	// then a takeover simply moves future requests). Idle projects unpin on
	// this cadence so the fleet rebalances.
	projectClaimTTL = 10 * time.Minute
	// projectClaimRefreshInterval bounds claim writes: a locally-owned
	// project renews its claim at most this often on the request path.
	projectClaimRefreshInterval = 10 * time.Second
)

// projectClaimKey pins by org/workspace/project NAME — the identity the
// request path carries; the UID-carrying scopes join in the handlers.
func projectClaimKey(orgUUID, workspaceUUID, projectName string) string {
	return store.ReplicaClaimKindProject + "/" + orgUUID + "/" + workspaceUUID + "/" + projectName
}

type replicaRouting struct {
	id    string
	addr  string
	token string

	mu       sync.Mutex
	owned    map[string]time.Time // claim key → last successful claim/renew
	misroute map[string]time.Time // claim key → last "owner has no addr" log
}

// SetReplicaRouting wires the replica identity for project affinity and run
// claims. addr is this replica's internal-listener address (podIP:port, empty
// disables forwarding); token authenticates peer-forwarded requests. Call
// before serving.
func (s *Server) SetReplicaRouting(replicaID, addr, token string) {
	if s == nil {
		return
	}
	s.replicaRouting = &replicaRouting{id: replicaID, addr: addr, token: token, owned: map[string]time.Time{}, misroute: map[string]time.Time{}}
	if s.assistantSupervisor != nil {
		s.assistantSupervisor.SetReplicaIdentity(replicaID, addr)
	}
}

// StripReplicaHeaders removes the spoofable affinity headers from PUBLIC
// traffic — the loop guard and internal token are only meaningful on the
// internal listener, which re-injects them after authenticating the peer.
func (s *Server) StripReplicaHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(replicaForwardedHeader)
		r.Header.Del(replicaInternalTokenHeader)
		next.ServeHTTP(w, r)
	})
}

// InternalReplicaHandler is the internal listener's wrapper: authenticate the
// peer, then mark the request forwarded so affinity serves it locally.
func (s *Server) InternalReplicaHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routing := s.routing()
		if routing == nil || routing.token == "" ||
			subtle.ConstantTimeCompare([]byte(r.Header.Get(replicaInternalTokenHeader)), []byte(routing.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Header.Set(replicaForwardedHeader, "1")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routing() *replicaRouting {
	if s == nil {
		return nil
	}
	return s.replicaRouting
}

// ReplicaAffinity routes project-scoped requests to the project's owning
// replica. Wrap the full handler with it on every listener; requests outside
// /api/projects/{project}, store-backed reads, and forwarded requests pass
// straight through.
func (s *Server) ReplicaAffinity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routing := s.routing()
		if routing == nil || r.Header.Get(replicaForwardedHeader) != "" {
			next.ServeHTTP(w, r)
			return
		}
		project, rest, scoped := splitProjectPath(r.URL.Path)
		if !scoped || localSafeProjectRead(r.Method, rest) {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := identityFromRequest(w, r)
		if !ok {
			return
		}
		key := projectClaimKey(id.orgUUID, id.workspaceUUID, project)

		// Fast path: recently confirmed local ownership.
		routing.mu.Lock()
		last, owned := routing.owned[key]
		routing.mu.Unlock()
		if owned && time.Since(last) < projectClaimRefreshInterval {
			next.ServeHTTP(w, r)
			return
		}

		// The prior claim decides whether an acquisition is an ADOPTION (the
		// project last lived on another replica — or nowhere — and this
		// replica's workspace tree must be rebuilt from git) or a plain
		// renewal.
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		prev, prevOK, err := s.store.GetReplicaClaim(ctx, key)
		if err != nil {
			cancel()
			klog.Background().Error(err, "project affinity claim read failed", "project", project)
			http.Error(w, "project ownership unavailable", http.StatusServiceUnavailable)
			return
		}
		claim, held, err := s.store.TryClaimReplica(ctx, store.ReplicaClaim{
			Key:          key,
			Kind:         store.ReplicaClaimKindProject,
			ScopeKey:     key,
			OwnerReplica: routing.id,
			OwnerAddr:    routing.addr,
		}, projectClaimTTL)
		cancel()
		if err != nil {
			klog.Background().Error(err, "project affinity claim failed", "project", project)
			http.Error(w, "project ownership unavailable", http.StatusServiceUnavailable)
			return
		}
		if held {
			routing.mu.Lock()
			routing.owned[key] = time.Now()
			routing.mu.Unlock()
			if !prevOK || prev.OwnerReplica != routing.id {
				s.adoptProject(r, id, project, prev, prevOK, claim)
			}
			next.ServeHTTP(w, r)
			return
		}
		routing.mu.Lock()
		delete(routing.owned, key)
		routing.mu.Unlock()
		if claim.OwnerAddr == "" || claim.OwnerAddr == routing.addr {
			// An owner without a forwardable address (routing disabled on it,
			// or a stale self entry): serve locally rather than fail — the
			// workspace divergence risk only exists once multi-replica routing
			// is fully configured, in which case every owner has an address.
			routing.mu.Lock()
			if time.Since(routing.misroute[key]) > time.Minute {
				routing.misroute[key] = time.Now()
				klog.Background().Info("project owner has no forwardable address; serving locally",
					"project", project, "owner", claim.OwnerReplica)
			}
			routing.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}
		s.forwardToOwner(w, r, routing, claim.OwnerAddr)
	})
}

// forwardToOwner proxies the request — path, query, caller Authorization and
// identity headers untouched — to the owning replica's internal listener.
func (s *Server) forwardToOwner(w http.ResponseWriter, r *http.Request, routing *replicaRouting, addr string) {
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = r.URL.Path
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Header.Set(replicaInternalTokenHeader, routing.token)
		},
		// SSE and long polls must stream through unbuffered.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			klog.Background().Error(err, "forwarding to project owner failed", "owner", addr, "path", r.URL.Path)
			http.Error(w, "project owner unreachable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// splitProjectPath extracts the {project} segment and the remainder from
// /api/projects/{project}[/rest]. Literal collection endpoints that happen to
// sit under /api/projects/ are not project-scoped.
func splitProjectPath(path string) (project, rest string, ok bool) {
	const prefix = "/api/projects/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	tail := strings.TrimPrefix(path, prefix)
	project, rest, _ = strings.Cut(tail, "/")
	if rest != "" {
		rest = "/" + rest
	}
	switch project {
	case "", "stream", "create-readiness", "plan", "development-templates", "import-repositories", "llm-settings":
		return "", "", false
	}
	return project, rest, true
}

// localSafeProjectRead reports whether a project-scoped request is safe on
// any replica: read-only AND backed by the store/CRs/data-plane rather than
// the pod-local workspace. Workspace-reading GETs (files, skills) must go to
// the owner; everything mutating always does. Defaulting unknown routes to
// "forward" keeps a forgotten route correct at the cost of one hop.
func localSafeProjectRead(method, rest string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if strings.HasPrefix(rest, "/files") || strings.HasPrefix(rest, "/assistant/skills") {
		return false
	}
	return true
}

// adoptProject prepares this replica's workspace after it takes over a
// project (Phase C of docs/app-studio-replica-awareness.md): seed the
// source-revision fence from the claim's durable floor, then rebuild the tree
// from git unless the previous owner was THIS pod (same forwarding address —
// a process restart on a persistent volume, where the local tree is newer
// than the last commit). Hydration failures are logged and the request served
// anyway: a project without a repository has nothing to hydrate, and failing
// closed would brick every project whenever the code provider is down.
func (s *Server) adoptProject(r *http.Request, id identity, projectName string, prev store.ReplicaClaim, prevOK bool, claim store.ReplicaClaim) {
	routing := s.routing()
	if routing == nil || s.workspaces == nil {
		return
	}
	ctx := r.Context()
	logger := klog.Background().WithValues("project", projectName, "previousOwner", prev.OwnerReplica)
	c, err := s.clientFor(id)
	if err != nil {
		logger.Error(err, "project adoption: building tenant client; serving with the local tree as-is")
		return
	}
	p, err := c.Projects().Get(ctx, projectName, metav1.GetOptions{})
	if err != nil {
		logger.Error(err, "project adoption: reading Project; serving with the local tree as-is")
		return
	}
	scope := projectWorkspaceScope(id, p)
	if claim.Revision > 0 {
		if err := s.workspaces.EnsureSourceRevisionFloor(ctx, scope, uint64(claim.Revision)); err != nil {
			logger.Error(err, "project adoption: seeding source-revision floor")
		}
	}
	if prevOK && prev.OwnerAddr != "" && prev.OwnerAddr == routing.addr {
		// Same pod, new process: the local volume outlived the restart and may
		// hold uncommitted work newer than the last commit — keep it.
		return
	}
	hydrated, err := s.hydrateWorkspaceFromRepository(ctx, id, p, r, "")
	if err != nil {
		var validationErr *ValidationError
		if errors.As(err, &validationErr) {
			// No repository yet (fresh project) — nothing to rebuild.
			logger.V(4).Info("project adoption: nothing to hydrate", "reason", err.Error())
			return
		}
		logger.Error(err, "project adoption: hydrating workspace from git failed; the local tree may be stale until a manual hydrate")
		return
	}
	logger.Info("project adopted: workspace rebuilt from git",
		"commit", hydrated.CommitSHA, "files", len(hydrated.Written), "revisionFloor", claim.Revision)
}

// OwnsProject reports whether the project's workspace commit convergence may
// run on this replica: yes when it holds the live project claim, or when no
// live claim exists (a crashed owner's leftover dirty tree must still
// converge — replicas without local files no-op naturally). An unreadable
// store refuses: never commit on uncertainty.
func (s *Server) OwnsProject(scope workspace.Scope) bool {
	routing := s.routing()
	if routing == nil {
		return true
	}
	if s.store == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	claim, ok, err := s.store.GetReplicaClaim(ctx, projectClaimKey(scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName))
	if err != nil {
		klog.Background().Error(err, "reading project claim; refusing commit convergence", "project", scope.ProjectName)
		return false
	}
	if !ok || !claim.Live(time.Now().UTC(), projectClaimTTL) {
		return true
	}
	return claim.OwnerReplica == routing.id
}

// recordProjectClaimRevision raises the durable revision floor on the
// project's claim, so the workspace source-revision fence survives the
// project moving between replicas (seeded back on hydration — Phase C).
func (s *Server) recordProjectClaimRevision(id identity, projectName string, revision uint64) {
	routing := s.routing()
	if routing == nil || s.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	key := projectClaimKey(id.orgUUID, id.workspaceUUID, projectName)
	if err := s.store.BumpReplicaClaimRevision(ctx, key, routing.id, int64(revision)); err != nil {
		klog.Background().Error(err, "recording project claim revision", "project", projectName)
	}
}
