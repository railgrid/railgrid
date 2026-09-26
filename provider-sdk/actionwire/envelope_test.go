// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actionwire

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestEnvelopePreservesNullResultsAndExclusiveErrors(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	e := New(r, "code", "find-pull-request", ResourceRef{APIVersion: "code.railgrid.ai/v1alpha1", Kind: "Repository", Resource: "repositories", Name: "repo"})
	data, err := e.Success(nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Envelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RequestID == "" || string(decoded.Result) != "null" || decoded.Error != nil {
		t.Fatalf("invalid empty lookup: %s", data)
	}
	w := httptest.NewRecorder()
	decoded.Failure(w, 403, "forbidden", "Action forbidden", false)
	var failure Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error == nil || failure.Error.Retryable || len(failure.Result) != 0 || failure.RequestID != decoded.RequestID {
		t.Fatalf("invalid error: %s", w.Body.String())
	}
}
