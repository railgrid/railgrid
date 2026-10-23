// Copyright 2026 The Railgrid Authors.
// SPDX-License-Identifier: Apache-2.0

package mcpaggregate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// appResourceURI accepts only a tool's declared MCP Apps resource. A resource
// identifier is never interpreted as an HTTP URL or a federation target.
func appResourceURI(t discoveredTool) string {
	ui, ok := t.Meta["ui"].(map[string]any)
	if !ok {
		return ""
	}
	raw, _ := ui["resourceUri"].(string)
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "ui" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return ""
	}
	return raw
}

func federatedAppURI(provider, resource string) string {
	return "ui://railgrid-provider/" + url.PathEscape(provider) + "/" + base64.RawURLEncoding.EncodeToString([]byte(resource))
}

// proxyToolMeta forwards only standardized UI linkage, alongside coordinates
// validated against the registry. Provider-specific metadata stays private.
func proxyToolMeta(p ProviderTarget, t discoveredTool) mcp.Meta {
	meta := toolCoordinateMeta(p, t)
	if uri := appResourceURI(t); uri != "" {
		if meta == nil {
			meta = mcp.Meta{}
		}
		ui := map[string]any{"resourceUri": federatedAppURI(p.Name, uri)}
		if v, ok := t.Meta["ui"].(map[string]any)["visibility"]; ok {
			ui["visibility"] = v
		}
		meta["ui"] = ui
	}
	return meta
}

func registerAppResources(srv *mcp.Server, target *providerTools) {
	seen := map[string]bool{}
	for _, tool := range target.tools {
		original := appResourceURI(tool)
		if !appToolModelVisible(tool) || original == "" || seen[original] {
			continue
		}
		seen[original] = true
		uri := federatedAppURI(target.provider.Name, original)
		srv.AddResource(&mcp.Resource{URI: uri, Name: target.provider.Name + " app", MIMEType: "text/html;profile=mcp-app"}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			params, _ := json.Marshal(map[string]string{"uri": original})
			// Resource payloads have a separate, smaller bound than tool data.
			client := *target.client
			if client.maxResponseBytes <= 0 || client.maxResponseBytes > 8<<20 {
				client.maxResponseBytes = 8 << 20
			}
			body, err := client.rpc(ctx, target.provider.MCPURL, "resources/read", params, target.client.discoveryTimeout)
			if err != nil {
				return nil, fmt.Errorf("provider app resource unavailable: %w", err)
			}
			var result mcp.ReadResourceResult
			if err = json.Unmarshal(body, &result); err != nil {
				return nil, fmt.Errorf("decode provider app resource: %w", err)
			}
			if len(result.Contents) != 1 || result.Contents[0] == nil || result.Contents[0].URI != original {
				return nil, fmt.Errorf("provider app resource identity mismatch")
			}
			content := result.Contents[0]
			if !validFederatedAppContent(content) {
				return nil, fmt.Errorf("provider app resource has unsupported MIME type or size")
			}
			result.Meta = nil
			content.Meta = appResourceMeta(content.Meta)
			content.URI = uri
			return &result, nil
		})
	}
}

// A private app-only tool must not become a model-callable tool merely because
// it traversed federation. Invalid visibility declarations also fail closed.
func appToolModelVisible(t discoveredTool) bool {
	ui, ok := t.Meta["ui"].(map[string]any)
	if !ok {
		return true
	}
	raw, exists := ui["visibility"]
	if !exists {
		return true
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	var visibility []string
	if json.Unmarshal(encoded, &visibility) != nil || len(visibility) == 0 {
		return false
	}
	visible := false
	for _, v := range visibility {
		if v != "model" && v != "app" {
			return false
		}
		visible = visible || strings.EqualFold(v, "model")
	}
	return visible
}

func validFederatedAppContent(content *mcp.ResourceContents) bool {
	return content != nil && content.MIMEType == "text/html;profile=mcp-app" &&
		(content.Text == "") != (len(content.Blob) == 0) && len(content.Text)+len(content.Blob) <= 1<<20
}

// Resource federation forwards the standard UI namespace, not provider-private
// metadata. App capabilities are still validated by the consuming host.
func appResourceMeta(meta mcp.Meta) mcp.Meta {
	ui, ok := meta["ui"].(map[string]any)
	if !ok {
		return nil
	}
	return mcp.Meta{"ui": ui}
}
