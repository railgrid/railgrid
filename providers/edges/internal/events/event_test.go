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

import "testing"

func TestDecodeUniFiFrame_Inline(t *testing.T) {
	// Epoch millis for a fixed instant.
	const startMs = 1_700_000_000_000
	data := []byte(`{"id":"e1","type":"smartDetectZone","start":1700000000000,"camera":"cam1","score":88,"smartDetectTypes":["person"]}`)
	evs, err := decodeUniFiFrame(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	e := evs[0]
	if e.ID != "e1" || e.Type != "smartDetectZone" || e.CameraID != "cam1" || e.Score != 88 {
		t.Fatalf("bad decode: %+v", e)
	}
	if e.Start.UnixMilli() != startMs {
		t.Fatalf("bad start: %v", e.Start)
	}
	if len(e.SmartTypes) != 1 || e.SmartTypes[0] != "person" {
		t.Fatalf("bad smart types: %v", e.SmartTypes)
	}
}

func TestDecodeUniFiFrame_NestedItemAndArray(t *testing.T) {
	// An array of envelopes with the event nested under "item", using cameraId.
	data := []byte(`[{"action":"add","item":{"id":"a","type":"motion","start":1700000000000,"cameraId":"c9"}},{"action":"add","item":{"id":"b","type":"ring","start":1700000001000,"camera":"c9"}}]`)
	evs, err := decodeUniFiFrame(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	if evs[0].Type != "motion" || evs[0].CameraID != "c9" {
		t.Fatalf("bad first: %+v", evs[0])
	}
	if evs[1].Type != "ring" || evs[1].CameraID != "c9" {
		t.Fatalf("bad second: %+v", evs[1])
	}
}

func TestDecodeUniFiFrame_SkipsNonEvents(t *testing.T) {
	// Keep-alive / status frames without a type must not produce events, and
	// must not error (the stream keeps running).
	for _, raw := range []string{`{"hello":"world"}`, `pong`, ``, `{}`} {
		evs, err := decodeUniFiFrame([]byte(raw))
		if err != nil {
			t.Fatalf("%q errored: %v", raw, err)
		}
		if len(evs) != 0 {
			t.Fatalf("%q produced events: %+v", raw, evs)
		}
	}
}

func TestDecodeUniFiFrame_StampsMissingTimestamp(t *testing.T) {
	evs, err := decodeUniFiFrame([]byte(`{"type":"motion","camera":"c1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1, got %d", len(evs))
	}
	if evs[0].Start.IsZero() {
		t.Fatal("expected a receipt timestamp when the feed omits start")
	}
}
