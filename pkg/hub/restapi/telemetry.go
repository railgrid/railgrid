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

package restapi

import (
	"context"

	hubtelemetry "github.com/faroshq/faros/pkg/hub/telemetry"
)

// PlatformTelemetry is the REST-owned seam for platform activation events.
// Keeping the interface here lets handlers remain testable without coupling
// them to the telemetry queue implementation.
type PlatformTelemetry interface {
	TrackPlatform(context.Context, hubtelemetry.Event) error
}

type noopPlatformTelemetry struct{}

func (noopPlatformTelemetry) TrackPlatform(context.Context, hubtelemetry.Event) error { return nil }

func (m *Manager) trackPlatform(ctx context.Context, event hubtelemetry.Event) {
	if m.telemetry == nil {
		return
	}
	// Product success is authoritative at the handler boundary. Telemetry is
	// best effort and must never change the response when its queue is full,
	// disabled, or otherwise unavailable.
	_ = m.telemetry.TrackPlatform(ctx, event)
}
