// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package oauthgithub

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderResultPreservesOriginPayloadAndCloseTiming(t *testing.T) {
	h := &Handler{cfg: Config{PortalOrigin: "https://portal.example"}}

	t.Run("success", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.renderResult(rec, callbackResult{
			State:  `state<&`,
			Token:  `token<&`,
			Login:  `octo-user`,
			Scopes: `repo,workflow`,
		})

		body := rec.Body.String()
		if got, want := rec.Header().Get("Content-Type"), "text/html; charset=utf-8"; got != want {
			t.Fatalf("Content-Type = %q, want %q", got, want)
		}
		for _, want := range []string{
			`<html lang="en">`,
			`role="status" aria-live="polite"`,
			`<h1 id="status-heading">GitHub connected</h1>`,
			`postMessage(payload, "https://portal.example")`,
			`"type":"faros-github-oauth"`,
			`"state":"state\u003c\u0026"`,
			`"token":"token\u003c\u0026"`,
			`setTimeout(function(){ window.close(); }, payload.error ? 4000 : 600);`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("success callback missing %q: %s", want, body)
			}
		}
		if strings.Contains(body, "Connecting to GitHub") {
			t.Fatalf("success callback still presents an in-progress heading: %s", body)
		}
		if strings.Contains(body, `token<&`) {
			t.Fatalf("token was inserted without JSON escaping: %s", body)
		}
	})

	t.Run("error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.renderResult(rec, callbackResult{State: "state", Error: `denied <script>`})

		body := rec.Body.String()
		for _, want := range []string{
			`role="alert" aria-live="assertive"`,
			`GitHub connection failed`,
			`message.textContent = payload.error ? ('Failed: ' + payload.error)`,
			`setTimeout(function(){ window.close(); }, payload.error ? 4000 : 600);`,
			`"error":"denied \u003cscript\u003e"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("error callback missing %q: %s", want, body)
			}
		}
	})
}
