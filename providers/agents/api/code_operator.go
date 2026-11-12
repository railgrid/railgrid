// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/identityclient"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/tools"
)

// Code's API coordinates. They are the contract between the two providers.
const (
	codeAPIGroup          = "code.railgrid.ai"
	codeAPIVersion        = "v1alpha1"
	codePullRequests      = "pullrequests"
	codeOperateVerb       = "operate"
	codeResponseBodyLimit = 128 << 10
)

// codeOperator calls Code as the hub-minted identity of one Run.
//
// Agents used to call Code as itself, through a permission claim on its own
// APIExport. A claim on another provider's group is also a dependency edge,
// so the hub refused to enable Agents in any workspace without Code. Each call
// now mints (or refreshes) an identity owned by the Run, with get and operate
// on the one PullRequest only. Where Code is not enabled the hub refuses the
// mint, and only the Code tool fails.
//
// The identity is owned by the Run, not the Agent. One Agent can run against
// several PullRequests at once, and every rule must name its resource, so a
// per-Agent identity would have to rewrite its rules for each PullRequest.
// The hub collects the identity when the Run object is deleted.
type codeOperator struct {
	clusterID string
	runID     string
	// unavailable, when set, is returned by every call.
	unavailable error
	runs        func(context.Context, string) (dynamic.Interface, error)
	mint        func(context.Context, identityclient.Request) (*identityclient.Token, error)
	origin      string
	client      *http.Client
}

// codeOperatorFor returns the Code caller for one Run, or nil for a run with
// no workspace or Run ID, which cannot carry a Run identity.
func (s *Server) codeOperatorFor(clusterID, runID string) tools.CodeOperator {
	if clusterID == "" || runID == "" {
		return nil
	}
	op := &codeOperator{clusterID: clusterID, runID: runID}
	if s.bg == nil || s.bg.identities == nil || s.bg.identities.client == nil {
		op.unavailable = errors.New("this provider has no identity client, and Code operations act as the Run's own hub-minted identity")
		return op
	}
	if s.providerBase == nil {
		op.unavailable = errors.New("this provider has no kubeconfig of its own (RAILGRID_PROVIDER_KUBECONFIG), so it cannot reach Code through the hub")
		return op
	}
	origin, err := frontDoor(s.providerBase.Host)
	if err != nil {
		op.unavailable = err
		return op
	}
	// Keep the provider's TLS settings, but none of its credentials: the
	// request carries only the Run's token.
	client, err := rest.HTTPClientFor(rest.AnonymousClientConfig(s.providerBase))
	if err != nil {
		op.unavailable = fmt.Errorf("building the Code client: %w", err)
		return op
	}
	op.runs = s.bg.scoped
	op.mint = s.bg.identities.client.Ensure
	op.origin = origin
	op.client = client
	return op
}

// codeRules is everything a Run's Code identity may do: see the one
// PullRequest (dataplane.Gate checks get on it) and call its operate verb.
// The hub admits the first as clause B and the second as clause C, both only
// where Code is enabled in the workspace.
func codeRules(pullRequest string) []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{{
		APIGroups:     []string{codeAPIGroup},
		Resources:     []string{codePullRequests},
		ResourceNames: []string{pullRequest},
		Verbs:         []string{"get"},
	}, {
		APIGroups:     []string{codeAPIGroup},
		Resources:     []string{codePullRequests + "/" + codeOperateVerb},
		ResourceNames: []string{pullRequest},
		Verbs:         []string{"create"},
	}}
}

func (o *codeOperator) Operate(ctx context.Context, pullRequest string, input map[string]any) (string, error) {
	if o.unavailable != nil {
		return "", o.unavailable
	}
	dyn, err := o.runs(ctx, o.clusterID)
	if err != nil {
		return "", fmt.Errorf("reaching workspace %s for this Run's Code identity: %w", o.clusterID, err)
	}
	run, err := dyn.Resource(agentsclient.RunGVR).Get(ctx, o.runID, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading Run %s, whose own identity Code operations act as: %w", o.runID, err)
	}
	gv := agentsv1alpha1.SchemeGroupVersion
	token, err := o.mint(ctx, identityclient.Request{
		Owner: identityclient.Owner{
			Kind: "Run", Group: gv.Group, Version: gv.Version, Resource: agentsclient.RunGVR.Resource,
			Name: run.GetName(), UID: string(run.GetUID()), ClusterID: o.clusterID,
		},
		ClusterID: o.clusterID,
		Rules:     codeRules(pullRequest),
	})
	if err != nil {
		return "", fmt.Errorf("minting this Run's Code identity: %w", err)
	}
	if token == nil || token.Token == "" {
		return "", errors.New("the hub returned an empty Code identity for this Run")
	}

	input["invocationRun"] = run.GetName()
	input["invocationRunUID"] = string(run.GetUID())
	inner, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]json.RawMessage{"input": inner})
	if err != nil {
		return "", err
	}
	path, err := dataplane.SubresourcePath(codeAPIGroup, codeAPIVersion, dataplane.Request{
		ClusterID: o.clusterID, Resource: codePullRequests, Name: pullRequest, Verb: codeOperateVerb,
	})
	if err != nil {
		return "", fmt.Errorf("addressing Code pull request %q: %w", pullRequest, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.origin+path, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.Token)
	response, err := o.client.Do(req)
	if err != nil {
		return "", errors.New("the Code operation could not reach Code")
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, codeResponseBodyLimit))
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("refused by Code: %s", body)
	}
	return string(body), nil
}
