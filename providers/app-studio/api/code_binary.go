/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"k8s.io/klog/v2"

	"github.com/faroshq/provider-app-studio/hubmcp"
)

// Binary files and the Code provider. App Studio never sends base64 to, or
// asks base64 from, a Code provider whose tool schema does not advertise it
// (see hubmcp/binary.go); both answers are read from one tools/list call and
// cached per workspace cluster.

// codeBinaryCapabilities reports whether code__commit_files accepts base64
// file items and whether code__checkout_repository can return binaries. A
// failed probe answers false for this call only (nothing is cached).
func (s *Server) codeBinaryCapabilities(ctx context.Context, r *http.Request, id identity) (commit, checkout bool) {
	cluster := strings.TrimSpace(id.clusterID)
	commit, commitOK := s.codeCommitBinary.Get(cluster)
	checkout, checkoutOK := s.codeCheckoutBinary.Get(cluster)
	if commitOK && checkoutOK {
		return commit, checkout
	}
	if r == nil || cluster == "" {
		return false, false
	}
	tools, err := fetchProjectMCPTools(ctx, s.mcpEndpoint(cluster), r, id.tenant, s.mcpInsecureSkipTLSVerify)
	if err != nil {
		klog.V(2).Infof("read Code provider tool catalog for cluster %s: %v", cluster, err)
		return false, false
	}
	catalog := make([]hubmcp.Tool, 0, len(tools))
	for _, tool := range tools {
		catalog = append(catalog, hubmcp.Tool{Name: tool.Name, InputSchema: tool.InputSchema})
	}
	commit = hubmcp.CommitFilesSupportsEncoding(catalog)
	checkout = hubmcp.CheckoutSupportsBinaryEncoding(catalog)
	s.codeCommitBinary.Set(cluster, commit)
	s.codeCheckoutBinary.Set(cluster, checkout)
	return commit, checkout
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
	if _, checkout := s.codeBinaryCapabilities(ctx, r, id); checkout {
		args["binaryEncoding"] = hubmcp.EncodingBase64
	}
	return args
}

// projectCommitSkippedBinaryPaths reads the paths commit_project_files left
// out (binary on a provider without base64 support) from its tool result.
func projectCommitSkippedBinaryPaths(result string) []string {
	var decoded struct {
		SkippedBinaryPaths []string `json:"skippedBinaryPaths"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(result)), &decoded) != nil {
		return nil
	}
	return decoded.SkippedBinaryPaths
}
