/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"net/http"
	"strings"

	"k8s.io/klog/v2"

	"github.com/railgrid/provider-app-studio/hubmcp"
)

// Binary files and the Code provider. What is left here is about CHECKOUT
// only: `code__checkout_repository` is still an MCP tool, and older Code
// providers cannot return binary blobs, so the opt-in is probed from the
// tool's advertised input schema (see hubmcp/binary.go) and cached per
// workspace cluster.
//
// Commit is no longer probed at all. It is the `repositories/commit/v1`
// action, whose schema declares the encoding for every file, so base64 is
// always accepted and there is no capability to discover — which also retires
// the "binary files stayed dirty because this provider is too old" path that
// used to leak into the assistant's settlement.

// codeCheckoutBinaryEncoding reports whether code__checkout_repository can
// return binaries. A failed probe answers false for this call only (nothing is
// cached).
func (s *Server) codeCheckoutBinaryEncoding(ctx context.Context, r *http.Request, id identity) bool {
	cluster := strings.TrimSpace(id.clusterID)
	if checkout, ok := s.codeCheckoutBinary.Get(cluster); ok {
		return checkout
	}
	if r == nil || cluster == "" {
		return false
	}
	tools, err := fetchProjectMCPTools(ctx, s.mcpEndpoint(cluster), s.hubRequest(r, id), id.tenant, s.mcpInsecureSkipTLSVerify)
	if err != nil {
		klog.V(2).Infof("read Code provider tool catalog for cluster %s: %v", cluster, err)
		return false
	}
	catalog := make([]hubmcp.Tool, 0, len(tools))
	for _, tool := range tools {
		catalog = append(catalog, hubmcp.Tool{Name: tool.Name, InputSchema: tool.InputSchema})
	}
	checkout := hubmcp.CheckoutSupportsBinaryEncoding(catalog)
	s.codeCheckoutBinary.Set(cluster, checkout)
	return checkout
}

// checkoutToolFile is one code__checkout_repository file entry; encoding is
// omitted for text and "base64" for binaries.
type checkoutToolFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

// bytes decodes the entry's content.
func (f checkoutToolFile) bytes() ([]byte, error) {
	return hubmcp.DecodeWireContent(f.Content, f.Encoding)
}

// checkoutArgs adds the binary opt-in when the provider supports it.
func (s *Server) checkoutArgs(ctx context.Context, r *http.Request, id identity, args map[string]any) map[string]any {
	if s.codeCheckoutBinaryEncoding(ctx, r, id) {
		args["binaryEncoding"] = hubmcp.EncodingBase64
	}
	return args
}
