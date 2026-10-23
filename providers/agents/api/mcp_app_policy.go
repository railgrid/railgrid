// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	mcpAppApprovedDigestsConfigMaxBytes = 32 * 1024
	mcpAppApprovedProviderMaxCount      = 64
	mcpAppApprovedDigestMaxCount        = 256
	mcpAppApprovedDigestsPerProvider    = 64
)

// ParseMCPAppApprovedDigests parses the operator's explicit MCP App pin list.
// An empty value produces an empty allowlist, which denies every hosted app.
func ParseMCPAppApprovedDigests(raw string) (map[string][]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string][]string{}, nil
	}
	if len(raw) > mcpAppApprovedDigestsConfigMaxBytes {
		return nil, fmt.Errorf("MCP App approved-digest configuration exceeds %d bytes", mcpAppApprovedDigestsConfigMaxBytes)
	}
	input, err := decodeMCPAppApprovedDigests(raw)
	if err != nil {
		return nil, fmt.Errorf("MCP App approved-digest configuration must be a JSON object mapping provider names to SHA-256 arrays")
	}
	if len(input) > mcpAppApprovedProviderMaxCount {
		return nil, fmt.Errorf("MCP App approved-digest configuration has more than %d providers", mcpAppApprovedProviderMaxCount)
	}
	if err := validateMCPAppApprovedDigests(input); err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(input))
	for provider, digests := range input {
		out[provider] = append([]string(nil), digests...)
	}
	return out, nil
}

func decodeMCPAppApprovedDigests(raw string) (map[string][]string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	input := map[string][]string{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		provider, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("provider key is not a string")
		}
		if _, duplicate := input[provider]; duplicate {
			return nil, fmt.Errorf("provider %q is repeated", provider)
		}
		var digests []string
		if err := decoder.Decode(&digests); err != nil {
			return nil, err
		}
		input[provider] = digests
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if closing != json.Delim('}') {
		return nil, fmt.Errorf("expected closing object delimiter")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return nil, fmt.Errorf("configuration has trailing data")
	}
	return input, nil
}

func validateMCPAppApprovedDigests(input map[string][]string) error {
	if len(input) > mcpAppApprovedProviderMaxCount {
		return fmt.Errorf("MCP App approved-digest configuration has more than %d providers", mcpAppApprovedProviderMaxCount)
	}
	total := 0
	for provider, digests := range input {
		if len(validation.IsDNS1123Label(provider)) != 0 || strings.TrimSpace(provider) != provider {
			return fmt.Errorf("MCP App approved-digest provider %q must be a DNS label", provider)
		}
		if len(digests) == 0 || len(digests) > mcpAppApprovedDigestsPerProvider {
			return fmt.Errorf("MCP App approved-digest provider %q must have between 1 and %d pins", provider, mcpAppApprovedDigestsPerProvider)
		}
		seen := make(map[string]bool, len(digests))
		for _, digest := range digests {
			if !validMCPAppDigest(digest) {
				return fmt.Errorf("MCP App approved-digest provider %q has a non-canonical SHA-256 pin", provider)
			}
			if seen[digest] {
				return fmt.Errorf("MCP App approved-digest provider %q repeats a SHA-256 pin", provider)
			}
			seen[digest] = true
			total++
			if total > mcpAppApprovedDigestMaxCount {
				return fmt.Errorf("MCP App approved-digest configuration has more than %d pins", mcpAppApprovedDigestMaxCount)
			}
		}
	}
	return nil
}

func validMCPAppDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func mcpAppDigestApproved(allowlist map[string][]string, provider, digest string) bool {
	if !validMCPAppDigest(digest) {
		return false
	}
	for _, approved := range allowlist[provider] {
		if approved == digest {
			return true
		}
	}
	return false
}
