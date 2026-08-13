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

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const projectAssistantPreviewInteractionMaxSteps = 20

// interactProjectDevelopmentPreview is the tool entry point: it resolves the
// preview target, parses the action script + post-interaction assertions, drives
// the shared browser, and returns the observed result as JSON text.
func (s *Server) interactProjectDevelopmentPreview(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
	if s == nil {
		return "", errors.New("server is not configured")
	}
	if req.Project == nil {
		return "", errors.New("project is required")
	}
	ref, ok := s.resolveBrowserDataPlaneRef(ctx, req.Identity)
	if !ok {
		return projectAssistantToolJSONResult(projectAssistantPreviewInteractionResult{
			projectAssistantPreviewInspectionResult: projectAssistantPreviewInspectionResult{
				Status:      "unavailable",
				FailureKind: "worker_unavailable",
				Summary:     "Preview interaction is unavailable: no shared browser is ready in this workspace.",
			},
		}, nil)
	}
	// Interacting with a page whose latest edit has not synced is misleading —
	// same freshness guard inspection uses.
	if req.RunState != nil {
		if revision, _ := req.RunState.SourceMutationRevisions(); revision > 0 {
			if status, failure := req.RunState.DevelopmentSyncEvidence(revision); status != "succeeded" {
				if failure == "" {
					failure = "the current workspace mutation has not completed development synchronization"
				}
				return projectAssistantToolJSONResult(projectAssistantPreviewInteractionResult{
					projectAssistantPreviewInspectionResult: projectAssistantPreviewInspectionResult{
						Status: "failed", FailureKind: "not_current", Summary: failure,
					},
				}, nil)
			}
		}
	}
	previewURL, err := s.resolveProjectPreviewInspectionURL(ctx, req.Identity, req.Project)
	if err != nil {
		return "", err
	}
	targetURL, err := projectAssistantPreviewInspectionTargetURL(previewURL, projectToolString(req.Arguments["path"]))
	if err != nil {
		return "", err
	}
	steps, err := projectAssistantPreviewInteractionSteps(req.Arguments["steps"])
	if err != nil {
		return "", err
	}
	assertions, err := projectAssistantPreviewInspectionAssertions(req.Arguments["assertions"])
	if err != nil {
		return "", err
	}
	result, err := s.interactPreviewViaBrowserMCP(ctx, req.Identity, ref, projectAssistantPreviewInteractionRequest{
		URL:        targetURL,
		Steps:      steps,
		Assertions: assertions,
	})
	if err != nil {
		return "", err
	}
	result.EvidenceScope = "post_interaction_state"
	result.InteractionEvidence = result.Status == "succeeded"
	result.Limitations = []string{
		"Evidence reflects the page state after the listed actions were applied in order. Only these actions were performed; no other interaction was exercised.",
	}
	return projectAssistantPreviewInteractionTextResult(result)
}

// projectAssistantPreviewInteractionSteps parses and validates the action list.
func projectAssistantPreviewInteractionSteps(value any) ([]projectAssistantPreviewInteractionStep, error) {
	if value == nil {
		return nil, errors.New("interaction requires at least one step")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var steps []projectAssistantPreviewInteractionStep
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&steps); err != nil {
		return nil, errors.New("interaction steps are invalid")
	}
	if len(steps) == 0 {
		return nil, errors.New("interaction requires at least one step")
	}
	if len(steps) > projectAssistantPreviewInteractionMaxSteps {
		return nil, fmt.Errorf("interaction accepts at most %d steps", projectAssistantPreviewInteractionMaxSteps)
	}
	for i := range steps {
		step := &steps[i]
		step.Action = strings.ToLower(strings.TrimSpace(step.Action))
		step.Role = strings.TrimSpace(step.Role)
		step.Name = strings.TrimSpace(step.Name)
		switch step.Action {
		case "click", "hover":
			if step.Role == "" && step.Name == "" {
				return nil, fmt.Errorf("step %d: %s requires role and/or name", i+1, step.Action)
			}
		case "type", "fill":
			if step.Role == "" && step.Name == "" {
				return nil, fmt.Errorf("step %d: %s requires role and/or name", i+1, step.Action)
			}
		case "select":
			if step.Role == "" && step.Name == "" {
				return nil, fmt.Errorf("step %d: select requires role and/or name", i+1)
			}
			if len(step.Values) == 0 {
				return nil, fmt.Errorf("step %d: select requires values", i+1)
			}
		case "press":
			if strings.TrimSpace(step.Key) == "" {
				return nil, fmt.Errorf("step %d: press requires key", i+1)
			}
		default:
			return nil, fmt.Errorf("step %d: unsupported action %q", i+1, step.Action)
		}
	}
	return steps, nil
}

// projectAssistantPreviewInteractionTextResult renders the result as JSON,
// dropping any screenshot bytes from the model-facing text (reported as a size).
func projectAssistantPreviewInteractionTextResult(result projectAssistantPreviewInteractionResult) (string, error) {
	if result.Screenshot != nil {
		screenshot := *result.Screenshot
		screenshot.Bytes = len(screenshot.Base64) * 3 / 4
		screenshot.Base64 = ""
		result.Screenshot = &screenshot
	}
	return projectAssistantToolJSONResult(result, nil)
}

// Interactive preview inspection. Unlike inspect_development_preview (read-only:
// navigate + snapshot + assertions), interact_development_preview drives the
// shared Playwright MCP browser through a bounded action script — click, type,
// fill, press, select, hover — then observes the resulting rendered state. It is
// how the assistant verifies real interaction (a login click, a form submit)
// rather than only static markup.
//
// Two guardrails distinguish it from the raw MCP tools:
//   - Only the safe interactive verbs are exposed; browser_run_code_unsafe and
//     browser_evaluate (arbitrary JS) are never reachable.
//   - The shared browser is a per-workspace singleton (one Chromium, one tab),
//     so every call takes a per-instance lock and runs its whole
//     navigate→act→observe sequence atomically. Concurrent turns cannot collide
//     mid-flow on the same tab. (A future refinement is an isolated context per
//     call; the lock is the correct, simple v1.)

// browserInstanceLocks serializes access to each shared browser instance,
// keyed by cluster + resource + name.
var browserInstanceLocks sync.Map // string -> *sync.Mutex

func lockBrowserInstance(clusterID string, ref dataPlaneRef) func() {
	key := clusterID + "|" + ref.Resource + "|" + ref.Name
	actual, _ := browserInstanceLocks.LoadOrStore(key, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type projectAssistantPreviewInteractionStep struct {
	// Action is one of click, type, fill, press, select, hover.
	Action string `json:"action"`
	// Role + Name target an element by accessible role and (optional) accessible
	// name, resolved against the live accessibility snapshot — the same
	// vocabulary as inspection assertions. Ignored by press.
	Role  string `json:"role,omitempty"`
	Name  string `json:"name,omitempty"`
	Exact bool   `json:"exact,omitempty"`
	// Value is the text to type (type/fill). Key is the key to press (press).
	// Values are the option values to select (select).
	Value  string   `json:"value,omitempty"`
	Key    string   `json:"key,omitempty"`
	Values []string `json:"values,omitempty"`
	// Submit presses Enter after typing (type/fill), to submit a form.
	Submit bool `json:"submit,omitempty"`
}

type projectAssistantPreviewInteractionRequest struct {
	URL               string
	Steps             []projectAssistantPreviewInteractionStep
	Assertions        []projectAssistantPreviewInspectionAssertion
	IncludeScreenshot bool
}

type projectAssistantPreviewInteractionStepResult struct {
	projectAssistantPreviewInteractionStep
	Applied bool   `json:"applied"`
	Message string `json:"message,omitempty"`
}

// projectAssistantPreviewInteractionResult is the observed state after the
// action script, plus a per-step outcome list. It embeds the inspection result
// so the post-action snapshot, assertions, and console reuse one shape.
type projectAssistantPreviewInteractionResult struct {
	projectAssistantPreviewInspectionResult
	Steps []projectAssistantPreviewInteractionStepResult `json:"steps,omitempty"`
}

// interactPreviewViaBrowserMCP navigates to the preview, applies the action
// steps in order (stopping at the first that cannot be applied), and observes
// the resulting page. It holds the per-instance lock for the whole sequence.
func (s *Server) interactPreviewViaBrowserMCP(ctx context.Context, id identity, ref dataPlaneRef, req projectAssistantPreviewInteractionRequest) (projectAssistantPreviewInteractionResult, error) {
	unlock := lockBrowserInstance(id.clusterID, ref)
	defer unlock()

	session, err := s.newBrowserMCPSession(ctx, id, ref)
	if err != nil {
		return projectAssistantPreviewInteractionResult{}, err
	}
	defer session.close()

	nav, err := session.callTool(ctx, browserMCPToolNavigate, map[string]any{"url": req.URL})
	if err != nil {
		return projectAssistantPreviewInteractionResult{}, err
	}
	var out projectAssistantPreviewInteractionResult
	if nav.isError {
		out.Status = "failed"
		out.FailureKind = "navigation"
		out.Summary = browserMCPFirstLine(nav.text, "the preview did not load")
		out.FinalURL = req.URL
		return out, nil
	}

	stepFailed := false
	for _, step := range req.Steps {
		result, err := s.applyInteractionStep(ctx, session, step)
		if err != nil {
			return projectAssistantPreviewInteractionResult{}, err
		}
		out.Steps = append(out.Steps, result)
		if !result.Applied {
			stepFailed = true
			break // a step that could not be applied invalidates the rest
		}
	}

	// Observe the resulting state, exactly as inspection does.
	snap, err := session.callTool(ctx, browserMCPToolSnapshot, map[string]any{})
	if err != nil {
		return projectAssistantPreviewInteractionResult{}, err
	}
	snapshotText := snap.text
	console, err := session.callTool(ctx, browserMCPToolConsole, map[string]any{})
	if err != nil {
		console = browserMCPContent{}
	}
	out.projectAssistantPreviewInspectionResult = projectAssistantPreviewInspectionResult{
		Status:   "succeeded",
		FinalURL: browserMCPParseField(snapshotText, "Page URL", req.URL),
		Title:    browserMCPParseField(snapshotText, "Page Title", ""),
		Snapshot: browserMCPExtractSnapshotTree(snapshotText),
		Console:  browserMCPParseConsole(console.text),
	}
	nodes := browserMCPParseAccessibilityNodes(out.Snapshot)
	failed := 0
	for _, assertion := range req.Assertions {
		outcome := browserMCPEvaluateAssertion(assertion, out.Snapshot, nodes)
		if !outcome.Passed {
			failed++
		}
		out.Assertions = append(out.Assertions, outcome)
	}
	switch {
	case stepFailed:
		out.Status = "failed"
		out.FailureKind = "interaction"
		out.Summary = "an interaction step could not be applied to the preview"
	case failed > 0:
		out.Status = "failed"
		out.FailureKind = "assertion"
		out.Summary = fmt.Sprintf("%d of %d post-interaction assertion(s) did not hold", failed, len(req.Assertions))
	}

	if req.IncludeScreenshot {
		if shot, err := session.callTool(ctx, browserMCPToolScreenshot, map[string]any{"type": "png"}); err == nil {
			out.Screenshot = browserMCPScreenshot(shot)
		}
	}
	return out, nil
}

// applyInteractionStep performs one action against the current page. For
// element-targeting actions it snapshots, resolves the target's ref, then calls
// the matching Playwright MCP tool. A step whose target cannot be found (or whose
// tool reports an error) is returned Applied=false with a reason.
func (s *Server) applyInteractionStep(ctx context.Context, session *browserMCPSession, step projectAssistantPreviewInteractionStep) (projectAssistantPreviewInteractionStepResult, error) {
	res := projectAssistantPreviewInteractionStepResult{projectAssistantPreviewInteractionStep: step}

	if step.Action == "press" {
		if strings.TrimSpace(step.Key) == "" {
			res.Message = "press requires key"
			return res, nil
		}
		out, err := session.callTool(ctx, "browser_press_key", map[string]any{"key": step.Key})
		if err != nil {
			return res, err
		}
		res.Applied = !out.isError
		if out.isError {
			res.Message = browserMCPFirstLine(out.text, "press failed")
		}
		return res, nil
	}

	// Element-targeting actions need a fresh snapshot to resolve the ref.
	snap, err := session.callTool(ctx, browserMCPToolSnapshot, map[string]any{})
	if err != nil {
		return res, err
	}
	node, ok := findInteractionTarget(browserMCPExtractSnapshotTree(snap.text), step)
	if !ok {
		res.Message = describeMissingTarget(step)
		return res, nil
	}
	element := describeInteractionNode(node)

	// Playwright MCP targets an element by `target` (a selector) with an optional
	// human-readable `element` description. Snapshot refs resolve through
	// Playwright's aria-ref selector engine — a bare ref is parsed as CSS and
	// fails, so it must be prefixed.
	target := "aria-ref=" + node.ref
	var name string
	var args map[string]any
	switch step.Action {
	case "click":
		name, args = "browser_click", map[string]any{"element": element, "target": target}
	case "type", "fill":
		name, args = "browser_type", map[string]any{"element": element, "target": target, "text": step.Value, "submit": step.Submit}
	case "select":
		name, args = "browser_select_option", map[string]any{"element": element, "target": target, "values": step.Values}
	case "hover":
		name, args = "browser_hover", map[string]any{"element": element, "target": target}
	default:
		res.Message = fmt.Sprintf("unsupported action %q", step.Action)
		return res, nil
	}

	out, err := session.callTool(ctx, name, args)
	if err != nil {
		return res, err
	}
	res.Applied = !out.isError
	if out.isError {
		res.Message = browserMCPFirstLine(out.text, step.Action+" failed")
	}
	return res, nil
}

// findInteractionTarget returns the first accessibility node matching the step's
// role and (optional) accessible-name filter, and only when it carries a ref the
// interaction tools can target.
func findInteractionTarget(snapshotTree string, step projectAssistantPreviewInteractionStep) (browserMCPNode, bool) {
	role := strings.ToLower(strings.TrimSpace(step.Role))
	for _, node := range browserMCPParseAccessibilityNodes(snapshotTree) {
		if node.ref == "" {
			continue
		}
		if role != "" && node.role != role {
			continue
		}
		if step.Name != "" && !browserMCPTextMatches(node.name, step.Name, step.Exact) {
			continue
		}
		return node, true
	}
	return browserMCPNode{}, false
}

func describeInteractionNode(node browserMCPNode) string {
	if node.name != "" {
		return fmt.Sprintf("%s %q", node.role, node.name)
	}
	return node.role
}

func describeMissingTarget(step projectAssistantPreviewInteractionStep) string {
	switch {
	case step.Role != "" && step.Name != "":
		return fmt.Sprintf("no %q with name %q found to %s", step.Role, step.Name, step.Action)
	case step.Role != "":
		return fmt.Sprintf("no %q found to %s", step.Role, step.Action)
	case step.Name != "":
		return fmt.Sprintf("no element named %q found to %s", step.Name, step.Action)
	default:
		return "the step did not name a target (role or name)"
	}
}
