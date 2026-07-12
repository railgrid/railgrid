/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// GraphQL-backed tenant access. App Studio reaches a tenant's kcp workspace
// through the hub's embedded GraphQL gateway at <hubBase>/graphql/<clusterID>,
// authenticating as the caller. Unlike the hub's kcp user-proxy (which gates
// every request to the caller's DefaultCluster), the gateway serves any
// workspace the caller has RBAC in, so App Studio works in non-default
// workspaces.
//
// Every operation exchanges whole serialized objects (the gateway's <Kind>Yaml
// / <Plural>Yaml queries and applyYaml / applyStatusYaml mutations), so this
// client never needs a typed GraphQL field-selection set over App Studio's
// unstructured resources. GraphQL errors are mapped back onto k8s apierrors so
// callers' IsNotFound / IsConflict / IsAlreadyExists checks keep working.
package tenant

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// GraphQLClient is a factory for per-(cluster, caller) GraphQL access to the
// hub's embedded gateway.
type GraphQLClient struct {
	hubBase string
	http    *http.Client
}

// NewGraphQLClient targets the hub's GraphQL gateway under hubBase (the hub's
// base URL, e.g. https://kedge-hub.kedge.svc:9443). insecureSkipVerify relaxes
// TLS for in-cluster hub certs that aren't in the provider's trust store.
func NewGraphQLClient(hubBase string, insecureSkipVerify bool) *GraphQLClient {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for in-cluster hub cert
	}
	return &GraphQLClient{
		hubBase: strings.TrimRight(hubBase, "/"),
		http:    &http.Client{Transport: tr},
	}
}

// Resource identifies a kcp resource for GraphQL field-name derivation. The
// gateway nests fields as <sanitizedGroup>.<version>.<field>, with field names
// derived from the Kind (singular) and its pluralization.
type Resource struct {
	GVR        schema.GroupVersionResource
	Kind       string // e.g. "Project" — GraphQL singular field + create/update/delete suffix
	Plural     string // e.g. "Projects" — GraphQL list field
	Namespaced bool
}

var (
	invalidGroupChar = regexp.MustCompile(`[^a-zA-Z0-9_]`)
	validGroupStart  = regexp.MustCompile(`^[a-zA-Z_]`)
)

// groupField mirrors the gateway's SanitizeGroupName: non-[a-zA-Z0-9_] become
// "_", and a leading invalid char gets an "_" prefix. The core group ("")
// returns "" — its versions sit directly on the root, with no group wrapper.
func groupField(group string) string {
	s := invalidGroupChar.ReplaceAllString(group, "_")
	if s != "" && !validGroupStart.MatchString(s) {
		s = "_" + s
	}
	return s
}

// wrapSelection nests the inner field selection under the resource's group and
// version, and returns the matching response-data path. The core group ("")
// has no group wrapper — its version sits directly on the root query/mutation.
func wrapSelection(res Resource, inner string) (selection string, path []string) {
	gf := groupField(res.GVR.Group)
	v := res.GVR.Version
	if gf == "" {
		return fmt.Sprintf("%s { %s }", v, inner), []string{v}
	}
	return fmt.Sprintf("%s { %s { %s } }", gf, v, inner), []string{gf, v}
}

// Scope is a GraphQL client bound to one workspace cluster and caller token.
type Scope struct {
	c         *GraphQLClient
	clusterID string
	token     string
}

// For returns a client scoped to clusterID (the X-Kedge-Cluster the hub
// injected) authenticating as the caller via token.
func (c *GraphQLClient) For(clusterID, token string) (*Scope, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("no cluster id (X-Kedge-Cluster missing) — cannot target the tenant workspace")
	}
	if token == "" {
		return nil, fmt.Errorf("no bearer token on request — cannot act on the tenant's behalf")
	}
	return &Scope{c: c, clusterID: clusterID, token: token}, nil
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

// exec POSTs a GraphQL request and returns the raw data object. res/name give
// error mapping the resource + object identity for apierrors construction.
func (s *Scope) exec(ctx context.Context, query string, vars map[string]any, res *Resource, name string) (json.RawMessage, error) {
	reqBody, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	url := s.c.hubBase + "/graphql/" + s.clusterID
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		// No schema for this cluster yet: the gateway hasn't generated a schema
		// for the workspace. Distinct from an object-not-found (which comes back
		// as a GraphQL error) so callers can treat it as "initializing".
		return nil, fmt.Errorf("graphql gateway has no schema for cluster %q yet — workspace initializing", s.clusterID)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("graphql gateway HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out gqlResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode graphql response: %w", err)
	}
	if len(out.Errors) > 0 {
		return nil, mapGraphQLError(out.Errors, res, name)
	}
	return out.Data, nil
}

// mapGraphQLError converts a gateway error into a k8s apierror where the
// message identifies a well-known condition, so callers' apierrors.Is* checks
// keep working across the migration.
func mapGraphQLError(errs []gqlError, res *Resource, name string) error {
	first := errs[0].Message
	low := strings.ToLower(first)
	gr := schema.GroupResource{}
	if res != nil {
		gr = res.GVR.GroupResource()
	}
	switch {
	case strings.Contains(low, "not found"), strings.Contains(low, "could not find"):
		return apierrors.NewNotFound(gr, name)
	case strings.Contains(low, "already exists"):
		return apierrors.NewAlreadyExists(gr, name)
	case strings.Contains(low, "conflict"):
		return apierrors.NewConflict(gr, name, fmt.Errorf("%s", first))
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Message)
	}
	return fmt.Errorf("graphql: %s", strings.Join(msgs, "; "))
}

// Get fetches one object via the <Kind>Yaml query.
func (s *Scope) Get(ctx context.Context, res Resource, namespace, name string) (*unstructured.Unstructured, error) {
	field := res.Kind + "Yaml"
	varDefs, args, vars := itemArgs(res, namespace, name)
	sel, path := wrapSelection(res, fmt.Sprintf("%s(%s)", field, args))
	q := fmt.Sprintf("query(%s) { %s }", varDefs, sel)

	data, err := s.exec(ctx, q, vars, &res, name)
	if err != nil {
		return nil, err
	}
	str, err := nestedString(data, append(path, field)...)
	if err != nil {
		return nil, err
	}
	return decodeObject(str)
}

// List fetches all objects via the <Plural>Yaml query. namespace is optional
// (empty lists across all namespaces for namespaced resources).
func (s *Scope) List(ctx context.Context, res Resource, namespace string) ([]unstructured.Unstructured, error) {
	field := res.Plural + "Yaml"
	var (
		varDefs string
		inner   = field
		vars    = map[string]any{}
	)
	if res.Namespaced && namespace != "" {
		varDefs = "$namespace: String"
		inner = field + "(namespace: $namespace)"
		vars["namespace"] = namespace
	}
	sel, path := wrapSelection(res, inner)
	q := ""
	if varDefs != "" {
		q = fmt.Sprintf("query(%s) { %s }", varDefs, sel)
	} else {
		q = fmt.Sprintf("query { %s }", sel)
	}

	data, err := s.exec(ctx, q, vars, &res, "")
	if err != nil {
		return nil, err
	}
	str, err := nestedString(data, append(path, field)...)
	if err != nil {
		return nil, err
	}
	return decodeList(str)
}

// Apply create-or-updates obj via the generic applyYaml mutation.
func (s *Scope) Apply(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	y, err := yaml.Marshal(obj.Object)
	if err != nil {
		return nil, err
	}
	q := "mutation($yaml: String!) { applyYaml(yaml: $yaml) }"
	res := resourceFromObject(obj)
	data, err := s.exec(ctx, q, map[string]any{"yaml": string(y)}, res, obj.GetName())
	if err != nil {
		return nil, err
	}
	str, err := nestedString(data, "applyYaml")
	if err != nil {
		return nil, err
	}
	return decodeObject(str)
}

// ApplyStatus merge-patches obj's status subresource via applyStatusYaml.
func (s *Scope) ApplyStatus(ctx context.Context, obj *unstructured.Unstructured) error {
	y, err := yaml.Marshal(obj.Object)
	if err != nil {
		return err
	}
	q := "mutation($yaml: String!) { applyStatusYaml(yaml: $yaml) }"
	res := resourceFromObject(obj)
	_, err = s.exec(ctx, q, map[string]any{"yaml": string(y)}, res, obj.GetName())
	return err
}

// Delete removes one object via the delete<Kind> mutation.
func (s *Scope) Delete(ctx context.Context, res Resource, namespace, name string) error {
	field := "delete" + res.Kind
	varDefs, args, vars := itemArgs(res, namespace, name)
	sel, _ := wrapSelection(res, fmt.Sprintf("%s(%s)", field, args))
	q := fmt.Sprintf("mutation(%s) { %s }", varDefs, sel)
	_, err := s.exec(ctx, q, vars, &res, name)
	return err
}

// itemArgs builds the (varDefs, args, vars) for a single-item operation.
func itemArgs(res Resource, namespace, name string) (string, string, map[string]any) {
	varDefs := "$name: String!"
	args := "name: $name"
	vars := map[string]any{"name": name}
	if res.Namespaced {
		varDefs += ", $namespace: String!"
		args += ", namespace: $namespace"
		vars["namespace"] = namespace
	}
	return varDefs, args, vars
}

// resourceFromObject derives a Resource for error mapping from an object's GVK.
func resourceFromObject(obj *unstructured.Unstructured) *Resource {
	gvk := obj.GroupVersionKind()
	return &Resource{
		GVR:  schema.GroupVersionResource{Group: gvk.Group, Version: gvk.Version, Resource: strings.ToLower(gvk.Kind) + "s"},
		Kind: gvk.Kind,
	}
}

// nestedString walks the GraphQL data object along path and returns the leaf
// string (the serialized object/list the *Yaml fields return).
func nestedString(data json.RawMessage, path ...string) (string, error) {
	var cur any
	if err := json.Unmarshal(data, &cur); err != nil {
		return "", fmt.Errorf("decode graphql data: %w", err)
	}
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok || m[key] == nil {
			return "", fmt.Errorf("graphql response missing field %q", key)
		}
		cur = m[key]
	}
	str, ok := cur.(string)
	if !ok {
		return "", fmt.Errorf("graphql field %q is not a string", path[len(path)-1])
	}
	return str, nil
}

// decodeObject parses a serialized (YAML or JSON) object into unstructured.
func decodeObject(s string) (*unstructured.Unstructured, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("empty object payload")
	}
	m := map[string]any{}
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("decode object: %w", err)
	}
	return &unstructured.Unstructured{Object: m}, nil
}

// decodeList parses a serialized YAML sequence of objects into unstructured.
func decodeList(s string) ([]unstructured.Unstructured, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var raw []map[string]any
	if err := yaml.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	out := make([]unstructured.Unstructured, 0, len(raw))
	for _, m := range raw {
		out = append(out, unstructured.Unstructured{Object: m})
	}
	return out, nil
}
