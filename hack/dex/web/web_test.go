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

package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedFarosWebAssetsKeepOIDCFrontendContract(t *testing.T) {
	assets := FS()
	for _, path := range []string{
		"static/main.css",
		"static/faros-mark.svg",
		"static/fonts/instrument-sans-latin-wght-normal.woff2",
		"static/fonts/ibm-plex-mono-latin-400-normal.woff2",
		"static/fonts/ibm-plex-mono-latin-600-normal.woff2",
		"static/fonts/ibm-plex-mono-latin-700-normal.woff2",
		"static/fonts/LICENSE-instrument-sans.txt",
		"static/fonts/LICENSE-ibm-plex-mono.txt",
		"static/img/google-icon.svg",
		"templates/header.html",
		"templates/login.html",
		"templates/password.html",
		"templates/approval.html",
		"templates/device.html",
		"templates/device_success.html",
		"templates/error.html",
		"templates/oob.html",
		"templates/footer.html",
		"themes/dark/styles.css",
		"themes/light/styles.css",
	} {
		if _, err := fs.ReadFile(assets, path); err != nil {
			t.Fatalf("embedded asset %q is unavailable: %v", path, err)
		}
	}

	header := readEmbedded(t, assets, "templates/header.html")
	if !strings.Contains(header, "<title>Faros — Sign in</title>") ||
		!strings.Contains(header, "Sign in to Faros securely") ||
		!strings.Contains(header, `static/faros-mark.svg`) {
		t.Fatalf("header lost Faros title, description, or mark: %s", header)
	}

	login := readEmbedded(t, assets, "templates/login.html")
	if !strings.Contains(login, "Sign in with {{ $c.Name }}") || strings.Contains(login, "Continue with") {
		t.Fatalf("connector action copy is not Faros sign-in copy: %s", login)
	}
	password := readEmbedded(t, assets, "templates/password.html")
	if !strings.Contains(password, "Sign in with Faros") {
		t.Fatalf("password action does not identify Faros: %s", password)
	}

	var pages strings.Builder
	var templatePages strings.Builder
	for _, path := range []string{
		"templates/header.html",
		"templates/login.html",
		"templates/password.html",
		"templates/approval.html",
		"templates/device.html",
		"templates/device_success.html",
		"templates/error.html",
		"templates/oob.html",
		"static/main.css",
		"themes/dark/styles.css",
		"themes/light/styles.css",
	} {
		content := readEmbedded(t, assets, path)
		pages.WriteString(content)
		if strings.HasPrefix(path, "templates/") {
			templatePages.WriteString(content)
		}
	}
	content := pages.String()
	for _, forbidden := range []string{
		"fonts.googleapis.com",
		"fonts.gstatic.com",
		"preconnect",
		"@import",
		"Continue with",
	} {
		if strings.Contains(strings.ToLower(content), strings.ToLower(forbidden)) {
			t.Fatalf("embedded Dex overlay contains forbidden upstream/dependency text %q", forbidden)
		}
	}

	for _, localFont := range []string{
		`url("./fonts/instrument-sans-latin-wght-normal.woff2")`,
		`url("./fonts/ibm-plex-mono-latin-400-normal.woff2")`,
		`url("./fonts/ibm-plex-mono-latin-600-normal.woff2")`,
		`url("./fonts/ibm-plex-mono-latin-700-normal.woff2")`,
	} {
		if !strings.Contains(content, localFont) {
			t.Fatalf("embedded Dex overlay does not load local design-system font %q", localFont)
		}
	}

	// Ignore class names and template attributes when checking user-visible
	// copy: Dex's template contract necessarily retains dex-* selectors.
	visible := stripMarkupAndTemplateActions(templatePages.String())
	if strings.Contains(strings.ToLower(visible), "dex") {
		t.Fatalf("embedded page copy exposes upstream Dex branding: %q", visible)
	}
	if strings.Contains(visible, "faros terms") {
		t.Fatalf("embedded page copy uses lowercase Faros branding: %q", visible)
	}
}

func readEmbedded(t *testing.T, assets fs.FS, path string) string {
	t.Helper()
	data, err := fs.ReadFile(assets, path)
	if err != nil {
		t.Fatalf("read embedded asset %q: %v", path, err)
	}
	return string(data)
}

var (
	markupPattern   = regexp.MustCompile(`<[^>]*>`)
	templatePattern = regexp.MustCompile(`\{\{[^}]*\}\}`)
	commentPattern  = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

func stripMarkupAndTemplateActions(content string) string {
	content = commentPattern.ReplaceAllString(content, " ")
	content = markupPattern.ReplaceAllString(content, " ")
	return templatePattern.ReplaceAllString(content, " ")
}
