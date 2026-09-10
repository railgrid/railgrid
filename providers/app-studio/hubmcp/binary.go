/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package hubmcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Binary files on the Code provider wire. Every JSON file entry is
// {"path", "content", "encoding"?}: encoding is omitted (or "utf-8") for
// UTF-8 text without NUL bytes and "base64" (standard, padded) otherwise.
// Sizes and limits are measured on decoded bytes.
//
// Capability gating: base64 is only ever sent to a Code provider whose tool
// schema advertises it, so an older provider never writes base64 text into a
// repository. code__commit_files advertises an "encoding" property on its
// file items; code__checkout_repository advertises a "binaryEncoding" input.

const (
	EncodingUTF8   = "utf-8"
	EncodingBase64 = "base64"

	ToolCommitFiles        = "code__commit_files"
	ToolCheckoutRepository = "code__checkout_repository"

	// CommitTextMaxBytes is the Code provider's per-file text bound.
	CommitTextMaxBytes = 2 << 20
	// BinaryFileMaxBytes bounds one binary file on every wire.
	BinaryFileMaxBytes = 25 << 20
	// BundleMaxBytes bounds the decoded bytes of one commit/checkout/sync.
	BundleMaxBytes = 48 << 20
	// BundleMaxFiles bounds the files of one commit/checkout/sync.
	BundleMaxFiles = 500
)

// IsText reports whether data travels as UTF-8 text on the wire.
func IsText(data []byte) bool {
	return utf8.Valid(data) && bytes.IndexByte(data, 0) < 0
}

// WireFile encodes one file entry, choosing the encoding from the bytes.
func WireFile(path string, data []byte) map[string]string {
	if IsText(data) {
		return map[string]string{"path": path, "content": string(data)}
	}
	return map[string]string{"path": path, "content": base64.StdEncoding.EncodeToString(data), "encoding": EncodingBase64}
}

// DecodeWireContent returns the bytes of one wire entry.
func DecodeWireContent(content, encoding string) ([]byte, error) {
	switch strings.TrimSpace(encoding) {
	case "", EncodingUTF8:
		return []byte(content), nil
	case EncodingBase64:
		data, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 content: %w", err)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("unsupported file encoding %q", encoding)
	}
}

// CommitFilesSupportsEncoding reports whether the catalog's
// code__commit_files accepts base64 file items.
func CommitFilesSupportsEncoding(tools []Tool) bool {
	return toolSchemaHas(tools, ToolCommitFiles, "properties", "files", "items", "properties", "encoding")
}

// CheckoutSupportsBinaryEncoding reports whether the catalog's
// code__checkout_repository can return binaries as base64.
func CheckoutSupportsBinaryEncoding(tools []Tool) bool {
	return toolSchemaHas(tools, ToolCheckoutRepository, "properties", "binaryEncoding")
}

func toolSchemaHas(tools []Tool, name string, path ...string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return SchemaHasPath(tool.InputSchema, path...)
		}
	}
	return false
}

// SchemaHasPath walks nested JSON-schema object keys.
func SchemaHasPath(schema json.RawMessage, path ...string) bool {
	var node any
	if len(schema) == 0 || json.Unmarshal(schema, &node) != nil {
		return false
	}
	for _, key := range path {
		object, ok := node.(map[string]any)
		if !ok {
			return false
		}
		if node, ok = object[key]; !ok {
			return false
		}
	}
	return true
}

// CapabilityCache remembers one capability answer per key (a workspace
// cluster) for a bounded time, so a provider upgrade is noticed without a
// tools/list round trip on every commit.
type CapabilityCache struct {
	TTL time.Duration

	mu      sync.Mutex
	entries map[string]capabilityEntry
	now     func() time.Time
}

type capabilityEntry struct {
	value   bool
	expires time.Time
}

// DefaultCapabilityTTL re-probes a Code provider's schema at this interval.
const DefaultCapabilityTTL = 10 * time.Minute

func (c *CapabilityCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Get returns a cached answer that has not expired.
func (c *CapabilityCache) Get(key string) (bool, bool) {
	if c == nil {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !c.clock().Before(entry.expires) {
		return false, false
	}
	return entry.value, true
}

// Set records an answer for the cache TTL.
func (c *CapabilityCache) Set(key string, value bool) {
	if c == nil {
		return
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultCapabilityTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]capabilityEntry{}
	}
	c.entries[key] = capabilityEntry{value: value, expires: c.clock().Add(ttl)}
}
