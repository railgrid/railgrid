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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/faros-kedge/providers/infrastructure/kro"
	"github.com/faroshq/faros-kedge/providers/infrastructure/tenant"
)

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
	Name      string `json:"name"`
	Template  string `json:"template"`
	Phase     string `json:"phase"`
	Message   string `json:"message,omitempty"`
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
		Description: "List every template available in the central kro cluster, optionally filtered by category or cloud. Call this first when the user asks 'what can I deploy?'.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTemplatesInput) (*mcp.CallToolResult, listTemplatesOutput, error) {
		ts, err := deps.Kro.ListTemplates(ctx, kro.TemplateFilter{Category: in.Category, Cloud: in.Cloud})
		if err != nil {
			return nil, listTemplatesOutput{}, fmt.Errorf("list templates: %w", err)
		}
		out := listTemplatesOutput{Templates: make([]templateSummary, 0, len(ts))}
		for _, t := range ts {
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
		t, err := deps.Kro.GetTemplate(ctx, in.Name, in.Version)
		if err != nil {
			if errors.Is(err, kro.ErrTemplateNotFound) {
				return nil, kro.Template{}, fmt.Errorf("template %q not found", in.Name)
			}
			return nil, kro.Template{}, fmt.Errorf("get template: %w", err)
		}
		return nil, *t, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:  "provision",
		Title: "Materialize a template into the central kro cluster",
		Description: "Create an instance of the named template in the user's tenant namespace. Reads cloud-credentials from the tenant workspace, bridges them into a per-instance Secret, then creates the kro CR. Identity is taken from the bearer token; the user does not need to supply a tenant path.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  false,
			DestructiveHint: &no,
			OpenWorldHint:   &yes,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in provisionInput) (*mcp.CallToolResult, provisionOutput, error) {
		if ident.tenantPath == "" {
			return nil, provisionOutput{}, errors.New("no tenant identity on this request — bearer token did not resolve to a workspace")
		}
		t, err := deps.Kro.GetTemplate(ctx, in.Template, in.TemplateVersion)
		if err != nil {
			return nil, provisionOutput{}, fmt.Errorf("get template: %w", err)
		}
		var creds map[string][]byte
		if deps.Tenant != nil {
			creds, err = tenant.ResolveCloudCredentials(ctx, deps.Tenant, ident.tenantPath)
			if err != nil {
				switch {
				case errors.Is(err, tenant.ErrCredentialsMissing):
					return nil, provisionOutput{}, errors.New("no cloud-credentials Secret in your workspace; create one before provisioning (see kedge credentials docs)")
				case errors.Is(err, tenant.ErrAPIBindingMissing):
					return nil, provisionOutput{}, errors.New("this provider is not enabled in your workspace; enable it via the kedge portal first")
				default:
					return nil, provisionOutput{}, fmt.Errorf("resolve creds: %w", err)
				}
			}
		}
		inst, err := deps.Kro.CreateInstance(ctx, kro.CreateInstanceParams{
			TenantPath:   ident.tenantPath,
			User:         ident.user,
			Template:     *t,
			InstanceName: in.Name,
			Values:       in.Values,
			Credentials:  creds,
		})
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
		if ident.tenantPath == "" {
			return nil, listInstancesOutput{}, errors.New("no tenant identity on this request")
		}
		ins, err := deps.Kro.ListInstances(ctx, ident.tenantPath)
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
		if ident.tenantPath == "" {
			return nil, kro.Instance{}, errors.New("no tenant identity on this request")
		}
		inst, err := deps.Kro.GetInstance(ctx, ident.tenantPath, in.Name)
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
		if ident.tenantPath == "" {
			return nil, deleteOutput{}, errors.New("no tenant identity on this request")
		}
		err := deps.Kro.DeleteInstance(ctx, ident.tenantPath, in.Name)
		if err != nil && !errors.Is(err, kro.ErrInstanceNotFound) {
			return nil, deleteOutput{}, fmt.Errorf("delete instance: %w", err)
		}
		return nil, deleteOutput{Deleted: true}, nil
	})
}
