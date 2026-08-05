// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package client provides typed, workspace-scoped access to the vibe-studio
// Project CRD over the hub's GraphQL gateway. Built per request from the
// caller's bearer token, so the provider always acts as the calling user.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/tenant"
)

// ProjectGVR is the Project resource in the tenant workspace (available once
// the tenant has Enabled the provider — the APIBinding materializes it).
var ProjectGVR = schema.GroupVersionResource{
	Group: vibev1alpha1.GroupName, Version: vibev1alpha1.Version, Resource: "projects",
}

var projectResource = tenant.Resource{
	GVR: ProjectGVR, Kind: "Project", Plural: "Projects", Namespaced: false,
}

// TemplateGVR is the infrastructure provider's Template catalog resource,
// read as the caller (same path app-studio uses — no permission claim needed
// for catalog reads).
var TemplateGVR = schema.GroupVersionResource{
	Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "templates",
}

var templateResource = tenant.Resource{
	GVR: TemplateGVR, Kind: "Template", Plural: "Templates", Namespaced: false,
}

// GetTemplate fetches one infrastructure Template, unstructured (its spec is
// template-defined; callers pick out the fields they need).
func (c *Client) GetTemplate(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	return c.scope.Get(ctx, templateResource, "", name)
}

// Code provider resources (repo creation + seeding). Reads/creates ride the
// caller's identity; the reconciler's writes ride the code claims.
var (
	connectionResource = tenant.Resource{
		GVR:  schema.GroupVersionResource{Group: "code.kedge.faros.sh", Version: "v1alpha1", Resource: "connections"},
		Kind: "Connection", Plural: "Connections", Namespaced: false,
	}
	repositoryResource = tenant.Resource{
		GVR:  schema.GroupVersionResource{Group: "code.kedge.faros.sh", Version: "v1alpha1", Resource: "repositories"},
		Kind: "Repository", Plural: "Repositories", Namespaced: false,
	}
)

var secretResource = tenant.Resource{
	GVR:  schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
	Kind: "Secret", Plural: "Secrets", Namespaced: true,
}

var sessionResource = tenant.Resource{
	GVR:  schema.GroupVersionResource{Group: vibev1alpha1.GroupName, Version: vibev1alpha1.Version, Resource: "sessions"},
	Kind: "Session", Plural: "Sessions", Namespaced: false,
}

// ApplySession create-or-updates a Session CR in the tenant workspace.
func (c *Client) ApplySession(ctx context.Context, s *vibev1alpha1.Session) (*vibev1alpha1.Session, error) {
	s.APIVersion = vibev1alpha1.SchemeGroupVersion.String()
	s.Kind = "Session"
	u, err := toUnstructured(s)
	if err != nil {
		return nil, err
	}
	out, err := c.scope.Apply(ctx, u)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[vibev1alpha1.Session](out)
}

var modelResource = tenant.Resource{
	GVR:  schema.GroupVersionResource{Group: vibev1alpha1.GroupName, Version: vibev1alpha1.Version, Resource: "models"},
	Kind: "Model", Plural: "Models", Namespaced: false,
}

// ErrResourceUnavailable reports that a resource type is not served in this
// workspace yet — the provider's APIExport hasn't been re-initialized with
// the schema, or the tenant's APIBinding predates it. Callers degrade
// (empty list + guidance) instead of failing.
var ErrResourceUnavailable = errors.New("resource type is not available in this workspace")

// unavailable detects the gateway's "no such field/type" answer for a
// resource the workspace doesn't serve.
func unavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Cannot query field") ||
		strings.Contains(msg, "Unknown type") ||
		strings.Contains(msg, "Cannot query mutation field")
}

// ListModels lists the workspace's configured Models.
func (c *Client) ListModels(ctx context.Context) ([]vibev1alpha1.Model, error) {
	items, err := c.scope.List(ctx, modelResource, "")
	if unavailable(err) {
		return nil, ErrResourceUnavailable
	}
	if err != nil {
		return nil, err
	}
	out := make([]vibev1alpha1.Model, 0, len(items))
	for i := range items {
		m, err := fromUnstructured[vibev1alpha1.Model](&items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetModel fetches one Model.
func (c *Client) GetModel(ctx context.Context, name string) (*vibev1alpha1.Model, error) {
	u, err := c.scope.Get(ctx, modelResource, "", name)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[vibev1alpha1.Model](u)
}

// ApplyModel create-or-updates a Model.
func (c *Client) ApplyModel(ctx context.Context, m *vibev1alpha1.Model) (*vibev1alpha1.Model, error) {
	m.APIVersion = vibev1alpha1.SchemeGroupVersion.String()
	m.Kind = "Model"
	u, err := toUnstructured(m)
	if err != nil {
		return nil, err
	}
	out, err := c.scope.Apply(ctx, u)
	if unavailable(err) {
		return nil, ErrResourceUnavailable
	}
	if err != nil {
		return nil, err
	}
	return fromUnstructured[vibev1alpha1.Model](out)
}

// DeleteModel removes a Model.
func (c *Client) DeleteModel(ctx context.Context, name string) error {
	return c.scope.Delete(ctx, modelResource, "", name)
}

// ApplySecret create-or-updates an opaque Secret (model API keys).
func (c *Client) ApplySecret(ctx context.Context, namespace, name string, data map[string]string) error {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"type":       "Opaque",
		"stringData": toAnyMap(data),
	}}
	_, err := c.scope.Apply(ctx, u)
	return err
}

// DeleteSecret removes a Secret.
func (c *Client) DeleteSecret(ctx context.Context, namespace, name string) error {
	return c.scope.Delete(ctx, secretResource, namespace, name)
}

func toAnyMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// DeleteSession removes a Session CR — its finalizer purges the store, and
// the owned Project (and its instances) garbage-collect behind it.
func (c *Client) DeleteSession(ctx context.Context, name string) error {
	return c.scope.Delete(ctx, sessionResource, "", name)
}

// GetSession fetches one Session CR.
func (c *Client) GetSession(ctx context.Context, name string) (*vibev1alpha1.Session, error) {
	u, err := c.scope.Get(ctx, sessionResource, "", name)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[vibev1alpha1.Session](u)
}

// ListProjects lists the tenant's Project CRs — the source of truth for the
// portal's home page (sessions are conversational attachments, not projects).
func (c *Client) ListProjects(ctx context.Context) ([]vibev1alpha1.Project, error) {
	items, err := c.scope.List(ctx, projectResource, "")
	if err != nil {
		return nil, err
	}
	out := make([]vibev1alpha1.Project, 0, len(items))
	for i := range items {
		p, err := fromUnstructured[vibev1alpha1.Project](&items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

// GetSecret reads one tenant-workspace Secret as the caller (LLM settings).
func (c *Client) GetSecret(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return c.scope.Get(ctx, secretResource, namespace, name)
}

// ListTemplates lists the tenant's Template catalog, unstructured.
func (c *Client) ListTemplates(ctx context.Context) ([]unstructured.Unstructured, error) {
	return c.scope.List(ctx, templateResource, "")
}

// FirstValidatedConnection lists code Connections and returns the name of the
// first (by name) whose Validated condition is True; "" when none qualify.
func (c *Client) FirstValidatedConnection(ctx context.Context) (string, error) {
	items, err := c.scope.List(ctx, connectionResource, "")
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(items))
	byName := map[string]*unstructured.Unstructured{}
	for i := range items {
		n := items[i].GetName()
		names = append(names, n)
		byName[n] = &items[i]
	}
	sort.Strings(names)
	for _, n := range names {
		if hasCondition(byName[n], "Validated", "True") {
			return n, nil
		}
	}
	return "", nil
}

// GetRepository fetches one code Repository, unstructured.
func (c *Client) GetRepository(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	return c.scope.Get(ctx, repositoryResource, "", name)
}

// RepositoryReady reports whether the Repository's Ready condition is True.
func RepositoryReady(repo *unstructured.Unstructured) bool {
	return hasCondition(repo, "Ready", "True")
}

func hasCondition(u *unstructured.Unstructured, condType, want string) bool {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == condType {
			s, _ := cm["status"].(string)
			return s == want
		}
	}
	return false
}

// GetInstance fetches one infrastructure template instance by its resolved
// CRD coordinates (from Template.spec.instanceCRD), unstructured.
func (c *Client) GetInstance(ctx context.Context, group, version, resource, kind, name string) (*unstructured.Unstructured, error) {
	res := tenant.Resource{
		GVR:  schema.GroupVersionResource{Group: group, Version: version, Resource: resource},
		Kind: kind, Plural: kind + "s", Namespaced: false,
	}
	return c.scope.Get(ctx, res, "", name)
}

// Client is a per-request, caller-scoped accessor for vibe-studio resources.
type Client struct {
	scope *tenant.Scope
}

// New binds a client to one workspace cluster + caller token.
func New(scope *tenant.Scope) *Client { return &Client{scope: scope} }

// ApplyProject create-or-updates a Project in the tenant workspace.
func (c *Client) ApplyProject(ctx context.Context, p *vibev1alpha1.Project) (*vibev1alpha1.Project, error) {
	p.APIVersion = vibev1alpha1.SchemeGroupVersion.String()
	p.Kind = "Project"
	u, err := toUnstructured(p)
	if err != nil {
		return nil, err
	}
	out, err := c.scope.Apply(ctx, u)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[vibev1alpha1.Project](out)
}

// GetProject fetches one Project.
func (c *Client) GetProject(ctx context.Context, name string) (*vibev1alpha1.Project, error) {
	u, err := c.scope.Get(ctx, projectResource, "", name)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[vibev1alpha1.Project](u)
}

// DeleteProject removes one Project.
func (c *Client) DeleteProject(ctx context.Context, name string) error {
	return c.scope.Delete(ctx, projectResource, "", name)
}

func toUnstructured(obj any) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshaling to JSON: %w", err)
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &u.Object); err != nil {
		return nil, fmt.Errorf("unmarshaling to unstructured: %w", err)
	}
	return u, nil
}

func fromUnstructured[T any](u *unstructured.Unstructured) (*T, error) {
	var obj T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &obj); err != nil {
		return nil, fmt.Errorf("converting from unstructured: %w", err)
	}
	return &obj, nil
}

// studioResource is the workspace's Studio singleton.
var studioResource = tenant.Resource{
	GVR:  schema.GroupVersionResource{Group: vibev1alpha1.GroupName, Version: vibev1alpha1.Version, Resource: "studios"},
	Kind: "Studio", Plural: "Studios", Namespaced: false,
}

// GetStudio fetches the workspace's Studio.
func (c *Client) GetStudio(ctx context.Context, name string) (*vibev1alpha1.Studio, error) {
	u, err := c.scope.Get(ctx, studioResource, "", name)
	if unavailable(err) {
		return nil, ErrResourceUnavailable
	}
	if err != nil {
		return nil, err
	}
	return fromUnstructured[vibev1alpha1.Studio](u)
}

// ApplyStudio create-or-updates the workspace's Studio.
func (c *Client) ApplyStudio(ctx context.Context, st *vibev1alpha1.Studio) (*vibev1alpha1.Studio, error) {
	st.APIVersion = vibev1alpha1.SchemeGroupVersion.String()
	st.Kind = "Studio"
	u, err := toUnstructured(st)
	if err != nil {
		return nil, err
	}
	out, err := c.scope.Apply(ctx, u)
	if unavailable(err) {
		return nil, ErrResourceUnavailable
	}
	if err != nil {
		return nil, err
	}
	return fromUnstructured[vibev1alpha1.Studio](out)
}

// packageResource is the code provider's published-artifact record. The
// PackageController crawls the git host and authors these, so vibe-studio
// only ever reads them.
var packageResource = tenant.Resource{
	GVR:  schema.GroupVersionResource{Group: "code.kedge.faros.sh", Version: "v1alpha1", Resource: "packages"},
	Kind: "Package", Plural: "Packages", Namespaced: false,
}

// PackageVersion is one published artifact version.
type PackageVersion struct {
	Digest string
	Tags   []string
}

// Package is a published artifact stream for one repository component.
type Package struct {
	// Repository is the Repository CR the package belongs to.
	Repository string
	// Name is the package's name on the host, "<repo>/<component>".
	Name string
	// ImageRepository is the pullable registry path, without tag or digest.
	ImageRepository string
	Versions        []PackageVersion
}

// ListPackages returns the workspace's published packages. Reads ride the
// caller's identity, so a user only ever sees their own workspace's.
func (c *Client) ListPackages(ctx context.Context) ([]Package, error) {
	items, err := c.scope.List(ctx, packageResource, "")
	if unavailable(err) {
		return nil, ErrResourceUnavailable
	}
	if err != nil {
		return nil, err
	}
	out := make([]Package, 0, len(items))
	for i := range items {
		u := items[i]
		p := Package{Repository: u.GetLabels()["code.kedge.faros.sh/repository"]}
		p.Name, _, _ = unstructured.NestedString(u.Object, "status", "packageName")
		p.ImageRepository, _, _ = unstructured.NestedString(u.Object, "status", "imageRepository")
		versions, _, _ := unstructured.NestedSlice(u.Object, "status", "versions")
		for _, v := range versions {
			vm, ok := v.(map[string]any)
			if !ok {
				continue
			}
			pv := PackageVersion{}
			pv.Digest, _ = vm["digest"].(string)
			tags, _, _ := unstructured.NestedStringSlice(vm, "tags")
			pv.Tags = tags
			p.Versions = append(p.Versions, pv)
		}
		out = append(out, p)
	}
	return out, nil
}

// GitToken locates a Connection's git-host token and the account it belongs
// to. Resolved as the CALLER: the Project reconciler has no claim on
// Connections, so the answer is copied onto the Project spec instead.
func (c *Client) GitToken(ctx context.Context, connection string) (ref *vibev1alpha1.SecretKeyReference, login string, err error) {
	conn, err := c.scope.Get(ctx, connectionResource, "", connection)
	if err != nil {
		return nil, "", err
	}
	name, _, _ := unstructured.NestedString(conn.Object, "spec", "secretRef", "name")
	if strings.TrimSpace(name) == "" {
		return nil, "", nil
	}
	key, _, _ := unstructured.NestedString(conn.Object, "spec", "secretRef", "key")
	namespace, _, _ := unstructured.NestedString(conn.Object, "spec", "secretRef", "namespace")
	login, _, _ = unstructured.NestedString(conn.Object, "status", "login")
	if key == "" {
		key = "token"
	}
	if namespace == "" {
		namespace = "default"
	}
	return &vibev1alpha1.SecretKeyReference{Name: name, Namespace: namespace, Key: key}, login, nil
}
