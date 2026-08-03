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
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

const (
	projectAssistantPreviewInspectionTimeout       = 20 * time.Second
	projectAssistantPreviewInspectionHealthTimeout = 750 * time.Millisecond
	projectAssistantPreviewInspectionMaxResponse   = 4 << 20
)

type projectAssistantPreviewInspector interface {
	Health(context.Context) error
	Inspect(context.Context, projectAssistantPreviewInspectionRequest) (projectAssistantPreviewInspectionResult, error)
}

type projectAssistantPreviewInspectionRequest struct {
	URL               string                                       `json:"url"`
	Assertions        []projectAssistantPreviewInspectionAssertion `json:"assertions,omitempty"`
	IncludeScreenshot bool                                         `json:"includeScreenshot,omitempty"`
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
	Screenshot          *projectAssistantPreviewInspectionScreenshot       `json:"screenshot,omitempty"`
}

type httpProjectAssistantPreviewInspector struct {
	baseURL string
	client  *http.Client
}

func newHTTPProjectAssistantPreviewInspector(rawURL string) (projectAssistantPreviewInspector, error) {
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("browser worker URL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("browser worker URL must use HTTP(S)")
	}
	return &httpProjectAssistantPreviewInspector{
		baseURL: rawURL,
		client:  &http.Client{Timeout: projectAssistantPreviewInspectionTimeout},
	}, nil
}

func (c *httpProjectAssistantPreviewInspector) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("browser worker health returned %s", response.Status)
	}
	return nil
}

func (c *httpProjectAssistantPreviewInspector) Inspect(ctx context.Context, input projectAssistantPreviewInspectionRequest) (projectAssistantPreviewInspectionResult, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/inspect", bytes.NewReader(body))
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, projectAssistantPreviewInspectionMaxResponse+1))
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	if len(raw) > projectAssistantPreviewInspectionMaxResponse {
		return projectAssistantPreviewInspectionResult{}, errors.New("browser worker response exceeded the configured limit")
	}
	if response.StatusCode != http.StatusOK {
		return projectAssistantPreviewInspectionResult{}, fmt.Errorf("browser worker returned %s", response.Status)
	}
	var result projectAssistantPreviewInspectionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return projectAssistantPreviewInspectionResult{}, fmt.Errorf("decode browser worker response: %w", err)
	}
	if result.Status != "succeeded" && result.Status != "failed" {
		return projectAssistantPreviewInspectionResult{}, errors.New("browser worker returned an unsupported status")
	}
	return result, nil
}

func (s *Server) ConfigurePreviewInspection(rawURL string) error {
	if s == nil {
		return errors.New("server is not configured")
	}
	if strings.TrimSpace(rawURL) == "" {
		s.previewInspector = nil
		return nil
	}
	inspector, err := newHTTPProjectAssistantPreviewInspector(rawURL)
	if err != nil {
		return err
	}
	s.previewInspector = inspector
	return nil
}

func (s *Server) projectAssistantPreviewInspectionAvailable(ctx context.Context) bool {
	if s == nil || s.previewInspector == nil {
		return false
	}
	healthCtx, cancel := context.WithTimeout(ctx, projectAssistantPreviewInspectionHealthTimeout)
	defer cancel()
	return s.previewInspector.Health(healthCtx) == nil
}

func (s *Server) inspectProjectDevelopmentPreview(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
	result, err := s.inspectProjectDevelopmentPreviewResult(ctx, req, false)
	if err != nil {
		return "", err
	}
	return projectAssistantPreviewInspectionTextResult(result)
}

func (s *Server) inspectProjectDevelopmentPreviewResult(ctx context.Context, req projectAssistantToolCallRequest, includeScreenshot bool) (projectAssistantPreviewInspectionResult, error) {
	if s == nil || s.previewInspector == nil {
		return projectAssistantPreviewInspectionResult{
			Status:      "unavailable",
			FailureKind: "worker_unavailable",
			Summary:     "Development preview inspection is unavailable.",
		}, nil
	}
	if req.Project == nil {
		return projectAssistantPreviewInspectionResult{}, errors.New("project is required")
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
					Status:      "failed",
					FailureKind: "not_current",
					Summary:     failure,
				}, nil
			}
		}
	}
	previewURL, err := s.resolveProjectPreviewInspectionURL(ctx, req.Identity, req.Project)
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	targetURL, err := projectAssistantPreviewInspectionTargetURL(previewURL, projectToolString(req.Arguments["path"]))
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	assertions, err := projectAssistantPreviewInspectionAssertions(req.Arguments["assertions"])
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	result, err := s.previewInspector.Inspect(ctx, projectAssistantPreviewInspectionRequest{
		URL:               targetURL,
		Assertions:        assertions,
		IncludeScreenshot: includeScreenshot,
	})
	if err != nil {
		return projectAssistantPreviewInspectionResult{}, err
	}
	result.EvidenceScope = "rendered_state_only"
	result.InteractionEvidence = false
	result.Limitations = []string{
		"This inspection did not click, type, press keys, submit forms, or execute application interactions. Static text and role assertions do not verify interaction behavior.",
	}
	return result, nil
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
	if s.previewInspectionResolveURL != nil {
		return s.previewInspectionResolveURL(ctx, id, project)
	}
	client, err := s.clientFor(id)
	if err != nil {
		return "", err
	}
	preview, bound := s.resolveProjectSandboxRuntime(ctx, client, id, project)
	if !bound {
		return "", errors.New("development preview is not configured")
	}
	if !preview.Ready || strings.TrimSpace(preview.PreviewURL) == "" {
		message := strings.TrimSpace(preview.Message)
		if message == "" {
			message = "development preview is not ready"
		}
		return "", errors.New(message)
	}
	return preview.PreviewURL, nil
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
