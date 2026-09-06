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

package providers

import (
	"fmt"
	"strings"
)

// DelegationMode selects which providers receive a delegated ServiceAccount
// token in place of the caller's own hub bearer on the backend proxy.
//
// Org-owned providers are never a question of mode: they always receive the
// delegated token (see serveOverEdge). The mode decides what a PLATFORM
// provider — one in root:faros:providers, dialled directly by the hub — gets.
type DelegationMode string

const (
	// DelegationOff forwards the caller's hub bearer to platform providers
	// as-is. This is the historical behaviour and the default for this
	// release.
	DelegationOff DelegationMode = "off"
	// DelegationPlatform swaps the bearer for a delegated token on every
	// platform provider except those listed in DelegationPolicy.Exclude.
	DelegationPlatform DelegationMode = "platform"
	// DelegationAll swaps the bearer on every platform provider and ignores
	// the exclusion list. For deployments that have verified — or do not
	// run — the providers the default exclusion covers.
	DelegationAll DelegationMode = "all"
)

// ParseDelegationMode accepts the flag spelling of a DelegationMode. An empty
// string is DelegationOff so an unset option behaves like the default.
func ParseDelegationMode(s string) (DelegationMode, error) {
	switch m := DelegationMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return DelegationOff, nil
	case DelegationOff, DelegationPlatform, DelegationAll:
		return m, nil
	default:
		return "", fmt.Errorf("unknown provider delegated-tokens mode %q (want off, platform, or all)", s)
	}
}

// DefaultDelegationExclude is the built-in exclusion list for
// DelegationPlatform: platform providers whose backend cannot act on a
// workspace-scoped ServiceAccount token. See docs/provider-scoping.md
// "Delegated tokens" for the per-provider verdicts behind this list.
//
// edges: its SSH data plane resolves the caller's identity with a TokenReview
// and, for an edge with spec.sshUserMapping=identity, uses the resulting
// username as the Linux user to log in as
// (providers/edges/internal/tunnel/edges_proxy_builder.go fetchSSHCredentials).
// A delegated token resolves to system:serviceaccount:default:faros-du-<hash>,
// which is not the human's account and is not a login name on any host, so the
// session would be refused or land on the wrong user. Everything else in edges
// — the tunnel, the k8s subresource, the Service proxy — works with a
// delegated token and already receives one on the org-owned provider path
// (serveOverEdge). Lifting this needs the SSH path to take its identity from
// X-Faros-User (which the hub still sends) rather than from the bearer.
var DefaultDelegationExclude = []string{EdgesProviderName}

// DelegationPolicy is the backend proxy's answer to "does this platform
// provider get the caller's bearer or a delegated token?".
type DelegationPolicy struct {
	Mode DelegationMode
	// Exclude names platform providers that keep receiving the caller's
	// bearer under DelegationPlatform. Ignored under DelegationAll. Matching
	// is exact on the provider name.
	Exclude []string
}

// DelegatesPlatform reports whether the platform provider name should have the
// caller's bearer replaced with a delegated token.
func (pol DelegationPolicy) DelegatesPlatform(name string) bool {
	switch pol.Mode {
	case DelegationAll:
		return true
	case DelegationPlatform:
		for _, excluded := range pol.Exclude {
			if strings.TrimSpace(excluded) == name {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// SetDelegationPolicy installs the platform-provider delegation policy. Wire
// alongside SetDelegatedTokenIssuer; without a call the proxy stays at
// DelegationOff. Only the backend proxy consults it — the UI proxy carries no
// credential to substitute.
func (p *ProviderProxy) SetDelegationPolicy(pol DelegationPolicy) {
	p.delegation = pol
}
