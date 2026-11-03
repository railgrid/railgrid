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

package providers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/railgrid/provider-sdk/dataplane"
)

// ForwardShardAction is an in-process shard transport callback. The shard has
// already authorized the verb and selected the export from the APIBinding.
// Unlike the browser/MCP proxy it forwards no caller credential and does not
// mint a delegated bearer. The hub-signed action proof remains attached so a
// provider can redeem its narrowly scoped hub capability. The provider runs
// its ordinary SAR on the preserved
// shard-stamped identity, then acts as itself.
func (p *ProviderProxy) ForwardShardAction(r *http.Request, exportPath, exportName, exportCluster string) (*http.Response, error) {
	parts := strings.Split(exportPath, ":")
	if len(parts) != 6 || strings.Join(parts[:3], ":") != "root:railgrid:tenants" || parts[4] != "providers" {
		return nil, fmt.Errorf("invalid provider export path")
	}
	prov, ok := p.reg.BoundCopy(parts[3], parts[5], exportPath, exportName, exportCluster)
	if !ok || prov.APIExportPath != exportPath || prov.APIExportName != exportName || prov.BackendURL == nil {
		return nil, fmt.Errorf("provider export is not registered")
	}
	if r.URL.Scheme != prov.BackendURL.Scheme || r.URL.Host != prov.BackendURL.Host || prov.BackendURL.Path != "" && prov.BackendURL.Path != "/" {
		return nil, fmt.Errorf("provider action endpoint does not match its registration")
	}
	hop, err := p.resolveEdgeHop(prov)
	if err != nil {
		return nil, err
	}
	identity, err := dataplane.EncodeUpstreamIdentity(r.Header)
	if err != nil {
		return nil, err
	}
	out := r.Clone(r.Context())
	out.URL = hop.url(r.URL.Path)
	out.URL.RawQuery = r.URL.RawQuery
	out.Host = out.URL.Host
	// The outer request authenticates only as the hub to the platform Edge.
	// Never leak the user's bearer, shard bearer, cookies or impersonation headers.
	out.Header = make(http.Header)
	for _, key := range []string{"Content-Type", "Accept", "Connection", "Upgrade", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Protocol", dataplane.HeaderActionProof} {
		if values := r.Header.Values(key); len(values) > 0 {
			out.Header[key] = values
		}
	}
	out.Header.Set(dataplane.HeaderUpstreamIdentity, identity)
	return hop.transport.RoundTrip(out)
}
