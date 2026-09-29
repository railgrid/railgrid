/*
Copyright 2026 The Railgrid Authors.

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

package tunnel

import (
	"sync"

	"github.com/railgrid/railgrid/pkg/agent/discovery"
)

// RunnerSource is the harness plane as the tunnel sees it: what runners are
// running, and what bearer each one expects. The tunnel deliberately knows
// nothing else about harnesses.
type RunnerSource interface {
	// Services is one entry per RUNNING runner, advertised to the provider's
	// discovery loop alongside the detector results.
	Services() []discovery.DiscoveredService
	// RunnerToken returns the bearer for a loopback port this agent supervises,
	// and false for every other port. That "false" is the SSRF-shaped half of
	// the guarantee: a Service aimed at some other local port can never borrow a
	// runner's identity.
	RunnerToken(port int) (string, bool)
}

// RunnerRegistry is a late-bound holder for the harness plane.
//
// The tunnel starts BEFORE the plane does: on a join-token bootstrap the agent
// has no hub credential until the provider issues one, and the plane needs that
// credential to watch spec.harness. The registry lets the tunnel's handlers be
// wired once, at startup, and pick the plane up when it exists. Every method is
// safe on a nil receiver and on an unset source, which is what an agent with no
// harness plane at all looks like.
type RunnerRegistry struct {
	mu     sync.RWMutex
	source RunnerSource
}

// Set installs the harness plane. Called once, when the plane starts.
func (r *RunnerRegistry) Set(source RunnerSource) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.source = source
}

// Services returns the running runners, or nothing.
func (r *RunnerRegistry) Services() []discovery.DiscoveredService {
	source := r.get()
	if source == nil {
		return nil
	}
	return source.Services()
}

// Token returns the bearer for a supervised runner port.
func (r *RunnerRegistry) Token(port int) (string, bool) {
	source := r.get()
	if source == nil {
		return "", false
	}
	return source.RunnerToken(port)
}

func (r *RunnerRegistry) get() RunnerSource {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.source
}
