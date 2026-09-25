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

package mcpaggregate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-logr/logr"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// capFixture is a hub whose registry holds two platform providers that
// declare a contract, plus one that declares nothing, plus one belonging to
// another Org. Each provider's /mcp answers tools/list with tools that claim
// coordinates — some real, some not — so one fixture covers the resource, the
// _meta stamping and the refusal of an unbacked claim.
type capFixture struct {
	handler http.Handler
}

// capTool is one tool a fake provider advertises, with the raw _meta it
// attaches. A nil meta means the provider claims no coordinate.
type capTool struct {
	name string
	meta map[string]any
}

func serveCapMCP(w http.ResponseWriter, r *http.Request, tools []capTool) {
	var req struct {
		Method string `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	w.Header().Set("Content-Type", "application/json")
	if req.Method != "tools/list" {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		return
	}
	list := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		entry := map[string]any{"name": t.name, "inputSchema": map[string]any{"type": "object"}}
		if t.meta != nil {
			entry["_meta"] = t.meta
		}
		list = append(list, entry)
	}
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": list}})
	_, _ = w.Write(out)
}

func newCapFixture(t *testing.T) *capFixture {
	t.Helper()
	backend := func(tools []capTool) *url.URL {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveCapMCP(w, r, tools)
		}))
		t.Cleanup(srv.Close)
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatalf("parse %q: %v", srv.URL, err)
		}
		return u
	}

	codeURL := backend([]capTool{
		// Backs a declared action.
		{name: "list_branches", meta: map[string]any{"railgrid": map[string]any{"action": "branches/v1"}}},
		// Claims an action this provider does not declare.
		{name: "wire_money", meta: map[string]any{"railgrid": map[string]any{"action": "payouts/v9"}}},
		// Claims nothing.
		{name: "list_connections"},
	})
	infraURL := backend([]capTool{
		// Backs a declared verb.
		{name: "dev_logs", meta: map[string]any{"railgrid": map[string]any{"verb": "instances/log"}}},
		// Claims a coordinate that belongs to a DIFFERENT provider.
		{name: "borrowed", meta: map[string]any{"railgrid": map[string]any{"action": "branches/v1"}}},
		// Malformed claim: not a string.
		{name: "junk", meta: map[string]any{"railgrid": map[string]any{"verb": 7}}},
	})
	plainURL := backend([]capTool{{name: "ping"}})
	otherOrgURL := backend([]capTool{{name: "invoice"}})

	reg := providers.NewRegistry()
	reg.Upsert(providers.Provider{
		Name: "code", DisplayName: "Code", BackendURL: codeURL, EndpointsValid: true,
		Actions: []providers.ProviderAction{{
			Name: "branches", Version: "v1",
			DisplayName: "List branches", Description: "List a bounded page of branch names.",
			Resource: providers.ProviderActionResource{
				APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository", Resource: "repositories",
			},
			ReadOnly:     true,
			Risk:         providersv1alpha1.ProviderActionRiskLow,
			SchemaDigest: "sha256:cafe",
			Consent:      providersv1alpha1.ProviderActionConsent{Required: false},
			// Limits and compiled validators must NOT reach the document.
			Limits:      providers.ProviderActionLimits{TimeoutSeconds: 30, MaxInputBytes: 4096},
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	reg.Upsert(providers.Provider{
		Name: "infrastructure", DisplayName: "Infrastructure", BackendURL: infraURL, EndpointsValid: true,
		Export: &providersv1alpha1.ProviderExport{
			Name: "infrastructure.providers.railgrid.ai",
			Resources: []providersv1alpha1.ProviderExportResource{{
				Name: "instances", APIVersion: "infrastructure.railgrid.ai/v1alpha1", Kind: "Instance",
				Verbs: []providersv1alpha1.ProviderVerb{
					{Name: "log", Description: "Stream the instance's logs.", Stream: true, ReadOnly: true},
					{Name: "exec", Description: "Run a command in the instance.", Stream: true},
				},
			}},
		},
	})
	// Declares neither an action nor a verb: it federates its tools but has
	// nothing to publish, so it must not appear as an empty entry.
	reg.Upsert(providers.Provider{Name: "plain", BackendURL: plainURL, EndpointsValid: true})
	// Another Org's provider: never visible to org A, contract included.
	reg.Upsert(providers.Provider{
		Name: "billing", OrgUUID: orgB, BackendURL: otherOrgURL, EndpointsValid: true,
		Actions: []providers.ProviderAction{{Name: "invoice", Version: "v1"}},
	})

	return &capFixture{handler: New(Options{
		Providers: RegistryEnumerator(reg, providers.NewBackendProxy(reg, logr.Discard()), logr.Discard()),
		Verifier: BearerVerifierFunc(func(_ *http.Request, token, _, _ string) (Caller, error) {
			c, ok := callers[token]
			if !ok {
				return Caller{}, ErrUnauthenticated
			}
			return c, nil
		}),
	})}
}

// readCapabilities reads railgrid://providers/capabilities and decodes it.
func readCapabilities(t *testing.T, h http.Handler, bearer string) capabilitiesDoc {
	t.Helper()
	raw := mcpCall(t, h, bearer, "resources/read", `{"uri":"`+CapabilitiesResourceURI+`"}`)
	var res struct {
		Contents []struct {
			URI      string `json:"uri"`
			MIMEType string `json:"mimeType"`
			Text     string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode resources/read: %v (body=%s)", err, raw)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents = %d entries, want 1", len(res.Contents))
	}
	if res.Contents[0].URI != CapabilitiesResourceURI {
		t.Fatalf("content URI = %q, want %q", res.Contents[0].URI, CapabilitiesResourceURI)
	}
	if res.Contents[0].MIMEType != "application/json" {
		t.Fatalf("content MIME type = %q, want application/json", res.Contents[0].MIMEType)
	}
	var doc capabilitiesDoc
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &doc); err != nil {
		t.Fatalf("decode capability document: %v (text=%s)", err, res.Contents[0].Text)
	}
	return doc
}

func TestCapabilitiesResourceIsListed(t *testing.T) {
	f := newCapFixture(t)
	var out struct {
		Resources []struct {
			URI      string `json:"uri"`
			Name     string `json:"name"`
			MIMEType string `json:"mimeType"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(mcpCall(t, f.handler, "alice-a", "resources/list", `{}`), &out); err != nil {
		t.Fatalf("decode resources/list: %v", err)
	}
	var found bool
	for _, r := range out.Resources {
		if r.URI == CapabilitiesResourceURI {
			found = true
			if r.Name != "railgrid-provider-capabilities" {
				t.Errorf("resource name = %q", r.Name)
			}
		}
	}
	if !found {
		t.Fatalf("resources/list does not offer %s: %+v", CapabilitiesResourceURI, out.Resources)
	}
}

func TestCapabilitiesResourceProjectsTheRegistry(t *testing.T) {
	f := newCapFixture(t)
	doc := readCapabilities(t, f.handler, "alice-a")

	if doc.Tenant != "some-cluster" {
		t.Errorf("tenant = %q, want some-cluster", doc.Tenant)
	}
	if doc.MCPServer != "default" {
		t.Errorf("mcpServer = %q, want default", doc.MCPServer)
	}

	names := make([]string, 0, len(doc.Providers))
	for _, p := range doc.Providers {
		names = append(names, p.Provider)
	}
	// "plain" declares nothing, so it is omitted; "billing" is another Org's.
	// Order follows enumeration, which is sorted by provider name.
	if got, want := strings.Join(names, ","), "code,infrastructure"; got != want {
		t.Fatalf("providers = %q, want %q", got, want)
	}

	code := doc.Providers[0]
	if code.DisplayName != "Code" {
		t.Errorf("code displayName = %q", code.DisplayName)
	}
	if len(code.Actions) != 1 || len(code.Verbs) != 0 {
		t.Fatalf("code = %d actions / %d verbs, want 1/0", len(code.Actions), len(code.Verbs))
	}
	a := code.Actions[0]
	want := DeclaredAction{
		ID: "branches/v1", Name: "branches", Version: "v1",
		DisplayName: "List branches", Description: "List a bounded page of branch names.",
		BoundResource: DeclaredBoundResource{
			APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository", Resource: "repositories",
		},
		ReadOnly: true, Risk: "low", SchemaDigest: "sha256:cafe",
		Consent: DeclaredConsent{Required: false},
	}
	if a != want {
		t.Errorf("action = %+v, want %+v", a, want)
	}

	infra := doc.Providers[1]
	if len(infra.Actions) != 0 || len(infra.Verbs) != 2 {
		t.Fatalf("infrastructure = %d actions / %d verbs, want 0/2", len(infra.Actions), len(infra.Verbs))
	}
	// Sorted by coordinate: exec before log.
	if got := infra.Verbs[0]; got != (DeclaredVerb{
		Coordinate: "instances/exec", Resource: "instances", Verb: "exec",
		Description: "Run a command in the instance.", Stream: true,
	}) {
		t.Errorf("verb[0] = %+v", got)
	}
	if got := infra.Verbs[1]; got != (DeclaredVerb{
		Coordinate: "instances/log", Resource: "instances", Verb: "log",
		Description: "Stream the instance's logs.", Stream: true, ReadOnly: true,
	}) {
		t.Errorf("verb[1] = %+v", got)
	}
}

// The document is discovery metadata, not a directory: nothing in it may be
// dialable, and none of the registry's execution machinery may leak.
func TestCapabilitiesResourceCarriesNoURLsOrLimits(t *testing.T) {
	f := newCapFixture(t)
	raw := mcpCall(t, f.handler, "alice-a", "resources/read", `{"uri":"`+CapabilitiesResourceURI+`"}`)
	var res struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	text := res.Contents[0].Text
	for _, forbidden := range []string{"http://", "https://", "127.0.0.1", "/mcp", "timeout", "maxInput", "inputSchema", "Bearer"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("capability document contains %q:\n%s", forbidden, text)
		}
	}
}

// The document is scoped exactly the way federation is: a caller only ever
// reads the declared contract of a provider its own Org can reach. Another
// Org's provider is never listed, and an org-owned provider this caller has
// no delegated route to is not published either — publishing a coordinate
// nobody here can reach would be an invitation to a 403.
func TestCapabilitiesResourceIsScopedToTheCallersOrg(t *testing.T) {
	f := newCapFixture(t)
	// billing belongs to Org B and is reached only over a delegated edge
	// route, which this fixture wires for nobody.
	for _, bearer := range []string{"alice-a", "bob-b"} {
		if got := capNames(readCapabilities(t, f.handler, bearer)); strings.Contains(got, "billing") {
			t.Errorf("capabilities as %q = %q, want no billing", bearer, got)
		}
	}
	// A ServiceAccount bearer outside the tenants tree reads the platform
	// catalog, never any Org's.
	if got := capNames(readCapabilities(t, f.handler, "platform")); got != "code,infrastructure" {
		t.Errorf("platform capabilities = %q, want code,infrastructure", got)
	}
}

func capNames(doc capabilitiesDoc) string {
	names := make([]string, 0, len(doc.Providers))
	for _, p := range doc.Providers {
		names = append(names, p.Provider)
	}
	return strings.Join(names, ",")
}

// toolMeta returns the _meta of one aggregate tool, or nil.
func toolMetaOf(t *testing.T, h http.Handler, bearer, name string) map[string]any {
	t.Helper()
	var out struct {
		Tools []struct {
			Name string         `json:"name"`
			Meta map[string]any `json:"_meta"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(mcpCall(t, h, bearer, "tools/list", `{}`), &out); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	for _, tl := range out.Tools {
		if tl.Name == name {
			return tl.Meta
		}
	}
	t.Fatalf("tool %q not in aggregate tool list", name)
	return nil
}

func TestFederatedToolCarriesDeclaredActionMeta(t *testing.T) {
	f := newCapFixture(t)
	meta := toolMetaOf(t, f.handler, "alice-a", "code__list_branches")
	ns, ok := meta["railgrid"].(map[string]any)
	if !ok {
		t.Fatalf("_meta = %+v, want a railgrid entry", meta)
	}
	if ns["provider"] != "code" {
		t.Errorf("_meta.railgrid.provider = %v, want code", ns["provider"])
	}
	action, ok := ns["action"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.railgrid.action = %v, want an object", ns["action"])
	}
	if action["id"] != "branches/v1" {
		t.Errorf("action.id = %v, want branches/v1", action["id"])
	}
	if action["schemaDigest"] != "sha256:cafe" {
		t.Errorf("action.schemaDigest = %v, want sha256:cafe", action["schemaDigest"])
	}
	if action["readOnly"] != true {
		t.Errorf("action.readOnly = %v, want true", action["readOnly"])
	}
	if _, ok := ns["verb"]; ok {
		t.Errorf("_meta.railgrid carries both an action and a verb: %+v", ns)
	}
}

func TestFederatedToolCarriesDeclaredVerbMeta(t *testing.T) {
	f := newCapFixture(t)
	meta := toolMetaOf(t, f.handler, "alice-a", "infrastructure__dev_logs")
	ns, ok := meta["railgrid"].(map[string]any)
	if !ok {
		t.Fatalf("_meta = %+v, want a railgrid entry", meta)
	}
	verb, ok := ns["verb"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.railgrid.verb = %v, want an object", ns["verb"])
	}
	if verb["coordinate"] != "instances/log" {
		t.Errorf("verb.coordinate = %v, want instances/log", verb["coordinate"])
	}
	if verb["stream"] != true || verb["readOnly"] != true {
		t.Errorf("verb = %+v, want stream and readOnly true", verb)
	}
	// A verb is a coordinate, not a contract: no digest is invented for it.
	if _, ok := verb["schemaDigest"]; ok {
		t.Errorf("verb carries a schemaDigest: %+v", verb)
	}
}

// A provider cannot advertise a contract the hub never admitted: an
// undeclared coordinate, another provider's coordinate, and a malformed
// claim all produce no _meta at all.
func TestUnbackedCoordinateClaimIsDropped(t *testing.T) {
	f := newCapFixture(t)
	for _, tool := range []string{
		"code__wire_money",         // action this provider does not declare
		"infrastructure__borrowed", // another provider's action
		"infrastructure__junk",     // malformed claim
		"code__list_connections",   // no claim at all
		"plain__ping",              // provider declares nothing
	} {
		if meta := toolMetaOf(t, f.handler, "alice-a", tool); len(meta) != 0 {
			t.Errorf("tool %s carries _meta %+v, want none", tool, meta)
		}
	}
}

// Unit-level: capabilitiesFor is what the resource serves, and it decides
// nothing about visibility — it projects exactly the targets it is given.
func TestCapabilitiesForOmitsEmptyProviders(t *testing.T) {
	doc := capabilitiesFor("c1", "srv", []ProviderTarget{
		{Name: "empty"},
		{Name: "b", Verbs: []DeclaredVerb{{Coordinate: "x/y", Resource: "x", Verb: "y"}}},
		{Name: "a", Actions: []DeclaredAction{{ID: "z/v1"}}},
	})
	if got := capNames(doc); got != "b,a" {
		t.Fatalf("providers = %q, want b,a (enumeration order preserved)", got)
	}
	// A provider with only verbs still serializes an empty actions array, so
	// clients never have to distinguish null from absent.
	payload, err := json.Marshal(doc.Providers[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(payload), `"actions":[]`) {
		t.Errorf("provider without actions serialized as %s", payload)
	}
}
