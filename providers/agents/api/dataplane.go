// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// The provider's whole tenant-facing surface, expressed as data-plane verbs on
// bound resources. Every verb is a kcp CUSTOM SUBRESOURCE — the APIExport
// declares "{resource}/{verb}" — reached like any other kube path on the kcp
// front door the caller holds a credential for:
//
//	/clusters/{clusterID}/apis/agents.railgrid.ai/v1alpha1/{resource}/{name}/{verb}[/{tail...}]
//
// There is no /api/* facade, no bespoke /s2s/* route and no hub-proxied
// spelling of a verb. kcp authenticates the caller, authorizes the HTTP method
// as the RBAC verb on the {resource}/{verb} noun, and reverse-proxies the
// request here with the caller's identity stamped in requestheader headers.
// provider-sdk/serve's subresource adapter is the one parser in front of this
// handler: it refuses a malformed path and an undeclared coordinate, reads the
// stamped identity, and hands both to the handler in the request context.
//
// What the handler then does is the contract's gate, run by provider-sdk/
// dataplane: the caller must be able to SEE the addressed object, decided with
// a SubjectAccessReview on the caller's behalf and then read AS THE PROVIDER
// through its APIExport virtual workspace. There is no caller bearer on a verb,
// so after the gate every handler acts as the provider: the client stashed on
// the request context is the provider's, scoped to the path's cluster, and any
// further question about the caller is dataplane.Authorize.
//
// That is why the service-to-service path could be deleted outright rather than
// ported: a ServiceAccount holding `create` on `agents/run` in the tenant's
// workspace passes exactly the same gate a human does, so the provider no
// longer runs its own TokenReview against a bespoke `agents/delegate`
// subresource, and no longer needs a cluster→workspace map to find the tenant
// behind a caller — the path names the cluster.
//
// Handlers below are the same ones the /api/* mux used to call. The router
// resolves the identity from the PATH and the stamped caller, and stashes it,
// together with the gated object and the provider client, on the request
// context; Server.identityFromRequest picks it up so no handler has to know
// how it was reached.

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/railgrid/provider-sdk/dataplane"

	agentsclient "github.com/railgrid/provider-agents/client"
)

// tailPolicy says what a verb does with the path segments after it.
type tailPolicy int

const (
	// tailNone: the verb addresses the object and nothing else.
	tailNone tailPolicy = iota
	// tailRequired: the verb needs exactly one more segment (a session id, an
	// inbox item id) which is not an object of its own.
	tailRequired
	// tailFree: the verb routes on its own tail (the runs family).
	tailFree
)

// verbRoute is one served verb.
type verbRoute struct {
	// methods are the HTTP methods this verb answers; anything else is 405.
	// kcp authorizes the method as the RBAC verb on {resource}/{verb}; a
	// grant on a verb coordinate is "*", so the method is this provider's to
	// police.
	methods []string
	tail    tailPolicy
	handler http.HandlerFunc
	// stream and readOnly mirror the verb's .stream / .readOnly in the
	// CatalogEntry's spec.export.resources[].verbs[]. They are carried here so
	// the declaration and the implementation are one edit apart and a test can
	// prove they agree.
	stream   bool
	readOnly bool
}

// resourceRoutes is one bound resource and the verbs served on it.
type resourceRoutes struct {
	gvr   schema.GroupVersionResource
	verbs map[string]verbRoute
}

// routes is the complete table. It is the single source of truth for what this
// provider serves, and it must stay identical to spec.export.resources[].verbs
// in manifest.yaml and deploy/chart/templates/catalogentry.yaml —
// TestDataPlaneVerbsMatchManifest fails the build when they drift, on the verbs
// and on the apiVersion/kind each resource binds them to. The manifest is ALSO
// what serve's adapter dispatches from
// (serve.SubresourcesFromCatalogEntryFile), so a verb missing from either side
// is never reached.
func (s *Server) routes() map[string]resourceRoutes {
	get := []string{http.MethodGet}
	post := []string{http.MethodPost}
	del := []string{http.MethodDelete}
	return map[string]resourceRoutes{
		"agents": {gvr: agentsclient.AgentGVR, verbs: map[string]verbRoute{
			"chat":          {methods: post, handler: s.chat, stream: true},
			"run":           {methods: post, handler: s.invokeAgentRun},
			"sessions":      {methods: get, handler: s.listSessions, readOnly: true},
			"session":       {methods: del, tail: tailRequired, handler: s.deleteSession},
			"messages":      {methods: get, handler: s.listMessages, readOnly: true},
			"usage":         {methods: get, handler: s.usageRollup, readOnly: true},
			"inbox":         {methods: get, handler: s.listInboxItems, readOnly: true},
			"inbox-resolve": {methods: post, tail: tailRequired, handler: s.resolveInboxItem},
			"events":        {methods: get, handler: s.streamEvents, stream: true, readOnly: true},
		}},
		// A model credential is an object, so its probes are verbs on IT
		// rather than on some agent that happens to use it. That is what makes
		// first-run work: a workspace with no agent yet can still save a
		// credential and ask whether it answers.
		"modelcredentials": {gvr: agentsclient.ModelCredentialGVR, verbs: map[string]verbRoute{
			"test":     {methods: post, handler: s.testModelCredential},
			"discover": {methods: post, handler: s.discoverModelCredential, readOnly: true},
		}},
		// Runs are objects now, so listing them, reading one and watching one are
		// Pillar 1 — the caller does that with a kube client and this provider
		// serves no route for it. What is left here is the half that is NOT on
		// the object: the Postgres-backed trace, and the two things that need
		// the executor.
		"runs": {gvr: agentsclient.RunGVR, verbs: map[string]verbRoute{
			"trace":  {methods: get, handler: s.runTrace, readOnly: true},
			"wait":   {methods: get, handler: s.waitRunHandler, readOnly: true},
			"cancel": {methods: post, handler: s.cancelRun},
		}},
		"connections": {gvr: agentsclient.ConnectionGVR, verbs: map[string]verbRoute{
			"test":           {methods: post, handler: s.testConnection},
			"enable-inbound": {methods: post, handler: s.enableInbound},
			"authorize":      {methods: post, handler: s.oauthAuthorize},
		}},
		"schedules": {gvr: agentsclient.ScheduleGVR, verbs: map[string]verbRoute{
			"run": {methods: post, handler: s.runScheduleNow},
		}},
		"triggers": {gvr: agentsclient.TriggerGVR, verbs: map[string]verbRoute{
			"run": {methods: post, handler: s.runTriggerNow},
		}},
	}
}

// DataPlane is the class-(a) handler serve.New dispatches every declared
// custom subresource to. It is reached only through serve's adapter, which has
// parsed the shard-forwarded path and put the route and the stamped caller in
// the request context; a request that arrives with no route did not come
// through the adapter and is refused, whatever its URL says.
func (s *Server) DataPlane() http.Handler {
	table := s.routes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := dataplane.RouteFrom(r.Context())
		if !ok || route.Group != agentsclient.AgentGVR.Group {
			dataplane.WriteError(w, dataplane.ErrBadPath)
			return
		}
		req := route.Request
		// No verb here hangs off a component; a component form is a path this
		// provider does not serve rather than one it serves differently.
		if req.Component != "" {
			dataplane.WriteError(w, dataplane.ErrBadPath)
			return
		}
		resource, ok := table[req.Resource]
		if !ok {
			// The adapter only dispatches declared coordinates, so this is
			// belt and braces: a verb this handler does not implement is not
			// served.
			http.NotFound(w, r)
			return
		}
		verb, ok := resource.verbs[req.Verb]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch verb.tail {
		case tailNone:
			if req.Tail != "" {
				dataplane.WriteError(w, dataplane.ErrBadPath)
				return
			}
		case tailRequired:
			if req.Tail == "" || strings.Contains(req.Tail, "/") {
				dataplane.WriteError(w, dataplane.ErrBadPath)
				return
			}
		case tailFree:
		}
		// Method is checked before the gate: a 405 is not a probe, the path
		// already told the caller the verb exists.
		if !slices.Contains(verb.methods, r.Method) {
			w.Header().Set("Allow", strings.Join(verb.methods, ", "))
			writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed",
				"this verb answers "+strings.Join(verb.methods, ", "))
			return
		}

		object, provider, err := dataplane.Gate(r.Context(), s.callers, resource.gvr, req)
		if err != nil {
			// The detail stays here: WriteError answers with the status text
			// alone so a refusal cannot be used to probe for objects.
			log.Printf("agents: data-plane %s %s refused: %v", r.Method, r.URL.Path, err)
			dataplane.WriteError(w, err)
			return
		}

		id := s.dataPlaneIdentity(r.Context(), req)
		r = r.WithContext(withGate(r.Context(), &gateInfo{
			request: req, object: object, provider: provider, identity: id,
		}))
		// The handlers predate the grammar and read their subject from path
		// values; keep that contract rather than rewriting sixteen signatures.
		r.SetPathValue("name", req.Name)
		r.SetPathValue("tail", req.Tail)
		verb.handler(w, r)
	})
}

// gateInfo is everything the router resolved for a gated request.
type gateInfo struct {
	request dataplane.Request
	// object is what the gate read, as the provider, once the caller's
	// visibility of it was settled. A handler acts on this object rather than
	// re-reading and trusting a second read.
	object *unstructured.Unstructured
	// provider is the client the gate returned: THIS PROVIDER, through its
	// APIExport virtual workspace, scoped to the path's cluster. There is no
	// caller credential on a verb, so it is the only client a handler has.
	provider dynamic.Interface
	// identity is the tenant context derived from the PATH plus the caller
	// kcp stamped.
	identity identity
}

type gateContextKey struct{}

func withGate(ctx context.Context, g *gateInfo) context.Context {
	return context.WithValue(ctx, gateContextKey{}, g)
}

// gateFrom returns what the data-plane router resolved, when the request came
// through it.
func gateFrom(ctx context.Context) (*gateInfo, bool) {
	g, ok := ctx.Value(gateContextKey{}).(*gateInfo)
	return g, ok && g != nil
}

// dataPlaneIdentity builds the tenant context for a gated request.
//
// The cluster comes from the PATH, which is authoritative: serve's adapter
// parsed it and set the addressing header from it. The user is the identity
// kcp stamped — the same one the gate decided with — and is used for labels
// and audit lines, never as a trust root. There is no bearer: a verb never
// carries one, so the org/workspace scope the store is keyed on is resolved
// per cluster (resolveClusterScope) rather than read as the caller.
func (s *Server) dataPlaneIdentity(ctx context.Context, req dataplane.Request) identity {
	id := identity{
		tenant:    req.ClusterID,
		clusterID: req.ClusterID,
	}
	if caller, ok := dataplane.ProxiedIdentityFrom(ctx); ok {
		id.user = caller.User
	}
	s.resolveClusterScope(ctx, &id)
	return id
}

// gatedRun is the Run object the gates read for this request, and the agent it
// belongs to.
//
// The agent comes off the OBJECT, never off the request: the caller addressed a
// run, and which agent that run belongs to is a fact of the run, not something
// a caller gets to assert. It is also what scopes the store lookup, so a caller
// who may cancel run X cannot read run Y's transcript by naming a different
// agent.
func gatedRunAgent(r *http.Request) (string, bool) {
	gate, ok := gateFrom(r.Context())
	if !ok || gate.object == nil {
		return "", false
	}
	agent, _, err := unstructured.NestedString(gate.object.Object, "spec", "agentRef")
	if err != nil || agent == "" {
		return "", false
	}
	return agent, true
}

// ---- the other Pillar 2 route classes ---------------------------------------
//
// These are not data-plane verbs and are deliberately NOT reachable as custom
// subresources: they are mounted by provider-sdk/serve as their own classes
// and are exported only so main can hand each one to the right Options field.

// OAuthCallback serves GET /oauth/callback — class (d). Anonymous by design:
// the signed state parameter is the authentication, because the identity
// provider redirects the browser here with no credential of ours attached.
func (s *Server) OAuthCallback(w http.ResponseWriter, r *http.Request) { s.oauthCallback(w, r) }

// ListOAuthProviders serves GET /oauth/providers — class (d). It reports which
// platform-wide OAuth apps the OPERATOR configured, which is a property of the
// deployment and not of any workspace, so it has no object to be a verb on.
func (s *Server) ListOAuthProviders(w http.ResponseWriter, r *http.Request) {
	s.listOAuthProviders(w, r)
}

// WebhookTrigger serves POST /webhooks/triggers/{cluster}/{name}/{token} —
// class (g). The HMAC token in the path is the entire credential: an external
// sender reaches this through the hub's anonymous forwarding and carries no
// tenant identity at all.
func (s *Server) WebhookTrigger(w http.ResponseWriter, r *http.Request) { s.webhookTrigger(w, r) }

// WebhookChannel serves POST /webhooks/channels/{cluster}/{name}/{token} —
// class (g), the inbound half of chat from Telegram/Slack/Discord.
func (s *Server) WebhookChannel(w http.ResponseWriter, r *http.Request) { s.webhookChannel(w, r) }
