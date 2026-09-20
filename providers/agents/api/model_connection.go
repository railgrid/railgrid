// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/llm"
)

// The two probe verbs hang off a ModelCredential:
//
//	modelcredentials/{name}/test      POST  one real chat round-trip
//	modelcredentials/{name}/discover  POST  GET {baseURL}/models
//
// They used to hang off an AGENT, with a third shape that probed an unsaved
// draft carrying a raw apiKey in the request body. Both were consequences of
// a credential having no object of its own: the grammar addresses an object,
// so the probe borrowed the agent that would use the credential — and a
// workspace with no agent yet could not probe at all, which is exactly the
// moment a person is setting their first model up. The draft shape existed to
// paper over the same gap and put a key on the wire to do it.
//
// A ModelCredential is an object, so neither is needed. The credential is
// saved first and probed as a saved object: `create` on
// modelcredentials/{verb} is a grant about the thing being probed, the key
// never leaves the workspace, and the answer is the same one the reconciler
// writes to status.

// gatedModelCredential returns the ModelCredential the data-plane gates read
// for this request, plus a caller-scoped client.
//
// The object comes off the gate rather than being re-read: gate 1 already GET
// it as the caller, and re-reading would be a second read whose result the
// caller's grant did not cover.
func (s *Server) gatedModelCredential(w http.ResponseWriter, r *http.Request) (*agentsv1alpha1.ModelCredential, *agentsclient.Client, bool) {
	c, _, ok := s.requireClient(w, r)
	if !ok {
		return nil, nil, false
	}
	gate, ok := gateFrom(r.Context())
	if !ok || gate.object == nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "model credential not resolved")
		return nil, nil, false
	}
	var cred agentsv1alpha1.ModelCredential
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(gate.object.Object, &cred); err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "model credential could not be read")
		return nil, nil, false
	}
	return &cred, c, true
}

// resolveGatedProfile turns the gated credential into the profile to probe,
// reading its Secret AS THE CALLER. A Secret the caller cannot read is a
// failed probe with recovery copy, not a 500: it is the ordinary
// half-finished state (the object saved, the Secret not).
func (s *Server) resolveGatedProfile(w http.ResponseWriter, r *http.Request) (*agentsv1alpha1.ModelCredential, *agentsclient.Client, llm.Profile, bool) {
	cred, c, ok := s.gatedModelCredential(w, r)
	if !ok {
		return nil, nil, llm.Profile{}, false
	}
	profile, err := llm.ProfileFor(r.Context(), c, cred)
	if err != nil {
		writeJSON(w, http.StatusOK, credentialTestResult{
			OK:    false,
			Error: "This credential's API key could not be read. Check that Secret " + cred.Spec.SecretRef.Name + " exists in namespace default and holds the key.",
		})
		return nil, nil, llm.Profile{}, false
	}
	return cred, c, profile, true
}

// credentialTestRequest is the `test` verb's optional body.
//
// Model overrides the model for THIS probe only. It exists because the model a
// person just picked in the editor was previously only exercised after "Save
// changes" — by the first agent run, i.e. in a chat window, hours later, as a
// 404 from the provider. Everything else about the probe (endpoint, key) still
// comes from the saved object and its Secret, so an override cannot aim the
// saved credential's key at a different endpoint.
type credentialTestRequest struct {
	Model string `json:"model,omitempty"`
}

const (
	// maxTestRequestBody bounds the `test` body. It carries one model id and
	// nothing else; anything larger is not a request this verb understands.
	maxTestRequestBody = 4 << 10

	// maxTestModelID matches ModelCredentialSpec.Model's MaxLength, because an
	// override is a candidate for that field.
	maxTestModelID = 253
)

// testModelCredential serves the `test` verb: a real chat round-trip against
// the model, so "verified" means the model answered and not merely that the
// endpoint resolved. Discovery is deliberately a separate verb and cannot mark
// a credential verified.
//
// The body is optional: {"model": "<id>"} probes that id instead of the saved
// spec.model, which is what lets the editor verify a pick BEFORE it is saved.
func (s *Server) testModelCredential(w http.ResponseWriter, r *http.Request) {
	// Parsed before the gate's Secret is read: a body this verb cannot honour
	// is a 400, and a 400 should not have touched the tenant's key first.
	override, ok := testModelOverride(w, r)
	if !ok {
		return
	}
	_, _, profile, ok := s.resolveGatedProfile(w, r)
	if !ok {
		return
	}
	if override != "" {
		profile.Model = override
	}
	writeJSON(w, http.StatusOK, verifyCredentialModel(r.Context(), profile))
}

// testModelOverride reads and validates the `test` body's optional model id.
// An absent or empty body means "probe the saved model", which is what every
// caller that predates the override sends.
func testModelOverride(w http.ResponseWriter, r *http.Request) (string, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTestRequestBody+1))
	if err != nil {
		rejectTestRequest(w, "The request body could not be read.")
		return "", false
	}
	if len(body) > maxTestRequestBody {
		rejectTestRequest(w, "The request body is too large; this verb takes at most a model id.")
		return "", false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "", true
	}
	var req credentialTestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		rejectTestRequest(w, "The request body is not valid JSON; send {\"model\": \"<id>\"} or no body at all.")
		return "", false
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return "", true
	}
	if len(model) > maxTestModelID {
		rejectTestRequest(w, "That model id is too long; a model id is at most 253 characters.")
		return "", false
	}
	// The same curation discovery applies, so the verb cannot be used to
	// "verify" a model the picker would never have offered — which is exactly
	// how gpt-5.3-codex was saved in the first place.
	if !llm.ChatCapable(model) {
		rejectTestRequest(w, "Model "+model+" is not usable for chat: this provider runs agents on the Chat Completions API and that model is not served there. Pick another model for this credential.")
		return "", false
	}
	return model, true
}

// rejectedTest is the body a refused `test` request gets.
//
// It is two shapes at once, deliberately. `ok`/`error` is the verb's ordinary
// result shape, so anything reading the probe's answer — curl, the MCP tool,
// a script — sees a failed probe with the reason in the field it already
// reads. `reason`/`message` is the Status shape the portal's generic error
// path (api.ts fail()) reads, so the browser shows the sentence instead of
// "Bad Request". Writing one and not the other loses the message in one of
// the two callers.
type rejectedTest struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func rejectTestRequest(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusBadRequest, rejectedTest{
		Error:   message,
		Reason:  "BadRequest",
		Message: message,
		Code:    http.StatusBadRequest,
	})
}

// discoverModelCredential serves the `discover` verb: GET {baseURL}/models
// with the key, so the editor can offer the model ids the endpoint actually
// serves. It also refreshes status.models, so the answer a person just saw and
// the answer the object reports are the same one.
func (s *Server) discoverModelCredential(w http.ResponseWriter, r *http.Request) {
	cred, c, profile, ok := s.resolveGatedProfile(w, r)
	if !ok {
		return
	}
	models, latency, err := llm.DiscoverModels(r.Context(), profile.BaseURL, profile.APIKey)
	result := credentialTestResult{OK: err == nil, Models: models, LatencyMS: latency.Milliseconds()}
	if err != nil {
		result.Error = "Could not find models. Check the endpoint and credential, or enter a model ID manually."
	}
	refreshDiscoveredModels(r.Context(), c, cred, models, err)
	writeJSON(w, http.StatusOK, result)
}

// refreshDiscoveredModels writes what the probe just learned onto the object,
// best effort.
//
// Best effort on purpose: the write runs as the CALLER, and a caller who may
// probe a credential does not necessarily hold `update` on its status
// subresource. The reconciler is the authority on this status and re-derives
// all of it on its own cadence; this only shortens the window in which the
// object disagrees with what the person is looking at.
func refreshDiscoveredModels(ctx context.Context, c *agentsclient.Client, cred *agentsv1alpha1.ModelCredential, models []string, probeErr error) {
	if len(models) > llm.MaxStatusModels {
		models = models[:llm.MaxStatusModels]
	}
	status := agentsv1alpha1.ModelCredentialStatus{
		ObservedGeneration: cred.Generation,
		LastProbeTime:      &metav1.Time{Time: time.Now().UTC()},
	}
	if probeErr != nil {
		status.LastProbeError = boundedProbeError(probeErr)
	} else {
		status.Models = models
	}
	// A short context of its own: the caller's request may be finishing, and
	// an abandoned status write is not worth failing the verb over.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = c.ModelCredentials().PatchStatus(writeCtx, cred.Name, status)
}

// maxProbeErrorLength bounds status.lastProbeError, matching the kind's
// MaxLength. An upstream body can be megabytes and can echo request headers,
// so it is truncated here and never stored whole.
const maxProbeErrorLength = 512

// boundedProbeError renders a probe failure for status.lastProbeError.
func boundedProbeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > maxProbeErrorLength {
		msg = msg[:maxProbeErrorLength]
	}
	return msg
}

func verifyCredentialModel(ctx context.Context, profile llm.Profile) credentialTestResult {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	model, err := llm.BuildModel(ctx, profile)
	if err == nil {
		var response *schema.Message
		response, err = model.Generate(ctx, []*schema.Message{schema.UserMessage("Reply with OK to confirm this model connection.")})
		if err == nil && response == nil {
			err = errors.New("provider returned no response")
		}
	}
	result := credentialTestResult{OK: err == nil, LatencyMS: time.Since(start).Milliseconds()}
	// Upstream errors can echo request headers. Return recovery copy, never keys.
	if err != nil {
		result.Error = "The model did not respond successfully. Check the endpoint, model ID, and credential permissions, then retry."
	}
	if ctx.Err() != nil {
		result.Error = "Connection test timed out. Check the endpoint and retry."
		result.OK = false
	}
	return result
}
