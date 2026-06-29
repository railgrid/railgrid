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

package aggregatemcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
)

// toolsListJSON is a minimal JSON-RPC tools/list response advertising a
// single tool named `name`. Enough for the federation client to discover
// and proxy it.
func toolsListJSON(name string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"tools":[`+
		`{"name":%q,"description":"x","inputSchema":{"type":"object"}}]}}`, name)
}

// TestRegisterProviderTools_HungProviderDoesNotBlockAggregate is the
// hub-level resilience guard: one provider whose /mcp hangs must not take
// the whole aggregate tools/list down with it. Discovery fans out with a
// per-provider deadline, so the healthy provider's tools still register and
// the hung one simply drops out of this round.
func TestRegisterProviderTools_HungProviderDoesNotBlockAggregate(t *testing.T) {
	// Lower the per-provider deadline so the test exercises the timeout
	// path in milliseconds, not the production 8s.
	prev := providerDiscoveryTimeout
	providerDiscoveryTimeout = 300 * time.Millisecond
	defer func() { providerDiscoveryTimeout = prev }()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(toolsListJSON("ping")))
	}))
	defer healthy.Close()

	// The hung provider never responds within the discovery deadline: it
	// blocks until either the federation client cancels the request (its
	// deadline firing — the behaviour under test) or the test releases it
	// at cleanup. release is closed before hung.Close() (defers are LIFO),
	// so the handler returns promptly and Close doesn't stall.
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer hung.Close()
	defer close(release)

	cfg := Config{
		Enumerate: fakeEnumerate(nil, nil),
		Providers: func(_ context.Context) []builder.ProviderTarget {
			return []builder.ProviderTarget{
				{Name: "healthy", Ready: true, MCPURL: healthy.URL},
				{Name: "hung", Ready: true, MCPURL: hung.URL},
			}
		},
	}

	// newServer runs discovery synchronously; time it to prove the hung
	// provider only cost ~one discovery deadline, not the client's 15s
	// call timeout (and not a serial sum).
	start := time.Now()
	cs, cleanup := connectInMemory(t, cfg)
	defer cleanup()
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("aggregate build took %s — a hung provider blocked it instead of timing out fast", elapsed)
	}

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	if !got["healthy__ping"] {
		t.Errorf("healthy provider's tool missing; the hung provider took the aggregate down. got tools: %v", toolNames(res.Tools))
	}
	for name := range got {
		if len(name) >= 6 && name[:6] == "hung__" {
			t.Errorf("hung provider unexpectedly contributed tool %q", name)
		}
	}
}

func toolNames(tools []*mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tl := range tools {
		out = append(out, tl.Name)
	}
	return out
}
