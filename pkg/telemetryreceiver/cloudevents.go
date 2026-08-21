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
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	cehttp "github.com/cloudevents/sdk-go/v2/protocol/http"

	"github.com/faroshq/faros/telemetry/catalog"
	"github.com/faroshq/faros/telemetry/generated"
)

const cloudEventsTypePrefix = "dev.faros.telemetry."

var identifierHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

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
		if cloudEvent.SpecVersion() != CloudEventsSpecVersion {
			return nil, fmt.Errorf("cloud event %d: %w: unsupported specversion %q", i, ErrInvalidEvent, cloudEvent.SpecVersion())
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
	rawTenant, ok := tenancy.(string)
	if !ok {
		return Event{}, fmt.Errorf("%w: tenant extension must be a non-empty string", ErrInvalidEvent)
	}
	tenant, ok := normalizeTenantID(rawTenant)
	if !ok {
		return Event{}, fmt.Errorf("%w: tenant extension must be a non-empty identifier without embedded whitespace", ErrInvalidEvent)
	}
	typeName, ok := normalizeEventType(cloudEvent.Type())
	if !ok {
		return Event{}, fmt.Errorf("%w: event type %q is not declared", ErrInvalidEvent, cloudEvent.Type())
	}
	if len(cloudEvent.ID()) > 256 || len(cloudEvent.Source()) > 512 || len(typeName) > 256 || len(cloudEvent.Subject()) > 512 {
		return Event{}, fmt.Errorf("%w: metadata exceeds length limit", ErrInvalidEvent)
	}
	if len(cloudEvent.DataEncoded) == 0 || !json.Valid(cloudEvent.DataEncoded) {
		return Event{}, fmt.Errorf("%w: data must be valid JSON", ErrInvalidEvent)
	}
	definition, _ := generated.LookupEvent(typeName)
	record, err := decodeRecord(cloudEvent.DataEncoded, definition)
	if err != nil {
		return Event{}, err
	}
	if record.Action != typeName || record.Provider != cloudEvent.Subject() || record.Provider != definition.Owner {
		return Event{}, fmt.Errorf("%w: action, provider, type, subject, and catalog owner must agree", ErrInvalidEvent)
	}
	if record.InstallationID != tenant {
		return Event{}, fmt.Errorf("%w: installation_id must match tenant", ErrInvalidEvent)
	}
	wantSource := "faros://installation/" + tenant + "/hub"
	if cloudEvent.Source() != wantSource {
		return Event{}, fmt.Errorf("%w: source must identify the tenant installation hub", ErrInvalidEvent)
	}
	if record.OccurredAt.IsZero() || (!cloudEvent.Time().IsZero() && !record.OccurredAt.Equal(cloudEvent.Time())) {
		return Event{}, fmt.Errorf("%w: occurred_at must match CloudEvent time", ErrInvalidEvent)
	}
	contentType := cloudEvent.DataContentType()
	if contentType == "" {
		contentType = "application/json"
	}
	mediaType, _, mediaErr := mime.ParseMediaType(contentType)
	if mediaErr != nil || mediaType != "application/json" || len(contentType) > 128 {
		return Event{}, fmt.Errorf("%w: invalid datacontenttype", ErrInvalidEvent)
	}
	eventTime := record.OccurredAt
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	} else {
		eventTime = eventTime.UTC()
	}
	return Event{
		Tenant:          tenant,
		ID:              cloudEvent.ID(),
		Source:          cloudEvent.Source(),
		Type:            typeName,
		Subject:         cloudEvent.Subject(),
		Time:            eventTime,
		DataContentType: contentType,
		Data:            append([]byte(nil), cloudEvent.DataEncoded...),
		Record:          record,
	}, nil
}

func decodeRecord(data []byte, definition catalog.EventDefinition) (Record, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("%w: data is not a hub telemetry Record: %v", ErrInvalidEvent, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Record{}, fmt.Errorf("%w: data contains trailing JSON", ErrInvalidEvent)
	}
	if len(record.Identifiers) != len(definition.Identifiers) {
		return Record{}, fmt.Errorf("%w: identifiers do not match catalog", ErrInvalidEvent)
	}
	for _, name := range definition.Identifiers {
		value, ok := record.Identifiers[name]
		if !ok || !identifierHashPattern.MatchString(value) {
			return Record{}, fmt.Errorf("%w: identifier %s must be a SHA-256 pseudonym", ErrInvalidEvent, name)
		}
	}
	if len(record.Properties) != len(definition.AdditionalProperties) {
		return Record{}, fmt.Errorf("%w: properties do not match catalog", ErrInvalidEvent)
	}
	for name, property := range definition.AdditionalProperties {
		value, ok := record.Properties[name]
		if !ok || !validRecordProperty(property, value) {
			return Record{}, fmt.Errorf("%w: property %s violates catalog", ErrInvalidEvent, name)
		}
	}
	return record, nil
}

func validRecordProperty(definition catalog.PropertyDefinition, value interface{}) bool {
	switch definition.Type {
	case "string":
		stringValue, ok := value.(string)
		if !ok {
			return false
		}
		for _, allowed := range definition.Enum {
			if stringValue == allowed {
				return true
			}
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		number, ok := value.(float64)
		return ok && definition.Minimum != nil && definition.Maximum != nil && number >= *definition.Minimum && number <= *definition.Maximum
	default:
		return false
	}
}

func normalizeTenantID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return "", false
	}
	return value, true
}

func normalizeEventType(value string) (string, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), cloudEventsTypePrefix)
	definition, ok := generated.LookupEvent(value)
	return value, ok && definition.Status == "active"
}
