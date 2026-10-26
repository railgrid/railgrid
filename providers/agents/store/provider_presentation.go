// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import "github.com/railgrid/provider-agents/resulttypes"

// Compatibility aliases keep the durable store's public API stable while the
// pure result contract remains independent of persistence.
const (
	MaxProviderPresentationBytes     = resulttypes.MaxProviderPresentationBytes
	MaxMCPAppHTMLBytes               = resulttypes.MaxMCPAppHTMLBytes
	MaxProviderPresentationWireBytes = resulttypes.MaxProviderPresentationWireBytes
	MCPAppMIMEType                   = resulttypes.MCPAppMIMEType
)

type ProviderPresentation = resulttypes.ProviderPresentation
type MCPAppReference = resulttypes.MCPAppReference
type MCPAppResource = resulttypes.MCPAppResource

func ParseProviderPresentation(raw []byte) (*ProviderPresentation, error) {
	return resulttypes.ParseProviderPresentation(raw)
}

// contentFreePresentation removes executable HTML before durable storage. A
// valid, source-backed app identity remains a receipt that an attended reader
// can resolve through the current Hub aggregate.
func contentFreePresentation(in *ProviderPresentation, sources []SourceReference) *ProviderPresentation {
	if in == nil {
		return nil
	}
	out := in.Clone()
	out.MCPApp = nil
	if len(sources) == 0 || out.MCPAppReference == nil || out.MCPAppReference.Validate() != nil {
		out.MCPAppReference = nil
	}
	if out.MCPAppReference == nil && out.Title == "" {
		return nil
	}
	return out
}
