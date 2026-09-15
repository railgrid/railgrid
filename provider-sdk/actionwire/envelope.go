// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package actionwire implements the Provider Action response contract.
package actionwire

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
)

type ResourceRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Name       string `json:"name"`
}
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
type Envelope struct {
	RequestID     string          `json:"requestID"`
	Provider      string          `json:"provider"`
	Action        string          `json:"action"`
	ActionVersion string          `json:"actionVersion"`
	ResourceRef   ResourceRef     `json:"resourceRef"`
	Result        json.RawMessage `json:"result,omitempty"`
	Error         *Error          `json:"error,omitempty"`
}

func New(r *http.Request, provider, action string, ref ResourceRef) Envelope {
	id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if id == "" || len(id) > 160 {
		id = rand.Text()
	}
	return Envelope{RequestID: id, Provider: provider, Action: action, ActionVersion: "v1", ResourceRef: ref}
}
func (e Envelope) Success(result any) ([]byte, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	e.Result, e.Error = data, nil
	return json.Marshal(e)
}
func (e Envelope) Failure(w http.ResponseWriter, status int, code, message string, retryable bool) {
	e.Result, e.Error = nil, &Error{Code: code, Message: message, Retryable: retryable}
	w.Header().Set("X-Request-ID", e.RequestID)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}
