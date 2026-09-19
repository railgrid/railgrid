/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
)

// greetVerb is the one verb this provider serves. It is also, verbatim, the
// subresource the hub grants: a consumer's RBAC rule is
// `create` on `greetings/greet`.
const greetVerb = "greet"

// greetLimits bound the verb. A data-plane verb declares its own limits rather
// than inheriting a server-wide default, so a slow or chatty verb cannot take
// the whole provider down with it.
var greetLimits = dataplane.Limits{
	Timeout:        10 * time.Second,
	MaxInputBytes:  4 << 10,
	MaxOutputBytes: 16 << 10,
}

// greetResult is the verb's response body, wrapped by the actionwire envelope.
type greetResult struct {
	Greeting string `json:"greeting"`
}

// ServeHTTP serves
//
//	POST /dataplane/clusters/{clusterID}/greetings/{name}/greet
//
// and is the one place a new provider author sees the two gates written out.
// Read it top to bottom; everything a data-plane verb must do is here and
// nothing else is.
func (s *dataPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Parse. The grammar is the contract's, not ours: ParseRequest refuses
	//    a traversal segment, a percent-encoded separator, a workspace path
	//    where a logical-cluster ID belongs, and the legacy `apis` dialect. A
	//    route that does not match falls through to a 400 rather than being
	//    reinterpreted.
	req, ok := dataplane.ParseRequest(dataplane.DataplaneRoot, r)
	if !ok || req.Resource != s.deps.Greetings.Resource || req.Component != "" || req.Tail != "" {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}

	// 2. The two gates, run AS THE CALLER with the caller's own bearer. The
	//    provider's identity is not involved: it lends only the hub's address
	//    and CA (see dataplane.NewCallerFactory in main.go).
	//
	//    Gate 1 is a real GET of greetings/{name} in {clusterID}. It proves
	//    the caller can see this object — which is what makes "workspace A's
	//    token cannot greet workspace B's Greeting" true — and it hands the
	//    object back, so the verb reads spec from what the CALLER could see
	//    rather than from a second, unauthorized read.
	//
	//    Gate 2 is a SelfSubjectAccessReview for `create` on the virtual
	//    subresource greetings/{verb}, scoped to {name}. `create` is not
	//    negotiable: the hub materializes every data-plane grant as exactly
	//    that rule, so any other verb string silently breaks workload
	//    identities.
	//
	//    Note the gates run before we check which verb was asked for. An
	//    unimplemented verb and an ungranted one then look identical to a
	//    caller, so the route cannot be used to enumerate what a provider can
	//    do.
	greeting, _, err := dataplane.Gate(r.Context(), r, s.deps.Callers, s.deps.Greetings, req)
	if err != nil {
		// The detail stays here. WriteError answers with the status text and
		// nothing else, so a denial cannot tell a caller whether the object
		// exists.
		log.Printf("greet refused: %v", err)
		dataplane.WriteError(w, err)
		return
	}
	if req.Verb != greetVerb {
		dataplane.WriteError(w, dataplane.ErrDenied)
		return
	}

	// 3. Serve. dataplane.Serve owns the rest of the response path: POST only,
	//    the input limit, a strict {"input": …} body, the timeout, the output
	//    limits, and the actionwire envelope on success and on failure alike.
	//    Write nothing to w after this point.
	_, _, user, _ := dataplane.Identity(r)
	env := actionwire.New(r, "quickstart", req.Verb, actionwire.ResourceRef{
		APIVersion: s.deps.Greetings.GroupVersion().String(),
		Kind:       "Greeting",
		Resource:   s.deps.Greetings.Resource,
		Name:       req.Name,
	})
	dataplane.Serve(w, r, env, greetLimits, func(_ context.Context, _ json.RawMessage) (any, *actionwire.Error) {
		return greet(greeting, user)
	})
}

// greet is the whole of the verb: render the message the tenant stored on the
// object for the caller the hub authenticated. It takes the object gate 1
// returned, never a fresh read — the point of gate 1 handing the object back is
// that the verb acts on what the caller was entitled to see.
func greet(greeting *unstructured.Unstructured, user string) (any, *actionwire.Error) {
	message, _, err := unstructured.NestedString(greeting.Object, "spec", "message")
	if err != nil || message == "" {
		// A greeting with no message is not a server fault; it is a Greeting
		// whose Ready condition already says so. Not retryable.
		return nil, &actionwire.Error{
			Code:    "message_empty",
			Message: "greeting has no spec.message yet",
		}
	}
	if user == "" {
		// X-Railgrid-User is addressing and labelling material only. It is
		// absent when the provider is reached without the hub in front, and
		// that is a cosmetic difference, never an authorization one.
		user = "anonymous"
	}
	return greetResult{Greeting: message + ", " + user}, nil
}
