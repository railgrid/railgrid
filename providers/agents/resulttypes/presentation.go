// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package resulttypes

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	MaxProviderPresentationBytes     = 8 * 1024
	MaxMCPAppHTMLBytes               = 1 << 20
	MaxProviderPresentationWireBytes = MaxProviderPresentationBytes + MaxMCPAppHTMLBytes*6 + 8*1024
	MCPAppMIMEType                   = "text/html;profile=mcp-app"
)

// ProviderPresentation is a bounded native fallback title plus an optional
// content-free MCP App receipt. HTML bytes are transient and returned only
// after source revalidation through the attended capture path.
type ProviderPresentation struct {
	Version         int              `json:"version"`
	Title           string           `json:"title,omitempty"`
	MCPAppReference *MCPAppReference `json:"mcpAppReference,omitempty"`
	MCPApp          *MCPAppResource  `json:"mcpApp,omitempty"`
}

// MCPAppReference is the content-free receipt recorded in a run trace. Its
// digest is computed by this provider from a resource read through the trusted
// Hub aggregate; capture and reopen fetch that exact resource again.
type MCPAppReference struct {
	Provider    string `json:"provider"`
	ResourceURI string `json:"resourceUri"`
	SHA256      string `json:"sha256"`
}

// MCPAppResource is an exact HTML resource fetched through the explicitly
// trusted aggregate connection for a source-revalidated attended response.
type MCPAppResource struct {
	ResourceURI string `json:"resourceUri"`
	MIMEType    string `json:"mimeType"`
	HTML        string `json:"html"`
	SHA256      string `json:"sha256"`
}

func (a *MCPAppResource) Clone() *MCPAppResource {
	if a == nil {
		return nil
	}
	out := *a
	return &out
}

func (a *MCPAppResource) Validate() error {
	if a == nil || !validMCPAppResourceURI(a.ResourceURI) {
		return fmt.Errorf("MCP App resource must have a bounded ui:// URI")
	}
	if a.MIMEType != MCPAppMIMEType {
		return fmt.Errorf("MCP App MIME type must be %q", MCPAppMIMEType)
	}
	if len(a.HTML) == 0 || len(a.HTML) > MaxMCPAppHTMLBytes || !utf8.ValidString(a.HTML) {
		return fmt.Errorf("MCP App HTML must be valid UTF-8 between 1 and %d bytes", MaxMCPAppHTMLBytes)
	}
	if !validSHA256(a.SHA256) {
		return fmt.Errorf("MCP App SHA-256 must be lowercase hexadecimal")
	}
	if _, err := hex.DecodeString(a.SHA256); err != nil {
		return fmt.Errorf("MCP App SHA-256 must be lowercase hexadecimal")
	}
	digest := sha256.Sum256([]byte(a.HTML))
	if a.SHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("MCP App SHA-256 does not match its HTML")
	}
	return nil
}

func (r *MCPAppReference) Validate() error {
	if r == nil || len(validation.IsDNS1123Label(r.Provider)) != 0 || strings.TrimSpace(r.Provider) != r.Provider {
		return fmt.Errorf("MCP App reference needs a canonical provider name")
	}
	if !validHubMCPAppResourceURI(r.ResourceURI, r.Provider) {
		return fmt.Errorf("MCP App reference needs its exact Hub-namespaced resource URI")
	}
	if !validSHA256(r.SHA256) {
		return fmt.Errorf("MCP App reference SHA-256 must be lowercase hexadecimal")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validHubMCPAppResourceURI(value, provider string) bool {
	if !validMCPAppResourceURI(value) {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Host != "railgrid-provider" || u.RawQuery != "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != provider || parts[1] == "" {
		return false
	}
	encoded, err := url.PathUnescape(parts[1])
	if err != nil || encoded != parts[1] || strings.ContainsAny(encoded, "/\\%") {
		return false
	}
	original, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(original) != encoded || !validMCPAppSourceURI(string(original)) {
		return false
	}
	return true
}

func validMCPAppSourceURI(value string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n\t ") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "ui" && u.Host != "" && u.User == nil && u.Opaque == "" && u.Fragment == ""
}

func validMCPAppResourceURI(value string) bool {
	if value == "" || len(value) > 4096 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n\t ") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "ui" && u.Host != "" && u.Opaque == "" && u.User == nil && u.Fragment == ""
}

func (p *ProviderPresentation) Clone() *ProviderPresentation {
	if p == nil {
		return nil
	}
	out := *p
	if p.MCPAppReference != nil {
		ref := *p.MCPAppReference
		out.MCPAppReference = &ref
	}
	out.MCPApp = p.MCPApp.Clone()
	return &out
}

func (p *ProviderPresentation) Validate() error {
	if p == nil || p.Version != 1 || len(p.Title) > 512 || !safePresentationText(p.Title) {
		return fmt.Errorf("invalid provider presentation")
	}
	if p.MCPApp != nil {
		if err := p.MCPApp.Validate(); err != nil {
			return err
		}
		if p.MCPAppReference == nil || p.MCPApp.ResourceURI != p.MCPAppReference.ResourceURI || p.MCPApp.SHA256 != p.MCPAppReference.SHA256 {
			return fmt.Errorf("MCP App resource does not match its content-free reference")
		}
	}
	if p.MCPAppReference != nil {
		if err := p.MCPAppReference.Validate(); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil || len(encoded) > MaxProviderPresentationWireBytes {
		return fmt.Errorf("provider presentation exceeds its encoded size limit")
	}
	if p.MCPApp == nil && len(encoded) > MaxProviderPresentationBytes {
		return fmt.Errorf("provider presentation exceeds %d bytes", MaxProviderPresentationBytes)
	}
	return nil
}

func safePresentationText(value string) bool {
	for _, r := range value {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}

// ParseProviderPresentation ignores unsupported fields so a remote tool can
// add metadata without changing the captured data fallback contract.
func ParseProviderPresentation(raw []byte) (*ProviderPresentation, error) {
	if len(raw) == 0 || len(raw) > MaxProviderPresentationBytes {
		return nil, fmt.Errorf("provider presentation must be between 1 and %d bytes", MaxProviderPresentationBytes)
	}
	var envelope struct {
		Version int    `json:"version"`
		Title   string `json:"title,omitempty"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Version != 1 || len(envelope.Title) > 512 || !safePresentationText(envelope.Title) {
		return nil, fmt.Errorf("invalid provider presentation")
	}
	out := &ProviderPresentation{Version: envelope.Version, Title: envelope.Title}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}
