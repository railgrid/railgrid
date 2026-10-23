// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/railgrid/provider-agents/backend"
	"github.com/railgrid/provider-agents/resulttypes"
)

// ErrToolResultReferenceUnavailable is returned before model or pending-tool
// execution when a reference-only result cannot be safely restored.
var ErrToolResultReferenceUnavailable = errors.New("engine: reference-only tool result is unavailable")

// BindToolResultReference binds adapter-authored opaque JSON to the exact
// registered tool call and raw argument bytes that produced it. Adapters are
// responsible for ensuring payload and receipt contain no result bytes or
// secrets.
func BindToolResultReference(toolName, callID, args string, payload, receipt json.RawMessage) (backend.ToolResultReference, error) {
	if strings.TrimSpace(toolName) == "" || len(toolName) > 256 || strings.TrimSpace(callID) == "" || len(callID) > 256 || len(payload) == 0 || !json.Valid(payload) {
		return backend.ToolResultReference{}, fmt.Errorf("engine: invalid reference-only tool result")
	}
	if len(receipt) > 0 && !json.Valid(receipt) {
		return backend.ToolResultReference{}, fmt.Errorf("engine: invalid reference-only tool receipt")
	}
	digest := sha256.Sum256([]byte(args))
	ref := backend.ToolResultReference{
		ToolName: toolName, ToolCallID: callID, ArgumentsDigest: hex.EncodeToString(digest[:]),
		Payload: append(json.RawMessage(nil), payload...), Receipt: append(json.RawMessage(nil), receipt...),
	}
	if err := validateReferenceEnvelope(ref); err != nil {
		return backend.ToolResultReference{}, err
	}
	return ref, nil
}

// ValidateToolResultReference checks the opaque reference's size and exact
// tool/call/raw-argument binding without interpreting adapter JSON.
func ValidateToolResultReference(ref backend.ToolResultReference, toolName, callID, args string) error {
	if err := validateReferenceEnvelope(ref); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(args))
	if ref.ToolName != toolName || ref.ToolCallID != callID || ref.ArgumentsDigest != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("engine: reference-only tool result is not bound to this call")
	}
	return nil
}

func validateReferenceEnvelope(ref backend.ToolResultReference) error {
	if strings.TrimSpace(ref.ToolName) == "" || len(ref.ToolName) > 256 || strings.TrimSpace(ref.ToolCallID) == "" || len(ref.ToolCallID) > 256 ||
		len(ref.ArgumentsDigest) != sha256.Size*2 || len(ref.Payload) == 0 || !json.Valid(ref.Payload) ||
		(len(ref.Receipt) > 0 && !json.Valid(ref.Receipt)) {
		return fmt.Errorf("engine: malformed reference-only tool result")
	}
	if _, err := hex.DecodeString(ref.ArgumentsDigest); err != nil {
		return fmt.Errorf("engine: malformed reference-only call binding")
	}
	encoded, err := json.Marshal(ref)
	if err != nil || len(encoded) > backend.MaxToolResultReferenceBytes {
		return fmt.Errorf("engine: reference-only tool result exceeds %d bytes", backend.MaxToolResultReferenceBytes)
	}
	return nil
}

// rehydrateReferenceOnly replaces checkpoint placeholders only after the
// currently registered adapter authorizes and reads the referenced result.
// It never invokes the original executor, which may have side effects.
func rehydrateReferenceOnly(ctx context.Context, messages []*schema.Message, identities map[*schema.Message]historyIdentity, byName map[string]Tool, pending []PendingCall) error {
	for _, message := range messages {
		if message == nil {
			return ErrToolResultReferenceUnavailable
		}
		identity, hasIdentity := identities[message]
		if message.Role != schema.Tool {
			if hasIdentity && (identity.resultReference != nil || identity.resultUnavailable) {
				return ErrToolResultReferenceUnavailable
			}
			continue
		}
		tool, registered := byName[message.ToolName]
		isReferenceOnly := registered && tool.ResultPolicy == backend.ToolResultReferenceOnly
		if !isReferenceOnly {
			if hasIdentity && identity.resultReference != nil {
				return ErrToolResultReferenceUnavailable
			}
			if hasIdentity && identity.resultUnavailable {
				if message.Content != backend.ToolResultUnavailablePlaceholder {
					return ErrToolResultReferenceUnavailable
				}
				message.Content = backend.ToolResultUnavailablePlaceholder
			}
			continue
		}
		if !hasIdentity || (identity.resultReference == nil && !identity.resultUnavailable) || (identity.resultReference != nil && identity.resultUnavailable) {
			return ErrToolResultReferenceUnavailable
		}
		if identity.resultUnavailable {
			if message.Content != backend.ToolResultUnavailablePlaceholder {
				return ErrToolResultReferenceUnavailable
			}
			message.Content = backend.ToolResultUnavailablePlaceholder
			continue
		}
		args, ok := findCallArgs(messages, message.ToolCallID, message.ToolName)
		if !ok || ValidateToolResultReference(*identity.resultReference, message.ToolName, message.ToolCallID, args) != nil ||
			message.Content != backend.ToolResultReferencePlaceholder || tool.RehydrateResult == nil {
			return ErrToolResultReferenceUnavailable
		}
		callCtx := backend.WithToolCallID(ctx, message.ToolCallID)
		opened, err := tool.RehydrateResult(callCtx, args, identity.resultReference.Clone())
		if errors.Is(err, backend.ErrToolResultReferenceCallerAuthorizationUnavailable) {
			return fmt.Errorf("%w: current caller authorization is unavailable", ErrToolResultReferenceUnavailable)
		}
		if err != nil || len(opened.Images) != 0 || len(opened.Sources) != 0 || opened.Presentation != nil || strings.TrimSpace(opened.Text) == "" ||
			!sameRawJSON(opened.ResultReference, identity.resultReference.Payload) || !sameRawJSON(opened.Receipt, identity.resultReference.Receipt) {
			return ErrToolResultReferenceUnavailable
		}
		message.Content = opened.Text
	}
	for _, item := range pending {
		tool, ok := byName[item.Name]
		if ok && tool.ResultPolicy == backend.ToolResultReferenceOnly && tool.RehydrateResult == nil {
			return ErrToolResultReferenceUnavailable
		}
	}
	return nil
}

func sameRawJSON(a, b json.RawMessage) bool {
	return string(a) == string(b)
}

func findCallArgs(messages []*schema.Message, callID, toolName string) (string, bool) {
	var args string
	found := false
	for _, message := range messages {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID != callID || call.Function.Name != toolName {
				continue
			}
			if found {
				return "", false
			}
			args, found = call.Function.Arguments, true
		}
	}
	return args, found
}

func validatedSources(in []resulttypes.SourceReference) []resulttypes.SourceReference {
	var out []resulttypes.SourceReference
	for _, source := range in {
		if source.Validate() != nil {
			continue
		}
		copy := source
		copy.Access = append([]resulttypes.SourceAccess(nil), source.Access...)
		out = append(out, copy)
	}
	return out
}

func cloneSourceReferences(in []resulttypes.SourceReference) []resulttypes.SourceReference {
	if len(in) == 0 {
		return nil
	}
	out := make([]resulttypes.SourceReference, len(in))
	for i, source := range in {
		out[i] = source
		out[i].Access = append([]resulttypes.SourceAccess(nil), source.Access...)
	}
	return out
}

func compactToolCitationReference(callID string) string {
	if len(callID) <= 16 {
		return callID
	}
	for i := 0; i < len(callID); i++ {
		c := callID[i]
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-", rune(c)) {
			return callID
		}
	}
	return "r_" + callID[len(callID)-12:]
}
