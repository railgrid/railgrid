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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	cehttp "github.com/cloudevents/sdk-go/v2/protocol/http"
)

// ParseBatch uses the CloudEvents SDK for the envelope and spec validation,
// then applies Faros-specific tenant, JSON-data, privacy, and size rules.
func ParseBatch(request *http.Request, payload []byte, maxEvents, maxEventBytes int) ([]Event, error) {
	if maxEvents <= 0 || maxEventBytes <= 0 {
		return nil, ErrInvalidConfig
	}
	var rawEvents []json.RawMessage
	if err := json.Unmarshal(payload, &rawEvents); err != nil {
		return nil, fmt.Errorf("decode cloud events batch: %w", err)
	}
	if len(rawEvents) == 0 {
		return nil, fmt.Errorf("decode cloud events batch: empty batch")
	}
	if len(rawEvents) > maxEvents {
		return nil, fmt.Errorf("decode cloud events batch: %d events exceeds limit %d", len(rawEvents), maxEvents)
	}
	clone := request.Clone(request.Context())
	clone.Body = io.NopCloser(bytes.NewReader(payload))
	clone.ContentLength = int64(len(payload))
	clone.Header = request.Header.Clone()
	clone.Header.Set("Content-Type", cloudevents.ApplicationCloudEventsBatchJSON)
	cloudEvents, err := cehttp.NewEventsFromHTTPRequest(clone)
	if err != nil {
		return nil, fmt.Errorf("decode cloud events batch: %w", err)
	}
	if len(cloudEvents) != len(rawEvents) {
		return nil, fmt.Errorf("decode cloud events batch: event count mismatch")
	}
	events := make([]Event, 0, len(cloudEvents))
	for i, cloudEvent := range cloudEvents {
		if len(rawEvents[i]) > maxEventBytes {
			return nil, fmt.Errorf("cloud event %d exceeds limit %d bytes", i, maxEventBytes)
		}
		if err := cloudEvent.Validate(); err != nil {
			return nil, fmt.Errorf("cloud event %d: %w", i, err)
		}
		event, err := normalizeEvent(cloudEvent)
		if err != nil {
			return nil, fmt.Errorf("cloud event %d: %w", i, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func normalizeEvent(cloudEvent cloudevents.Event) (Event, error) {
	tenancy, err := cloudEvent.Context.GetExtension("tenant")
	if err != nil {
		return Event{}, fmt.Errorf("%w: tenant extension is required", ErrInvalidEvent)
	}
	tenant, ok := tenancy.(string)
	if !ok || tenant == "" {
		return Event{}, fmt.Errorf("%w: tenant extension must be a non-empty string", ErrInvalidEvent)
	}
	if len(cloudEvent.ID()) > 256 || len(cloudEvent.Source()) > 512 || len(cloudEvent.Type()) > 256 || len(cloudEvent.Subject()) > 512 || len(tenant) > 256 {
		return Event{}, fmt.Errorf("%w: metadata exceeds length limit", ErrInvalidEvent)
	}
	if len(cloudEvent.DataEncoded) == 0 || !json.Valid(cloudEvent.DataEncoded) {
		return Event{}, fmt.Errorf("%w: data must be valid JSON", ErrInvalidEvent)
	}
	contentType := cloudEvent.DataContentType()
	if contentType == "" {
		contentType = "application/json"
	}
	if strings.TrimSpace(contentType) == "" || len(contentType) > 128 {
		return Event{}, fmt.Errorf("%w: invalid datacontenttype", ErrInvalidEvent)
	}
	eventTime := cloudEvent.Time()
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	} else {
		eventTime = eventTime.UTC()
	}
	return Event{
		Tenant:          tenant,
		ID:              cloudEvent.ID(),
		Source:          cloudEvent.Source(),
		Type:            cloudEvent.Type(),
		Subject:         cloudEvent.Subject(),
		Time:            eventTime,
		DataContentType: contentType,
		Data:            append([]byte(nil), cloudEvent.DataEncoded...),
	}, nil
}
