// Copyright 2026 The Railgrid Authors.
// SPDX-License-Identifier: Apache-2.0

package mcpaggregate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"strings"
	"unicode"

	"github.com/go-logr/logr"
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
	if strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "ui" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
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
	visibility, valid := appToolVisibility(t)
	if uri := appResourceURI(t); valid && uri != "" {
		if meta == nil {
			meta = mcp.Meta{}
		}
		ui := map[string]any{"resourceUri": federatedAppURI(p.Name, uri)}
		if visibility != nil {
			ui["visibility"] = visibility
		}
		meta["ui"] = ui
	}
	return meta
}

func registerAppResources(requestCtx context.Context, srv *mcp.Server, target *providerTools, log logr.Logger, readSlots chan struct{}) {
	seen := map[string]bool{}
	for _, tool := range target.tools {
		original := appResourceURI(tool)
		if !appToolModelVisible(tool) || original == "" || seen[original] {
			continue
		}
		seen[original] = true
		uri := federatedAppURI(target.provider.Name, original)
		name := target.provider.Name + " " + tool.Name + " app"
		err := addAppResource(srv, &mcp.Resource{URI: uri, Name: name, MIMEType: providerMCPAppMIMEType}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			ctx, cancel := context.WithTimeout(ctx, target.client.callTimeout)
			defer cancel()
			stop := context.AfterFunc(requestCtx, cancel)
			defer stop()
			// Slots are shared by the long-lived HTTP handler, not the stateless
			// per-request MCP server. Waiting consumes the same call deadline.
			select {
			case readSlots <- struct{}{}:
				defer func() { <-readSlots }()
			case <-ctx.Done():
				return nil, fmt.Errorf("provider app resource read capacity unavailable: %w", ctx.Err())
			}
			params, _ := json.Marshal(map[string]string{"uri": original})
			// Resource payloads have a separate, smaller bound than tool data.
			client := *target.client
			if client.maxResponseBytes <= 0 || client.maxResponseBytes > providerMCPAppEnvelopeBytes {
				client.maxResponseBytes = providerMCPAppEnvelopeBytes
			}
			body, err := client.rpc(ctx, target.provider.MCPURL, "resources/read", params, target.client.callTimeout)
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
			// Advertise one canonical MIME spelling to downstream hosts.
			content.MIMEType = providerMCPAppMIMEType
			result.Meta = nil
			content.Meta = appResourceMeta(content.Meta)
			content.URI = uri
			return &result, nil
		})
		if err != nil {
			log.Error(err, "provider app resource registration skipped", "provider", target.provider.Name)
		}
	}
}

// A private app-only tool must not become a model-callable tool merely because
// it traversed federation. Invalid visibility declarations also fail closed.
func appToolModelVisible(t discoveredTool) bool {
	visibility, valid := appToolVisibility(t)
	if !valid {
		return false
	}
	if visibility == nil {
		return true
	}
	for _, value := range visibility {
		if value == "model" {
			return true
		}
	}
	return false
}

// appToolVisibility validates and copies the standard two-token set without
// serializing arbitrary provider metadata. Nil means the default visibility.
func appToolVisibility(t discoveredTool) ([]string, bool) {
	ui, ok := t.Meta["ui"].(map[string]any)
	if !ok {
		return nil, true
	}
	raw, exists := ui["visibility"]
	if !exists {
		return nil, true
	}
	var values []string
	switch raw := raw.(type) {
	case []string:
		values = raw
	case []any:
		if len(raw) > 2 {
			return nil, false
		}
		for _, value := range raw {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			values = append(values, text)
		}
	default:
		return nil, false
	}
	if len(values) == 0 || len(values) > 2 {
		return nil, false
	}
	model, app := false, false
	for _, value := range values {
		switch value {
		case "model":
			model = true
		case "app":
			app = true
		default:
			return nil, false
		}
	}
	visibility := make([]string, 0, 2)
	if model {
		visibility = append(visibility, "model")
	}
	if app {
		visibility = append(visibility, "app")
	}
	return visibility, true
}

func validFederatedAppContent(content *mcp.ResourceContents) bool {
	if content == nil {
		return false
	}
	kind, params, err := mime.ParseMediaType(content.MIMEType)
	return err == nil && kind == "text/html" && params["profile"] == "mcp-app" &&
		(content.Text == "") != (len(content.Blob) == 0) && len(content.Text)+len(content.Blob) <= providerMCPAppContentBytes
}

func addAppResource(srv *mcp.Server, resource *mcp.Resource, handler mcp.ResourceHandler) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("invalid provider app resource")
		}
	}()
	srv.AddResource(resource, handler)
	return nil
}

// Resource federation forwards the standard UI namespace, not provider-private
// metadata. App capabilities are still validated by the consuming host.
func appResourceMeta(meta mcp.Meta) mcp.Meta {
	ui, ok := meta["ui"].(map[string]any)
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(ui)
	if err != nil || len(encoded) > providerMCPAppMetaBytes {
		return nil
	}
	return mcp.Meta{"ui": ui}
}
