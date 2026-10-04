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
	"log"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"k8s.io/client-go/rest"

	runnerclient "github.com/railgrid/railgrid/pkg/runner/client"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	backendharness "github.com/railgrid/provider-agents/backend/harness"
	"github.com/railgrid/provider-agents/channels"
	"github.com/railgrid/provider-agents/internal/edgeref"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
)

// runnerDialer builds a dispatcher for one enrolled runner. It is a field on the
// Server so a test can substitute a fake runner without standing up kcp, an
// edges Service proxy and a machine.
type runnerDialer func(ctx context.Context, ref runnerclient.ServiceRef, token string) (backendharness.Dispatcher, error)

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
func (s *Server) dialRunner(_ context.Context, ref runnerclient.ServiceRef, token string) (backendharness.Dispatcher, error) {
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
	return backendharness.Wrap(c), nil
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
}

// persistHarnessSession records the thread the harness actually used. The
// per-run record is useful for inspection, but only this session row is read
// by NextHarnessTurn when dispatching the next chat message.
func (s *Server) persistHarnessSession(ctx context.Context, scope store.Scope, session store.HarnessSession, observed backendharness.Observed, at time.Time) error {
	if observed.SessionID == "" {
		return nil
	}
	session.HarnessSessionID = observed.SessionID
	session.UpdatedAt = at
	persistCtx, cancel := boundedPersistContext(ctx)
	defer cancel()
	if err := s.store.PutHarnessSession(persistCtx, scope, session); err != nil {
		return fmt.Errorf("persisting the harness session for the next turn: %w", err)
	}
	return nil
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
	identity, err := llm.LoadHarnessIdentity(ctx, run.Creds, credName)
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

	// The epoch. Claimed durably and BEFORE the dispatch, because the runner
	// refuses a start whose epoch did not advance past the task's highest — which
	// is the protection that makes two replicas answering one message safe, and
	// only works if the number is not derived from something either replica could
	// read as equal.
	session, err := s.harnessSessionFor(ctx, run.Scope, sessionID, cont)
	if err != nil {
		return harnessTurn{}, fmt.Errorf("claiming this turn's number for session %s: %w", sessionID, err)
	}

	b := backendharness.New(backendharness.Config{
		Dispatcher: dispatcher,
		// session → task, run → attempt, turn number → epoch.
		TaskID:    harnessTaskID(agent.Name, sessionID),
		AttemptID: runID,
		Epoch:     uint64(session.Turns),
		// What an EARLIER turn's receipt reported, which is what makes
		// consecutive turns one conversation.
		SessionID:       session.HarnessSessionID,
		WorkspaceID:     harnessWorkspaceID(agent, sessionID, runID),
		RequiredHarness: advertised,
		Model:           strings.TrimSpace(cfg.Model),
		Credential:      identity,
		Provenance: map[string]any{
			"workspace": run.Scope.WorkspaceUUID,
			"org":       run.Scope.OrgUUID,
			"cluster":   run.ClusterID,
		},
		MaxDurationSeconds: int(agent.Spec.Limits.TimeoutSeconds),
	})
	return harnessTurn{backend: b, Session: session, Service: service, Harness: advertised}, nil
}

// A continuation addresses the attempt already running on the edge. Allocating
// another epoch would make its second approval obsolete and could let an older
// receipt overwrite a newer turn's saved session.
func (s *Server) harnessSessionFor(ctx context.Context, scope store.Scope, sessionID string, cont *continuation) (store.HarnessSession, error) {
	if cont == nil {
		return s.store.NextHarnessTurn(ctx, scope, sessionID, time.Now().UTC())
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
	return store.HarnessSession{
		SessionID: sessionID, HarnessSessionID: state.SessionID,
		Turns: int64(state.Epoch), UpdatedAt: time.Now().UTC(),
	}, nil
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

// harnessWorkspaceID names the runner directory this agent's turns run in.
//
// A "persistent" workspace is per agent and session, which is what makes a
// conversation about a checkout coherent across turns; an "ephemeral" one is per
// run, so nothing carries over.
func harnessWorkspaceID(agent *agentsv1alpha1.Agent, sessionID, runID string) string {
	if cfg := agent.Spec.Harness(); cfg != nil && cfg.Workspace == agentsv1alpha1.HarnessWorkspaceEphemeral {
		return protocolIdentifier("run-" + runID)
	}
	return protocolIdentifier("agent-" + agent.Name + "-" + sessionID)
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
func (s *Server) postHarnessQuestion(ctx context.Context, run taskRun, question string) string {
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
		log.Printf("run %s: filing the harness's question: %v", run.RunID, err)
		return ""
	}
	s.events.publish(wsScope, "inbox", map[string]any{
		"id": id, "state": "pending", "agent": run.Agent.Name, "runID": run.RunID, "kind": "question",
	})
	s.notifyInboxQuestion(ctx, run, question)
	return id
}

// postHarnessApproval files the inbox item a harness PERMISSION prompt parks
// on: an approval, bound to this run and to the exact call the harness asked
// about, so Approve and Deny both mean something.
//
// It is the sibling of postHarnessQuestion and exists for the same reason — the
// backend cannot reach the store — but it files the other kind, because what
// resolves it is a verdict on a named call rather than an answer in words.
func (s *Server) postHarnessApproval(ctx context.Context, run taskRun, tool, args string) string {
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
		log.Printf("run %s: filing the harness's permission request: %v", run.RunID, err)
		return ""
	}
	s.events.publish(wsScope, "inbox", map[string]any{
		"id": id, "state": "pending", "agent": run.Agent.Name, "runID": run.RunID,
		"kind": "approval", "tool": tool,
	})
	s.notifyInboxApproval(ctx, run, tool)
	return id
}

// approvableArgs keeps the stored disclosure a JSON object.
//
// An approval may only be GRANTED when its arguments can be read back as an
// object (approvalDisclosureAvailable), which is what stops a person approving
// something nobody can show them. A harness renders its tool input as JSON and
// the runner bounds it, so a large input arrives truncated and no longer parses
// — and the approval would become deny-only. Wrapping it keeps the disclosure
// honest about what it is: the harness's rendering, verbatim, under one key.
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
