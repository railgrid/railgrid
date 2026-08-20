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

package edgesconn

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// startChartRepo serves a one-chart helm repo (conn-helm 0.1.0) for the helm
// workload subtest and returns its base URL. Layout deliberately mirrors the
// real public repos (grafana, prometheus-community, k8s-at-home): the archive
// lives ONLY at a release-assets path announced by index.yaml, so a provider
// that guesses "<repo>/<chart>-<version>.tgz" instead of resolving through the
// index 404s here — the regression this subtest pins down.
//
// The chart defaults replicas to 0; the Workload's spec.helm.values must
// override it to 1 for a pod to ever appear, which proves the values
// RawExtension survives decode → helm render.
func startChartRepo(t *testing.T) string {
	t.Helper()
	archive := helmChartArchive(t, map[string]string{
		"conn-helm/Chart.yaml": "apiVersion: v2\nname: conn-helm\nversion: 0.1.0\n",
		"conn-helm/values.yaml": "replicas: 0\n" +
			"fullnameOverride: \"\"\n" +
			"nameOverride: \"\"\n",
		"conn-helm/templates/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Values.fullnameOverride | default .Release.Name }}
  labels:
    app: {{ .Values.fullnameOverride | default .Release.Name }}
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      app: {{ .Values.fullnameOverride | default .Release.Name }}
  template:
    metadata:
      labels:
        app: {{ .Values.fullnameOverride | default .Release.Name }}
    spec:
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
`,
	})

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "entries:\n  conn-helm:\n    - version: 0.1.0\n      urls:\n        - %s/release-assets/conn-helm-0.1.0.tgz\n", srv.URL)
	})
	mux.HandleFunc("/release-assets/conn-helm-0.1.0.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	return srv.URL
}

// helmChartArchive tars+gzips the given path→content map into a chart archive.
func helmChartArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
