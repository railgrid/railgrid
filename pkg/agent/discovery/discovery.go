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

// Package discovery detects HTTP services running on the edge host next to the
// agent (e.g. Home Assistant). The edges provider pulls the result over the
// tunnel via the agent's /api/v1/services endpoint and materializes an
// EdgeService per discovered service. Detectors must never fail the endpoint:
// every probe/exec error is treated as "not detected".
package discovery

import (
	"context"

	"k8s.io/klog/v2"
)

// DiscoveredService is one service the agent found on the host. JSON tags match
// the wire format the provider's discovery reconciler decodes.
type DiscoveredService struct {
	// Name is a human-readable service name (e.g. "home-assistant").
	Name string `json:"name"`
	// Type is the detector id; it matches an EdgeServiceType value.
	Type string `json:"type"`
	// Scheme is "http" or "https".
	Scheme string `json:"scheme"`
	// Port is the loopback port the service listens on.
	Port int32 `json:"port"`
	// Version is best-effort (e.g. a container image tag).
	Version string `json:"version,omitempty"`
	// InstallType is how the service is installed: container|core|haos|supervised.
	InstallType string `json:"installType,omitempty"`
}

// Detector probes for one kind of service.
type Detector interface {
	// Name is the detector id, used for logging.
	Name() string
	// Detect returns the discovered service and true when found. It must not
	// return partial results with false, and must never block longer than a
	// few seconds.
	Detect(ctx context.Context) (*DiscoveredService, bool)
}

// DefaultDetectors is the built-in detector set. Home Assistant is the first;
// add more here as new integrations land.
func DefaultDetectors() []Detector {
	return []Detector{
		&homeAssistantDetector{},
	}
}

// Run executes every detector and returns the services that were found. A
// detector that panics or errors is skipped — discovery is best-effort.
func Run(ctx context.Context, detectors []Detector) []DiscoveredService {
	logger := klog.FromContext(ctx).WithName("discovery")
	var out []DiscoveredService
	for _, d := range detectors {
		svc := runOne(ctx, logger, d)
		if svc != nil {
			out = append(out, *svc)
		}
	}
	return out
}

// runOne isolates a single detector so a panic in one cannot abort the sweep.
func runOne(ctx context.Context, logger klog.Logger, d Detector) (result *DiscoveredService) {
	defer func() {
		if r := recover(); r != nil {
			logger.V(2).Info("detector panicked (skipping)", "detector", d.Name(), "panic", r)
			result = nil
		}
	}()
	svc, ok := d.Detect(ctx)
	if !ok {
		return nil
	}
	logger.V(4).Info("service detected", "detector", d.Name(), "port", svc.Port, "installType", svc.InstallType)
	return svc
}
