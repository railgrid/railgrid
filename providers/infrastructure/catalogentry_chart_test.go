/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

// One CatalogEntry source.
//
// The provider used to describe itself twice: manifest.yaml (embedded into the
// binary, applied by the operator) and a hand-maintained copy inside
// deploy/chart/templates/catalogentry.yaml. Two copies of the same object drift
// — the chart's carried a per-template APIExport note the manifest had already
// retired — and only the chart one reaches production.
//
// manifest.yaml is now the only source. The chart template is GENERATED from
// it by renderChartCatalogEntry below: the same document, indented into the
// ConfigMap the bootstrap init container mounts, with the handful of values
// Helm must compute at install time (the release's version, the in-cluster
// Service URLs, the chart's own README) substituted by chartOverrides. This
// test regenerates it and fails on any drift; `go test ./... -update` rewrites
// the template.
//
// Why generation and not `{{ .Files.Get "files/manifest.yaml" }}` (factory's
// shape): Helm cannot patch fields of an opaque file it inlines, so those five
// install-time values would have to be applied by the init container instead —
// and hack/verify-provider-contract.mjs, which compares manifest and chart
// spec-by-spec, simulates Helm only far enough to read a literal block. A
// template whose CatalogEntry is one `.Files.Get` expression reads as empty
// there, so the parity check that makes this whole class of drift visible would
// stop working. Generation keeps both.

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

var updateChartCatalogEntry = flag.Bool("update", false, "rewrite deploy/chart/templates/catalogentry.yaml from manifest.yaml")

const (
	manifestPath   = "manifest.yaml"
	chartEntryPath = "deploy/chart/templates/catalogentry.yaml"
)

// chartOverride replaces one line of the manifest with the Helm expression the
// chart needs in its place. want is how many times the line must appear: a
// mismatch fails the test rather than silently skipping the substitution, so a
// manifest edit that moves one of these values is caught here instead of in a
// cluster.
type chartOverride struct {
	match   string
	replace []string
	want    int
}

// The complete set of values the chart computes at install time. Everything
// else in the CatalogEntry is the manifest's, verbatim.
var chartOverrides = []chartOverride{{
	// The release's version, stamped by `helm package --app-version`; the
	// manifest carries the development placeholder.
	match:   `  version: "0.1.0"`,
	replace: []string{`  version: {{ .Chart.AppVersion | quote }}`},
	want:    1,
}, {
	// ui.url and backend.url: the manifest points at the host binary's dev
	// port, the chart at the in-cluster Service it installs.
	match: `    url: "http://localhost:8082"`,
	replace: []string{
		`    url: "http://{{ include "infrastructure.fullname" . }}.{{ .Release.Namespace }}.svc.cluster.local:{{ .Values.service.port }}"`,
	},
	want: 2,
}, {
	// selfHosting.chart.version is the version a self-hoster installs, which
	// only the chart knows.
	match: `      name: "railgrid-infrastructure-provider"`,
	replace: []string{
		`      name: "railgrid-infrastructure-provider"`,
		`      version: {{ .Chart.Version | quote }}`,
	},
	want: 1,
}, {
	// The chart carries its own values reference so the portal can show it
	// offline, for the version actually installed.
	match: `    docsURL: "https://github.com/railgrid/railgrid/blob/main/providers/infrastructure/deploy/chart/README.md"`,
	replace: []string{
		`    docsURL: "https://github.com/railgrid/railgrid/blob/main/providers/infrastructure/deploy/chart/README.md"`,
		`    valuesDoc: |{{ .Files.Get "README.md" | nindent 10 }}`,
	},
	want: 1,
}, {
	// Carried from THIS platform's own configuration, not a literal: the gate
	// speaks the hub's app-access protocol, so a self-hosted copy running a
	// different build can fail against this hub.
	match:   `        value: ghcr.io/railgrid/railgrid-access-proxy:latest`,
	replace: []string{`        value: {{ .Values.publishing.accessProxyImage | quote }}`},
	want:    1,
}}

// chartHeader is the chart-specific preamble. It explains what the ConfigMap is
// for; everything after it is the manifest's own reference documentation.
const chartHeader = `{{- if and .Values.bootstrap.enabled .Values.catalogEntry.enabled -}}
# GENERATED FROM ../../../manifest.yaml — DO NOT EDIT.
# Edit manifest.yaml and run:
#   cd providers/infrastructure && go test -run TestChartCatalogEntryIsGeneratedFromManifest ./... -update
#
# Only rendered in bootstrap mode (bootstrap.enabled): the bootstrap init
# container mounts this ConfigMap and applies the CatalogEntry into kcp via the
# provider kubeconfig. In hub-provisioned mode (bootstrap.enabled=false) there is
# no init container, so the entry is registered out-of-band, not from here.
#
# The CatalogEntry is a kcp resource (providers.railgrid.ai/v1alpha1), so it
# MUST NOT be applied to the hosting cluster where this chart installs. Instead
# the chart renders it into the ConfigMap below; the init container mounts it
# and applies it into the provider workspace via the provider kubeconfig
# (RAILGRID_CATALOGENTRY_FILE -> sdkinstall.Bootstrap). Operator mode
# (operator.enabled=true) applies the same manifest from the binary's own copy,
# which is why it requires catalogEntry.enabled=false.
#
`

const chartConfigMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "infrastructure.fullname" . }}-catalogentry
  labels:
    {{- include "infrastructure.labels" . | nindent 4 }}
data:
  catalogentry.yaml: |
`

// helmLiteral escapes a `{{placeholder}}` the CatalogEntry carries as DATA (the
// hub substitutes them when it renders per-organization install instructions)
// so Helm emits it instead of trying to evaluate it.
var helmLiteral = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

// renderChartCatalogEntry turns manifest.yaml into the chart template.
func renderChartCatalogEntry(manifest []byte) (string, error) {
	lines := strings.Split(strings.TrimRight(string(manifest), "\n"), "\n")

	// The manifest is a comment preamble, its "Provider object reference"
	// block, a `---`, and the document. The chart keeps the reference block
	// (it documents the very object below it) and the document.
	reference, separator := -1, -1
	for i, line := range lines {
		if reference < 0 && strings.HasPrefix(line, "# ═") {
			reference = i
		}
		if strings.TrimSpace(line) == "---" {
			separator = i
			break
		}
	}
	if reference < 0 || separator < 0 || reference > separator {
		return "", fmt.Errorf("%s: expected a `# ═…` reference block followed by a `---` document separator", manifestPath)
	}

	var out strings.Builder
	out.WriteString(chartHeader)
	for _, line := range lines[reference:separator] {
		out.WriteString(line + "\n")
	}
	out.WriteString(chartConfigMap)

	// The document, with the install-time values substituted and everything
	// else — including the comments, which are the manifest's explanation of
	// each field — carried across unchanged.
	counts := map[string]int{}
	for _, line := range lines[separator+1:] {
		replaced, ok := applyChartOverride(line, counts)
		if !ok {
			replaced = []string{helmLiteral.ReplaceAllString(line, "{{`{{$1}}`}}")}
		}
		for _, r := range replaced {
			if strings.TrimSpace(r) == "" {
				out.WriteString("\n")
				continue
			}
			out.WriteString("    " + r + "\n")
		}
	}
	out.WriteString("{{- end }}\n")

	for _, override := range chartOverrides {
		if got := counts[override.match]; got != override.want {
			return "", fmt.Errorf("%s: line %q appears %d time(s), the chart override expects %d — the manifest changed shape; update chartOverrides",
				manifestPath, strings.TrimSpace(override.match), got, override.want)
		}
	}
	return out.String(), nil
}

func applyChartOverride(line string, counts map[string]int) ([]string, bool) {
	for _, override := range chartOverrides {
		if line == override.match {
			counts[override.match]++
			return override.replace, true
		}
	}
	return nil, false
}

// The drift guard. manifest.yaml is the source; the chart template is its
// projection. If this fails, the two descriptions of the provider have parted
// ways — and the chart's is the one tenants get.
func TestChartCatalogEntryIsGeneratedFromManifest(t *testing.T) {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := renderChartCatalogEntry(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if *updateChartCatalogEntry {
		if err := os.WriteFile(chartEntryPath, []byte(want), 0o644); err != nil { //nolint:gosec // a chart template, not a secret
			t.Fatal(err)
		}
		t.Logf("rewrote %s from %s", chartEntryPath, manifestPath)
		return
	}
	got, err := os.ReadFile(chartEntryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("%s has drifted from %s.\nRegenerate it with:\n\tcd providers/infrastructure && go test -run %s ./... -update\n\nfirst difference:\n%s",
			chartEntryPath, manifestPath, t.Name(), firstDifference(string(got), want))
	}
}

func firstDifference(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		g, w := "", ""
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			return fmt.Sprintf("  line %d\n   on disk: %q\n  expected: %q", i+1, g, w)
		}
	}
	return "  (files differ only in trailing bytes)"
}
