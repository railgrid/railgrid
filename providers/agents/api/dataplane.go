// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

// The provider's whole tenant-facing surface, expressed as data-plane verbs on
// bound resources:
//
//	/dataplane/clusters/{clusterID}/{resource}/{name}/{verb}[/{tail...}]
//
// There is no /api/* facade any more and no bespoke /s2s/* route. Every call —
// a signed-in user in the portal, an MCP client, another provider, a cron job —
// arrives here and passes the same two gates from provider-sdk/dataplane, run
// as the CALLER:
//
//  1. a real GET of the addressed object in the path's cluster, which proves
//     visibility and hands back the object;
//  2. a SelfSubjectAccessReview for `create` on the virtual subresource
//     {resource}/{verb}, scoped to the object's name.
//
// That is why the service-to-service path could be deleted outright rather than
// ported: a ServiceAccount holding `create` on `agents/run` in the tenant's
// workspace passes exactly the same gates a human does, so the provider no
// longer runs its own TokenReview + SubjectAccessReview against a bespoke
// `agents/delegate` subresource, and no longer needs a cluster→workspace map in
// Postgres to find the tenant behind a caller.
//
// Handlers below are the same ones the /api/* mux used to call. The router
// resolves the identity from the PATH (never from a header — the header is only
// cross-checked by Gate) and stashes it, together with the gated object and the
// caller-scoped client, on the request context; Server.identityFromRequest
// picks it up so no handler has to know which route class it is serving.

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
	methods []string
	tail    tailPolicy
	handler http.HandlerFunc
	// stream and readOnly mirror dataPlane.verbs[].stream / .readOnly in the
	// CatalogEntry. They are carried here so the declaration and the
	// implementation are one edit apart and a test can prove they agree.
	stream   bool
	readOnly bool
}

// resourceRoutes is one bound resource and the verbs served on it.
type resourceRoutes struct {
	gvr   schema.GroupVersionResource
	verbs map[string]verbRoute
}

// routes is the complete table. It is the single source of truth for what this
// provider serves, and it must stay identical to spec.dataPlane.verbs in
// manifest.yaml and deploy/chart/templates/catalogentry.yaml — TestDataPlaneVerbsMatchManifest
// fails the build when they drift.
func (s *Server) routes() map[string]resourceRoutes {
	get := []string{http.MethodGet}
	post := []string{http.MethodPost}
	del := []string{http.MethodDelete}
	return map[string]resourceRoutes{
		"agents": {gvr: agentsclient.AgentGVR, verbs: map[string]verbRoute{
			"chat":           {methods: post, handler: s.chat, stream: true},
			"run":            {methods: post, handler: s.invokeAgentRun},
			"sessions":       {methods: get, handler: s.listSessions, readOnly: true},
			"session":        {methods: del, tail: tailRequired, handler: s.deleteSession},
			"messages":       {methods: get, handler: s.listMessages, readOnly: true},
			"usage":          {methods: get, handler: s.usageRollup, readOnly: true},
			"inbox":          {methods: get, handler: s.listInboxItems, readOnly: true},
			"inbox-resolve":  {methods: post, tail: tailRequired, handler: s.resolveInboxItem},
			"events":         {methods: get, handler: s.streamEvents, stream: true, readOnly: true},
			"model-test":     {methods: post, handler: s.testModelCredential},
			"model-discover": {methods: post, handler: s.discoverModelCredential, readOnly: true},
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

// DataPlane is the class-(a) handler serve.New mounts under /dataplane/. It
// receives the path exactly as the caller sent it, so ParseRequest — not an
// http.ServeMux — decides what ".." and "//" mean.
func (s *Server) DataPlane() http.Handler {
	table := s.routes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, ok := dataplane.ParseRequest(dataplane.DataplaneRoot, r)
		if !ok {
			dataplane.WriteError(w, dataplane.ErrBadPath)
			return
		}
		// No verb here hangs off a component; a component form is a path this
		// provider does not serve rather than one it serves differently.
		if req.Component != "" {
			dataplane.WriteError(w, dataplane.ErrBadPath)
			return
		}
		resource, ok := table[req.Resource]
		if !ok {
			dataplane.WriteError(w, dataplane.ErrBadPath)
			return
		}
		route, ok := resource.verbs[req.Verb]
		if !ok {
			dataplane.WriteError(w, dataplane.ErrBadPath)
			return
		}
		switch route.tail {
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
		// Method is checked before the gates: a 405 is not a probe, the path
		// already told the caller the verb exists.
		if !slices.Contains(route.methods, r.Method) {
			w.Header().Set("Allow", strings.Join(route.methods, ", "))
			writeStatus(w, http.StatusMethodNotAllowed, "MethodNotAllowed",
				"this verb answers "+strings.Join(route.methods, ", "))
			return
		}

		object, caller, err := dataplane.Gate(r.Context(), r, s.callers, resource.gvr, req)
		if err != nil {
			// The detail stays here: WriteError answers with the status text
			// alone so a refusal cannot be used to probe for objects.
			log.Printf("agents: data-plane %s %s refused: %v", r.Method, r.URL.Path, err)
			dataplane.WriteError(w, err)
			return
		}

		id := s.dataPlaneIdentity(r, req)
		r = r.WithContext(withGate(r.Context(), &gateInfo{
			request: req, object: object, caller: caller, identity: id,
		}))
		// The handlers predate the grammar and read their subject from path
		// values; keep that contract rather than rewriting sixteen signatures.
		r.SetPathValue("name", req.Name)
		r.SetPathValue("tail", req.Tail)
		route.handler(w, r)
	})
}

// gateInfo is everything the router resolved for a gated request.
type gateInfo struct {
	request dataplane.Request
	// object is what gate 1 read, as the caller. A handler that later acts with
	// the provider's own identity pins this object's UID or spec rather than
	// re-reading and trusting the second read.
	object *unstructured.Unstructured
	// caller is the caller-scoped dynamic client gate 1 used.
	caller dynamic.Interface
	// identity is the tenant context derived from the PATH plus the bearer.
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
// The cluster comes from the PATH, which is authoritative (Gate has already
// refused a request whose X-Railgrid-Cluster disagreed with it). That is what
// lets a caller with no hub-injected identity headers at all — another
// provider's ServiceAccount, a job — use the same route as a signed-in user.
func (s *Server) dataPlaneIdentity(r *http.Request, req dataplane.Request) identity {
	bearer, _, user, _ := dataplane.Identity(r)
	id := identity{
		tenant:    req.ClusterID,
		clusterID: req.ClusterID,
		user:      user,
		token:     bearer,
	}
	s.resolveWorkspace(r.Context(), &id)
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
// These are not data-plane verbs and are deliberately NOT reachable under
// /dataplane/: they are mounted by provider-sdk/serve as their own classes and
// are exported only so main can hand each one to the right Options field.

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
