// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"errors"
	"net/http"
)

// The sentinel failures of a gated data-plane request. Gate wraps these with
// detail for the provider's own logs; WriteError writes only the status and a
// fixed phrase, so nothing about the tenant, the token or the backend reaches
// the caller.
var (
	// ErrNoCaller is returned when a verb request carries no caller: the
	// shard did not stamp an identity, or the request did not come through
	// serve's subresource adapter at all. Maps to 401.
	ErrNoCaller = errors.New("dataplane: no caller identity on request")
	// ErrNoBearer is returned when a caller-credentialed request (the MCP
	// class, where the hub aggregate forwards the caller's bearer) carries no
	// usable Authorization: Bearer credential. Maps to 401.
	ErrNoBearer = errors.New("dataplane: no bearer token on request")
	// ErrClusterMismatch is returned when the cluster in the path differs
	// from the X-Railgrid-Cluster header. serve's adapter sets the header from
	// the path, so a disagreement means the request was assembled by hand.
	// Maps to 400: the request is self-contradictory and retrying it
	// unchanged cannot succeed.
	ErrClusterMismatch = errors.New("dataplane: path cluster does not match the request cluster header")
	// ErrBadPath is returned for a request that does not match the grammar.
	// Maps to 400.
	ErrBadPath = errors.New("dataplane: malformed data-plane path")
	// ErrDenied is returned when the gate refuses: the caller cannot see the
	// addressed object, or the object is being deleted. Maps to 404 by
	// default so the response does not disclose whether the object exists.
	ErrDenied = errors.New("dataplane: denied")
)

// StatusFor maps err to the HTTP status a data-plane handler should return,
// answering ErrDenied with 404.
func StatusFor(err error) int { return StatusForAs(err, http.StatusNotFound) }

// StatusForAs is StatusFor with the status for ErrDenied chosen by the
// caller. Pass http.StatusForbidden on a route where the addressed object's
// existence is already known to the caller (it listed it a moment ago) and a
// 404 would only be confusing; pass http.StatusNotFound — the default —
// everywhere else.
func StatusForAs(err error, deniedStatus int) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, ErrNoCaller), errors.Is(err, ErrNoBearer), errors.Is(err, ErrNoProxiedIdentity):
		return http.StatusUnauthorized
	case errors.Is(err, ErrClusterMismatch), errors.Is(err, ErrBadPath):
		return http.StatusBadRequest
	case errors.Is(err, ErrDenied):
		if deniedStatus == 0 {
			return http.StatusNotFound
		}
		return deniedStatus
	default:
		return http.StatusInternalServerError
	}
}

// WriteError writes the status for err with a generic body. It never echoes
// err: a gate failure must not leak a tenant path, a token, a cluster ID or a
// backend message to the caller. Log err on the provider side instead.
func WriteError(w http.ResponseWriter, err error) {
	WriteErrorAs(w, err, http.StatusNotFound)
}

// WriteErrorAs is WriteError with the status for ErrDenied chosen by the
// caller; see StatusForAs.
func WriteErrorAs(w http.ResponseWriter, err error, deniedStatus int) {
	status := StatusForAs(err, deniedStatus)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, phraseFor(status), status)
}

// phraseFor is the whole body of an error response: the status text, nothing
// else.
func phraseFor(status int) string {
	if text := http.StatusText(status); text != "" {
		return text
	}
	return http.StatusText(http.StatusInternalServerError)
}
