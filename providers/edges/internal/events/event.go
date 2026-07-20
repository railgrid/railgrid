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

package events

import (
	"encoding/json"
	"strings"
	"time"
)

// Event is a normalized edge event, provider-agnostic in shape so the Store and
// the MCP tool don't depend on UniFi specifics. Timestamps are absolute; the
// original vendor payload is not retained (only the fields below are stored).
type Event struct {
	// ID is the vendor event id (best effort; may be empty).
	ID string `json:"id,omitempty"`
	// Type is the normalized event type, e.g. "motion", "ring", "smartDetect".
	Type string `json:"type"`
	// Start is when the event began.
	Start time.Time `json:"start"`
	// End is when the event ended, if known.
	End time.Time `json:"end,omitempty"`
	// CameraID is the source camera id, if the event is camera-scoped.
	CameraID string `json:"cameraId,omitempty"`
	// Score is the detection confidence (0–100), if reported.
	Score int `json:"score,omitempty"`
	// SmartTypes lists smart-detection classes (person, vehicle, package…),
	// when the event carries them.
	SmartTypes []string `json:"smartTypes,omitempty"`
}

// decodeUniFiFrame parses one UniFi Protect Integration API `/subscribe/events`
// WebSocket message into zero or more normalized Events. It is deliberately
// tolerant: the integration feed wraps event items in an envelope whose exact
// field names have shifted across Protect versions, so we accept the common
// spellings and skip anything we can't place rather than failing the stream.
//
// NOTE: the precise frame schema should be confirmed against a live console;
// unrecognized shapes are returned as (nil, nil) so an unexpected message never
// tears down the subscription.
func decodeUniFiFrame(data []byte) ([]Event, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "pong" {
		return nil, nil
	}

	// The feed may deliver a single object or an array of them.
	var raw json.RawMessage = data
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		arr = []json.RawMessage{raw}
	}

	var out []Event
	for _, item := range arr {
		ev, ok := decodeUniFiItem(item)
		if ok {
			out = append(out, ev)
		}
	}
	return out, nil
}

// uniFiEnvelope models the wrapper the integration feed may use: an action plus
// the event nested under "item" or "data". When neither is present the event
// fields are read from the message itself. Unknown fields are ignored.
type uniFiEnvelope struct {
	Action string          `json:"action,omitempty"`
	Item   json.RawMessage `json:"item,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type uniFiEventFields struct {
	ID        string `json:"id,omitempty"`
	EventType string `json:"type,omitempty"`
	// Timestamps are epoch milliseconds in the Protect feed.
	Start int64 `json:"start,omitempty"`
	End   int64 `json:"end,omitempty"`
	// Camera id under either "camera" or "cameraId".
	Camera   string `json:"camera,omitempty"`
	CameraID string `json:"cameraId,omitempty"`
	Score    int    `json:"score,omitempty"`
	// Smart-detect classes under either spelling.
	SmartDetectTypes []string `json:"smartDetectTypes,omitempty"`
	SmartTypes       []string `json:"smartTypes,omitempty"`
}

func decodeUniFiItem(item json.RawMessage) (Event, bool) {
	var env uniFiEnvelope
	_ = json.Unmarshal(item, &env) // best-effort: no wrapper is fine
	// Read event fields from the nested payload when present, else the message.
	payload := env.Item
	if len(payload) == 0 {
		payload = env.Data
	}
	if len(payload) == 0 {
		payload = item
	}
	var fields uniFiEventFields
	if err := json.Unmarshal(payload, &fields); err != nil {
		return Event{}, false
	}

	typ := fields.EventType
	if typ == "" {
		return Event{}, false // not an event frame (e.g. a keep-alive/status)
	}
	cam := firstNonEmpty(fields.CameraID, fields.Camera)
	smart := fields.SmartTypes
	if len(smart) == 0 {
		smart = fields.SmartDetectTypes
	}
	ev := Event{
		ID:         fields.ID,
		Type:       typ,
		CameraID:   cam,
		Score:      fields.Score,
		SmartTypes: smart,
	}
	if fields.Start > 0 {
		ev.Start = time.UnixMilli(fields.Start).UTC()
	} else {
		ev.Start = time.Now().UTC() // feed omitted a timestamp; stamp on receipt
	}
	if fields.End > 0 {
		ev.End = time.UnixMilli(fields.End).UTC()
	}
	return ev, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
