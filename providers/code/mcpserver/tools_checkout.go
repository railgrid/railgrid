/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/client-go/dynamic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-code/commitexec"
)

type checkoutRepositoryInput struct {
	RepositoryRef  string `json:"repositoryRef" jsonschema:"Name of the managed Repository CR to read"`
	Ref            string `json:"ref,omitempty" jsonschema:"Branch, tag, or commit SHA; defaults to the repository default branch"`
	BinaryEncoding string `json:"binaryEncoding,omitempty" jsonschema:"Set to base64 to also receive binary files (images, fonts, archives) as {path, content, encoding: base64} with content the RFC 4648 standard padded base64 of the bytes; limits then are 25 MiB per binary file and 48 MiB in total. Omit it and binary files are skipped and listed in skipped, text files are limited to 16 MiB in total, and no file carries an encoding field"`
}

// checkoutRepositoryOutput is the tool's result: the executor's, verbatim.
type checkoutRepositoryOutput = commitexec.CheckoutResult

// registerCheckoutTools wires the repository-read tool: the commit flow in
// reverse, and the MCP projection of the repositories/checkout verb
// (actions/checkout.go). Both run commitexec.Checkout; this one runs it AS
// THE CALLER, with the bearer the MCP request carried, so the caller needs
// `create` on repositorycheckouts in their workspace. A consumer that holds
// no such grant — another provider, or a hub-minted scoped identity, which
// is never minted a write on a foreign kind — invokes the verb instead.
//
// The result is returned ONLY as JSON text content, with no structuredContent
// (hence the untyped output): with a typed output the SDK would carry the
// whole tree twice — once as structuredContent and again as a text copy —
// and a checkout can hold up to 48 MiB of files. Every consumer reads the
// text block (the railgrid CLI falls back to it).
func registerCheckoutTools(srv *mcp.Server, deps Deps, ident identity) {
	yes := true
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "checkout_repository",
		Title:       "Read a repository's files",
		Description: "Read the tree of a managed Repository at a ref (default branch by default) and return the files inline as JSON text {repositoryRef, name, phase, ref, commitSHA, files:[{path, content, encoding?}], skipped}. Text files are UTF-8 (256 KiB each, 500 files). Binary files are skipped unless binaryEncoding=base64, which returns them with encoding=base64. Files over a limit are skipped and listed. Used to import an existing repository or read one into a workspace.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, OpenWorldHint: &yes},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in checkoutRepositoryInput) (*mcp.CallToolResult, any, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, nil, err
		}
		_, out, err := checkoutRepository(ctx, dyn, deps.Bundles, in)
		if err != nil {
			return nil, nil, err
		}
		res, err := checkoutToolResult(out)
		return res, nil, err
	})
}

// checkoutToolResult renders the checkout as a single JSON text block.
func checkoutToolResult(out checkoutRepositoryOutput) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode checkout result: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
}

// checkoutRepository runs the executor as dyn. The executor already words a
// timeout ("did not complete in time") and a failure (the controller's Ready
// message) the way the tool's callers parse them.
func checkoutRepository(ctx context.Context, dyn dynamic.Interface, bundles commitbundle.Store, in checkoutRepositoryInput) (*mcp.CallToolResult, checkoutRepositoryOutput, error) {
	out, err := commitexec.Checkout(ctx, dyn, bundles, commitexec.CheckoutRequest{
		RepositoryRef:  in.RepositoryRef,
		Ref:            in.Ref,
		BinaryEncoding: in.BinaryEncoding,
	})
	return nil, out, err
}
