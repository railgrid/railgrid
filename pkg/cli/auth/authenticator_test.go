// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

func TestLocalhostCallbackRendersBrandedSuccessPage(t *testing.T) {
	data, err := json.Marshal(tenancyv1alpha1.LoginResponse{UserID: "user-1", Email: "user@example.com"})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	req := httptest.NewRequest("GET", "/callback?response="+url.QueryEscape(base64.URLEncoding.EncodeToString(data)), nil)
	rec := httptest.NewRecorder()
	a := NewLocalhostCallbackAuthenticator()
	a.callback(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Header().Get("Content-Type"), "text/html"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<html lang="en">`,
		`<meta name="viewport" content="width=device-width, initial-scale=1">`,
		`role="status" aria-live="polite"`,
		`<span>Faros</span>`,
		`You can close this tab and return to the terminal.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("callback page missing %q: %s", want, body)
		}
	}
}
