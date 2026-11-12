// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-sdk/dataplane"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

// CodeScope binds one invocation to the admitted command. It is carried in the
// existing immutable Run input so recovery does not infer authority from a
// conversation, model response, mutable Agent, or tool arguments.
type CodeScope struct {
	ReviewOnly     bool   `json:"reviewOnly,omitempty"`
	Mode           string `json:"mode,omitempty"`
	Proof          string `json:"proof"`
	ScopeProof     string `json:"scopeProof,omitempty"`
	EventRef       string `json:"eventRef,omitempty"`
	EventUID       string `json:"eventUID,omitempty"`
	SourceUID      string `json:"sourceUID,omitempty"`
	SourceDigest   string `json:"sourceDigest,omitempty"`
	GrantRef       string `json:"grantRef,omitempty"`
	Repository     string `json:"repository,omitempty"`
	RepositoryUID  string `json:"repositoryUID,omitempty"`
	ConnectionUID  string `json:"connectionUID,omitempty"`
	PullRequest    string `json:"pullRequest"`
	PullRequestUID string `json:"pullRequestUID"`
}

const CodeScopePrefix = "railgrid-code-scope:"

// PublicRunTask removes private invocation credentials from a task before it
// is sent to a model or rendered outside execution storage.
func PublicRunTask(input string) string {
	for {
		line, rest, _ := strings.Cut(input, "\n")
		if !strings.HasPrefix(strings.TrimSpace(line), CodeScopePrefix) {
			return input
		}
		input = rest
	}
}

func ParseCodeScope(input string) *CodeScope {
	line, _, _ := strings.Cut(input, "\n")
	if !strings.HasPrefix(line, CodeScopePrefix) {
		return nil
	}
	var scope CodeScope
	if json.Unmarshal([]byte(strings.TrimPrefix(line, CodeScopePrefix)), &scope) != nil || scope.PullRequest == "" || scope.PullRequestUID == "" {
		return nil
	}
	if scope.Mode == "task" {
		if scope.SourceUID == "" || scope.SourceDigest == "" || scope.GrantRef == "" || scope.Repository == "" || scope.RepositoryUID == "" || scope.ConnectionUID == "" || scope.Proof == "" || scope.ScopeProof == "" {
			return nil
		}
	} else if scope.EventUID == "" || scope.Proof == "" {
		return nil
	}
	return &scope
}

// Code exposes Code-owned forge operations. No GitHub credential, PR lifecycle
// state, CI timer, retry policy or publication lease is owned by Agents.
func Code(d Deps, writable bool) []engine.Tool {
	out := codeTools(d, writable, false)
	if d.CodeScope == nil || !d.CodeScope.ReviewOnly {
		return out
	}
	original := out[0].Exec
	properties := out[0].JSONSchema["properties"].(map[string]any)
	out[0].JSONSchema = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"input"}, "properties": map[string]any{"input": properties["input"]}}
	out[0].Desc = "Observe the admitted PR or publish actionable COMMENT findings through Code. Supply input.op=observe or review. The PR identity and assigned review head are already bound by the runtime; do not supply repository coordinates. For review provide input.key, a concise input.body, input.findingsCount, and input.comments containing {path,line,side,body} for each actionable finding in the PR diff. Code validates locations and publishes them together as a formal inline review. A zero count creates no review post. No branch writes or status comments are permitted."
	out[0].Exec = func(ctx context.Context, args string) (string, error) {
		var in struct {
			PullRequest string         `json:"pullRequest"`
			Input       map[string]any `json:"input"`
		}
		if err := json.Unmarshal([]byte(args), &in); err != nil {
			return "", err
		}
		if in.PullRequest != "" && in.PullRequest != d.CodeScope.PullRequest {
			return "", fmt.Errorf("Code target is outside this invocation's admitted scope")
		}
		if nested, found := in.Input["pullRequest"]; found {
			if nested != d.CodeScope.PullRequest {
				return "", fmt.Errorf("Code target is outside this invocation's admitted scope")
			}
			delete(in.Input, "pullRequest")
		}
		if in.Input["op"] == "review" {
			if head, found := in.Input["expectedHead"]; !found || head == "" {
				in.Input["expectedHead"] = d.CodeReviewHead
			}
		}
		raw, _ := json.Marshal(map[string]any{"pullRequest": d.CodeScope.PullRequest, "input": in.Input})
		return original(ctx, string(raw))
	}
	return out
}
func codeTools(d Deps, writable, delegate bool) []engine.Tool {
	if d.CodeScope != nil && d.CodeScope.ReviewOnly {
		writable = false
	}
	ops := []string{"observe", "review"}
	if writable {
		ops = append(ops, "publish")
	}

	return []engine.Tool{{Name: "code_pull_request", Desc: "Call Code's bounded forge pullRequest. Operations: observe (read PR head, CI and mergeability), review (formal COMMENT review on the exact current expectedHead; provide comments [{path,line,side,body}] for actionable inline findings and matching findingsCount; zero suppresses the post), publish (canonical git snapshot under an atomic expected-head lease). Review and publish both require a stable key and concise body. Publish actionable findings while their reviewed head is still current: changing the head makes a review of the old head ineligible. Report clean results in the final summary; Factory updates one PR status comment. Copy the pullRequest name from the approved task; Agents inserts its pinned PR UID, and any input.pullRequestUID you provide must match. For publish, copy only the delegate result's nested snapshot object into input.snapshot. Code enforces mode, current maintainer permissions, deadline, branch restrictions. Never pass credentials. No merge or approval operation exists.",
		JSONSchema: map[string]any{"type": "object", "required": []string{"pullRequest", "input"}, "additionalProperties": false, "properties": map[string]any{
			"pullRequest": map[string]any{"type": "string", "maxLength": 253},
			"input": map[string]any{"type": "object", "required": []string{"op"}, "additionalProperties": false, "properties": map[string]any{
				"op":             map[string]any{"type": "string", "enum": ops},
				"pullRequestUID": map[string]any{"type": "string", "description": "Optional consistency check. Agents inserts the UID pinned in the approved invocation scope."},
				"expectedHead":   map[string]any{"type": "string", "pattern": "^[0-9a-f]{40}$", "description": "Required for review and publish: the exact current PR head observed through Code."},
				"key":            map[string]any{"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,100}$", "description": "Required for review and publish: stable operation idempotency key. Reuse it when retrying the same operation on the same head."},
				"body":           map[string]any{"type": "string", "maxLength": 16384, "description": "Required for review and publish: concise findings or fix summary."},
				"snapshot": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"baseCommit", "commit", "tree", "bundle", "message"}, "description": "Required for publish: copy the delegate's nested snapshot unchanged, including its baseCommit. Do not copy the outer delegate result wrapper or add report, runId or noChanges fields.", "properties": map[string]any{
					"baseCommit": map[string]any{"type": "string", "pattern": "^[0-9a-f]{40}$"},
					"commit":     map[string]any{"type": "string", "pattern": "^[0-9a-f]{40}$"},
					"tree":       map[string]any{"type": "string", "pattern": "^[0-9a-f]{40}$"},
					"bundle":     map[string]any{"type": "string", "description": "Base64 Git bundle copied unchanged from the delegate snapshot."},
					"message":    map[string]any{"type": "string"},
				}},
				"comments": map[string]any{"type": "array", "maxItems": 50, "description": "Actionable inline findings, one per finding. Use the repository-relative filename and a single line in the PR diff. RIGHT is the reviewed head; LEFT is the base/deleted side. Use the PR base-to-head diff, not just the latest commit. Omit for clean reviews.", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path", "line", "side", "body"}, "properties": map[string]any{
					"path": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024}, "line": map[string]any{"type": "integer", "minimum": 1}, "side": map[string]any{"type": "string", "enum": []string{"LEFT", "RIGHT"}}, "body": map[string]any{"type": "string", "minLength": 1, "maxLength": 8192},
				}}},
				"findingsCount": map[string]any{"type": "integer", "minimum": 0, "description": "Required for review: number of actionable findings. Zero produces no GitHub review post."},
			}},
		}}, Exec: func(ctx context.Context, args string) (string, error) {
			var in struct {
				PullRequest string          `json:"pullRequest"`
				Input       json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal([]byte(args), &in); err != nil {
				return "", err
			}
			var operation struct {
				Op string `json:"op"`
			}
			if err := json.Unmarshal(in.Input, &operation); err != nil {
				return "", err
			}
			if operation.Op != "observe" && operation.Op != "review" && operation.Op != "publish" && (!delegate || operation.Op != "delegate-review") {
				return "", fmt.Errorf("unsupported Code operation; status comments are managed by Factory")
			}
			if operation.Op == "publish" && !writable {
				return "", fmt.Errorf("read-only Code grant cannot publish code")
			}
			var payload map[string]any
			if err := json.Unmarshal(in.Input, &payload); err != nil {
				return "", err
			}
			if d.CodeScope == nil || d.CodeScope.PullRequestUID == "" || in.PullRequest != d.CodeScope.PullRequest {
				return "", fmt.Errorf("Code target is outside this invocation's admitted scope")
			}
			if uid, supplied := payload["pullRequestUID"]; supplied && uid != d.CodeScope.PullRequestUID {
				return "", fmt.Errorf("Code target is outside this invocation's admitted scope")
			}
			if operation.Op == "review" && d.CodeScope.ReviewOnly && (d.CodeReviewHead == "" || payload["expectedHead"] != d.CodeReviewHead) {
				return "", fmt.Errorf("review must target the worker's pinned repository commit")
			}
			// The resource UID is an immutable part of the approved invocation.
			// Make it optional for the model, then always send the scoped value.
			payload["pullRequestUID"] = d.CodeScope.PullRequestUID
			if len(validation.IsDNS1123Subdomain(in.PullRequest)) != 0 || !d.DataPlane.Available() {
				return "", fmt.Errorf("valid pullRequest and provider data plane required")
			}
			// Override rather than accept model-supplied command authority.
			if d.CodeScope.Mode == "task" {
				payload["sourceUID"] = d.CodeScope.SourceUID
				payload["sourceDigest"] = d.CodeScope.SourceDigest
				payload["grantRef"] = d.CodeScope.GrantRef
				payload["sourceProof"] = d.CodeScope.Proof
				payload["scopeProof"] = d.CodeScope.ScopeProof
				delete(payload, "eventUID")
			} else {
				payload["eventUID"] = d.CodeScope.EventUID
				payload["scopeProof"] = d.CodeScope.Proof
			}
			payload["reviewOnly"] = d.CodeScope.ReviewOnly
			payload["invocationKey"] = d.InvocationKey
			if d.Agent == nil {
				return "", fmt.Errorf("Code operation requires its executing Agent identity")
			}
			payload["invocationAgent"] = d.Agent.Name
			payload["invocationAgentUID"] = string(d.Agent.UID)
			in.Input, _ = json.Marshal(payload)
			u, err := d.DataPlane.Callers.ExportVerbURL(ctx, schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "pullrequests"}, dataplane.Request{ClusterID: d.DataPlane.ClusterID, Resource: "pullrequests", Name: in.PullRequest, Verb: "operate"})
			if err != nil {
				return "", err
			}
			raw, err := json.Marshal(map[string]any{"input": in.Input})
			if err != nil {
				return "", err
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
			if err != nil {
				return "", err
			}
			req.Header.Set("Content-Type", "application/json")
			client, err := d.DataPlane.HTTPClient()
			if err != nil {
				return "", err
			}
			response, err := client.Do(req)
			if err != nil {
				return "", fmt.Errorf("Code operation unavailable")
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(io.LimitReader(response.Body, 128<<10))
			if err != nil {
				return "", err
			}
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				return "", fmt.Errorf("Code refused operation: %s", body)
			}
			return string(body), nil
		}}}
}

// DelegateReviewScope is runtime-only: the model cannot choose the child Run
// key or Agent identity. The child receives no code-write or comment authority.
func DelegateReviewScope(ctx context.Context, d Deps, key, agent, uid string) (*CodeScope, error) {
	if d.CodeScope == nil {
		return nil, nil
	}
	args, _ := json.Marshal(map[string]any{"pullRequest": d.CodeScope.PullRequest, "input": map[string]any{"op": "delegate-review", "delegate": map[string]string{"invocationKey": key, "agent": agent, "agentUID": uid}}})
	raw, err := codeTools(d, false, true)[0].Exec(ctx, string(args))
	if err != nil {
		return nil, err
	}
	var result struct {
		Result struct {
			Proof string `json:"scopeProof"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(raw), &result) != nil || result.Result.Proof == "" {
		return nil, fmt.Errorf("Code returned no delegated review proof")
	}
	child := *d.CodeScope
	child.ReviewOnly = true
	if child.Mode == "task" {
		child.ScopeProof = result.Result.Proof
	} else {
		child.Proof = result.Result.Proof
	}
	return &child, nil
}
