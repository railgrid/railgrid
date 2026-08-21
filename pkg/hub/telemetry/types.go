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

// Package telemetry implements the hub's opt-in product telemetry boundary.
package telemetry

import (
	"context"
	"errors"
	"net/http"
	"time"

	sdktelemetry "github.com/faroshq/provider-sdk/telemetry"
)

const ProviderPathPrefix = "/api/providers/"

var (
	ErrDisabled        = errors.New("telemetry disabled")
	ErrInvalidConfig   = errors.New("invalid telemetry configuration")
	ErrInvalidEvent    = errors.New("invalid telemetry event")
	ErrUnauthorized    = errors.New("telemetry provider unauthorized")
	ErrQueueFull       = errors.New("telemetry queue full")
	ErrClosed          = errors.New("telemetry runtime closed")
	ErrPayloadTooLarge = errors.New("telemetry payload too large")
)

type Event = sdktelemetry.Event

type Identifiers struct {
	Org       string `json:"org,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Project   string `json:"project,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Actor     string `json:"actor,omitempty"`
}

// Record is the only data shape a sink can observe. Every identifier is a
// keyed pseudonym and Properties has already passed the generated catalog.
type Record struct {
	EventID        string         `json:"-"`
	InstallationID string         `json:"installation_id"`
	Provider       string         `json:"provider"`
	Action         string         `json:"action"`
	OccurredAt     time.Time      `json:"occurred_at"`
	Identifiers    Identifiers    `json:"identifiers"`
	Properties     map[string]any `json:"properties,omitempty"`
}

type Sink interface {
	Send(context.Context, []Record) error
}

type ProviderAuthenticator interface {
	Authenticate(context.Context, *http.Request, string) error
}
