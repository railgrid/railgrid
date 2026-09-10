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

package mcpaggregate

import (
	"context"
	"sort"
	"strings"

	"github.com/go-logr/logr"

	"github.com/faroshq/faros/pkg/hub/providers"
)

// OrgProviderRouter resolves how a hub-originated request reaches an org-owned
// provider's backend on behalf of a caller. The hub's backend proxy
// (*providers.ProviderProxy) implements it, so the aggregate reuses the exact
// edge hop and delegated-token substitution that /services/providers/{name}
// applies, rather than a second copy of that boundary.
type OrgProviderRouter interface {
	OrgProviderRoute(ctx context.Context, prov providers.Provider, caller providers.DelegatedCaller) (providers.OrgProviderRoute, error)
}

// RegistryEnumerator returns the ProviderEnumerator the hub wires into the
// aggregate endpoint and the MCPServer status controller.
//
// What a caller sees is the catalog of the Org its bearer was verified
// against, and nothing else:
//
//   - The provider set is reg.ListForOrg(caller.OrgUUID): every platform
//     provider plus that Org's own. Another Org's providers are never listed,
//     so their tools cannot appear and their backends are never contacted.
//     An empty OrgUUID lists the platform catalog only — never every org's.
//   - An org-owned provider shadows the platform provider of the same name
//     (ListForOrg drops the platform copy), matching what
//     /services/providers/{name} routes to for that Org. The two never both
//     register "<name>__*" tools, and when the org copy cannot be federated
//     for this caller the platform copy does NOT come back in its place: the
//     Org replaced it, so its tools would act on the wrong backend.
//   - A platform provider is federated exactly as before: its backend URL,
//     dialled directly with the caller's bearer.
//   - An org-owned provider is federated only through router: over the
//     platform edges tunnel, with a delegated token minted for the verified
//     (org, workspace, user) in place of the caller's bearer. Where no such
//     token can be minted — a ServiceAccount bearer (no human to delegate
//     for), an org-scope cluster (no team workspace to mint in), no issuer,
//     an unusable edge route — the provider is skipped for this request and
//     logged at V(1). It is never reached with the caller's bearer.
//
// Output is sorted by provider name so the aggregate's tool list is stable
// across requests.
func RegistryEnumerator(reg *providers.Registry, router OrgProviderRouter, log logr.Logger) ProviderEnumerator {
	return func(ctx context.Context, caller Caller) []ProviderTarget {
		if reg == nil {
			return nil
		}
		all := reg.ListForOrg(caller.OrgUUID)
		sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })

		out := make([]ProviderTarget, 0, len(all))
		for _, p := range all {
			if !p.Ready() || p.BackendURL == nil {
				continue
			}
			if p.OrgUUID == "" {
				// A provider's MCP transport is mounted at /mcp under its
				// backend URL (see providers/*/mcpserver).
				out = append(out, ProviderTarget{
					Name:        p.Name,
					DisplayName: p.DisplayName,
					MCPURL:      strings.TrimRight(p.BackendURL.String(), "/") + "/mcp",
				})
				continue
			}
			if t, ok := orgTarget(ctx, router, log, p, caller); ok {
				out = append(out, t)
			}
		}
		return out
	}
}

// orgTarget builds the federation target for one org-owned provider, or
// reports ok=false when this caller cannot reach it without the bearer.
func orgTarget(ctx context.Context, router OrgProviderRouter, log logr.Logger, p providers.Provider, caller Caller) (ProviderTarget, bool) {
	skip := func(reason string, kv ...any) (ProviderTarget, bool) {
		log.V(1).Info("provider federation: org-owned provider not federated for this caller",
			append([]any{"provider", p.Name, "org", p.OrgUUID, "reason", reason}, kv...)...)
		return ProviderTarget{}, false
	}
	switch {
	case p.OrgUUID != caller.OrgUUID:
		// ListForOrg never returns another Org's provider; refuse outright if
		// that ever stops being true rather than trust it.
		return skip("provider belongs to another organization")
	case caller.IsServiceAccount() || caller.User == "":
		return skip("caller is a ServiceAccount with no user to delegate for")
	case caller.WorkspaceUUID == "":
		return skip("org-scope cluster has no team workspace to mint a delegated token in")
	case router == nil:
		return skip("no org provider router wired")
	}
	route, err := router.OrgProviderRoute(ctx, p, providers.DelegatedCaller{
		User:          caller.User,
		OrgUUID:       caller.OrgUUID,
		WorkspaceUUID: caller.WorkspaceUUID,
	})
	if err != nil {
		return skip("no delegated route", "err", err.Error())
	}
	if route.Transport == nil || route.BaseURL == "" {
		return skip("delegated route is incomplete")
	}
	return ProviderTarget{
		Name:        p.Name,
		DisplayName: p.DisplayName,
		MCPURL:      strings.TrimRight(route.BaseURL, "/") + "/mcp",
		OrgUUID:     p.OrgUUID,
		Transport:   route.Transport,
	}, true
}
