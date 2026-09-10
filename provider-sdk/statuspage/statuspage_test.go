// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package statuspage

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

func TestRenderEscapesDisplayTextAndProvidesAccessibleErrorState(t *testing.T) {
	var out bytes.Buffer
	if err := Render(&out, Page{
		Title:   `<title><script>alert(1)</script>`,
		Heading: `<heading>&"`,
		Message: `<message>`,
		State:   Error,
	}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := out.String()
	for _, raw := range []string{"<title><script>", `<heading>&"`, "<message>"} {
		if strings.Contains(body, raw) {
			t.Fatalf("unescaped display text %q in %s", raw, body)
		}
	}
	for _, want := range []string{
		`<html lang="en">`,
		`<meta name="viewport" content="width=device-width, initial-scale=1">`,
		`role="alert" aria-live="assertive"`,
		`aria-labelledby="status-heading"`,
		`aria-describedby="status-message"`,
		`--faros-surface: #0a0b12`,
		`--faros-text-secondary: #8a8ca6`,
		`--faros-text-primary: #e9e9f2`,
		`overflow-wrap: anywhere`,
		`viewBox="0 0 24 24"`,
		`d="M21 16V8a2 2 0 0 0-1-1.73`,
		`<span>Faros</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered page missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `role="status"`) {
		t.Fatalf("error page has success status role: %s", body)
	}
	if strings.Contains(body, `faros-status__surface::before`) || strings.Contains(body, `faros-accent-glow`) {
		t.Fatalf("status page uses a decorative glow: %s", body)
	}
}

func TestRenderUsesPoliteSuccessStateAndCallerScript(t *testing.T) {
	var out bytes.Buffer
	if err := Render(&out, Page{
		Title:   "Complete",
		Heading: "Connected",
		Message: "Return to the portal.",
		State:   Success,
		Script:  template.HTML(`<script>document.title = "Connected";</script>`),
	}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `role="status" aria-live="polite"`) {
		t.Fatalf("success page is not a polite status: %s", body)
	}
	if !strings.Contains(body, `<script>document.title = "Connected";</script>`) {
		t.Fatalf("caller script was not preserved: %s", body)
	}
	if strings.Contains(body, "fonts.googleapis.com") || strings.Contains(body, "font-src") {
		t.Fatalf("status page requests external fonts or CSP resources: %s", body)
	}
}
