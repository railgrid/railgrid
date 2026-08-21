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
	"context"
	"strings"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

// These names intentionally mirror providers/app-studio/telemetry/events/*.yaml.
// Keep the provider boundary local: importing the repository's generated
// catalog from a standalone provider would couple its module to the hub.
const (
	appStudioProjectCreatedAction   = "app_studio_project_created"
	appStudioPreviewReadyAction     = "app_studio_preview_ready"
	appStudioProjectPublishedAction = "app_studio_project_published"
)

// SetTelemetryTracker replaces the optional product telemetry dependency.
// Passing nil restores the safe no-op implementation and is useful for tests
// that construct a Server without the production main package.
func (s *Server) SetTelemetryTracker(tracker producttelemetry.Tracker) {
	if tracker == nil {
		tracker = producttelemetry.NoopTracker{}
	}
	s.mu.Lock()
	s.telemetry = tracker
	s.mu.Unlock()
}

func (s *Server) telemetryTracker() producttelemetry.Tracker {
	if s == nil {
		return producttelemetry.NoopTracker{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.telemetry == nil {
		return producttelemetry.NoopTracker{}
	}
	return s.telemetry
}

// trackProductEvent deliberately discards SDK errors. The SDK validates and
// bounds events before enqueueing; once accepted, network failures are
// asynchronous. Neither class of telemetry failure may change a product
// request's result, and this helper never logs event data, tokens, or payloads.
func (s *Server) trackProductEvent(ctx context.Context, event producttelemetry.Event) {
	_ = s.telemetryTracker().Track(ctx, event)
}

func projectTelemetryEvent(action string, id identity, project *aiv1alpha1.Project, actor bool) (producttelemetry.Event, bool) {
	if project == nil || strings.TrimSpace(id.orgUUID) == "" || strings.TrimSpace(id.workspaceUUID) == "" || strings.TrimSpace(string(project.UID)) == "" {
		return producttelemetry.Event{}, false
	}
	if actor && strings.TrimSpace(id.user) == "" {
		return producttelemetry.Event{}, false
	}
	event := producttelemetry.Event{
		Action:      action,
		OrgID:       strings.TrimSpace(id.orgUUID),
		WorkspaceID: strings.TrimSpace(id.workspaceUUID),
		ProjectID:   strings.TrimSpace(string(project.UID)),
	}
	if actor {
		event.Actor = strings.TrimSpace(id.user)
	}
	return event, true
}

func (s *Server) trackProjectCreated(ctx context.Context, id identity, project *aiv1alpha1.Project) {
	event, ok := projectTelemetryEvent(appStudioProjectCreatedAction, id, project, true)
	if !ok {
		return
	}
	event.Properties = map[string]any{"outcome": "success"}
	s.trackProductEvent(ctx, event)
}

func (s *Server) trackProjectPublished(ctx context.Context, id identity, project *aiv1alpha1.Project, outcome string) {
	if outcome != "published" && outcome != "promoted" {
		return
	}
	event, ok := projectTelemetryEvent(appStudioProjectPublishedAction, id, project, true)
	if !ok {
		return
	}
	event.Properties = map[string]any{"outcome": outcome}
	s.trackProductEvent(ctx, event)
}

// observeDevelopmentPreviewReady records only the first successful readiness
// observation for a project in this process. The dedupe key intentionally uses
// immutable scope IDs plus the project UID; names and URLs are not telemetry
// fields. Restarts may emit another observation, as documented by the
// project-unique catalog metric and bounded process-local state.
func (s *Server) observeDevelopmentPreviewReady(ctx context.Context, id identity, project *aiv1alpha1.Project) {
	event, ok := projectTelemetryEvent(appStudioPreviewReadyAction, id, project, false)
	if !ok || s == nil {
		return
	}
	key := event.OrgID + "\x00" + event.WorkspaceID + "\x00" + event.ProjectID
	s.mu.Lock()
	if s.previewReadyProjects == nil {
		s.previewReadyProjects = make(map[string]struct{})
	}
	if _, seen := s.previewReadyProjects[key]; seen {
		s.mu.Unlock()
		return
	}
	// Reserve the key while Track runs outside the lock. Concurrent polls are
	// coalesced; a failed enqueue releases the reservation so a later real
	// readiness observation can retry.
	s.previewReadyProjects[key] = struct{}{}
	tracker := s.telemetry
	s.mu.Unlock()
	if tracker == nil {
		return
	}
	err := tracker.Track(ctx, producttelemetry.Event{
		Action:      appStudioPreviewReadyAction,
		OrgID:       event.OrgID,
		WorkspaceID: event.WorkspaceID,
		ProjectID:   event.ProjectID,
		Properties: map[string]any{
			"preview_kind": "development",
			"outcome":      "ready",
		},
	})
	if err != nil {
		s.mu.Lock()
		delete(s.previewReadyProjects, key)
		s.mu.Unlock()
	}
}
