// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Collections must serialize as [] and never as null. Go marshals a nil slice
// as null, which faulted the portal's Models dashboard on any workspace that
// had never run an agent: `usage.byModel.map(...)` threw during render and took
// the whole view down, including the controls to add a model.
func TestEmptyCollectionsMarshalAsArrays(t *testing.T) {
	t.Run("usage response", func(t *testing.T) {
		// Exactly what usageRollup builds for a workspace with no runs.
		resp := usageResponse{
			WindowDays: 30,
			Total:      usageBucket{Key: "total"},
			ByAgent:    []usageBucket{},
			ByModel:    []usageBucket{},
			Series:     []usagePoint{},
		}
		b, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"byAgent", "byModel", "series"} {
			if strings.Contains(string(b), `"`+field+`":null`) {
				t.Errorf("%s serialized as null; a client mapping over it would fault:\n%s", field, b)
			}
		}
	})

	t.Run("run detail", func(t *testing.T) {
		d := runDetail{runSummary: runSummary{ID: "r1"}, Steps: []runStep{}, Children: []runSummary{}}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"steps", "children"} {
			v, present := got[field]
			if !present {
				t.Errorf("%s is absent; the client sees undefined and faults on .map", field)
				continue
			}
			if _, ok := v.([]any); !ok {
				t.Errorf("%s = %#v, want an array", field, v)
			}
		}
	})

	t.Run("writeList normalizes a nil slice", func(t *testing.T) {
		w := httptest.NewRecorder()
		var items []runSummary // nil
		writeList(w, items)
		body := w.Body.String()
		if strings.Contains(body, `"items":null`) {
			t.Fatalf("nil slice leaked as null: %s", body)
		}
		if !strings.Contains(body, `"items":[]`) {
			t.Fatalf("want an empty array, got: %s", body)
		}
	})

	t.Run("writeList merges extra fields", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeList(w, []runSummary{{ID: "r1"}}, map[string]any{"nextCursor": "abc"})
		var got struct {
			Items      []runSummary `json:"items"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 1 || got.NextCursor != "abc" {
			t.Fatalf("got %+v", got)
		}
	})
}
