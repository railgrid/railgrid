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

package servicectrl

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/faroshq/provider-sdk/revdial"
)

// ConnManager is the subset of the tunnel ConnManager the reconcilers need.
// *tunnel.ConnManager satisfies it structurally.
type ConnManager interface {
	Load(key string) (*revdial.Dialer, bool)
	HasConnection(key string) bool
}

// connKey mirrors edgeConnKey in the tunnel package: "{resource}/{cluster}/{name}".
func connKey(resource, cluster, name string) string {
	return resource + "/" + cluster + "/" + name
}

// discoveredService mirrors pkg/agent/discovery.DiscoveredService (wire format).
type discoveredService struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Scheme      string `json:"scheme"`
	Port        int32  `json:"port"`
	Version     string `json:"version,omitempty"`
	InstallType string `json:"installType,omitempty"`
}

// fetchServices pulls the agent's discovered services by GETting /api/v1/services
// over the reverse tunnel.
func fetchServices(ctx context.Context, dialer *revdial.Dialer) ([]discoveredService, error) {
	conn, err := dialer.Dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("dialing edge agent: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://edge-agent/api/v1/services", nil)
	if err != nil {
		return nil, err
	}
	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("writing request to tunnel: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, fmt.Errorf("reading response from tunnel: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent /api/v1/services returned %d", resp.StatusCode)
	}
	var out struct {
		Services []discoveredService `json:"services"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding services: %w", err)
	}
	return out.Services, nil
}
