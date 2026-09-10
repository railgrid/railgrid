// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package statuspage renders small, self-contained Faros status documents for
// browser flows that finish outside the portal shell.
package statuspage

import (
	"html/template"
	"io"
)

// State controls the semantic status role and the status color used by the
// page. Callers should select Error when the document reports a failed action.
type State string

const (
	// Success is a completed action.
	Success State = "success"
	// Error is a failed action that needs the user's attention.
	Error State = "error"
)

// Page is the caller-owned content for a status document. Script is an
// optional, trusted script element owned by the caller; it is inserted after
// the status markup so the caller can preserve its own browser-flow behavior.
// Callers should build it with html/template. Display strings are escaped by
// the html/template renderer.
type Page struct {
	Title   string
	Heading string
	Message string
	State   State
	Script  template.HTML
}

// Render writes a bounded, branded status document. It only renders the
// presentation; callers retain ownership of response headers, CSP, auth, and
// callback behavior.
func Render(w io.Writer, page Page) error {
	page.State = normalizeState(page.State)
	return pageTemplate.Execute(w, page)
}

func normalizeState(state State) State {
	if state == Error {
		return Error
	}
	return Success
}

var pageTemplate = template.Must(template.New("faros-status-page").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .Title }}</title>
  <style>
    :root {
      color-scheme: dark;
      --faros-surface: #0a0b12;
      --faros-surface-raised: #111320;
      --faros-surface-overlay: #171927;
      --faros-border-default: rgba(255, 255, 255, 0.11);
      --faros-accent: #8b6bff;
      --faros-accent-hover: #a18aff;
      --faros-accent-subtle: rgba(139, 107, 255, 0.14);
      --faros-success: #2fd6a0;
      --faros-success-subtle: rgba(47, 214, 160, 0.12);
      --faros-danger: #ff5d5d;
      --faros-danger-subtle: rgba(255, 93, 93, 0.12);
      --faros-text-primary: #e9e9f2;
      --faros-text-secondary: #8a8ca6;
      --faros-text-muted: #8587a1;
      --faros-on-accent: #0a0b12;
      --faros-font-sans: ui-sans-serif, system-ui, sans-serif;
      --faros-font-mono: ui-monospace, "SFMono-Regular", Menlo, monospace;
    }

    *, *::before, *::after { box-sizing: border-box; }
    html, body { min-height: 100%; }
    body {
      min-width: 320px;
      margin: 0;
      background: var(--faros-surface);
      color: var(--faros-text-primary);
      font-family: var(--faros-font-sans);
      font-size: 16px;
      line-height: 1.5;
      -webkit-font-smoothing: antialiased;
    }
    .faros-status {
      display: grid;
      min-height: 100vh;
      align-content: center;
      justify-items: center;
      gap: 24px;
      padding: 32px 20px;
    }
    .faros-status__brand {
      display: inline-flex;
      align-items: center;
      gap: 10px;
      color: var(--faros-text-primary);
      font-family: var(--faros-font-mono);
      font-size: 12px;
      font-weight: 700;
      letter-spacing: 0.20em;
      line-height: 1;
      text-transform: uppercase;
    }
    .faros-status__mark {
      width: 28px;
      height: 28px;
      flex: 0 0 28px;
      padding: 4px;
      border: 1px solid var(--faros-border-default);
      border-radius: 6px;
      background: var(--faros-surface-overlay);
    }
    .faros-status__surface {
      width: min(100%, 32rem);
      padding: 32px;
      border: 1px solid var(--faros-border-default);
      border-radius: 6px;
      background: var(--faros-surface-raised);
      box-shadow: 0 1px 2px rgba(10, 11, 18, 0.40);
      text-align: center;
    }
    h1 {
      margin: 0;
      color: var(--faros-text-primary);
      font-size: clamp(22px, 5vw, 28px);
      font-weight: 650;
      letter-spacing: -0.02em;
      line-height: 1.15;
      overflow-wrap: anywhere;
    }
    p {
      max-width: 60ch;
      margin: 14px auto 0;
      color: var(--faros-text-secondary);
      overflow-wrap: anywhere;
    }
    .faros-status--success p { color: var(--faros-success); }
    .faros-status--error p { color: var(--faros-danger); }
    @media (max-width: 480px) {
      .faros-status { padding: 24px 16px; }
      .faros-status__surface { padding: 24px 20px; }
    }
    @media (prefers-reduced-motion: reduce) {
      *, *::before, *::after { animation-duration: 0.01ms !important; transition-duration: 0.01ms !important; }
    }
  </style>
</head>
<body>
  <main class="faros-status faros-status--{{ .State }}">
    <div class="faros-status__brand" aria-label="Faros">
      <svg class="faros-status__mark" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" fill="none" stroke="#8b6bff" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"/>
      </svg>
      <span>Faros</span>
    </div>
    <section class="faros-status__surface" {{ if eq .State "error" }}role="alert" aria-live="assertive"{{ else }}role="status" aria-live="polite"{{ end }} aria-labelledby="status-heading" aria-describedby="status-message">
      <h1 id="status-heading">{{ .Heading }}</h1>
      <p id="status-message">{{ .Message }}</p>
    </section>
  </main>
  {{ if .Script }}{{ .Script }}{{ end }}
</body>
</html>`))
