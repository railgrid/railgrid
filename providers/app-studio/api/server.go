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

// Package api serves the App Studio data-plane verbs and the assistant. It
// runs in the standalone provider binary: every verb is a kcp custom
// subresource on this provider's APIExport, which a kcp shard authorizes with
// ordinary RBAC and reverse-proxies here with the caller's identity stamped
// in requestheader headers. There is no caller bearer: after the gate decides
// the caller may see the addressed object, a handler acts AS THE PROVIDER
// through its APIExport virtual workspace, and any further question about
// the caller is a SubjectAccessReview on their behalf (dataplane.Authorize).
package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/provider-sdk/dataplane"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/hubmcp"
	"github.com/railgrid/provider-app-studio/internal/reconcilesignal"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/tenant"
	"github.com/railgrid/provider-app-studio/workspace"
)

// Server holds the dependencies the project handlers need. clients builds a
// per-(tenant, caller) dynamic client; store persists chat transcripts; hubBase
// locates the hub's MCP virtual workspace; mcpInsecureSkipTLSVerify relaxes TLS
// for explicitly enabled local hub calls (MCP and action-catalog lookup), while
// Provider Action invocation retains certificate validation; workspaces stores
// project files owned by App Studio; and assistantEngine runs project turns.
type Server struct {
	tenant *tenant.Client
	store  store.Store
	// attachments is a separate capability so message-only test stores remain
	// valid. Production wires the same Postgres/encrypted store for both.
	attachments              store.AttachmentStore
	attachmentsInStore       bool
	attachmentDraftRetention time.Duration
	workspaces               *workspace.FileStore
	hubBase                  string
	// hubPublicURL is the browser-reachable hub origin used by private preview
	// authorization redirects and one-use browser-session handoffs. It is
	// deliberately separate from hubBase, which may be an internal
	// cluster-local address used for MCP/data-plane traffic.
	hubPublicURL string
	// actionsExternalURL is the externally reachable hub origin used by
	// development workloads for the Provider Actions exchange and SDK base
	// URL. It is deliberately separate from hubBase, which may be an internal
	// cluster-local address used for MCP/heartbeat traffic.
	actionsExternalURL string
	// actionsCABundle is optional public trust material for action-enabled
	// development runtimes. Keep the load error until a grant actually needs
	// the value so actionless projects retain the normal system trust path.
	actionsCABundle    string
	actionsCABundleErr error
	// providerActionCatalogResolver is a test seam for the authenticated hub
	// catalog lookup. Production leaves it nil so grants always resolve via
	// GET /api/providers, as the provider (hubToken).
	providerActionCatalogResolver providerActionCatalogResolver
	// hubToken is the bearer this provider presents on the hub's OWN REST API
	// and MCP aggregate — the provider catalog, the membership rosters, the
	// browser-session handoff, the workspace MCP endpoint. Those are not
	// data-plane verbs and are still reached on the hub; a verb carries no
	// caller credential to forward there, so they are made as the provider,
	// with the kcp-authenticated caller's name as the X-Railgrid-User label.
	hubToken                     string
	mcpInsecureSkipTLSVerify     bool
	previewInsecureSkipTLSVerify bool
	assistantEngine              projectAssistantEngine
	// assistantThreadTitleGenerator is a test seam for the detached, one-shot
	// title request. Production leaves it nil and uses the connected project LLM.
	assistantThreadTitleGenerator func(context.Context, *asclient.Client, string) (string, error)
	// projectClientFor is an optional test seam for handlers that need a
	// workspace-scoped Project client without a provider credential.
	// Production leaves it nil and uses clientFor's provider-scoped client.
	projectClientFor func(identity) (*asclient.Client, error)
	// tenantWorkspaces maps the cluster ID a verb's path names to the
	// workspace's path / org / workspace UUIDs, read from kcp through this
	// provider's export virtual workspace. Nil without a provider credential;
	// identity then carries no org/workspace scope.
	tenantWorkspaces workspaceLookup
	// tenantActors is a TEST SEAM over the caller kcp stamped. Production
	// leaves it nil: the actor is dataplane.ProxiedIdentity.User, and
	// X-Railgrid-User is a label.
	tenantActors actorLookup
	// tenantProviders reports whether a dependency is enabled in a workspace,
	// by whether its claimed kinds are served through this provider's export.
	tenantProviders providerLookup
	// callers is this provider's caller factory: it acts as the provider in a
	// tenant workspace (the gate, every handler's client) and calls the verbs
	// of other providers this one has claimed, through its own export
	// virtual workspace. Nil fails every verb closed.
	callers providerCallers
	// llmDiscoveryHTTPClient is a narrow test seam for credential-scoped model
	// catalog requests. Production uses a redirect-denying bounded client.
	llmDiscoveryHTTPClient *http.Client
	assistantRunManager    *projectAssistantRunManager
	// browserSessions owns the stateful native Playwright MCP session for each
	// tenant/workspace/project/run owner tuple. It is intentionally process-local
	// because the browser instance itself is single-replica and stateful.
	browserSessions      *projectAssistantBrowserSessionManager
	assistantSupervisor  *projectAssistantSupervisor
	runSandboxManager    *projectAssistantSandboxManager
	runSandboxConfig     CodingSandboxConfig
	runSandboxConfigured bool
	// codingSandboxResolver resolves a caller's organization-scoped BYO
	// provider binding. Nil is fail-closed. Platform force mode never calls it.
	codingSandboxResolver  func(context.Context, identity, workspace.Scope) (CodingSandboxEligibility, error)
	runSandboxSetupFactory func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState, *projectAssistantSandboxCheckpoint) (*projectAssistantRunSandbox, func(), error)
	// runSandboxClientFactory is an App Studio-only seam for the Infrastructure
	// workspace protocol. Production uses the authenticated data-plane client.
	runSandboxClientFactory func(*Server) projectAssistantSandboxClient
	// sandboxDataPlaneClientFactory is a narrow HTTP seam for protocol tests;
	// production leaves it nil and uses the authenticated TLS transport.
	sandboxDataPlaneClientFactory func(time.Duration) *http.Client
	// replicaRouting carries this replica's identity/address/token for
	// project affinity and durable run claims (replica_affinity.go). Nil
	// until SetReplicaRouting; affinity is a no-op without it.
	replicaRouting               *replicaRouting
	assistantProjectionLocks     map[string]*assistantThreadProjectionLockEntry
	assistantThreadMirrors       map[string]struct{}
	developmentSyncLocks         map[string]*sync.Mutex
	developmentSyncTails         map[string]chan struct{}
	developmentSyncAfterMutation func(identity, *aiv1alpha1.Project, string) error
	// developmentSyncReadyTimeout / developmentSyncReadyBackoff override how
	// long, and how often, a post-mutation sync retries while the development
	// environment is being provisioned. Zero uses the package defaults; tests
	// shorten them.
	developmentSyncReadyTimeout time.Duration
	developmentSyncReadyBackoff time.Duration
	projectCreatePreflight      projectCreatePreflightGenerator
	// developmentSyncFailures records the most recent post-mutation sync
	// failure per project so verify_development_runtime can report it. A
	// failed background sync means the assistant's edits never reached the
	// sandbox; logging it alone made that invisible to the user AND to the
	// model, which would then diagnose a stale runtime as a code bug.
	developmentSyncFailures map[string]string
	// developmentSyncRevisionOffsets holds, per development component, how
	// far a plain (non-App Studio) sync pushed the agent's applied revision
	// ahead of the FileStore revision. See developmentSyncRevision.
	developmentSyncRevisionOffsets map[string]uint64
	// codeCheckoutBinary caches, per workspace cluster, whether the Code
	// provider's checkout_repository tool advertises base64 binaries. Commit
	// is not cached because it is not probed: it is an action whose schema
	// declares the encoding. syncBinary caches, per development component,
	// whether its agent's /status advertises base64 sync.
	codeCheckoutBinary hubmcp.CapabilityCache
	syncBinary         hubmcp.CapabilityCache
	// syncBinaryNotices remembers components already told (in the log) that
	// binaries are skipped, so the notice is not repeated on every sync.
	syncBinaryNotices map[string]bool
	// workspaceRebuilds records, per project, that this replica rebuilt the
	// workspace from git after taking the project over while the claim still
	// carried uncommitted source revisions. The tree on disk then no longer
	// matches what earlier assistant turns wrote; verification reports it as a
	// blocker until the next mutation lands on the rebuilt tree.
	workspaceRebuilds map[string]workspaceRebuildNotice
	// projectBuildRunCache keeps the explanatory CI observation short-lived.
	// Registry Package objects remain the promotion authority; this cache only
	// prevents the Production surface's polling loop from creating duplicate
	// Code-provider status requests for the same exact commit.
	projectBuildRunCache    map[string]projectBuildRunCacheEntry
	projectBuildRunInflight map[string]*projectBuildRunInflight
	projectBuildRunResolver func(context.Context, identity, *aiv1alpha1.Project, *http.Request, string) (*projectBuildRunObservation, error)
	// previewEdgeProbe + edgeReadyURLs implement the preview edge-readiness
	// gate (see preview_edge.go). Nil probe → the real HTTPS probe.
	previewEdgeProbe            func(context.Context, string) error
	edgeReadyURLs               edgeReadyURLsCache
	previewEdgeProbeInflight    map[string]*previewEdgeProbeInflight
	previewBridgeEnabled        bool
	previewBridgeStore          *previewBridgeStore
	previewBridgeSigner         *previewBridgeCapabilitySigner
	previewInspector            projectAssistantPreviewInspector
	previewInspectionResolveURL func(context.Context, identity, *aiv1alpha1.Project) (string, error)
	projectThumbnailContext     context.Context
	projectThumbnailCancel      context.CancelFunc
	projectThumbnailCaptures    map[string]*projectThumbnailCaptureRequest
	projectThumbnailCurrentness func(context.Context, identity, *aiv1alpha1.Project, uint64) error
	projectThumbnailFailures    map[string]time.Time
	projectThumbnailQueue       chan string
	projectThumbnailWorkersUp   bool
	// publishingMembershipFetcher is a test seam for the hub-mediated
	// membership lookup used by the publishing API. Production resolves the
	// current org/workspace membership through hubBase as the provider
	// (hubToken); App Studio never treats an email address as a grant identity.
	publishingMembershipFetcher func(context.Context, identity) ([]publishingMember, error)
	// publishingMemberInviter is the matching test seam for invite-by-email:
	// production POSTs the hub org-membership endpoint with invite semantics
	// and returns the pending User's stable name.
	publishingMemberInviter func(context.Context, identity, string) (publishingMember, error)
	publishingHTTPClient    *http.Client
	// sessionSignals / projectSignals wake the Session and Project
	// reconcilers on store and workspace transitions (reconcile_signals.go);
	// workspaceClusters maps a workspace UUID to the kcp cluster the signals
	// are addressed to.
	sessionSignals    *reconcilesignal.Bus
	projectSignals    *reconcilesignal.Bus
	workspaceClusters sync.Map
	mu                sync.Mutex
}

// New constructs a Server.
func New(tenantClient *tenant.Client, msgStore store.Store, hubBase string, mcpInsecureSkipTLSVerify bool) *Server {
	return NewWithWorkspace(tenantClient, msgStore, nil, hubBase, mcpInsecureSkipTLSVerify)
}

// NewWithWorkspace constructs a Server with an explicit project workspace store.
func NewWithWorkspace(tenantClient *tenant.Client, msgStore store.Store, workspaces *workspace.FileStore, hubBase string, mcpInsecureSkipTLSVerify bool) *Server {
	return NewWithWorkspaceContext(context.Background(), tenantClient, msgStore, workspaces, hubBase, mcpInsecureSkipTLSVerify)
}

// NewWithWorkspaceContext binds assistant workers to the provider lifecycle.
func NewWithWorkspaceContext(parent context.Context, tenantClient *tenant.Client, msgStore store.Store, workspaces *workspace.FileStore, hubBase string, mcpInsecureSkipTLSVerify bool) *Server {
	if parent == nil {
		parent = context.Background()
	}
	actionsCABundle, actionsCABundleErr := loadActionsCABundleFromEnv()
	thumbnailContext, thumbnailCancel := context.WithCancel(parent)
	s := &Server{
		tenant:                   tenantClient,
		store:                    msgStore,
		attachmentDraftRetention: store.DefaultAttachmentDraftRetention,
		workspaces:               workspaces,
		hubBase:                  hubBase,
		hubPublicURL:             strings.TrimSpace(os.Getenv("RAILGRID_HUB_PUBLIC_URL")),
		actionsExternalURL:       strings.TrimSpace(os.Getenv("RAILGRID_ACTIONS_EXTERNAL_URL")),
		actionsCABundle:          actionsCABundle,
		actionsCABundleErr:       actionsCABundleErr,
		mcpInsecureSkipTLSVerify: mcpInsecureSkipTLSVerify,
		projectThumbnailContext:  thumbnailContext,
		projectThumbnailCancel:   thumbnailCancel,
	}
	if attachmentStore, ok := msgStore.(store.AttachmentStore); ok {
		s.attachments = attachmentStore
		s.attachmentsInStore = true
	}
	s.publishingHTTPClient = newPublishingHTTPClient()
	s.assistantEngine = NewEinoAssistantEngine(s)
	s.assistantRunManager = newProjectAssistantRunManager()
	s.browserSessions = newProjectAssistantBrowserSessionManager()
	s.assistantSupervisor = newProjectAssistantSupervisor(parent, msgStore)
	s.assistantSupervisor.server = s
	s.runSandboxManager = newProjectAssistantSandboxManager()
	if config, _, err := ParseCodingSandboxConfig(getenv); err == nil {
		s.runSandboxConfig = config
		s.runSandboxConfigured = true
	}
	return s
}

// ConfigureCodingSandbox installs the immutable, server-owned sandbox policy
// validated by the process before it begins serving requests.
func (s *Server) ConfigureCodingSandbox(config CodingSandboxConfig) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runSandboxConfig = config
	s.runSandboxConfigured = true
}

func (s *Server) Shutdown(ctx context.Context) {
	if s.projectThumbnailCancel != nil {
		s.projectThumbnailCancel()
	}
	if s.browserSessions != nil {
		s.browserSessions.closeAll()
	}
	s.projectAssistantSupervisor().Shutdown(ctx)
}

func (s *Server) projectAssistantSupervisor() *projectAssistantSupervisor {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantSupervisor == nil {
		s.assistantSupervisor = newProjectAssistantSupervisor(context.Background(), s.store)
	}
	s.assistantSupervisor.server = s
	return s.assistantSupervisor
}

func (s *Server) projectAssistantEngine() projectAssistantEngine {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantEngine == nil {
		s.assistantEngine = NewEinoAssistantEngine(s)
	}
	return s.assistantEngine
}

func (s *Server) projectAssistantRunManager() *projectAssistantRunManager {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantRunManager == nil {
		s.assistantRunManager = newProjectAssistantRunManager()
	}
	return s.assistantRunManager
}

func (s *Server) projectAssistantSandboxManager() *projectAssistantSandboxManager {
	if s == nil {
		return newProjectAssistantSandboxManager()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runSandboxManager == nil {
		s.runSandboxManager = newProjectAssistantSandboxManager()
	}
	return s.runSandboxManager
}

func (s *Server) developmentSyncLock(id identity, project *aiv1alpha1.Project) *sync.Mutex {
	key := developmentSyncFailureKey(id, project)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.developmentSyncLocks == nil {
		s.developmentSyncLocks = map[string]*sync.Mutex{}
	}
	lock := s.developmentSyncLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.developmentSyncLocks[key] = lock
	}
	return lock
}

// MetricsHandler is the assistant's Prometheus surface. It is NOT part of the
// tenant-facing server: serve.New has no route class for it, and it never had
// one — it was mounted beside /api/* and reachable by any caller the hub
// proxied. main.go serves it on the internal listener instead.
func (s *Server) MetricsHandler() http.Handler {
	return http.HandlerFunc(projectAssistantMetricsHandler)
}

// ConfigureAttachmentDraftRetention changes the expiry applied to explicitly
// draft uploads. Non-draft receipts remain durable until project deletion.
func (s *Server) ConfigureAttachmentDraftRetention(retention time.Duration) {
	if s == nil || retention <= 0 {
		return
	}
	s.mu.Lock()
	s.attachmentDraftRetention = retention
	s.mu.Unlock()
}

func (s *Server) attachmentRetention() time.Duration {
	if s == nil {
		return store.DefaultAttachmentDraftRetention
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attachmentDraftRetention <= 0 {
		return store.DefaultAttachmentDraftRetention
	}
	return s.attachmentDraftRetention
}

// clientFor builds a workspace-scoped client acting AS THE PROVIDER in the
// caller's workspace cluster, through this provider's export virtual
// workspace. When the request came through the dispatcher it is the very
// client the gate read the addressed object with.
func (s *Server) clientFor(id identity) (*asclient.Client, error) {
	if s.projectClientFor != nil {
		return s.projectClientFor(id)
	}
	if id.provider != nil {
		return asclient.NewFromScope(tenant.NewScopeFromDynamic(id.provider)), nil
	}
	if s.tenant == nil {
		return nil, errors.New("no provider credential configured; cannot act in the tenant workspace")
	}
	scope, err := s.tenant.For(id.clusterID)
	if err != nil {
		return nil, err
	}
	return asclient.NewFromScope(scope), nil
}

// tenantClientFor wraps the caller factory as the tenant client handlers
// build their per-cluster scope from.
func tenantClientFor(callers dataplane.ProviderCallerFactory) *tenant.Client {
	if callers == nil {
		return nil
	}
	return tenant.NewClient(callers)
}

// SetHubToken installs the bearer this provider presents on the hub's own REST
// API and MCP aggregate (see Server.hubToken).
func (s *Server) SetHubToken(token string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hubToken = hubTokenFrom(token)
}

// requireProjectClient resolves the caller identity and a workspace-scoped
// client. Endpoints under /api/projects always require a workspace.
func (s *Server) requireProjectClient(w http.ResponseWriter, r *http.Request) (*asclient.Client, identity, bool) {
	id, ok := s.identityFromRequest(w, r)
	if !ok {
		return nil, identity{}, false
	}
	if id.workspaceUUID == "" {
		if id.workspaceErr != nil {
			log.Printf("app-studio: resolving workspace for cluster %s: %v", id.clusterID, id.workspaceErr)
			writeStatus(w, http.StatusBadGateway, "WorkspaceUnresolved", "could not resolve the workspace behind cluster "+id.clusterID+" from the hub: "+id.workspaceErr.Error())
			return nil, identity{}, false
		}
		writeStatus(w, http.StatusBadRequest, "BadRequest", "a workspace is required for this endpoint — cluster "+id.clusterID+" is an organization workspace; select a workspace first")
		return nil, identity{}, false
	}
	if s.tenant == nil && s.projectClientFor == nil && id.provider == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "tenant client not configured — provider has no kcp credential")
		return nil, identity{}, false
	}
	if id.clusterID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "no workspace cluster on request — the path names no logical cluster")
		return nil, identity{}, false
	}
	if id.user == "" {
		// Everything under /api/projects records or checks an actor somewhere
		// (thread and attachment ownership, approval decisions, the audit
		// trail). Serving a request whose caller could not be identified
		// would write those as if nobody did them.
		log.Printf("app-studio: resolving caller identity on cluster %s: %v", id.clusterID, id.userErr)
		writeStatus(w, http.StatusBadGateway, "ActorUnresolved", ErrActorUnresolved.Error()+" on cluster "+id.clusterID+": "+errorText(id.userErr))
		return nil, identity{}, false
	}
	c, err := s.clientFor(id)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "creating project client: "+err.Error())
		return nil, identity{}, false
	}
	return c, id, true
}

// requireProjectWithClient additionally fetches the named Project.
func (s *Server) requireProjectWithClient(w http.ResponseWriter, r *http.Request) (*asclient.Client, identity, *aiv1alpha1.Project, bool) {
	c, id, ok := s.requireProjectClient(w, r)
	if !ok {
		return nil, identity{}, nil, false
	}
	name := mux.Vars(r)["project"]
	p, err := c.Projects().Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		writeProjectError(w, err)
		return nil, identity{}, nil, false
	}
	s.noteWorkspaceCluster(id)
	return c, id, p, true
}

// requireStore guards against a nil message store.
func (s *Server) requireStore(w http.ResponseWriter) (store.Store, bool) {
	if s.store == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "project message store not configured on this provider")
		return nil, false
	}
	return s.store, true
}
