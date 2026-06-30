// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/provider-infrastructure/kro"
)

// tenantClient resolves a tenant-scoped kcp dynamic client that acts AS THE
// CALLER. All catalog + instance work is CRD-based against the tenant
// workspace (the same surface the portal drives), authenticated with the
// caller's own bearer token — there is no provider-wide identity, so every
// action is authorized by the caller's RBAC in their workspace.
func tenantClient(deps Deps, ident identity) (dynamic.Interface, error) {
	if ident.tenantPath == "" {
		return nil, errors.New("no tenant identity on this request — bearer token did not resolve to a workspace")
	}
	if ident.clusterID == "" {
		return nil, errors.New("no workspace cluster on this request (X-Kedge-Cluster missing) — cannot address the tenant workspace by ID")
	}
	if ident.token == "" {
		return nil, errors.New("no bearer token on this request — the MCP request must carry the caller's credentials")
	}
	if deps.Tenant == nil {
		return nil, errors.New("tenant client unavailable (INFRASTRUCTURE_KUBECONFIG not set)")
	}
	return deps.Tenant.For(ident.clusterID, ident.token)
}

// Tool input / output structs. Field tags inform the SDK's
// auto-generated JSON-schemas — keep `json` tags lowercase-kebab to
// stay consistent with the REST API shapes the catalog UI already
// uses.

type listTemplatesInput struct {
	Category string `json:"category,omitempty" jsonschema:"Filter by category, e.g. Databases, Workloads, Storage"`
	Cloud    string `json:"cloud,omitempty" jsonschema:"Filter by target cloud, e.g. aws, gcp, azure, k8s"`
}

type templateSummary struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Category    string `json:"category,omitempty"`
	Cloud       string `json:"cloud,omitempty"`
	Version     string `json:"version,omitempty"`
	Kind        string `json:"kind"`
}

type listTemplatesOutput struct {
	Templates []templateSummary `json:"templates"`
}

type describeTemplateInput struct {
	Name    string `json:"name" jsonschema:"Template slug (e.g. postgres or s3-bucket)"`
	Version string `json:"version,omitempty" jsonschema:"Optional version pin; omit for latest"`
}

type provisionInput struct {
	Template        string         `json:"template" jsonschema:"Template slug to provision"`
	TemplateVersion string         `json:"templateVersion,omitempty" jsonschema:"Optional version pin"`
	Name            string         `json:"name" jsonschema:"Instance name (DNS-1123 subdomain)"`
	Values          map[string]any `json:"values" jsonschema:"Input values per the template inputs schema"`
}

type provisionOutput struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Phase     string `json:"phase"`
	Template  string `json:"template"`
}

type instanceSummary struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Phase    string `json:"phase"`
	Message  string `json:"message,omitempty"`
}

type listInstancesOutput struct {
	Instances []instanceSummary `json:"instances"`
}

type instanceNameInput struct {
	Name string `json:"name" jsonschema:"Instance name"`
}

type deleteOutput struct {
	Deleted bool `json:"deleted"`
}

// registerTools wires every kro_* tool onto srv. Tool handlers close
// over deps + ident so the model never has to supply tenant context
// explicitly — it inherits identity from the bearer token.
func registerTools(srv *mcp.Server, deps Deps, ident identity) {
	yes := true
	no := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &yes}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_templates",
		Title:       "List provisioning templates",
		Description: "List every template available in your workspace catalog, optionally filtered by category or cloud. Call this first when the user asks 'what can I deploy?'.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTemplatesInput) (*mcp.CallToolResult, listTemplatesOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, listTemplatesOutput{}, err
		}
		ts, err := listTemplates(ctx, dyn)
		if err != nil {
			return nil, listTemplatesOutput{}, fmt.Errorf("list templates: %w", err)
		}
		out := listTemplatesOutput{Templates: make([]templateSummary, 0, len(ts))}
		for _, t := range ts {
			if in.Category != "" && t.Category != in.Category {
				continue
			}
			if in.Cloud != "" && t.Cloud != in.Cloud {
				continue
			}
			out.Templates = append(out.Templates, templateSummary{
				Name: t.Name, DisplayName: t.DisplayName, Description: t.Description,
				Category: t.Category, Cloud: t.Cloud, Version: t.Version, Kind: t.InstanceKind,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "describe_template",
		Title:       "Inspect a template's inputs schema",
		Description: "Return a template's metadata and JSON-schema for its inputs. Use this to learn what values kro_provision will require before calling it.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in describeTemplateInput) (*mcp.CallToolResult, kro.Template, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, kro.Template{}, err
		}
		t, err := getTemplate(ctx, dyn, in.Name)
		if err != nil {
			if errors.Is(err, kro.ErrTemplateNotFound) {
				return nil, kro.Template{}, fmt.Errorf("template %q not found", in.Name)
			}
			return nil, kro.Template{}, fmt.Errorf("get template: %w", err)
		}
		return nil, *t, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "provision",
		Title:       "Provision a template instance in your workspace",
		Description: "Create an instance of the named template as a CR in the caller's tenant workspace; the backend reconciles it. Identity is taken from the bearer token; the user does not need to supply a tenant path.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  false,
			DestructiveHint: &no,
			OpenWorldHint:   &yes,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in provisionInput) (*mcp.CallToolResult, provisionOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, provisionOutput{}, err
		}
		t, err := getTemplate(ctx, dyn, in.Template)
		if err != nil {
			if errors.Is(err, kro.ErrTemplateNotFound) {
				return nil, provisionOutput{}, fmt.Errorf("template %q not found", in.Template)
			}
			return nil, provisionOutput{}, fmt.Errorf("get template: %w", err)
		}
		// CRD-native: create the instance CR in the tenant workspace. The
		// backend controller reconciles it (and bridges cloud-credentials) —
		// the same path the portal uses; MCP no longer pre-bridges creds.
		inst, err := createInstance(ctx, dyn, t, in.Name, in.Values)
		if err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil, provisionOutput{}, fmt.Errorf("instance %q already exists", in.Name)
			}
			return nil, provisionOutput{}, fmt.Errorf("create instance: %w", err)
		}
		return nil, provisionOutput{
			Name: inst.Name, Namespace: inst.Namespace, Phase: inst.Phase, Template: inst.Template,
		}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_instances",
		Title:       "List instances the caller has provisioned",
		Description: "List every instance in the caller's tenant scope across all templates.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listInstancesOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, listInstancesOutput{}, err
		}
		ts, err := listTemplates(ctx, dyn)
		if err != nil {
			return nil, listInstancesOutput{}, fmt.Errorf("list templates: %w", err)
		}
		ins, err := listInstances(ctx, dyn, ts)
		if err != nil {
			return nil, listInstancesOutput{}, fmt.Errorf("list instances: %w", err)
		}
		out := listInstancesOutput{Instances: make([]instanceSummary, 0, len(ins))}
		for _, i := range ins {
			out.Instances = append(out.Instances, instanceSummary{
				Name: i.Name, Template: i.Template, Phase: i.Phase, Message: i.Message,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_instance",
		Title:       "Get one instance's full status",
		Description: "Return phase, conditions, and child resource status for an instance the caller previously provisioned.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in instanceNameInput) (*mcp.CallToolResult, kro.Instance, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, kro.Instance{}, err
		}
		ts, err := listTemplates(ctx, dyn)
		if err != nil {
			return nil, kro.Instance{}, fmt.Errorf("list templates: %w", err)
		}
		inst, err := getInstance(ctx, dyn, ts, in.Name)
		if err != nil {
			if errors.Is(err, kro.ErrInstanceNotFound) {
				return nil, kro.Instance{}, fmt.Errorf("instance %q not found", in.Name)
			}
			return nil, kro.Instance{}, fmt.Errorf("get instance: %w", err)
		}
		return nil, *inst, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_instance",
		Title:       "Delete an instance",
		Description: "Delete an instance (and its bridged credentials Secret via OwnerReference GC). Idempotent: returns deleted=true even if already gone.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in instanceNameInput) (*mcp.CallToolResult, deleteOutput, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, deleteOutput{}, err
		}
		ts, err := listTemplates(ctx, dyn)
		if err != nil {
			return nil, deleteOutput{}, fmt.Errorf("list templates: %w", err)
		}
		err = deleteInstance(ctx, dyn, ts, in.Name)
		if err != nil && !errors.Is(err, kro.ErrInstanceNotFound) {
			return nil, deleteOutput{}, fmt.Errorf("delete instance: %w", err)
		}
		return nil, deleteOutput{Deleted: true}, nil
	})
}
