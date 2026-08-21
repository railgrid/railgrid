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

package render

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// chartArchive builds a minimal valid helm chart tgz (just Chart.yaml) that
// loader.LoadArchive accepts.
func chartArchive(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	chartYAML := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version)
	if err := tw.WriteHeader(&tar.Header{Name: name + "/Chart.yaml", Mode: 0o644, Size: int64(len(chartYAML))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(chartYAML)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestFetchChartResolvesThroughIndex serves the archive ONLY at a
// release-assets path the guessed "<repo>/<name>-<version>.tgz" would miss —
// exactly how grafana/prometheus-community/k8s-at-home repos host charts. A
// regression to URL-guessing 404s here.
func TestFetchChartResolvesThroughIndex(t *testing.T) {
	archive := chartArchive(t, "demo", "1.2.3")
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "entries:\n  demo:\n    - version: 1.2.3\n      urls:\n        - %s/release-assets/demo-1.2.3.tgz\n", srv.URL)
	})
	mux.HandleFunc("/release-assets/demo-1.2.3.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})

	ch, err := fetchChart(context.Background(), srv.URL, "demo", "1.2.3")
	if err != nil {
		t.Fatalf("fetchChart: %v", err)
	}
	if ch.Name() != "demo" || ch.Metadata.Version != "1.2.3" {
		t.Fatalf("got chart %s-%s, want demo-1.2.3", ch.Name(), ch.Metadata.Version)
	}
}

// TestFetchChartRelativeIndexURL covers repos whose index entries are paths
// relative to the repo root.
func TestFetchChartRelativeIndexURL(t *testing.T) {
	archive := chartArchive(t, "demo", "1.2.3")
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "entries:\n  demo:\n    - version: 1.2.3\n      urls:\n        - charts/demo-1.2.3.tgz\n")
	})
	mux.HandleFunc("/charts/demo-1.2.3.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})

	if _, err := fetchChart(context.Background(), srv.URL, "demo", "1.2.3"); err != nil {
		t.Fatalf("fetchChart with relative index URL: %v", err)
	}
}

// TestFetchChartVersionNotInIndex: a parsed index that lacks the pin must
// surface errChartNotInIndex — retrying a guessed URL can't fix a wrong pin,
// and the guessed-path fallback would turn it into a misleading 404.
func TestFetchChartVersionNotInIndex(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "entries:\n  demo:\n    - version: 9.9.9\n      urls:\n        - charts/demo-9.9.9.tgz\n")
	})
	guessed := false
	mux.HandleFunc("/demo-1.2.3.tgz", func(w http.ResponseWriter, _ *http.Request) {
		guessed = true
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := fetchChart(context.Background(), srv.URL, "demo", "1.2.3")
	if !errors.Is(err, errChartNotInIndex) {
		t.Fatalf("want errChartNotInIndex, got: %v", err)
	}
	if guessed {
		t.Fatal("fetchChart fell back to the guessed path despite an authoritative index")
	}
}

// TestFetchChartFallsBackWithoutIndex keeps the predictable-path contract for
// repos that serve archives at the root but have no readable index.
func TestFetchChartFallsBackWithoutIndex(t *testing.T) {
	archive := chartArchive(t, "demo", "1.2.3")
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/demo-1.2.3.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})

	if _, err := fetchChart(context.Background(), srv.URL, "demo", "1.2.3"); err != nil {
		t.Fatalf("fetchChart without index: %v", err)
	}
}
