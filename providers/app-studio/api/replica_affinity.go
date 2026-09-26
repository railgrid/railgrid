// Copyright 2026 The Railgrid Authors.
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
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

const (
	// replicaForwardedHeader marks a request already forwarded by a peer —
	// the loop guard. Only the internal listener may set it; the public
	// listener strips it.
	replicaForwardedHeader = "X-Railgrid-AppStudio-Forwarded"
	// replicaInternalTokenHeader authenticates peer-forwarded requests on the
	// internal listener. Deliberately not Authorization, which is the
	// caller's own kcp credential on the way in and is never forwarded.
	replicaInternalTokenHeader = "X-Railgrid-AppStudio-Internal-Token"

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
// replica. Wrap the full handler with it on every listener; requests that are
// not a project verb, store-backed reads, and forwarded requests pass
// straight through. It runs BEFORE serve's subresource adapter, so it parses
// the kube path itself and reads the stamped caller off the same X-Remote-*
// headers the adapter will; a forwarded request carries them to the owner.
func (s *Server) ReplicaAffinity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routing := s.routing()
		if routing == nil || r.Header.Get(replicaForwardedHeader) != "" {
			next.ServeHTTP(w, r)
			return
		}
		project, rest, scoped := affinityProject(r)
		if !scoped || localSafeProjectRead(r.Method, rest) {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := s.identityFromRequest(w, r)
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
		if s.forwardToOwner(w, r, routing, claim.OwnerAddr) {
			return
		}
		s.takeOverUnreachableProject(w, r, next, routing, id, project, claim)
	})
}

// takeOverUnreachableProject handles a request whose owner could not even be
// dialled. A replica that restarts or crashes leaves its claim fresh for up to
// projectClaimTTL, and until it lapses every peer — including the replica that
// replaced it — would forward to a dead address and fail. Nothing listens
// there, so this replica takes the claim over (only if the unreachable owner
// still holds it) and serves the request, which was never sent. Should the
// owner in fact be alive behind a network fault, it finds the claim foreign on
// its next renewal and starts forwarding here, as for any other takeover.
func (s *Server) takeOverUnreachableProject(w http.ResponseWriter, r *http.Request, next http.Handler, routing *replicaRouting, id identity, project string, prev store.ReplicaClaim) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	claim, held, err := s.store.TakeOverReplicaClaim(ctx, store.ReplicaClaim{
		Key:          prev.Key,
		Kind:         store.ReplicaClaimKindProject,
		ScopeKey:     prev.Key,
		OwnerReplica: routing.id,
		OwnerAddr:    routing.addr,
	}, prev.OwnerReplica)
	cancel()
	if err != nil {
		klog.Background().Error(err, "taking over project from unreachable owner failed", "project", project, "owner", prev.OwnerReplica)
		http.Error(w, "project owner unreachable", http.StatusBadGateway)
		return
	}
	if !held {
		// Another replica moved first; let the client retry against it.
		w.Header().Set("Retry-After", "1")
		http.Error(w, "project owner changed; retry", http.StatusServiceUnavailable)
		return
	}
	klog.Background().Info("took over project from unreachable owner",
		"project", project, "previousOwner", prev.OwnerReplica, "previousAddr", prev.OwnerAddr)
	routing.mu.Lock()
	routing.owned[prev.Key] = time.Now()
	routing.mu.Unlock()
	s.adoptProject(r, id, project, prev, true, claim)
	next.ServeHTTP(w, r)
}

// RelinquishProjectClaims marks every project claim this replica holds stale,
// so whichever replica serves the project next adopts it at once rather than
// forwarding to this one's soon-dead address until projectClaimTTL lapses.
// Call it on shutdown after the listeners have drained: a request served
// afterwards would renew the claim.
func (s *Server) RelinquishProjectClaims(ctx context.Context) {
	routing := s.routing()
	if routing == nil || s.store == nil {
		return
	}
	routing.mu.Lock()
	clear(routing.owned)
	routing.mu.Unlock()
	n, err := s.store.RelinquishReplicaClaims(ctx, store.ReplicaClaimKindProject, routing.id)
	if err != nil {
		klog.Background().Error(err, "relinquishing project claims; peers forward to this replica until the claims go stale")
		return
	}
	klog.Background().Info("relinquished project claims", "replica", routing.id, "count", n)
}

// forwardToOwner proxies the request — path, query and the shard-stamped
// identity headers untouched — to the owning replica's internal listener. It
// returns false, having written nothing, when the owner could not be dialled
// and none of the request body was consumed, so the caller may still serve the
// request itself; every other failure is answered with 502.
func (s *Server) forwardToOwner(w http.ResponseWriter, r *http.Request, routing *replicaRouting, addr string) bool {
	target := &url.URL{Scheme: "http", Host: addr}
	out := r
	var body *forwardBody
	if r.Body != nil && r.Body != http.NoBody {
		// The proxy closes the body it sends. Shield the inbound one so it is
		// still readable if the request ends up served locally.
		body = &forwardBody{ReadCloser: r.Body}
		out = r.Clone(r.Context())
		out.Body = body
	}
	unreachable := false
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = r.URL.Path
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Header.Set(replicaInternalTokenHeader, routing.token)
		},
		// SSE and long polls must stream through unbuffered.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			klog.Background().Error(err, "forwarding to project owner failed", "owner", addr, "path", req.URL.Path)
			if ownerUnreachable(r.Context(), err) && (body == nil || !body.read.Load()) {
				unreachable = true
				return
			}
			http.Error(w, "project owner unreachable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, out)
	return !unreachable
}

// ownerUnreachable reports whether a forward failed while connecting — the
// owner's address has no listener, so the request never left this replica. A
// caller that went away is not evidence about the owner.
func ownerUnreachable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// forwardBody passes the inbound request body to the proxy without letting it
// be closed, and records whether the proxy started reading it.
type forwardBody struct {
	io.ReadCloser
	read atomic.Bool
}

func (b *forwardBody) Read(p []byte) (int, error) {
	b.read.Store(true)
	return b.ReadCloser.Read(p)
}

func (b *forwardBody) Close() error { return nil }

// splitProjectPath extracts the {project} segment and the verb-plus-tail from
// a project verb's kube path,
// /clusters/{id}/apis/ai.railgrid.ai/v1alpha1/projects/{project}/{verb}[/tail].
func splitProjectPath(path string) (project, rest string, ok bool) {
	request, err := dataplane.ParseSubresourcePath(path)
	if err != nil || request.Group != aiv1alpha1.GroupName || request.Resource != projectsGVR.Resource {
		// A session verb addresses a conversation, not a project, so the
		// project it belongs to is not in the path — it is read off the
		// gated Session, one layer down. Affinity therefore cannot route it
		// here; ReplicaAffinity falls back to the routing hint (see
		// affinityProject) and, failing that, serves locally.
		return "", "", false
	}
	rest = "/" + request.Verb
	if request.Tail != "" {
		rest += "/" + request.Tail
	}
	return request.Name, rest, true
}

// affinityProjectHeader is a ROUTING hint, and nothing else.
//
// The replica that owns a project's workspace volume is the one that must run
// anything touching its files. A session verb names the conversation, not the
// project, so the portal tells this layer which project the conversation
// belongs to — before any gate has run, because forwarding has to happen
// before the body is read.
//
// Nothing is authorized from it. A forged value routes the request to the
// wrong replica, which authorizes it exactly as this one would have: the
// gates run on the Session, against the caller's own RBAC, wherever the
// request lands. The worst a lie achieves is a wasted hop.
const affinityProjectHeader = "X-Railgrid-Project"

// affinityProject returns the project a request should be routed by: the one
// in the path for a project verb, or the routing hint for a session verb.
func affinityProject(r *http.Request) (project, rest string, ok bool) {
	if project, rest, ok = splitProjectPath(r.URL.Path); ok {
		return project, rest, true
	}
	request, err := dataplane.ParseSubresourcePath(r.URL.Path)
	if err != nil || request.Group != aiv1alpha1.GroupName || request.Resource != sessionsGVR.Resource {
		return "", "", false
	}
	hint := strings.TrimSpace(r.Header.Get(affinityProjectHeader))
	if hint == "" || !dataplaneSegmentSafe(hint) {
		return "", "", false
	}
	// A session verb always touches the conversation, and a turn touches the
	// workspace, so none of them is a local-safe read.
	return hint, "/" + request.Verb, true
}

// dataplaneSegmentSafe keeps a routing hint from becoming a path.
func dataplaneSegmentSafe(s string) bool {
	return s != "." && s != ".." && !strings.ContainsAny(s, "/\x00") && len(s) <= 253
}

// localSafeProjectRead reports whether a project-scoped request is safe on
// any replica: read-only AND backed by the store/CRs/data-plane rather than
// the pod-local workspace. Everything mutating always goes to the owner.
// Defaulting unknown verbs to "forward" keeps a forgotten one correct at the
// cost of one hop.
func localSafeProjectRead(method, rest string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	verb := strings.TrimPrefix(rest, "/")
	if i := strings.IndexByte(verb, '/'); i >= 0 {
		verb = verb[:i]
	}
	return !ownerAffineVerbs[verb]
}

// ownerAffineVerbs are the reads that touch the pod-local workspace volume and
// must therefore run on the replica that owns it.
//
//   - view carries the source-revision fence, which is pod-local state;
//   - every files* verb reads the working tree;
//   - every skill* verb reads the project's skill packages off that tree.
var ownerAffineVerbs = map[string]bool{
	"view":          true,
	"files":         true,
	"files-content": true,
	"files-raw":     true,
	"skills":        true,
	"skill":         true,
	"skill-detail":  true,
	"skill-export":  true,
}

// adoptProject prepares this replica's workspace after it takes over a
// project (Phase C of docs/app-studio-replica-awareness.md): seed the
// source-revision fence from the claim's durable floor, then rebuild the tree
// from git only when current source has not survived on the local volume.
// Pod addresses do not identify volumes: a replacement pod can mount the same
// PVC with uncommitted edits. Hydration failures are logged and the request served
// anyway: a project without a repository has nothing to hydrate, and failing
// closed would brick every project whenever the code provider is down.
func (s *Server) adoptProject(r *http.Request, id identity, projectName string, prev store.ReplicaClaim, _ bool, claim store.ReplicaClaim) {
	routing := s.routing()
	if routing == nil || s.workspaces == nil {
		return
	}
	ctx := s.withProjectLedger(r.Context(), id)
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
	// The claim's recorded revision is no longer the fence — the project's own
	// status is (§9 Cut D.3) — but a claim that ran AHEAD of a status write
	// means a revision the development data plane has already seen was lost,
	// so it is still repaired into the ledger before anything reads it.
	if claim.Revision > 0 {
		if err := s.workspaces.EnsureSourceRevisionFloor(ctx, scope, uint64(claim.Revision)); err != nil {
			logger.Error(err, "project adoption: repairing the source-revision floor from the claim")
		}
	}
	// Does THIS replica hold the tree the project is at? The comparison is
	// local-tag versus ledger, so an absent tree, a tree from before another
	// replica's edits, and a tree whose tag was never written all come back
	// false and get rebuilt.
	retained, err := s.workspaces.RetainsSource(ctx, scope)
	if err != nil {
		logger.Error(err, "project adoption: inspecting retained source; refusing to overwrite it")
		return
	}
	if retained {
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
	if claim.Revision > 0 {
		// The claim's revision floor proves earlier turns mutated the workspace.
		// Those edits were only ever on the previous owner's disk; the rebuilt
		// tree holds what git has. Record it so verification and the user see
		// it instead of a log line nobody reads.
		notice := workspaceRebuildNotice{
			CommitSHA:       hydrated.CommitSHA,
			Files:           len(hydrated.Written),
			DroppedRevision: uint64(claim.Revision),
			At:              time.Now(),
			PreviousOwner:   prev.OwnerReplica,
		}
		s.recordWorkspaceRebuild(id, p, notice)
		logger.Info("project adoption dropped uncommitted workspace revisions", "blocker", notice.Blocker())
	}
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
