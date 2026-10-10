// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Dispatching a harness-backed turn: resolving, per turn, the four things the
// runner protocol needs that the agent's spec only points at.
//
//	the RUNNER      spec.backend.harness.edgeRef + the credential's harness
//	                selector name the discovered Service <edge>-<selector>, which
//	                is also the runner identity the client must find answering.
//	the IDENTITY    spec.backend.harness.credentialRef's Secret, read per turn
//	                and never held: it is dispatch data, and the fewer places it
//	                sits the better.
//	the SESSION     the harness session earlier turns created, off the store,
//	                because a conversation that spans turns is one harness
//	                session and the provider is what remembers which.
//	the EPOCH       the next turn number for that session, claimed durably so two
//	                replicas answering the same message cannot dispatch the same
//	                one.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/runner"
	runnerclient "github.com/railgrid/railgrid/pkg/runner/client"
	"github.com/railgrid/railgrid/pkg/runner/dispatch"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/backend"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/channels"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/internal/connsecret"
	"github.com/railgrid/provider-agents/internal/edgeref"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

// runnerDialer builds a dispatcher for one enrolled runner. It is a field on the
// Server so a test can substitute a fake runner without standing up kcp, an
// edges Service proxy and a machine.
type runnerDialer func(ctx context.Context, ref runnerclient.ServiceRef, token string) (dispatch.Runner, error)

// dialRunner is the real dialer: the shared typed client for runner/v1, over the
// edges provider's published Service proxy, with this provider's own credential.
//
// The coordinate is the tenant's kcp front door plus /clusters/{id}, and that is
// the shared client's requirement rather than a choice made here: client.New
// refuses a config whose host is spelled any other way, and then uses only its
// scheme and host, rendering every path itself from the one renderer of the
// data-plane grammar. So what this has to supply is a config addressing that
// front door with a credential of its own — the provider's, the identity the
// edges.railgrid.ai claims in manifest.yaml are declared under.
func (s *Server) dialRunner(_ context.Context, ref runnerclient.ServiceRef, token string) (dispatch.Runner, error) {
	cfg, err := s.runnerRESTConfig(ref.Cluster, token)
	if err != nil {
		return nil, err
	}
	// Propagate the operator's own decision about this hub rather than making a
	// second one. When RAILGRID_HUB_INSECURE put this provider on an unverified
	// connection, every other call it makes to the same hub — including the ones
	// carrying its own identity — already goes that way, so refusing only the
	// runner hop would make the feature unusable on that stack without making
	// the harness credential any safer. A provider configured with the hub's CA
	// never reaches this branch.
	var opts []runnerclient.Option
	if cfg.Insecure {
		opts = append(opts, runnerclient.AllowInsecureTLS())
	}
	c, err := runnerclient.New(cfg, ref, opts...)
	if err != nil {
		return nil, err
	}
	return runnerWithArtifacts{Runner: dispatch.Wrap(c), client: c}, nil
}

// runnerWithArtifacts is the shared client as the lifecycle drives it, plus
// the one call the lifecycle does not make: reading an artifact, which only a
// repository attempt ever has. The harness backend asks for the reader by
// type (backendharness.ArtifactReader); a test's fake runner that lacks it
// simply cannot complete a repository attempt.
type runnerWithArtifacts struct {
	dispatch.Runner
	client *runnerclient.Client
}

func (r runnerWithArtifacts) Artifact(ctx context.Context, attemptID, artifactID string, w io.Writer) (runner.Artifact, error) {
	return r.client.Artifact(ctx, attemptID, artifactID, w)
}

// runnerRESTConfig is the provider's own credential aimed at one tenant
// workspace's front door.
func (s *Server) runnerRESTConfig(clusterID, token string) (*rest.Config, error) {
	if s.providerBase == nil {
		return nil, errors.New("harness-backed agents need the provider's own kubeconfig (RAILGRID_PROVIDER_KUBECONFIG); without it this provider cannot reach an edge runner")
	}
	// The TOKEN is the agent's hub-minted identity, not this provider's
	// bootstrap credential. The bootstrap credential authenticates the provider
	// to its own workspace and has no rights in a tenant's, so using it here
	// produced a 403 naming this provider's ServiceAccount. What carries rights
	// is the identity minted per agent from the requirements manifest.yaml
	// declares, name-scoped to the one runner Service (see api/agentidentity.go).
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("no identity was supplied for this runner call")
	}
	origin, err := frontDoor(s.providerBase.Host)
	if err != nil {
		return nil, err
	}
	// Only the coordinate and the credential are taken from the provider's own
	// config; the identity replaces its bearer outright rather than layering on
	// it, so nothing of the bootstrap credential reaches the tenant.
	cfg := rest.AnonymousClientConfig(s.providerBase)
	cfg.Host = origin + "/clusters/" + clusterID
	cfg.BearerToken = token
	return cfg, nil
}

// frontDoor strips a /clusters/{id} suffix off a kube host, leaving the front
// door a per-workspace path can be attached to. Mirrors
// provider-sdk/dataplane.stripClusterSuffix, which is unexported.
func frontDoor(host string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(host))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("the provider kubeconfig host %q is not an absolute URL", host)
	}
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// harnessIdentity mints (or refreshes) the agent's hub-minted identity for one
// workspace, which is what carries the rights to reach its runner.
//
// It fails loudly rather than returning "" the way the instance-tool path does.
// There the empty token is a degradation with a per-tool message; here it is the
// whole turn, and a run that silently could not authenticate would surface three
// hops away as somebody else's 403.
func (s *Server) harnessIdentity(ctx context.Context, clusterID string, agent *agentsv1alpha1.Agent) (string, error) {
	if s.bg == nil || s.bg.identities == nil {
		return "", errors.New("this provider cannot mint a workspace identity, so it cannot reach an edge runner; it needs its own kubeconfig (RAILGRID_PROVIDER_KUBECONFIG) and a discovered APIExport virtual workspace")
	}
	dyn, err := s.bg.scoped(ctx, clusterID)
	if err != nil {
		return "", fmt.Errorf("reaching workspace %s to mint this agent's identity: %w", clusterID, err)
	}
	token := s.bg.identities.token(ctx, dyn, clusterID, agent)
	if strings.TrimSpace(token) == "" {
		return "", errors.New("the hub did not mint an identity for this agent, so it cannot reach its runner; check that the agents provider is enabled in this workspace with its edges.railgrid.ai requirement accepted")
	}
	return token, nil
}

// harnessTurn is everything one harness-backed turn needs, resolved.
type harnessTurn struct {
	backend *backendharness.Backend
	// Session is the harness session row as it stood after this turn's epoch was
	// claimed, so the caller can persist what the receipt then reports against
	// the same row.
	Session store.HarnessSession
	// Service and Harness name what the turn was dispatched to, for the run
	// record and for a log line that says where the work went.
	Service string
	Harness string
	// Repository marks a repository attempt, whose Session is synthetic (the
	// run is its own task) and must never be written to the session store.
	Repository bool
}

// persistHarnessSession records the thread the harness actually used. The
// per-run record is useful for inspection, but only this session row is read
// by NextHarnessTurn when dispatching the next chat message.
func (s *Server) persistHarnessSession(ctx context.Context, scope store.Scope, session store.HarnessSession, observed backendharness.Observed, at time.Time) error {
	if observed.SessionID == "" {
		return nil
	}
	session.HarnessSessionID = observed.SessionID
	// Turns is the epoch of this receipt. Do not carry forward the row's
	// ObservedEpoch, which may belong to a previous attempt.
	session.ObservedEpoch = session.Turns
	session.UpdatedAt = at
	persistCtx, cancel := boundedPersistContext(ctx)
	defer cancel()
	if err := s.store.PutHarnessSession(persistCtx, scope, session); err != nil {
		return fmt.Errorf("persisting the harness session for the next turn: %w", err)
	}
	return nil
}

// persistHarnessTurn must not abandon a live permission gate when saving its
// session fails. The lifecycle will mark the run failed without filing an inbox
// item, so first stop the attempt that otherwise waits indefinitely for it.
func (s *Server) persistHarnessTurn(ctx context.Context, scope store.Scope, h *harnessTurn, run *backend.Run, out backend.Outcome, at time.Time) error {
	err := s.persistHarnessSession(ctx, scope, h.Session, h.backend.Observed(), at)
	if err == nil || out.Parked == nil {
		return err
	}
	if stopErr := stopHarnessTurn(ctx, h, run); stopErr != nil {
		err = errors.Join(err, fmt.Errorf("stopping the harness after the session could not be saved: %w", stopErr))
	}
	return err
}

// stopHarnessTurn asks the remote attempt to stop with a bounded context that
// survives request cancellation. It is used when a parked turn cannot be made
// durable, and by the run lifecycle's cancellation path.
func stopHarnessTurn(ctx context.Context, h *harnessTurn, run *backend.Run) error {
	if h == nil || h.backend == nil {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backendCancelTimeout)
	defer cancel()
	return h.backend.Cancel(stopCtx, run)
}

// stopParkedHarness closes the remote half of a run that was waiting for a
// person when this provider received cancel. A parked turn is no longer in
// liveRuns, so its backend.Cancel callback cannot be reached through the local
// registry. The checkpoint carries the runner coordinates; the current Agent
// config is used only to address the same runner and mint its scoped identity.
// This path deliberately does not load the harness credential: cancellation
// needs the runner identity, not the model provider's long-lived auth secret.
func (s *Server) stopParkedHarness(ctx context.Context, c *agentsclient.Client, id identity, run store.Run) error {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backendCancelTimeout)
	defer cancel()
	return s.cancelParkedHarness(stopCtx, c, id, run)
}

func (s *Server) cancelParkedHarness(ctx context.Context, c *agentsclient.Client, id identity, run store.Run) error {
	if run.Backend != agentsv1alpha1.AgentBackendHarness || len(run.Checkpoint) == 0 || c == nil || s.runners == nil {
		return nil
	}
	var checkpoint runCheckpoint
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		return fmt.Errorf("reading the harness cancellation checkpoint: %w", err)
	}
	if checkpoint.Backend != agentsv1alpha1.AgentBackendHarness || len(checkpoint.Harness) == 0 {
		return errors.New("the harness cancellation checkpoint is incomplete")
	}
	var state backendharness.State
	if err := json.Unmarshal(checkpoint.Harness, &state); err != nil {
		return fmt.Errorf("reading the harness attempt identity: %w", err)
	}
	if state.TaskID == "" || state.AttemptID == "" || state.Epoch == 0 {
		return errors.New("the harness cancellation checkpoint has no attempt coordinates")
	}
	if state.AttemptID != run.ID || (run.AttemptID != "" && state.AttemptID != run.AttemptID) {
		return errors.New("the harness cancellation checkpoint does not match this run")
	}
	sessionID := effectiveSessionID(run.SessionID, run.Trigger)

	agent, err := c.Agents().Get(ctx, run.AgentName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if run.Repository != nil {
		// A repository attempt is its own task (see repositoryHarnessTurn):
		// there is no session row to check it against, only the run.
		if state.TaskID != run.ID || state.Epoch != 1 {
			return errors.New("the harness cancellation checkpoint does not match this repository run")
		}
		if state.AgentUID != "" && state.AgentUID != string(agent.UID) {
			return errors.New("the harness checkpoint belongs to a different Agent incarnation")
		}
	} else {
		if err := s.verifyParkedHarnessOwner(ctx, id, run, agent, sessionID, checkpoint, state); err != nil {
			return err
		}
	}

	ref, grantAgent, ok := parkedHarnessRunner(id.clusterID, agent, checkpoint.HarnessRunner, state.BackendKey)
	if checkpoint.HarnessRunner != nil && !ok {
		// A saved target is authoritative. If it is malformed or no longer
		// matches the checkpoint, do not redirect a cancellation through the
		// Agent's current spec; leave the run retryable for the reconciler.
		return errors.New("the saved harness cancellation target does not match the checkpoint")
	}
	if checkpoint.HarnessRunner == nil {
		// Older checkpoints do not carry a runner target. They can still be
		// stopped while the Agent and its harness credential reference remain
		// intact; new checkpoints do not depend on either remaining unchanged.
		cfg := agent.Spec.Harness()
		if cfg == nil {
			return nil
		}
		cred, getErr := c.GetModelCredential(ctx, cfg.CredentialRef)
		if getErr != nil {
			return getErr
		}
		selector := llm.HarnessSelector(cred.Spec.Provider)
		if selector == "" {
			return nil
		}
		advertised := llm.HarnessAdvertisedName(cred.Spec.Provider)
		if state.BackendKey != "" && state.BackendKey != harnessBackendKey(id.clusterID, cfg.EdgeRef.Kind, cfg.EdgeRef.Name, advertised) {
			return nil
		}
		service := edgeref.RunnerServiceName(cfg.EdgeRef.Name, selector)
		ref = runnerclient.ServiceRef{Cluster: id.clusterID, Service: service, EdgeKind: cfg.EdgeRef.Kind, EdgeName: cfg.EdgeRef.Name, RunnerID: service}
		grantAgent = agent
	}
	token, err := s.harnessIdentity(ctx, id.clusterID, grantAgent)
	if err != nil {
		return err
	}
	dispatcher, err := s.runners(ctx, ref, token)
	if err != nil {
		return err
	}
	_, err = dispatch.Cancel(ctx, dispatcher, runner.CancelRequest{
		RequestID: uuid.NewString(), TaskID: state.TaskID,
		AttemptID: state.AttemptID, AttemptEpoch: state.Epoch,
	})
	return err
}

// verifyParkedHarnessOwner checks a parked conversational attempt's checkpoint
// against the Agent and the durable session row that own it.
func (s *Server) verifyParkedHarnessOwner(ctx context.Context, id identity, run store.Run, agent *agentsv1alpha1.Agent, sessionID string, checkpoint runCheckpoint, state backendharness.State) error {
	taskID, _, err := harnessTaskIdentity(agent, sessionID, &continuation{Checkpoint: checkpoint})
	if err != nil {
		return fmt.Errorf("validating the harness checkpoint owner: %w", err)
	}
	if taskID != state.TaskID {
		return errors.New("the harness cancellation task does not match its Agent owner")
	}
	scope := store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID, AgentName: run.AgentName}
	stored, found, err := s.store.GetHarnessSession(ctx, scope, sessionID)
	if err != nil {
		return fmt.Errorf("checking the harness session owner: %w", err)
	}
	if state.AgentUID == "" && !found {
		return errors.New("the legacy harness checkpoint has no durable session identity")
	}
	if found && stored.TaskID != "" && stored.TaskID != taskID {
		return errors.New("the harness checkpoint does not match the durable session task identity")
	}
	currentUID := string(agent.UID)
	if found && stored.AgentUID != "" && stored.AgentUID != currentUID {
		return errors.New("the harness checkpoint belongs to a different Agent incarnation")
	}
	if state.AgentUID == "" && found && stored.AgentUID == "" && currentUID != "" {
		stale, lifetimeErr := store.CheckLegacyHarnessSessionLifetime(agent.CreationTimestamp.Time, stored.UpdatedAt)
		if lifetimeErr != nil {
			return fmt.Errorf("the legacy harness checkpoint has no verifiable Agent lifetime: %w", lifetimeErr)
		}
		if stale {
			return errors.New("the legacy harness checkpoint predates this Agent incarnation")
		}
	}
	return nil
}

// parkedHarnessRunner validates and reconstructs the narrow runner grant saved
// with a new parked checkpoint. The persisted coordinates contain no URL or
// token; the owner UID comes from the current Agent object, while the grant is
// rebuilt for the one edge that held the attempt even if its spec has changed.
func parkedHarnessRunner(clusterID string, agent *agentsv1alpha1.Agent, target *harnessCancelTarget, backendKey string) (runnerclient.ServiceRef, *agentsv1alpha1.Agent, bool) {
	if target == nil || agent == nil || target.ClusterID == "" || target.ClusterID != clusterID || target.EdgeKind == "" || target.EdgeName == "" || target.Service == "" || target.RunnerID != target.Service {
		return runnerclient.ServiceRef{}, nil, false
	}
	claude := edgeref.RunnerServiceName(target.EdgeName, llm.HarnessSelector(agentsv1alpha1.ModelProviderClaudeCode))
	codex := edgeref.RunnerServiceName(target.EdgeName, llm.HarnessSelector(agentsv1alpha1.ModelProviderCodex))
	var advertised string
	switch target.Service {
	case claude:
		advertised = llm.HarnessAdvertisedName(agentsv1alpha1.ModelProviderClaudeCode)
	case codex:
		advertised = llm.HarnessAdvertisedName(agentsv1alpha1.ModelProviderCodex)
	default:
		return runnerclient.ServiceRef{}, nil, false
	}
	if backendKey != "" && backendKey != harnessBackendKey(clusterID, target.EdgeKind, target.EdgeName, advertised) {
		return runnerclient.ServiceRef{}, nil, false
	}
	grantAgent := agent.DeepCopy()
	grantAgent.Spec.Backend = agentsv1alpha1.AgentBackendSpec{
		Type: agentsv1alpha1.AgentBackendHarness,
		Harness: &agentsv1alpha1.AgentHarnessBackend{
			EdgeRef: agentsv1alpha1.AgentHarnessEdgeRef{Kind: target.EdgeKind, Name: target.EdgeName},
		},
	}
	return runnerclient.ServiceRef{
		Cluster: clusterID, Service: target.Service,
		EdgeKind: target.EdgeKind, EdgeName: target.EdgeName, RunnerID: target.RunnerID,
	}, grantAgent, true
}

// unbilledHarnessUsage includes consumption recovered from an in-flight
// checkpoint. Only usage persisted on the run at an earlier park has already
// been charged; the backend's saved cursor and Spent are observation boundaries,
// not billing boundaries.
func unbilledHarnessUsage(total, billed backend.Cost) backend.Cost {
	return backend.Cost{
		Tokens: backend.Tokens{
			InputTokens:  max(0, total.InputTokens-billed.InputTokens),
			OutputTokens: max(0, total.OutputTokens-billed.OutputTokens),
		},
		CostMicros: max(0, total.CostMicros-billed.CostMicros),
	}
}

// harnessBackendFor resolves a harness-backed agent's turn.
//
// Everything it reads it reads with the run's own access (run.Creds), which is
// the same identity that reads a model credential — a harness credential is a
// ModelCredential, and an unattended run must be able to reach it without a
// caller to borrow.
func (s *Server) harnessBackendFor(ctx context.Context, run taskRun, sessionID, runID string, cont *continuation) (harnessTurn, error) {
	agent := run.Agent
	cfg := agent.Spec.Harness()
	if cfg == nil {
		return harnessTurn{}, errors.New("this agent is not harness-backed")
	}
	if s.runners == nil {
		return harnessTurn{}, errors.New("harness-backed agents are unavailable: this provider has no way to reach an edge runner")
	}
	if strings.TrimSpace(run.ClusterID) == "" {
		return harnessTurn{}, errors.New("this run has no workspace context, so the edge its harness runs on cannot be addressed")
	}

	credName := strings.TrimSpace(cfg.CredentialRef)
	cred, err := run.Creds.GetModelCredential(ctx, credName)
	if err != nil {
		return harnessTurn{}, fmt.Errorf("reading the harness credential %q: %w", credName, err)
	}
	selector := llm.HarnessSelector(cred.Spec.Provider)
	advertised := llm.HarnessAdvertisedName(cred.Spec.Provider)
	if selector == "" {
		return harnessTurn{}, fmt.Errorf("model credential %q has provider %q, which is not a harness identity; a harness-backed agent needs %q or %q",
			credName, cred.Spec.Provider, agentsv1alpha1.ModelProviderClaudeCode, agentsv1alpha1.ModelProviderCodex)
	}
	backendKey := harnessBackendKey(run.ClusterID, cfg.EdgeRef.Kind, cfg.EdgeRef.Name, advertised)
	identity, err := llm.LoadHarnessIdentity(ctx, run.Creds, credName)
	if err != nil {
		return harnessTurn{}, err
	}
	environment, err := s.harnessEnvironment(ctx, run, cfg)
	if err != nil {
		return harnessTurn{}, err
	}

	// The identity this dispatch is made as. NOT the caller's: a data-plane verb
	// carries no caller bearer (see identity.token), so the portal's chat — the
	// most ordinary way to use a harness-backed agent — would have none to
	// borrow. It is the agent's own hub-minted identity, name-scoped to this
	// machine's runner Services, which is also a tighter grant than a caller's
	// would be.
	token, err := s.harnessIdentity(ctx, run.ClusterID, agent)
	if err != nil {
		return harnessTurn{}, err
	}

	service := edgeref.RunnerServiceName(cfg.EdgeRef.Name, selector)
	dispatcher, err := s.runners(ctx, runnerclient.ServiceRef{
		Cluster:  run.ClusterID,
		Service:  service,
		EdgeKind: cfg.EdgeRef.Kind,
		EdgeName: cfg.EdgeRef.Name,
		// The runner identity and the Service name are the same string by
		// construction on both sides (see internal/edgeref.RunnerServiceName).
		RunnerID: service,
	}, token)
	if err != nil {
		return harnessTurn{}, fmt.Errorf("reaching the runner on edge %s/%s: %w", cfg.EdgeRef.Kind, cfg.EdgeRef.Name, err)
	}

	if run.Repository != nil {
		return s.repositoryHarnessTurn(run, runID, sessionID, cont, repositoryTurnDeps{
			dispatcher: dispatcher, backendKey: backendKey, service: service, advertised: advertised,
			model: strings.TrimSpace(cfg.Model), identity: identity, environment: environment,
		})
	}

	// The epoch. Claimed durably and BEFORE the dispatch, because the runner
	// refuses a start whose epoch did not advance past the task's highest — which
	// is the protection that makes two replicas answering one message safe, and
	// only works if the number is not derived from something either replica could
	// read as equal.
	session, err := s.harnessSessionFor(ctx, agent, run.Scope, sessionID, backendKey, cont)
	if err != nil {
		return harnessTurn{}, fmt.Errorf("claiming this turn's number for session %s: %w", sessionID, err)
	}
	agentUID, err := harnessAgentUIDForTask(agent, sessionID, session.TaskID)
	if err != nil {
		return harnessTurn{}, err
	}
	taskID := session.TaskID

	b := backendharness.New(backendharness.Config{
		Runner: dispatcher,
		// session → task, run → attempt, turn number → epoch.
		TaskID:    taskID,
		AgentUID:  agentUID,
		AttemptID: runID,
		Epoch:     uint64(session.Turns),
		// What an EARLIER turn's receipt reported, which is what makes
		// consecutive turns one conversation.
		SessionID:       session.HarnessSessionID,
		BackendKey:      backendKey,
		WorkspaceID:     harnessWorkspaceIDForTask(agent, runID, taskID),
		RequiredHarness: advertised,
		Model:           strings.TrimSpace(cfg.Model),
		Credential:      identity,
		Environment:     environment,
		Provenance: map[string]any{
			"workspace": run.Scope.WorkspaceUUID,
			"org":       run.Scope.OrgUUID,
			"cluster":   run.ClusterID,
		},
		MaxDurationSeconds: int(agent.Spec.Limits.TimeoutSeconds),
	})
	return harnessTurn{backend: b, Session: session, Service: service, Harness: advertised}, nil
}

// repositoryTurnDeps is what harnessBackendFor resolved before the two
// attempt shapes part ways: the runner, its identity, and the harness.
type repositoryTurnDeps struct {
	dispatcher  dispatch.Runner
	backendKey  string
	service     string
	advertised  string
	model       string
	identity    llm.HarnessIdentity
	environment []runner.EnvironmentVariable
}

// harnessEnvironment is what the harness child runs with beyond its model
// credential: today the token of the GitHub Connection the agent names, as
// GH_TOKEN and GITHUB_TOKEN, so `gh pr review` and git over HTTPS authenticate
// as that connection for the length of the turn.
//
// It is read per turn with the run's own access, exactly as the harness
// credential is, and handed to the runner as dispatch data: it is never
// written to the machine, and a turn whose connection is gone fails here with
// the reason rather than running as nobody and reporting "not logged in" from
// inside the harness.
func (s *Server) harnessEnvironment(ctx context.Context, run taskRun, cfg *agentsv1alpha1.AgentHarnessBackend) ([]runner.EnvironmentVariable, error) {
	connName := strings.TrimSpace(cfg.GitHubConnectionRef)
	if connName == "" {
		return nil, nil
	}
	if run.CR == nil {
		return nil, fmt.Errorf("spec.backend.harness.githubConnectionRef names %q, but this run cannot read connections", connName)
	}
	conn, err := run.CR.GetConnection(ctx, connName)
	if err != nil {
		return nil, fmt.Errorf("reading the GitHub connection %q named by spec.backend.harness.githubConnectionRef: %w", connName, err)
	}
	if conn.Spec.Type != agentsv1alpha1.ConnectionTypeGitHub {
		return nil, fmt.Errorf("spec.backend.harness.githubConnectionRef names connection %q of type %q; it must be a github connection", connName, conn.Spec.Type)
	}
	secretName := strings.TrimSpace(conn.Spec.SecretRef)
	if secretName == "" {
		secretName = connsecret.Name(connName)
	}
	secret, err := run.Creds.GetSecret(ctx, llm.SecretNamespace, secretName)
	if err != nil {
		return nil, fmt.Errorf("reading the token of GitHub connection %q (Secret %s): %w", connName, secretName, err)
	}
	token := strings.TrimSpace(string(secret.Data["token"]))
	if token == "" {
		return nil, fmt.Errorf("GitHub connection %q holds no token (Secret %s has no \"token\" key); add one or connect it with OAuth", connName, secretName)
	}
	// Both names: gh reads GH_TOKEN first and falls back to GITHUB_TOKEN, and
	// git credential helpers and most CI-shaped scripts read the second.
	return []runner.EnvironmentVariable{
		{Name: "GH_TOKEN", Value: token},
		{Name: "GITHUB_TOKEN", Value: token},
	}, nil
}

// repositoryHarnessTurn resolves a REPOSITORY attempt: the run is its own task
// (taskID = attemptID = run id, epoch 1), it names no workspace and chains onto
// no harness session, and it claims nothing from the session store — a fresh
// checkout has no conversation to number. A continuation must address that
// same attempt; its coordinates come off the checkpoint and are checked
// against the run rather than against an Agent session.
func (s *Server) repositoryHarnessTurn(run taskRun, runID, sessionID string, cont *continuation, deps repositoryTurnDeps) (harnessTurn, error) {
	agent := run.Agent
	if cont != nil {
		raw, err := cont.Checkpoint.backendState()
		if err != nil {
			return harnessTurn{}, err
		}
		var state backendharness.State
		if err := json.Unmarshal(raw, &state); err != nil {
			return harnessTurn{}, fmt.Errorf("reading the repository attempt's resume state: %w", err)
		}
		if state.TaskID != runID || state.AttemptID != runID || state.Epoch != 1 {
			return harnessTurn{}, errors.New("the harness checkpoint does not address this repository run")
		}
		if state.AgentUID != "" && state.AgentUID != string(agent.UID) {
			return harnessTurn{}, errors.New("the harness checkpoint belongs to a different Agent incarnation")
		}
	}
	maxDuration := run.Repository.MaxDurationSeconds
	if maxDuration <= 0 {
		maxDuration = int(agent.Spec.Limits.TimeoutSeconds)
	}
	maxDuration = min(maxDuration, maxRepositoryDurationSecs)
	// Only the reader a real runner connection has; a fake without one cannot
	// finish a repository attempt, which is the honest answer.
	artifacts, _ := deps.dispatcher.(backendharness.ArtifactReader)
	b := backendharness.New(backendharness.Config{
		Runner:    deps.dispatcher,
		TaskID:    runID,
		AgentUID:  string(agent.UID),
		AttemptID: runID,
		Epoch:     1,
		// No WorkspaceID, no SessionID: the two things a repository attempt
		// never has.
		BackendKey:      deps.backendKey,
		RequiredHarness: deps.advertised,
		Model:           deps.model,
		Credential:      deps.identity,
		Environment:     deps.environment,
		Provenance: map[string]any{
			"workspace": run.Scope.WorkspaceUUID,
			"org":       run.Scope.OrgUUID,
			"cluster":   run.ClusterID,
		},
		MaxDurationSeconds: maxDuration,
		Repository:         &run.Repository.Repository,
		Artifacts:          artifacts,
	})
	// A synthetic session row: what the checkpoint and the run record read
	// their coordinates from. It is never written to the store.
	session := store.HarnessSession{
		SessionID: sessionID, TaskID: runID, AgentUID: string(agent.UID),
		BackendKey: deps.backendKey, Turns: 1, UpdatedAt: time.Now().UTC(),
	}
	return harnessTurn{backend: b, Session: session, Service: deps.service, Harness: deps.advertised, Repository: true}, nil
}

// A continuation addresses the attempt already running on the edge. Allocating
// another epoch would make its second approval obsolete and could let an older
// receipt overwrite a newer turn's saved session.
func (s *Server) harnessSessionFor(ctx context.Context, agent *agentsv1alpha1.Agent, scope store.Scope, sessionID, backendKey string, cont *continuation) (store.HarnessSession, error) {
	if cont == nil {
		initialTaskID, _, err := harnessTaskIdentity(agent, sessionID, nil)
		if err != nil {
			return store.HarnessSession{}, err
		}
		legacyTaskID := harnessTaskID(agent.Name, sessionID)
		identity := store.HarnessIdentity{
			TaskID: initialTaskID, LegacyTaskID: legacyTaskID,
			AgentUID: string(agent.UID), CreatedAt: agent.CreationTimestamp.Time,
		}
		session, err := s.store.NextHarnessTurn(ctx, scope, sessionID, time.Now().UTC(), identity)
		if err != nil {
			return store.HarnessSession{}, err
		}
		if _, err := harnessAgentUIDForTask(agent, sessionID, session.TaskID); err != nil {
			return store.HarnessSession{}, err
		}
		// Legacy rows and sessions observed on a different runner remain in the
		// store for history, but this dispatch must start a fresh native session.
		// Keep the durable pair intact until a receipt from this backend replaces
		// it, so a failed dispatch does not destroy the last working continuity.
		if session.BackendKey != backendKey {
			session.HarnessSessionID = ""
		}
		session.BackendKey = backendKey
		return session, nil
	}
	raw, err := cont.Checkpoint.backendState()
	if err != nil {
		return store.HarnessSession{}, err
	}
	var state backendharness.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return store.HarnessSession{}, fmt.Errorf("reading the harness attempt epoch: %w", err)
	}
	if state.Epoch == 0 || state.Epoch > math.MaxInt64 {
		return store.HarnessSession{}, errors.New("the harness checkpoint carries an invalid attempt epoch")
	}
	taskID, _, err := harnessTaskIdentity(agent, sessionID, cont)
	if err != nil {
		return store.HarnessSession{}, err
	}
	stored, found, err := s.store.GetHarnessSession(ctx, scope, sessionID)
	if err != nil {
		return store.HarnessSession{}, err
	}
	if state.AgentUID == "" && !found {
		return store.HarnessSession{}, errors.New("the legacy harness checkpoint has no durable session identity")
	}
	if found && stored.TaskID != "" && stored.TaskID != taskID {
		return store.HarnessSession{}, errors.New("the harness checkpoint does not match this session's durable task identity")
	}
	currentUID := string(agent.UID)
	if found && stored.AgentUID != "" && stored.AgentUID != currentUID {
		return store.HarnessSession{}, errors.New("the harness checkpoint belongs to a different Agent incarnation")
	}
	if found && stored.AgentUID == "" && currentUID != "" {
		stale, lifetimeErr := store.CheckLegacyHarnessSessionLifetime(agent.CreationTimestamp.Time, stored.UpdatedAt)
		if lifetimeErr != nil {
			return store.HarnessSession{}, fmt.Errorf("the legacy harness checkpoint has no verifiable Agent lifetime: %w", lifetimeErr)
		}
		if stale {
			return store.HarnessSession{}, errors.New("the legacy harness checkpoint predates this Agent incarnation")
		}
	}
	session := store.HarnessSession{
		SessionID: sessionID, TaskID: taskID, AgentUID: currentUID, HarnessSessionID: state.SessionID, BackendKey: backendKey,
		Turns: int64(state.Epoch), ObservedEpoch: int64(state.Epoch), UpdatedAt: time.Now().UTC(),
	}
	// A parked checkpoint can predate the durable task marker. Record the
	// verified identity before resuming so a later turn keeps the same workspace.
	if !found || stored.TaskID == "" || stored.AgentUID == "" {
		if err := s.store.PutHarnessSession(ctx, scope, session); err != nil {
			return store.HarnessSession{}, err
		}
	}
	return session, nil
}

// harnessBackendKey binds a native session to the runner that reported it.
// Length-prefixing each component keeps the encoding unambiguous even if a
// future coordinate admits punctuation used by another component.
func harnessBackendKey(clusterID, edgeKind, edgeName, advertisedHarness string) string {
	var identity strings.Builder
	for _, part := range []string{clusterID, edgeKind, edgeName, advertisedHarness} {
		identity.WriteString(strconv.Itoa(len(part)))
		identity.WriteByte(':')
		identity.WriteString(part)
	}
	sum := sha256.Sum256([]byte(identity.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// harnessTaskID is the conversation's identity on the runner.
//
// It is scoped by agent as well as session because a session id is the
// provider's and a task id is the runner's: two agents on one machine must not
// share a task, or one's turn would be refused as the other's stale epoch.
// Bounded and sanitized to the protocol's identifier grammar, because the runner
// refuses anything else and a session id can be a trigger name.
func harnessTaskID(agent, sessionID string) string {
	return protocolIdentifier("agent-" + agent + "-" + sessionID)
}

// harnessTaskIdentity selects the runner task ID and the incarnation encoded
// in its checkpoint. Fresh tasks include the Agent UID, so deleting and
// recreating an Agent with the same name/session cannot inherit its runner
// epoch. Parked checkpoints keep their original task ID across upgrades: old
// checkpoints are accepted only when they match the exact pre-UID name/session
// format, while UID-bearing checkpoints must match the current Agent object.
func harnessTaskIdentity(agent *agentsv1alpha1.Agent, sessionID string, cont *continuation) (taskID, agentUID string, err error) {
	if agent == nil {
		return "", "", errors.New("a harness turn needs an Agent")
	}
	currentUID := string(agent.UID)
	if cont == nil {
		return harnessTaskIDFor(agent.Name, currentUID, sessionID), currentUID, nil
	}
	raw, err := cont.Checkpoint.backendState()
	if err != nil {
		return "", "", fmt.Errorf("reading the harness task identity: %w", err)
	}
	var state backendharness.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return "", "", fmt.Errorf("reading the harness task identity: %w", err)
	}
	if state.TaskID == "" {
		return "", "", errors.New("the harness checkpoint carries no task identity")
	}
	if state.AgentUID != "" {
		if currentUID == "" || state.AgentUID != currentUID {
			return "", "", errors.New("the harness checkpoint belongs to a different Agent incarnation")
		}
		uidTaskID := harnessTaskIDFor(agent.Name, state.AgentUID, sessionID)
		legacyTaskID := harnessTaskID(agent.Name, sessionID)
		if state.TaskID != uidTaskID && state.TaskID != legacyTaskID {
			return "", "", errors.New("the harness checkpoint task identity does not match its Agent incarnation")
		}
		return state.TaskID, state.AgentUID, nil
	}
	legacyTaskID := harnessTaskID(agent.Name, sessionID)
	if state.TaskID != legacyTaskID {
		return "", "", errors.New("the legacy harness checkpoint task identity does not match this Agent session")
	}
	return state.TaskID, "", nil
}

func harnessAgentUIDForTask(agent *agentsv1alpha1.Agent, sessionID, taskID string) (string, error) {
	if agent == nil {
		return "", errors.New("a harness turn needs an Agent")
	}
	uid := string(agent.UID)
	if taskID == harnessTaskID(agent.Name, sessionID) || (uid != "" && taskID == harnessTaskIDFor(agent.Name, uid, sessionID)) {
		return uid, nil
	}
	return "", errors.New("the harness session task identity does not match this Agent incarnation")
}

func harnessTaskIDFor(agentName, agentUID, sessionID string) string {
	if agentUID == "" {
		return harnessTaskID(agentName, sessionID)
	}
	raw := "agent-" + agentName + "-" + agentUID + "-" + sessionID
	return protocolIdentifierWithDigest(raw)
}

func harnessWorkspaceIDForTask(agent *agentsv1alpha1.Agent, runID, taskID string) string {
	if cfg := agent.Spec.Harness(); cfg != nil && cfg.Workspace == agentsv1alpha1.HarnessWorkspaceEphemeral {
		return protocolIdentifier("run-" + runID)
	}
	return taskID
}

// protocolIdentifierMax is the runner's identifier length bound (its pattern
// allows a leading character plus 127 more).
const protocolIdentifierMax = 128

// protocolIdentifier renders a string as a runner/v1 identifier:
// ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$.
//
// The mapping is deliberately lossy in one direction only — every character
// outside the grammar becomes "-" — and the result is bounded by hashing the
// tail rather than truncating it, so two long session ids that share a prefix do
// not collapse onto one runner workspace.
func protocolIdentifier(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "x"
	}
	// A leading character outside [A-Za-z0-9] is not allowed.
	if c := out[0]; (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
		out = "x" + out
	}
	if len(out) > protocolIdentifierMax {
		out = out[:protocolIdentifierMax-9] + "-" + shortDigest(raw)
	}
	return out
}

// protocolIdentifierWithDigest keeps new UID-scoped identities distinct even
// when two raw session IDs sanitize to the same runner-safe spelling (for
// example, "a/b" and "a-b").
func protocolIdentifierWithDigest(raw string) string {
	base := protocolIdentifier(raw)
	sum := sha256.Sum256([]byte(raw))
	suffix := "-" + hex.EncodeToString(sum[:8])
	if len(base)+len(suffix) > protocolIdentifierMax {
		base = base[:protocolIdentifierMax-len(suffix)]
	}
	return base + suffix
}

// shortDigest is 8 hex characters of sha256, enough to keep two long names that
// share a prefix apart without pretending to be a security boundary.
func shortDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:4])
}

// postHarnessQuestion files the inbox item a harness's own question parks on,
// and pushes it to the agent's channel the way an approval request is pushed.
//
// It is the provider's side of a Parked outcome that carries a Question rather
// than a gated call: the backend cannot reach the store, and a run parked on
// something nobody can see is a run nobody can finish. The item is a QUESTION,
// not an approval — what resolves it is an answer, and answering it is what
// resumes the run (see resolveInboxItem).
func (s *Server) postHarnessQuestion(ctx context.Context, run taskRun, question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		question = "the harness is waiting for input"
	}
	now := time.Now().UTC()
	wsScope := store.Scope{OrgUUID: run.Scope.OrgUUID, WorkspaceUUID: run.Scope.WorkspaceUUID}
	id := uuid.NewString()
	if err := s.store.AddInboxItem(ctx, wsScope, store.InboxItem{
		ID: id, AgentName: run.Agent.Name, RunID: run.RunID, Kind: store.InboxKindQuestion,
		State:  store.InboxStatePending,
		Prompt: safeTruncate(question, maxHarnessQuestion),
		Payload: map[string]any{
			"harness": true, "trigger": run.Trigger,
		},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return "", fmt.Errorf("filing the harness's question: %w", err)
	}
	s.events.publish(wsScope, "inbox", map[string]any{
		"id": id, "state": "pending", "agent": run.Agent.Name, "runID": run.RunID, "kind": "question",
	})
	s.notifyInboxQuestion(ctx, run, question)
	return id, nil
}

// postHarnessApproval files the inbox item a harness PERMISSION prompt parks
// on: an approval, bound to this run and to the exact call the harness asked
// about, so Approve and Deny both mean something.
//
// It is the sibling of postHarnessQuestion and exists for the same reason — the
// backend cannot reach the store — but it files the other kind, because what
// resolves it is a verdict on a named call rather than an answer in words.
func (s *Server) postHarnessApproval(ctx context.Context, run taskRun, tool, args string) (string, error) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "a tool"
	}
	now := time.Now().UTC()
	wsScope := store.Scope{OrgUUID: run.Scope.OrgUUID, WorkspaceUUID: run.Scope.WorkspaceUUID}
	id := uuid.NewString()
	if err := s.store.AddInboxItem(ctx, wsScope, store.InboxItem{
		ID: id, AgentName: run.Agent.Name, RunID: run.RunID, Kind: store.InboxKindApproval,
		State:  store.InboxStatePending,
		Prompt: "Allow " + run.Agent.Name + " to run " + tool + "?",
		Payload: map[string]any{
			"tool": tool, "args": redactArgs(approvableArgs(args)), "trigger": run.Trigger,
			"harness": true,
		},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return "", fmt.Errorf("filing the harness's permission request: %w", err)
	}
	s.events.publish(wsScope, "inbox", map[string]any{
		"id": id, "state": "pending", "agent": run.Agent.Name, "runID": run.RunID,
		"kind": "approval", "tool": tool,
	})
	s.notifyInboxApproval(ctx, run, tool)
	return id, nil
}

// approvableArgs keeps the stored disclosure a JSON object.
//
// An approval may only be GRANTED when its arguments can be read back as an
// object (approvalDisclosureAvailable), which stops a person approving
// something nobody can show them. Older/plain-text inputs are wrapped under an
// explicit input key so the disclosure remains a JSON object; oversized Codex
// permission inputs are rejected before the runner can truncate them.
func approvableArgs(args string) string {
	args = strings.TrimSpace(args)
	var object map[string]json.RawMessage
	if args != "" && json.Unmarshal([]byte(args), &object) == nil && object != nil {
		return args
	}
	wrapped, err := json.Marshal(map[string]string{"input": args})
	if err != nil {
		return `{"input":""}`
	}
	return string(wrapped)
}

// notifyInboxApproval pushes a harness permission request to the agent's
// primary channel. Best-effort, exactly like the question notification.
func (s *Server) notifyInboxApproval(ctx context.Context, run taskRun, tool string) {
	if run.CR == nil {
		return
	}
	connName, ok := run.Agent.Spec.ResolveChannelConnection("")
	if !ok {
		return
	}
	conn, err := run.CR.GetConnection(ctx, connName)
	if err != nil {
		return
	}
	token := ""
	if sec, serr := run.Creds.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(connName)); serr == nil {
		if v, present := sec.Data["token"]; present {
			token = string(v)
		}
	}
	_ = channels.Send(ctx, channels.Message{
		Type: conn.Spec.Type, Token: token, Target: conn.Spec.Channel, Config: conn.Spec.Config,
		Text: "🔐 " + run.Agent.Name + " is asking permission to use " + safeTruncate(tool, 200) +
			".\nReply /inbox to review — the run continues in place once you decide.",
	})
}

// maxHarnessQuestion bounds the stored prompt. A harness question is a sentence;
// a wall of text is a malfunction, and the inbox is not where to store it.
const maxHarnessQuestion = 2000

// notifyInboxQuestion pushes the question to the agent's primary channel, so it
// reaches the user where they live rather than only in the portal inbox.
// Best-effort: an undeliverable notification must not fail the park.
func (s *Server) notifyInboxQuestion(ctx context.Context, run taskRun, question string) {
	if run.CR == nil {
		return
	}
	connName, ok := run.Agent.Spec.ResolveChannelConnection("")
	if !ok {
		return
	}
	conn, err := run.CR.GetConnection(ctx, connName)
	if err != nil {
		return
	}
	token := ""
	if sec, serr := run.Creds.GetSecret(ctx, llm.SecretNamespace, connectionSecretName(connName)); serr == nil {
		if v, present := sec.Data["token"]; present {
			token = string(v)
		}
	}
	_ = channels.Send(ctx, channels.Message{
		Type: conn.Spec.Type, Token: token, Target: conn.Spec.Channel, Config: conn.Spec.Config,
		Text: "❓ " + run.Agent.Name + " needs an answer to continue: " + safeTruncate(question, 3000) +
			"\nReply /inbox to review — the run resumes automatically once you answer.",
	})
}
