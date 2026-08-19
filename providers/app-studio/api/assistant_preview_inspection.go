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
	"net/url"
	"strings"
	"time"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

const (
	projectAssistantPreviewInspectionHealthTimeout = 750 * time.Millisecond
	projectAssistantPreviewInspectionMaxResponse   = 4 << 20

	projectAssistantPreviewScreenshotNotRequested        = "not_requested"
	projectAssistantPreviewScreenshotCaptured            = "captured"
	projectAssistantPreviewScreenshotModelUnsupported    = "model_unsupported"
	projectAssistantPreviewScreenshotBrowserUnreachable  = "browser_unreachable"
	projectAssistantPreviewScreenshotCaptureFailed       = "capture_failed"
	projectAssistantPreviewScreenshotArtifactUnavailable = "artifact_unavailable"
)

type projectAssistantPreviewInspector interface {
	Health(context.Context) error
	Inspect(context.Context, projectAssistantPreviewInspectionRequest) (projectAssistantPreviewInspectionResult, error)
}

type projectAssistantPreviewInspectionRequest struct {
	URL                string                                       `json:"url"`
	Assertions         []projectAssistantPreviewInspectionAssertion `json:"assertions,omitempty"`
	IncludeScreenshot  bool                                         `json:"includeScreenshot,omitempty"`
	RequiresHubSession bool                                         `json:"-"`
}

type projectAssistantPreviewInspectionAssertion struct {
	Kind  string `json:"kind"`
	Text  string `json:"text,omitempty"`
	Exact bool   `json:"exact,omitempty"`
	Role  string `json:"role,omitempty"`
	Name  string `json:"name,omitempty"`
	Min   *int   `json:"min,omitempty"`
	Max   *int   `json:"max,omitempty"`
}

type projectAssistantPreviewInspectionAssertionResult struct {
	projectAssistantPreviewInspectionAssertion
	Passed      bool   `json:"passed"`
	ActualCount *int   `json:"actualCount,omitempty"`
	Message     string `json:"message,omitempty"`
}

type projectAssistantPreviewInspectionConsoleEvent struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type projectAssistantPreviewInspectionNetworkEvent struct {
	URL     string `json:"url"`
	Method  string `json:"method,omitempty"`
	Failure string `json:"failure"`
}

type projectAssistantPreviewInspectionScreenshot struct {
	MIMEType string `json:"mimeType"`
	Base64   string `json:"base64,omitempty"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	SHA256   string `json:"sha256"`
	Bytes    int    `json:"bytes,omitempty"`
}

type projectAssistantPreviewInspectionResult struct {
	Status              string                                             `json:"status"`
	FailureKind         string                                             `json:"failureKind,omitempty"`
	Summary             string                                             `json:"summary,omitempty"`
	EvidenceScope       string                                             `json:"evidenceScope,omitempty"`
	InteractionEvidence bool                                               `json:"interactionEvidence"`
	Limitations         []string                                           `json:"limitations,omitempty"`
	FinalURL            string                                             `json:"finalURL,omitempty"`
	Title               string                                             `json:"title,omitempty"`
	Snapshot            string                                             `json:"snapshot,omitempty"`
	Assertions          []projectAssistantPreviewInspectionAssertionResult `json:"assertions,omitempty"`
	Console             []projectAssistantPreviewInspectionConsoleEvent    `json:"console,omitempty"`
	Network             []projectAssistantPreviewInspectionNetworkEvent    `json:"network,omitempty"`
	ScreenshotStatus    string                                             `json:"screenshotStatus,omitempty"`
	Screenshot          *projectAssistantPreviewInspectionScreenshot       `json:"screenshot,omitempty"`
}

// projectAssistantPreviewInspectionAction carries only bounded, server-owned
// presentation metadata into the durable action feed. Browser snapshots,
// console output, network URLs, titles, and assertion text are deliberately
// excluded because preview content is untrusted application data.
type projectAssistantPreviewInspectionAction struct {
	FailureKind          string `json:"failureKind,omitempty"`
	AssertionCount       int    `json:"assertionCount,omitempty"`
	FailedAssertionCount int    `json:"failedAssertionCount,omitempty"`
}

func projectAssistantPreviewInspectionActionFromResult(result projectAssistantPreviewInspectionResult) *projectAssistantPreviewInspectionAction {
	failureKind := strings.TrimSpace(result.FailureKind)
	switch failureKind {
	case "assertion", "application", "navigation", "worker_unavailable", "not_current":
	default:
		return nil
	}
	action := &projectAssistantPreviewInspectionAction{FailureKind: failureKind}
	if failureKind != "assertion" {
		return action
	}
	action.AssertionCount = len(result.Assertions)
	for _, assertion := range result.Assertions {
		if !assertion.Passed {
			action.FailedAssertionCount++
		}
	}
	return action
}

func projectAssistantPreviewInspectionActionFromText(raw string) *projectAssistantPreviewInspectionAction {
	var result projectAssistantPreviewInspectionResult
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &result) != nil {
		return nil
	}
	return projectAssistantPreviewInspectionActionFromResult(result)
}

func projectAssistantPreviewInspectionActionFromToolResult(name, raw string) *projectAssistantPreviewInspectionAction {
	switch projectToolBaseName(name) {
	case projectToolInspectDevelopmentPreview, projectToolInteractDevelopmentPreview:
	default:
		return nil
	}
	return projectAssistantPreviewInspectionActionFromText(raw)
}

// projectAssistantPreviewInspectionAvailable reports whether preview inspection
// can run for this caller. In production that means the workspace has a Ready
// shared browser (the Studio's Playwright MCP instance); tests inject a
// previewInspector fake and gate on its Health probe instead.
func (s *Server) projectAssistantPreviewInspectionAvailable(ctx context.Context, id identity) bool {
	if s == nil {
		return false
	}
	if s.previewInspector != nil {
		healthCtx, cancel := context.WithTimeout(ctx, projectAssistantPreviewInspectionHealthTimeout)
		defer cancel()
		return s.previewInspector.Health(healthCtx) == nil
	}
	_, ok := s.resolveBrowserDataPlaneRef(ctx, id)
	return ok
}

func (s *Server) inspectProjectDevelopmentPreview(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
	result, err := s.inspectProjectDevelopmentPreviewResult(ctx, req, false)
	if err != nil {
		return "", err
	}
	return projectAssistantPreviewInspectionTextResult(result)
}

func (s *Server) inspectProjectDevelopmentPreviewResult(ctx context.Context, req projectAssistantToolCallRequest, includeScreenshot bool) (projectAssistantPreviewInspectionResult, error) {
	if s == nil {
		return projectAssistantPreviewInspectionResult{}, errors.New("server is not configured")
	}
	if req.Project == nil {
		return projectAssistantPreviewInspectionResult{}, errors.New("project is required")
	}
	// Production drives the shared browser over the data plane; tests inject a
	// previewInspector fake. Absent both, inspection is unavailable.
	var browserRef dataPlaneRef
	if s.previewInspector == nil {
		ref, ok := s.resolveBrowserDataPlaneRef(ctx, req.Identity)
		if !ok {
			return projectAssistantPreviewInspectionResult{
				Status:           "unavailable",
				FailureKind:      "worker_unavailable",
				Summary:          "Development preview inspection is unavailable: no shared browser is ready in this workspace.",
				ScreenshotStatus: projectAssistantPreviewScreenshotStatusForUnavailable(includeScreenshot),
			}, nil
		}
		browserRef = ref
	}
	if req.RunState != nil {
		revision, _ := req.RunState.SourceMutationRevisions()
		if revision > 0 {
			status, failure := req.RunState.DevelopmentSyncEvidence(revision)
			if status != "succeeded" {
				if failure == "" {
					failure = "the current workspace mutation has not completed development synchronization"
				}
				return projectAssistantPreviewInspectionResult{
					Status:           "failed",
					FailureKind:      "not_current",
					Summary:          failure,
					ScreenshotStatus: projectAssistantPreviewScreenshotStatusForUnavailable(includeScreenshot),
				}, nil
			}
		}
	}
	preview, err := s.resolveProjectPreviewInspectionTarget(ctx, req.Identity, req.Project)
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	targetURL, err := projectAssistantPreviewInspectionTargetURL(preview.PreviewURL, projectToolString(req.Arguments["path"]))
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	assertions, err := projectAssistantPreviewInspectionAssertions(req.Arguments["assertions"])
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	inspectionReq := projectAssistantPreviewInspectionRequest{
		URL:                targetURL,
		Assertions:         assertions,
		IncludeScreenshot:  includeScreenshot,
		RequiresHubSession: strings.EqualFold(strings.TrimSpace(preview.ObservedAccess), "private"),
	}
	var result projectAssistantPreviewInspectionResult
	if s.previewInspector != nil {
		result, err = s.previewInspector.Inspect(ctx, inspectionReq)
	} else {
		result, err = s.inspectPreviewViaBrowserMCP(ctx, req.Identity, browserRef, inspectionReq)
	}
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	projectAssistantFinalizePreviewScreenshotStatus(&result, includeScreenshot)
	result.EvidenceScope = "rendered_state_only"
	result.InteractionEvidence = false
	result.Limitations = []string{
		"This inspection did not click, type, press keys, submit forms, or execute application interactions. Static text and role assertions do not verify interaction behavior.",
	}
	return result, nil
}

func projectAssistantPreviewScreenshotStatusForUnavailable(requested bool) string {
	if requested {
		return projectAssistantPreviewScreenshotBrowserUnreachable
	}
	return projectAssistantPreviewScreenshotNotRequested
}

func projectAssistantFinalizePreviewScreenshotStatus(result *projectAssistantPreviewInspectionResult, requested bool) {
	if result == nil {
		return
	}
	if !requested {
		result.ScreenshotStatus = projectAssistantPreviewScreenshotNotRequested
		result.Screenshot = nil
		return
	}
	if result.Screenshot != nil && strings.TrimSpace(result.Screenshot.Base64) != "" {
		result.ScreenshotStatus = projectAssistantPreviewScreenshotCaptured
		return
	}
	if strings.TrimSpace(result.ScreenshotStatus) == "" {
		result.ScreenshotStatus = projectAssistantPreviewScreenshotCaptureFailed
	}
}

func projectAssistantPreviewInspectionTextResult(result projectAssistantPreviewInspectionResult) (string, error) {
	if result.Screenshot != nil {
		screenshot := *result.Screenshot
		screenshot.Bytes = len(screenshot.Base64) * 3 / 4
		screenshot.Base64 = ""
		result.Screenshot = &screenshot
	}
	return projectAssistantToolJSONResult(result, nil)
}

func (s *Server) resolveProjectPreviewInspectionURL(ctx context.Context, id identity, project *aiv1alpha1.Project) (string, error) {
	preview, err := s.resolveProjectPreviewInspectionTarget(ctx, id, project)
	return preview.PreviewURL, err
}

func (s *Server) resolveProjectPreviewInspectionTarget(ctx context.Context, id identity, project *aiv1alpha1.Project) (projectSandboxPreviewURLResponse, error) {
	if s.previewInspectionResolveURL != nil {
		previewURL, err := s.previewInspectionResolveURL(ctx, id, project)
		return projectSandboxPreviewURLResponse{Ready: err == nil, PreviewURL: previewURL}, err
	}
	client, err := s.clientFor(id)
	if err != nil {
		return projectSandboxPreviewURLResponse{}, err
	}
	preview, bound := s.resolveProjectSandboxRuntime(ctx, client, id, project)
	if !bound {
		return projectSandboxPreviewURLResponse{}, errors.New("development preview is not configured")
	}
	if !preview.Ready || strings.TrimSpace(preview.PreviewURL) == "" {
		message := strings.TrimSpace(preview.Message)
		if message == "" {
			message = "development preview is not ready"
		}
		return projectSandboxPreviewURLResponse{}, errors.New(message)
	}
	return preview, nil
}

func projectAssistantPreviewInspectionTargetURL(baseURL, path string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("development preview URL is invalid")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	reference, err := url.Parse(path)
	if err != nil || reference.IsAbs() || reference.Host != "" || !strings.HasPrefix(reference.Path, "/") || strings.HasPrefix(path, "//") {
		return "", errors.New("preview path must be an absolute project-relative path")
	}
	reference.Fragment = ""
	target := base.ResolveReference(reference)
	if !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
		return "", errors.New("preview path cannot change the preview origin")
	}
	return target.String(), nil
}

func projectAssistantPreviewInspectionAssertions(value any) ([]projectAssistantPreviewInspectionAssertion, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var assertions []projectAssistantPreviewInspectionAssertion
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&assertions); err != nil {
		return nil, errors.New("preview assertions are invalid")
	}
	if len(assertions) > 12 {
		return nil, errors.New("preview inspection accepts at most 12 assertions")
	}
	for index := range assertions {
		assertion := &assertions[index]
		assertion.Kind = strings.TrimSpace(assertion.Kind)
		assertion.Text = strings.TrimSpace(assertion.Text)
		assertion.Role = strings.TrimSpace(assertion.Role)
		assertion.Name = strings.TrimSpace(assertion.Name)
		switch assertion.Kind {
		case "text_present":
			if assertion.Text == "" || assertion.Role != "" || assertion.Name != "" || assertion.Min != nil || assertion.Max != nil {
				return nil, fmt.Errorf("preview assertion %d: text_present requires only text and optional exact", index+1)
			}
		case "role_present", "role_count":
			if assertion.Role == "" {
				return nil, fmt.Errorf("preview assertion %d: %s requires role", index+1, assertion.Kind)
			}
			// Models commonly call the accessible-name filter "text". Accept
			// that unambiguous spelling at App Studio's boundary and send the
			// browser worker its canonical `name` field.
			if assertion.Name == "" && assertion.Text != "" {
				assertion.Name = assertion.Text
				assertion.Text = ""
			}
			if assertion.Text != "" {
				return nil, fmt.Errorf("preview assertion %d: use name, not text, to filter a role", index+1)
			}
			if assertion.Kind == "role_present" && (assertion.Min != nil || assertion.Max != nil) {
				return nil, fmt.Errorf("preview assertion %d: role_present does not accept min or max", index+1)
			}
			if assertion.Min != nil && assertion.Max != nil && *assertion.Min > *assertion.Max {
				return nil, fmt.Errorf("preview assertion %d: min cannot exceed max", index+1)
			}
		default:
			return nil, fmt.Errorf("preview assertion %d: unsupported kind %q", index+1, assertion.Kind)
		}
	}
	return assertions, nil
}
