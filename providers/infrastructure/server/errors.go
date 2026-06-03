// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"encoding/json"
	"net/http"
)

// errorResponse is the universal shape for non-2xx responses. The
// portal branches on Reason — UI flows (e.g. "show the
// MissingCredentialsPage") are gated by these strings, so changing
// them is a UI break.
type errorResponse struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// Stable reason codes the portal and MCP tools check by string.
const (
	ReasonBadRequest              = "BadRequest"
	ReasonUnauthorized            = "Unauthorized"
	ReasonForbidden               = "Forbidden"
	ReasonNotFound                = "NotFound"
	ReasonConflict                = "Conflict"
	ReasonInternal                = "InternalError"
	ReasonUpstream                = "UpstreamError"
	ReasonTenantMissing           = "TenantMissing"
	ReasonCloudCredentialsMissing = "CloudCredentialsMissing"
	ReasonAPIBindingMissing       = "APIBindingMissing"
	ReasonTemplateNotFound        = "TemplateNotFound"
	ReasonInstanceNotFound        = "InstanceNotFound"
)

func writeJSONError(w http.ResponseWriter, status int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Reason: reason, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
