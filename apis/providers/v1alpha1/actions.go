/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
)

const maxProviderActionSchemaBytes = 256 << 10

var (
	providerActionIDPattern         = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}/v[1-9][0-9]{0,7}$`)
	providerActionAPIVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	providerActionKindPattern       = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	providerActionResourcePattern   = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)
	providerActionDigestPattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ValidateProviderActions validates the complete action map declared by one
// CatalogEntry. Kubernetes enforces the list-map shape at admission, and the
// controller repeats validation so malformed or untrusted catalog state fails
// closed before entering the provider routing registry.
func ValidateProviderActions(actions []ProviderActionSpec) error {
	seen := make(map[string]struct{}, len(actions))
	for i, action := range actions {
		if err := ValidateProviderAction(action); err != nil {
			return fmt.Errorf("actions[%d] (%q): %w", i, action.ID, err)
		}
		if _, ok := seen[action.ID]; ok {
			return fmt.Errorf("actions[%d] (%q): duplicate action ID", i, action.ID)
		}
		seen[action.ID] = struct{}{}
	}
	return nil
}

// ValidateProviderAction validates one action independently of its siblings.
func ValidateProviderAction(action ProviderActionSpec) error {
	if !providerActionIDPattern.MatchString(action.ID) {
		return fmt.Errorf("id must match name/vN (lowercase name and numeric version)")
	}
	if strings.TrimSpace(action.DisplayName) == "" {
		return fmt.Errorf("displayName is required")
	}
	if len(action.Description) > 512 {
		return fmt.Errorf("description exceeds 512 characters")
	}
	if err := validateBoundResource(action.BoundResource); err != nil {
		return fmt.Errorf("boundResource: %w", err)
	}
	if action.InputSchema == nil {
		return fmt.Errorf("inputSchema is required")
	}
	if action.OutputSchema == nil {
		return fmt.Errorf("outputSchema is required")
	}
	if _, err := canonicalProviderActionSchema(action.InputSchema); err != nil {
		return fmt.Errorf("inputSchema: %w", err)
	}
	if _, err := canonicalProviderActionSchema(action.OutputSchema); err != nil {
		return fmt.Errorf("outputSchema: %w", err)
	}

	switch action.ExecutionMode {
	case ProviderActionExecutionSync, ProviderActionExecutionAsync:
	default:
		return fmt.Errorf("executionMode must be sync or async")
	}
	switch action.Risk {
	case ProviderActionRiskLow, ProviderActionRiskMedium, ProviderActionRiskHigh:
	default:
		return fmt.Errorf("risk must be low, medium, or high")
	}
	switch action.Idempotency {
	case ProviderActionIdempotencyInherent, ProviderActionIdempotencyKeyed, ProviderActionIdempotencyNone:
	default:
		return fmt.Errorf("idempotency must be inherent, keyed, or none")
	}
	if err := validateProviderActionLimits(action.Limits); err != nil {
		return fmt.Errorf("limits: %w", err)
	}
	if err := validateProviderActionConsent(action.Consent); err != nil {
		return fmt.Errorf("consent: %w", err)
	}
	if err := validateProviderActionDeprecation(action.Deprecation, action.ID); err != nil {
		return fmt.Errorf("deprecation: %w", err)
	}

	digest, err := ProviderActionSchemaDigest(action)
	if err != nil {
		return err
	}
	if !providerActionDigestPattern.MatchString(action.SchemaDigest) {
		return fmt.Errorf("schemaDigest must match sha256:<64 lowercase hex digits>")
	}
	if action.SchemaDigest != digest {
		return fmt.Errorf("schemaDigest %q does not match canonical schemas (want %s)", action.SchemaDigest, digest)
	}
	return nil
}

func validateBoundResource(resource ProviderActionBoundResource) error {
	if !providerActionAPIVersionPattern.MatchString(resource.APIVersion) || len(resource.APIVersion) > 253 {
		return fmt.Errorf("apiVersion must be a non-empty API version identifier")
	}
	if !providerActionKindPattern.MatchString(resource.Kind) || len(resource.Kind) > 63 {
		return fmt.Errorf("kind must be a PascalCase identifier")
	}
	if !providerActionResourcePattern.MatchString(resource.Resource) || len(resource.Resource) > 63 {
		return fmt.Errorf("resource must be a lowercase DNS-like name")
	}
	return nil
}

func validateProviderActionLimits(limits ProviderActionLimits) error {
	if limits.TimeoutSeconds < 1 || limits.TimeoutSeconds > 3600 {
		return fmt.Errorf("timeoutSeconds must be between 1 and 3600")
	}
	if limits.MaxInputBytes < 1 || limits.MaxInputBytes > 1048576 {
		return fmt.Errorf("maxInputBytes must be between 1 and 1048576")
	}
	if limits.MaxOutputBytes < 1 || limits.MaxOutputBytes > 67108864 {
		return fmt.Errorf("maxOutputBytes must be between 1 and 67108864")
	}
	if limits.MaxResultItems < 1 || limits.MaxResultItems > 10000 {
		return fmt.Errorf("maxResultItems must be between 1 and 10000")
	}
	return nil
}

func validateProviderActionConsent(consent ProviderActionConsent) error {
	if len(consent.Prompt) > 512 {
		return fmt.Errorf("prompt exceeds 512 characters")
	}
	if len(consent.Scope) > 128 {
		return fmt.Errorf("scope exceeds 128 characters")
	}
	if consent.Required {
		if strings.TrimSpace(consent.Prompt) == "" {
			return fmt.Errorf("prompt is required when consent is required")
		}
		if strings.TrimSpace(consent.Scope) == "" {
			return fmt.Errorf("scope is required when consent is required")
		}
		return nil
	}
	if consent.Prompt != "" || consent.Scope != "" {
		return fmt.Errorf("prompt and scope must be empty when consent is not required")
	}
	return nil
}

func validateProviderActionDeprecation(deprecation *ProviderActionDeprecation, actionID string) error {
	if deprecation == nil {
		return nil
	}
	if !deprecation.Deprecated {
		return fmt.Errorf("deprecated must be true when deprecation is present")
	}
	if strings.TrimSpace(deprecation.Message) == "" {
		return fmt.Errorf("message is required for a deprecated action")
	}
	if len(deprecation.Message) > 512 {
		return fmt.Errorf("message exceeds 512 characters")
	}
	if deprecation.ReplacementID != "" {
		if !providerActionIDPattern.MatchString(deprecation.ReplacementID) {
			return fmt.Errorf("replacementID must match name/vN")
		}
		if deprecation.ReplacementID == actionID {
			return fmt.Errorf("replacementID must differ from id")
		}
	}
	return nil
}

// ProviderActionSchemaDigest returns a deterministic digest over the
// canonical JSON object {"input": <schema>, "output": <schema>}. Map keys,
// insignificant whitespace, and schema extension ordering are normalized by
// encoding/json; the digest is stable across equivalent YAML declarations.
func ProviderActionSchemaDigest(action ProviderActionSpec) (string, error) {
	input, err := canonicalProviderActionSchema(action.InputSchema)
	if err != nil {
		return "", fmt.Errorf("inputSchema: %w", err)
	}
	output, err := canonicalProviderActionSchema(action.OutputSchema)
	if err != nil {
		return "", fmt.Errorf("outputSchema: %w", err)
	}
	envelope := struct {
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	}{Input: input, Output: output}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal schema envelope: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalProviderActionSchema(extension *runtime.RawExtension) (json.RawMessage, error) {
	if extension == nil {
		return nil, fmt.Errorf("schema is required")
	}
	raw := extension.Raw
	if len(raw) == 0 && extension.Object != nil {
		var err error
		raw, err = json.Marshal(extension.Object)
		if err != nil {
			return nil, fmt.Errorf("marshal object: %w", err)
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("schema must contain a JSON object")
	}
	if len(raw) > maxProviderActionSchemaBytes {
		return nil, fmt.Errorf("schema exceeds %d bytes", maxProviderActionSchemaBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values are not allowed")
		}
		return nil, fmt.Errorf("invalid trailing JSON: %w", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema must be a JSON object")
	}
	if err := validateJSONSchemaShape(object); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("canonicalize JSON: %w", err)
	}
	return canonical, nil
}

func validateJSONSchemaShape(schema map[string]any) error {
	if schemaValue, ok := schema["$schema"]; ok {
		if _, ok := schemaValue.(string); !ok {
			return fmt.Errorf("$schema must be a string when present")
		}
	}
	if schemaType, ok := schema["type"]; ok {
		switch value := schemaType.(type) {
		case string:
			if !validJSONSchemaType(value) {
				return fmt.Errorf("type %q is not a JSON Schema type", value)
			}
		case []any:
			if len(value) == 0 {
				return fmt.Errorf("type array must not be empty")
			}
			for _, item := range value {
				name, ok := item.(string)
				if !ok || !validJSONSchemaType(name) {
					return fmt.Errorf("type array contains an invalid JSON Schema type")
				}
			}
		default:
			return fmt.Errorf("type must be a string or array of strings")
		}
	}
	if properties, ok := schema["properties"]; ok {
		if _, ok := properties.(map[string]any); !ok {
			return fmt.Errorf("properties must be an object")
		}
	}
	if required, ok := schema["required"]; ok {
		items, ok := required.([]any)
		if !ok {
			return fmt.Errorf("required must be an array of strings")
		}
		for _, item := range items {
			name, ok := item.(string)
			if !ok || strings.TrimSpace(name) == "" {
				return fmt.Errorf("required must contain only non-empty strings")
			}
		}
	}
	return nil
}

func validJSONSchemaType(value string) bool {
	switch value {
	case "array", "boolean", "integer", "null", "number", "object", "string":
		return true
	default:
		return false
	}
}
