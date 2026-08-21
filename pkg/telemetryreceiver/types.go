// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package telemetryreceiver

import (
	"context"
	"errors"
	"time"
)

const (
	CloudEventsBatchContentType = "application/cloudevents-batch+json"
	CloudEventsSpecVersion      = "1.0"
	InstallationHeader          = "X-Faros-Installation-ID"
	// aggregateComponent is deliberately fixed. Aggregate rows are anonymous
	// and must not expose caller-controlled source or installation dimensions.
	aggregateComponent = "faros-hub"
	defaultBucket      = time.Minute
)

var (
	ErrInvalidEvent       = errors.New("invalid cloud event")
	ErrErasureConflict    = errors.New("erasure request id belongs to another installation")
	ErrInstallationErased = errors.New("telemetry installation has been erased")
	ErrInvalidConfig      = errors.New("invalid telemetry configuration")
)

// Event is the normalized form of a structured CloudEvent accepted by the
// receiver. Tenant is a required CloudEvents extension owned by Faros.
type Event struct {
	Tenant          string
	ID              string
	Source          string
	Type            string
	Subject         string
	Time            time.Time
	DataContentType string
	Data            []byte
	Record          Record
	ReceivedAt      time.Time
}

// Record is the defensively decoded hub payload. Identifier values are keyed
// pseudonyms; no raw product identifiers or content are accepted here.
type Record struct {
	InstallationID string                 `json:"installation_id"`
	Provider       string                 `json:"provider"`
	Action         string                 `json:"action"`
	OccurredAt     time.Time              `json:"occurred_at"`
	Identifiers    map[string]string      `json:"identifiers"`
	Properties     map[string]interface{} `json:"properties,omitempty"`
}

type IngestStats struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
}

type ErasureRequest struct {
	RequestID      string `json:"request_id"`
	InstallationID string `json:"installation_id"`
}

type ErasureResult struct {
	RequestID        string `json:"request_id"`
	InstallationID   string `json:"installation_id"`
	DeletedRaw       int64  `json:"deleted_raw"`
	DeletedAggregate int64  `json:"deleted_aggregate"`
	Existing         bool   `json:"existing"`
}

type PurgeResult struct {
	DeletedRaw       int64
	DeletedAggregate int64
}

// Store is the receiver's persistence boundary. Implementations must make
// Insert and EraseInstallation transactional and idempotent.
type Store interface {
	Ping(context.Context) error
	Insert(context.Context, []Event) (IngestStats, error)
	EraseInstallation(context.Context, ErasureRequest) (ErasureResult, error)
	PurgeExpired(context.Context, time.Time, time.Duration, time.Duration) (PurgeResult, error)
}
