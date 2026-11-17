// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// Catalogued actions: the two halves of an unattended run, published in the
// CatalogEntry under spec.export.resources[agents].actions so a generated
// application can be GRANTED them.
//
//	POST …/agents/{name}/start-run   {"input": {task, idempotencyKey, …}} → {runId, phase, reused}
//	POST …/agents/{name}/get-run     {"input": {runId}}                    → {runId, phase, …, structuredResult}
//
// They exist because an App Studio Project can only be granted a catalogued
// action (docs/provider-actions.md): the `run` verb and the runs/* verbs are
// reachable by anyone holding RBAC on them, but a Project's workload identity
// is minted with exactly the rules its saved integration grants, and a grant
// names a catalogued action with a pinned schema digest. Without these an app
// could not ask an agent to do anything without a long-lived ServiceAccount
// token of its own.
//
// Two actions rather than one because a grant's call has a bounded budget and
// a run does not: a research run outlives any single request. start-run admits
// the run and returns at once; get-run reads where it got to. Both are bound
// to the AGENT, so one grant (on agents/{name}) covers both halves, and get-run
// refuses a run that belongs to a different agent — a grant to read one
// agent's runs is not a grant to read the workspace's.
//
// start-run is the `run` verb with a narrower input: it goes through the same
// invokeRun, so idempotency, the agent-identity fence and admission are one
// code path, and the run acts as the agent's own identity exactly as any
// unattended run does. The caller's grant authorizes STARTING the run; it does
// not lend the agent the caller's reach, nor the agent the caller's.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
)

const (
	// ActionStartRun and ActionGetRun are the catalogued action names on
	// agents/{name}. Their contract version is actionContractVersion.
	ActionStartRun = "start-run"
	ActionGetRun   = "get-run"

	actionContractVersion = "v1"

	// maxActionTaskChars bounds start-run's task, in characters, matching the
	// declared schema's maxLength (which JSON Schema counts in characters).
	// A task is a prompt, and 64 Ki characters is far past any a caller should
	// be composing by hand.
	maxActionTaskChars = 64 << 10
	// maxActionFinalResponseBytes bounds the final answer get-run returns
	// inline, before encoding. A longer answer is truncated and says so; the
	// whole transcript stays readable through the runs/{id}/trace verb.
	maxActionFinalResponseBytes = 256 << 10
	// actionEnvelopeHeadroom is what get-run leaves, under its declared output
	// limit, for the envelope around its result: request ID, action name and
	// the resource reference. They are a few hundred bytes; this is generous.
	actionEnvelopeHeadroom = 4 << 10
)

// actionLimits are the limits each action's CatalogEntry declaration states.
// An action missing here is not served, so declaring one and serving it are
// one edit apart; TestAgentActionsMatchManifest holds the two together.
//
// start-run's input limit is the catalog's ceiling (1 MiB) because it has to
// hold the largest task the schema allows: 64 Ki characters, each up to 12
// bytes once a client \u-escapes a supplementary-plane rune, plus a 16 KiB
// resultSchema escaped the same way.
var actionLimits = map[string]dataplane.Limits{
	ActionStartRun: {Timeout: 60 * time.Second, MaxInputBytes: 1 << 20, MaxOutputBytes: 4 << 10, MaxResultItems: 1},
	ActionGetRun:   {Timeout: 15 * time.Second, MaxInputBytes: 1 << 10, MaxOutputBytes: 512 << 10, MaxResultItems: 1},
}

// startRunActionInput is start-run's input. Unknown fields are refused, which is
// what keeps the action narrower than the verb: no session continuation, no
// repository attempt, no callback URL, no model override.
type startRunActionInput struct {
	Task             string           `json:"task"`
	IdempotencyKey   string           `json:"idempotencyKey"`
	ExpectedAgentUID string           `json:"expectedAgentUID,omitempty"`
	ResultSchema     json.RawMessage  `json:"resultSchema,omitempty"`
	RunLimits        *invokeRunLimits `json:"runLimits,omitempty"`
}

type startRunActionOutput struct {
	RunID  string `json:"runId"`
	Phase  string `json:"phase"`
	Reused bool   `json:"reused"`
}

type getRunActionInput struct {
	RunID string `json:"runId"`
}

type getRunActionOutput struct {
	RunID      string     `json:"runId"`
	Phase      string     `json:"phase"`
	Message    string     `json:"message,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// FinalResponse is the agent's final answer, bounded; Truncated says the
	// bound cut it.
	FinalResponse    string                  `json:"finalResponse,omitempty"`
	Truncated        bool                    `json:"truncated,omitempty"`
	StructuredResult *actionStructuredResult `json:"structuredResult,omitempty"`
	Usage            actionRunUsage          `json:"usage"`
}

// actionStructuredResult is the run's structured-result verdict and, when it
// is valid, the document itself — the reason a caller asked for a schema.
type actionStructuredResult struct {
	State string          `json:"state"`
	Value json.RawMessage `json:"value,omitempty"`
}

type actionRunUsage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	USDMicros    int64 `json:"usdMicros"`
}

// actionStatus maps each typed failure to its HTTP status. Every code is one
// this file produces; callers branch on code and retryable, never the status.
var actionStatus = map[string]int{
	"invalid_action_input": http.StatusBadRequest,
	"agent_not_found":      http.StatusNotFound,
	"agent_not_ready":      http.StatusFailedDependency,
	"run_not_found":        http.StatusNotFound,
	"invocation_conflict":  http.StatusConflict,
	"action_unavailable":   http.StatusServiceUnavailable,
	"backend_failure":      http.StatusBadGateway,
}

// Actions is the class-(a′) handler serve.New dispatches every catalogued
// action to. Like DataPlane it is reached only through serve's adapter, which
// parsed the shard-forwarded path, checked the coordinate against the
// manifest and restored the contract version onto the route.
func (s *Server) Actions() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := dataplane.RouteFrom(r.Context())
		if !ok {
			dataplane.WriteError(w, dataplane.ErrBadPath)
			return
		}
		req := route.Request
		gvr := agentsclient.AgentGVR
		limits, served := actionLimits[req.Verb]
		if route.Group != gvr.Group || route.APIVersion != gvr.Version || req.Resource != gvr.Resource ||
			req.Component != "" || req.Tail != "" || req.Version != actionContractVersion || !served {
			http.NotFound(w, r)
			return
		}
		envelope := actionwire.New(r, "agents", req.Verb, actionwire.ResourceRef{
			APIVersion: gvr.GroupVersion().String(), Kind: "Agent", Resource: gvr.Resource, Name: req.Name,
		})

		// The gate: the caller can see this agent (kcp already authorized
		// agents/{action} itself). What comes back is the provider's read of it
		// and a client acting as the provider — the only client there is.
		object, provider, err := dataplane.Gate(r.Context(), s.callers, gvr, req)
		if err != nil {
			log.Printf("agents: action %s on agents/%s refused: %v", req.Verb, req.Name, err)
			status := dataplane.StatusFor(err)
			code, message, retryable := actionGateFailure(status)
			envelope.Failure(w, status, code, message, retryable)
			return
		}
		// The identity always carries a workspace: resolveClusterScope falls
		// back to the cluster-keyed scope rather than leaving it empty.
		gate := &gateInfo{request: req, object: object, provider: provider, identity: s.dataPlaneIdentity(r.Context(), req)}
		r = r.WithContext(withGate(r.Context(), gate))
		c, id := gate.providerClient(), gate.identity

		dataplane.Serve(w, r, envelope, limits, func(ctx context.Context, raw json.RawMessage) (any, *actionwire.Error) {
			switch req.Verb {
			case ActionStartRun:
				return s.startRunAction(r.WithContext(ctx), c, id, req.Name, raw)
			default:
				return s.getRunAction(ctx, id, req.Name, string(object.GetUID()), raw)
			}
		}, dataplane.WithErrorStatus(func(e *actionwire.Error) int {
			if status, ok := actionStatus[e.Code]; ok {
				return status
			}
			return http.StatusBadGateway
		}))
	})
}

func (s *Server) startRunAction(r *http.Request, c *agentsclient.Client, id identity, agent string, raw json.RawMessage) (any, *actionwire.Error) {
	var in startRunActionInput
	if err := decodeActionInput(raw, &in); err != nil {
		return nil, invalidInput(err.Error())
	}
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if in.IdempotencyKey == "" {
		// Keyed, not optional: a caller that retries a timed-out start must
		// get the run it already started back, not a second run.
		return nil, invalidInput("idempotencyKey is required")
	}
	// Counted in characters, as the declared schema's maxLength is: a task the
	// catalog calls valid is not refused here. The declared input limit is
	// sized so a task of that many characters always fits in it.
	if utf8.RuneCountInString(in.Task) > maxActionTaskChars {
		return nil, invalidInput("task exceeds 65536 characters")
	}
	_, resp, err := s.invokeRun(r, c, id, agent, invokeRunRequest{
		Task: in.Task, IdempotencyKey: in.IdempotencyKey, ExpectedAgentUID: in.ExpectedAgentUID,
		ResultSchema: in.ResultSchema, Limits: in.RunLimits,
		// start-run is an automation grant: the run spends the agent's
		// configured models as a Schedule or Trigger run does
		// (docs/agents-model-provider-connections.md, "Access"). Recorded on
		// the run, so every turn sees it, whichever request resumes it.
		modelUseDelegated: true,
	})
	if err != nil {
		return nil, actionErrorFor(err)
	}
	return startRunActionOutput{RunID: resp.RunID, Phase: resp.Phase, Reused: resp.Reused}, nil
}

func (s *Server) getRunAction(ctx context.Context, id identity, agent, agentUID string, raw json.RawMessage) (any, *actionwire.Error) {
	var in getRunActionInput
	if err := decodeActionInput(raw, &in); err != nil {
		return nil, invalidInput(err.Error())
	}
	in.RunID = strings.TrimSpace(in.RunID)
	if in.RunID == "" || len(in.RunID) > 128 {
		return nil, invalidInput("runId is required and at most 128 bytes")
	}
	scope := id.scope(agent)
	run, err := s.store.GetRun(ctx, scope, in.RunID)
	if errors.Is(err, store.ErrRunNotFound) || (err == nil && !getRunMayRead(run, agent, agentUID)) {
		return nil, &actionwire.Error{Code: "run_not_found", Message: "this agent has no run " + in.RunID}
	}
	if err != nil {
		log.Printf("agents: get-run %s on agents/%s: %v", in.RunID, agent, err)
		return nil, &actionwire.Error{Code: "action_unavailable", Message: "the run could not be read; retry later", Retryable: true}
	}
	out := getRunActionOutput{
		RunID: run.ID, Phase: string(run.Phase), Message: run.Message,
		CreatedAt: run.CreatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Usage: actionRunUsage{InputTokens: run.InputTokens, OutputTokens: run.OutputTokens, USDMicros: run.USDMicros},
	}
	if run.StructuredResult != nil {
		result := &actionStructuredResult{State: run.StructuredResult.State}
		if run.StructuredResult.State == "valid" {
			artifact, found, err := s.store.GetRunArtifact(ctx, scope, run.ID, StructuredResultArtifactName)
			if err != nil || !found {
				// A "valid" verdict without its document is not a result: an
				// app would take the absent value for the answer. Say it is
				// unavailable instead, so the caller retries rather than
				// acting on nothing.
				log.Printf("agents: get-run %s on agents/%s: reading the structured result: found=%v err=%v", run.ID, agent, found, err)
				return nil, &actionwire.Error{Code: "action_unavailable", Message: "the structured result could not be read; retry later", Retryable: true}
			}
			result.Value = json.RawMessage(artifact.Data)
		}
		out.StructuredResult = result
	}
	budget := int(actionLimits[ActionGetRun].MaxOutputBytes) - actionEnvelopeHeadroom
	if !fitFinalResponse(&out, run.Output, budget) {
		log.Printf("agents: get-run %s on agents/%s: the result does not fit %d bytes even without the answer", run.ID, agent, budget)
		return nil, &actionwire.Error{Code: "backend_failure", Message: "the run's result is too large to return"}
	}
	return out, nil
}

// getRunMayRead says whether a grant on agents/{agent} reaches this run. The
// store is keyed by tenant, not by agent, so get-run checks it here, and any
// run it refuses is as absent to the caller as one that does not exist:
//
//   - it belongs to the addressed agent, by name AND by incarnation: a grant on
//     a recreated "scout" does not read the runs of the "scout" deleted before
//     it, whose UID differs;
//   - it is an API run — one started by start-run or the run verb. A chat
//     turn, a schedule, a trigger or a channel message is someone else's
//     conversation with the agent, and a grant to start runs and read what
//     they returned is not a grant to read those.
func getRunMayRead(run store.Run, agent, agentUID string) bool {
	if run.AgentName != agent || run.Trigger != agentsv1alpha1.RunTriggerAPI {
		return false
	}
	return agentUID == "" || run.AgentUID == agentUID
}

// fitFinalResponse sets out's final answer to the longest prefix of output
// that keeps the ENCODED result within budget bytes, and reports false only
// when even an empty answer does not fit.
//
// The limit dataplane.Serve enforces is on the JSON-encoded envelope, and
// encoding is not the identity: a quote or newline doubles, and '<', '>', '&'
// and control characters become six-byte escapes. A raw-byte cap alone lets
// an escape-heavy answer of a finished run exceed the limit, and every read of
// that run would then fail — so the answer is cut against its encoded size.
func fitFinalResponse(out *getRunActionOutput, output string, budget int) bool {
	full := runePrefix(output, maxActionFinalResponseBytes)
	fits := func(answer string) bool {
		out.FinalResponse = answer
		out.Truncated = len(answer) < len(output)
		data, err := json.Marshal(out)
		return err == nil && len(data) <= budget
	}
	if fits(full) {
		return true
	}
	if !fits("") {
		return false
	}
	// The longest prefix that fits: encoded size grows with the prefix, so a
	// binary search over the cut finds it in a couple of dozen encodes.
	lo, hi := 0, len(full)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(runePrefix(full, mid)) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return fits(runePrefix(full, lo))
}

// decodeActionInput decodes an action's input strictly: one JSON object, no
// unknown fields, nothing after it. The hub compiled the declared schema, but
// the provider authored it and is the one that enforces it.
func decodeActionInput(raw json.RawMessage, into any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return errors.New("input does not match the action's schema: " + err.Error())
	}
	if d.More() {
		return errors.New("input has trailing content")
	}
	return nil
}

func invalidInput(msg string) *actionwire.Error {
	return &actionwire.Error{Code: "invalid_action_input", Message: msg}
}

// actionErrorFor maps an invokeRun refusal onto a typed action failure. The
// message is the provider's own (invokeRun writes nothing tenant-sensitive
// into them), so it is safe to relay.
func actionErrorFor(err error) *actionwire.Error {
	code, _ := errorClass(err)
	switch code {
	case http.StatusBadRequest:
		return invalidInput(err.Error())
	case http.StatusNotFound:
		if _, ok := errors.AsType[agentMissingError](err); ok {
			return &actionwire.Error{Code: "agent_not_found", Message: "the agent does not exist"}
		}
		// The agent exists; something it is configured with does not (its
		// model credential). Fixing that is the agent owner's job, so this is
		// not retryable — but it is not the agent missing either.
		return &actionwire.Error{Code: "agent_not_ready", Message: "the agent cannot run: " + err.Error()}
	case http.StatusConflict:
		return &actionwire.Error{Code: "invocation_conflict", Message: err.Error()}
	case http.StatusServiceUnavailable:
		return &actionwire.Error{Code: "action_unavailable", Message: err.Error(), Retryable: true}
	default:
		return &actionwire.Error{Code: "backend_failure", Message: "the run could not be started", Retryable: true}
	}
}

// actionGateFailure names a gate refusal by the status it maps to, so the
// envelope says as little as the status does. A denial is reported as the
// agent being absent — the gate does not disclose whether it exists — but a
// gate that could not ask (a SubjectAccessReview or export read that failed)
// is retryable: a brief outage must not read as a deleted agent.
func actionGateFailure(status int) (code, message string, retryable bool) {
	switch {
	case status == http.StatusUnauthorized:
		return "action_unauthenticated", "no caller identity on this request", false
	case status == http.StatusBadRequest:
		return "invalid_action_input", "the request does not address an agent", false
	case status >= http.StatusInternalServerError:
		return "action_unavailable", "the agent could not be checked; retry later", true
	default:
		return "agent_not_found", "the agent is not available to this caller", false
	}
}
