// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

// Development-loop tools: dev_sync / dev_logs / dev_restart drive the
// template-declared data-plane verbs (sync, log, restart) on a development-mode
// instance, so an MCP agent can edit source locally and hot-reload it in the
// sandbox without building an image. The calls go through the provider's own
// /dataplane/* handler IN-PROCESS (deps.DataPlane) — the same caller-token
// authorization, template-contract resolution, and runtime proxying as the hub
// HTTP route; these tools only add addressing (cluster ID from the request
// identity) and workspacePath file routing on top.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/kro"
)

const (
	// devLogDefaultBytes / devLogMaxBytes bound dev_logs responses so a noisy
	// dev server cannot blow the caller's model context.
	devLogDefaultBytes = 64 << 10
	devLogMaxBytes     = 256 << 10

	// devSyncMaxBytes bounds one dev_sync payload (all files, pre-routing).
	// Matches the data-plane handler's own 16MB read bound.
	devSyncMaxBytes = 16 << 20
)

type devSyncFile struct {
	Path    string `json:"path" jsonschema:"Workspace-relative file path (e.g. web/src/App.jsx)"`
	Content string `json:"content" jsonschema:"Full UTF-8 file content"`
}

type devSyncInput struct {
	Instance string        `json:"instance" jsonschema:"Development-mode instance name (provisioned with values.farosMode=development)"`
	Files    []devSyncFile `json:"files" jsonschema:"Files to sync, paths relative to the workspace root; each is routed to the component whose workspacePath prefixes it"`
	Restart  string        `json:"restart,omitempty" jsonschema:"auto (default) restarts the dev process when needed per the template's reload rules; none only writes files"`
}

type devSyncComponentResult struct {
	Files    int             `json:"files"`
	Response json.RawMessage `json:"response,omitempty"`
}

type devSyncOutput struct {
	Instance   string                            `json:"instance"`
	Components map[string]devSyncComponentResult `json:"components"`
}

type devLogsInput struct {
	Instance  string `json:"instance" jsonschema:"Development-mode instance name"`
	Component string `json:"component" jsonschema:"Development component to read logs from (see the template's development.components)"`
	MaxBytes  int    `json:"maxBytes,omitempty" jsonschema:"Response byte cap; default 65536, max 262144"`
}

type devLogsOutput struct {
	Instance  string `json:"instance"`
	Component string `json:"component"`
	Log       string `json:"log"`
	Truncated bool   `json:"truncated"`
}

type devRestartInput struct {
	Instance  string `json:"instance" jsonschema:"Development-mode instance name"`
	Component string `json:"component" jsonschema:"Development component whose dev process to restart"`
}

type devRestartOutput struct {
	Instance  string          `json:"instance"`
	Component string          `json:"component"`
	Response  json.RawMessage `json:"response,omitempty"`
}

// devSandboxRequest is the control-plane sync payload the per-component dev
// agent accepts (mirrors app-studio's projectSandboxSyncRequest).
type devSandboxRequest struct {
	Files   []devSyncFile `json:"files"`
	Restart string        `json:"restart,omitempty"`
}

// devTarget is a resolved development instance: the instance CR plus its
// template's plural (data-plane addressing) and development contract.
type devTarget struct {
	resource string
	instance *kro.Instance
	// components is the template's development contract per component name:
	// workspacePath (sync routing) plus toolchain and start command (what the
	// sandbox will actually execute).
	components map[string]kro.TemplateDevelopmentComponent
}

// registerDevTools wires the dev-loop tools. Registered unconditionally so
// they are discoverable; each call fails with a clear reason when the data
// plane is unavailable on this provider (REST-only dev, no runtime cluster).
func registerDevTools(srv *mcp.Server, deps Deps, ident identity) {
	yes := true
	no := false

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "dev_sync",
		Title:       "Sync source files into a development instance",
		Description: "Push workspace files into a development-mode instance's sandbox with hot reload — no image build. Files are routed to components by the template's development.components workspacePath prefixes (see describe_template); files outside every component directory are rejected. Requires an instance provisioned with values.farosMode=\"development\".",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &yes},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devSyncInput) (*mcp.CallToolResult, devSyncOutput, error) {
		target, err := resolveDevTarget(ctx, deps, ident, in.Instance)
		if err != nil {
			return nil, devSyncOutput{}, err
		}
		if len(in.Files) == 0 {
			return nil, devSyncOutput{}, fmt.Errorf("no files to sync — pass the changed files with workspace-relative paths")
		}
		total := 0
		for _, f := range in.Files {
			total += len(f.Content)
		}
		if total > devSyncMaxBytes {
			return nil, devSyncOutput{}, fmt.Errorf("sync payload is %d bytes, above the %d limit — sync fewer files per call", total, devSyncMaxBytes)
		}
		restart := strings.TrimSpace(in.Restart)
		if restart == "" {
			restart = "auto"
		}
		if restart != "auto" && restart != "none" {
			return nil, devSyncOutput{}, fmt.Errorf("restart must be \"auto\" or \"none\", got %q", in.Restart)
		}

		routed := routeDevSyncFiles(in.Files, target.components)
		if countRoutedDevFiles(routed) == 0 {
			return nil, devSyncOutput{}, fmt.Errorf(
				"none of the %d files are under a development component directory (%s); source must live under those directories to reach the sandbox",
				len(in.Files), devComponentSummary(target.components))
		}
		// Files in the right directory but written for the wrong runtime sync
		// "successfully" and then never start: the sandbox image has no
		// toolchain for them. Fail here rather than leaving a dead component.
		if err := validateDevSyncToolchains(routed, target.components); err != nil {
			return nil, devSyncOutput{}, err
		}

		out := devSyncOutput{Instance: in.Instance, Components: map[string]devSyncComponentResult{}}
		for _, component := range sortedDevComponents(target.components) {
			payload, err := json.Marshal(devSandboxRequest{Files: routed[component], Restart: restart})
			if err != nil {
				return nil, devSyncOutput{}, fmt.Errorf("encode %s sync payload: %w", component, err)
			}
			body, status, err := callDataPlane(ctx, deps.DataPlane, ident, http.MethodPost, target.resource, in.Instance, component, "sync", payload)
			if err != nil {
				return nil, devSyncOutput{}, fmt.Errorf("component %s: %w", component, err)
			}
			if status < 200 || status >= 300 {
				return nil, devSyncOutput{}, fmt.Errorf("component %s sync returned %d: %s", component, status, strings.TrimSpace(string(body)))
			}
			out.Components[component] = devSyncComponentResult{Files: len(routed[component]), Response: json.RawMessage(body)}
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "dev_logs",
		Title:       "Read a development component's dev-server logs",
		Description: "Return the dev-server log buffer for one component of a development-mode instance. Use it to diagnose why the sandbox app is failing after a dev_sync.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &yes},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devLogsInput) (*mcp.CallToolResult, devLogsOutput, error) {
		target, err := resolveDevTarget(ctx, deps, ident, in.Instance)
		if err != nil {
			return nil, devLogsOutput{}, err
		}
		component, err := requireDevComponent(target, in.Component)
		if err != nil {
			return nil, devLogsOutput{}, err
		}
		maxBytes := in.MaxBytes
		if maxBytes <= 0 {
			maxBytes = devLogDefaultBytes
		}
		if maxBytes > devLogMaxBytes {
			maxBytes = devLogMaxBytes
		}
		body, status, err := callDataPlane(ctx, deps.DataPlane, ident, http.MethodGet, target.resource, in.Instance, component, "log", nil)
		if err != nil {
			return nil, devLogsOutput{}, err
		}
		if status < 200 || status >= 300 {
			return nil, devLogsOutput{}, fmt.Errorf("log returned %d: %s", status, strings.TrimSpace(string(body)))
		}
		out := devLogsOutput{Instance: in.Instance, Component: component}
		// Keep the TAIL when over the cap — the newest lines hold the failure.
		if len(body) > maxBytes {
			out.Log = string(body[len(body)-maxBytes:])
			out.Truncated = true
		} else {
			out.Log = string(body)
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "dev_restart",
		Title:       "Restart a development component's dev process",
		Description: "Restart the dev-server process of one component of a development-mode instance without syncing files. Rarely needed — dev_sync with restart \"auto\" already restarts per the template's reload rules.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &yes},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in devRestartInput) (*mcp.CallToolResult, devRestartOutput, error) {
		target, err := resolveDevTarget(ctx, deps, ident, in.Instance)
		if err != nil {
			return nil, devRestartOutput{}, err
		}
		component, err := requireDevComponent(target, in.Component)
		if err != nil {
			return nil, devRestartOutput{}, err
		}
		body, status, err := callDataPlane(ctx, deps.DataPlane, ident, http.MethodPost, target.resource, in.Instance, component, "restart", []byte(`{}`))
		if err != nil {
			return nil, devRestartOutput{}, err
		}
		if status < 200 || status >= 300 {
			return nil, devRestartOutput{}, fmt.Errorf("restart returned %d: %s", status, strings.TrimSpace(string(body)))
		}
		return nil, devRestartOutput{Instance: in.Instance, Component: component, Response: json.RawMessage(body)}, nil
	})
}

// resolveDevTarget authorizes the caller (tenant client), fetches the
// instance, and returns its template's development contract. Fails with
// actionable messages for every non-dev case: data plane off, no such
// instance, template without a development block, instance not provisioned
// in development mode.
func resolveDevTarget(ctx context.Context, deps Deps, ident identity, instanceName string) (devTarget, error) {
	if deps.DataPlane == nil {
		return devTarget{}, fmt.Errorf("the development data plane is not available on this provider deployment")
	}
	if strings.TrimSpace(instanceName) == "" {
		return devTarget{}, fmt.Errorf("instance is required")
	}
	dyn, err := tenantClient(deps, ident)
	if err != nil {
		return devTarget{}, err
	}
	templates, err := listTemplates(ctx, dyn)
	if err != nil {
		return devTarget{}, fmt.Errorf("list templates: %w", err)
	}
	inst, err := getInstance(ctx, dyn, instanceName)
	if err != nil {
		if err == kro.ErrInstanceNotFound {
			return devTarget{}, fmt.Errorf("instance %q not found — provision it first (values.farosMode=\"development\")", instanceName)
		}
		return devTarget{}, fmt.Errorf("get instance: %w", err)
	}
	var tmpl *kro.Template
	for i := range templates {
		if templates[i].Name == inst.Template {
			tmpl = &templates[i]
			break
		}
	}
	if tmpl == nil {
		return devTarget{}, fmt.Errorf("instance %q references template %q which is no longer in the catalog", instanceName, inst.Template)
	}
	if tmpl.Development == nil || len(tmpl.Development.Components) == 0 {
		return devTarget{}, fmt.Errorf("template %q has no development mode — the dev tools only work on development-capable templates (see describe_template)", tmpl.Name)
	}
	if mode, _ := inst.Values["farosMode"].(string); mode != "development" {
		return devTarget{}, fmt.Errorf("instance %q is not in development mode (farosMode=%q) — provision a dev instance with values.farosMode=\"development\"", instanceName, mode)
	}
	components := make(map[string]kro.TemplateDevelopmentComponent, len(tmpl.Development.Components))
	maps.Copy(components, tmpl.Development.Components)
	return devTarget{resource: infrav1alpha1.InstancesResource, instance: inst, components: components}, nil
}

// requireDevComponent validates a caller-supplied component name against the
// template's development contract, listing the valid names on mismatch.
func requireDevComponent(target devTarget, component string) (string, error) {
	component = strings.TrimSpace(component)
	if component == "" {
		names := sortedDevComponents(target.components)
		if len(names) == 1 {
			return names[0], nil
		}
		return "", fmt.Errorf("component is required; this template's development components are: %s", strings.Join(names, ", "))
	}
	if _, ok := target.components[component]; !ok {
		return "", fmt.Errorf("unknown component %q; this template's development components are: %s", component, strings.Join(sortedDevComponents(target.components), ", "))
	}
	return component, nil
}

// callDataPlane drives the provider's own data-plane handler in-process with
// a synthesized request: same path shape and identity headers as the hub
// route, so authorization (caller token → instance RBAC), contract method
// allowlisting, and runtime proxying are all reused rather than duplicated.
func callDataPlane(ctx context.Context, dp http.Handler, ident identity, method, resource, name, component, verb string, payload []byte) ([]byte, int, error) {
	if strings.TrimSpace(ident.clusterID) == "" {
		return nil, 0, fmt.Errorf("no workspace cluster on this request (X-Faros-Cluster missing) — cannot address the development data plane")
	}
	p := "/dataplane/clusters/" + url.PathEscape(ident.clusterID) +
		"/" + url.PathEscape(resource) + "/" + url.PathEscape(name)
	if component != "" {
		p += "/components/" + url.PathEscape(component)
	}
	p += "/" + url.PathEscape(verb)

	var body io.Reader
	if payload != nil {
		body = strings.NewReader(string(payload))
	}
	req := httptest.NewRequest(method, p, body).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+ident.token)
	req.Header.Set("X-Faros-Tenant", ident.tenantPath)
	req.Header.Set("X-Faros-User", ident.user)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	dp.ServeHTTP(rec, req)
	return rec.Body.Bytes(), rec.Code, nil
}

// routeDevSyncFiles groups files by development component: a component whose
// workspacePath is "." receives every file as-is; otherwise files under
// "<workspacePath>/" are routed with the prefix stripped (the sandbox works
// tree is the component directory). Files outside every component route
// nowhere. Mirrors app-studio's routing so both edit paths behave identically.
func routeDevSyncFiles(files []devSyncFile, components map[string]kro.TemplateDevelopmentComponent) map[string][]devSyncFile {
	out := make(map[string][]devSyncFile, len(components))
	for component, comp := range components {
		wp := path.Clean(strings.TrimSpace(comp.WorkspacePath))
		if wp == "." {
			out[component] = files
			continue
		}
		prefix := wp + "/"
		for _, f := range files {
			if rest, ok := strings.CutPrefix(f.Path, prefix); ok {
				out[component] = append(out[component], devSyncFile{Path: rest, Content: f.Content})
			}
		}
	}
	return out
}

// devToolchainManifests names the file each known toolchain needs at a
// component's root before its start command can run. Keyed by the toolchain
// from the template's ${faros.devImage.<toolchain>} token. A toolchain absent
// here is never validated: the template, not this server, is the authority on
// what its sandbox can run, so an unknown toolchain must not block a sync.
var devToolchainManifests = map[string]struct {
	Files []string
	Hint  string
}{
	"node":   {Files: []string{"package.json"}, Hint: "add a package.json whose \"dev\" or \"start\" script launches the server on $PORT"},
	"python": {Files: []string{"requirements.txt", "pyproject.toml", "Pipfile", "setup.py"}, Hint: "add a requirements.txt or pyproject.toml"},
	"go":     {Files: []string{"go.mod"}, Hint: "add a go.mod at the component root"},
	"ruby":   {Files: []string{"Gemfile"}, Hint: "add a Gemfile at the component root"},
}

// validateDevSyncToolchains rejects a sync whose files cannot run in the
// component's sandbox. It fires only when a component received files, its
// toolchain is known, and that toolchain's manifest is missing from the
// component root — so partial syncs and unknown toolchains pass untouched.
func validateDevSyncToolchains(routed map[string][]devSyncFile, components map[string]kro.TemplateDevelopmentComponent) error {
	for _, name := range sortedDevComponents(components) {
		files := routed[name]
		if len(files) == 0 {
			continue
		}
		comp := components[name]
		manifest, known := devToolchainManifests[comp.Toolchain]
		if !known || devFilesContainManifest(files, manifest.Files) {
			continue
		}
		where := path.Clean(strings.TrimSpace(comp.WorkspacePath))
		if where == "." {
			where = "the workspace root"
		} else {
			where += "/"
		}
		return fmt.Errorf(
			"component %q runs a %s development sandbox but %s has no %s — %s. The sandbox has no other toolchain installed and starts this component with: %s",
			name, comp.Toolchain, where, strings.Join(manifest.Files, " / "), manifest.Hint,
			summarizeDevStartCommand(comp.StartCommand))
	}
	return nil
}

// devFilesContainManifest reports whether an accepted manifest sits at the
// component root. Paths here are component-relative (the router strips the
// workspacePath prefix), so a root manifest contains no separator — a nested
// one does not make the component runnable.
func devFilesContainManifest(files []devSyncFile, accepted []string) bool {
	for _, f := range files {
		p := path.Clean(strings.TrimSpace(f.Path))
		if strings.Contains(p, "/") {
			continue
		}
		if slices.Contains(accepted, p) {
			return true
		}
	}
	return false
}

// devStartCommandSummaryMaxChars bounds a start command in an error message:
// templates may inline a long config shim, and the leading command is the
// part that tells an agent what its source must provide.
const devStartCommandSummaryMaxChars = 160

func summarizeDevStartCommand(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if cmd == "" {
		return "(the template declares no start command)"
	}
	if len(cmd) > devStartCommandSummaryMaxChars {
		return cmd[:devStartCommandSummaryMaxChars] + "..."
	}
	return cmd
}

func countRoutedDevFiles(routed map[string][]devSyncFile) int {
	total := 0
	for _, files := range routed {
		total += len(files)
	}
	return total
}

func sortedDevComponents(components map[string]kro.TemplateDevelopmentComponent) []string {
	names := make([]string, 0, len(components))
	for name := range components {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// devComponentSummary renders the component → workspacePath map for error
// messages, sorted by component name (e.g. "backend → api/, frontend → web/").
func devComponentSummary(components map[string]kro.TemplateDevelopmentComponent) string {
	parts := make([]string, 0, len(components))
	for _, name := range sortedDevComponents(components) {
		wp := path.Clean(strings.TrimSpace(components[name].WorkspacePath))
		if wp == "." {
			parts = append(parts, name+" → the workspace root")
			continue
		}
		parts = append(parts, name+" → "+wp+"/")
	}
	return strings.Join(parts, ", ")
}
