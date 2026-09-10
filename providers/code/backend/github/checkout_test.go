/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

func TestIsBinaryContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		want bool
	}{
		{"empty is text", nil, false},
		{"plain text", []byte("package main\n"), false},
		{"utf8 multibyte", []byte("héllo → wörld"), false},
		{"nul byte", []byte{0x89, 'P', 'N', 'G', 0x00}, true},
		{"invalid utf8", []byte{0xff, 0xfe, 0x41}, true},
	} {
		if got := isBinaryContent(tc.raw); got != tc.want {
			t.Errorf("%s: isBinaryContent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAppendSkipBounds(t *testing.T) {
	var skipped []string
	for i := range 300 {
		skipped = appendSkip(skipped, strings.Repeat("x", 3)+"-"+string(rune('a'+i%26)))
	}
	if len(skipped) != 100 {
		t.Fatalf("skipped length = %d, want 100 (bounded)", len(skipped))
	}
	if skipped[99] != "(more paths skipped)" {
		t.Errorf("last entry = %q, want the overflow marker", skipped[99])
	}
}

var checkoutTestLogo = []byte{0x89, 'P', 'N', 'G', 0x00, 0x01, 0x02}

// checkoutTestServer serves one tree whose blobs exercise every cap; the
// returned map records which blobs were downloaded.
func checkoutTestServer(t *testing.T) (*httptest.Server, map[string][]byte, map[string]bool) {
	t.Helper()
	blobs := map[string][]byte{
		"blob-readme": []byte("hello"),                           // text, 5 bytes
		"blob-logo":   checkoutTestLogo,                          // binary, 7 bytes
		"blob-long":   []byte("0123456789ab"),                    // text over the 8-byte text cap
		"blob-huge":   make([]byte, 20),                          // over both per-file caps
		"blob-late":   make([]byte, 14),                          // binary beyond the total cap
		"blob-wide":   append([]byte{0xff}, make([]byte, 14)...), // binary under its cap, over the text cap
	}
	downloaded := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/repos/acme/widgets/commits/main":
			_, _ = w.Write([]byte("commit-sha"))
		case r.URL.Path == "/api/v3/repos/acme/widgets/git/trees/commit-sha":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sha":"commit-sha","tree":[
				{"path":"README.md","type":"blob","sha":"blob-readme","size":5},
				{"path":"assets/logo.png","type":"blob","sha":"blob-logo","size":7},
				{"path":"assets/wide.bin","type":"blob","sha":"blob-wide","size":15},
				{"path":"docs/long.txt","type":"blob","sha":"blob-long","size":12},
				{"path":"dist/huge.bin","type":"blob","sha":"blob-huge","size":20},
				{"path":"zz/late.bin","type":"blob","sha":"blob-late","size":14},
				{"path":"src","type":"tree","sha":"tree-src"}
			]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v3/repos/acme/widgets/git/blobs/"):
			sha := strings.TrimPrefix(r.URL.Path, "/api/v3/repos/acme/widgets/git/blobs/")
			raw, ok := blobs[sha]
			if !ok {
				http.NotFound(w, r)
				return
			}
			downloaded[sha] = true
			_, _ = w.Write(raw)
		default:
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, blobs, downloaded
}

func runTestCheckout(t *testing.T, srv *httptest.Server, input backend.RepositoryCheckoutInput) backend.RepositoryCheckoutResult {
	t.Helper()
	got, err := New().CheckoutFiles(context.Background(),
		&codev1alpha1.Connection{Spec: codev1alpha1.ConnectionSpec{Owner: "acme", BaseURL: srv.URL}},
		backend.Credential{Token: "token"},
		&codev1alpha1.Repository{Spec: codev1alpha1.RepositorySpec{Name: "widgets", DefaultBranch: "main"}},
		input,
	)
	if err != nil {
		t.Fatalf("CheckoutFiles returned error: %v", err)
	}
	if got.CommitSHA != "commit-sha" || got.Ref != "main" {
		t.Fatalf("ref/sha = %s/%s", got.Ref, got.CommitSHA)
	}
	return got
}

// TestCheckoutFilesReturnsBinariesAsBase64 checks the per-class caps with
// IncludeBinary: text is verbatim under the text cap, binaries come back
// base64 under the binary cap, and everything else is skipped — oversized
// blobs without being downloaded.
func TestCheckoutFilesReturnsBinariesAsBase64(t *testing.T) {
	srv, blobs, downloaded := checkoutTestServer(t)
	got := runTestCheckout(t, srv, backend.RepositoryCheckoutInput{MaxFiles: 10, MaxFileBytes: 8, IncludeBinary: true, MaxBinaryFileBytes: 16, MaxTotalBytes: 40})
	want := []backend.RepositoryCommitFile{
		{Path: "README.md", Content: "hello"},
		{Path: "assets/logo.png", Content: base64.StdEncoding.EncodeToString(checkoutTestLogo), Encoding: backend.EncodingBase64},
		{Path: "assets/wide.bin", Content: base64.StdEncoding.EncodeToString(blobs["blob-wide"]), Encoding: backend.EncodingBase64},
	}
	if fmt.Sprint(got.Files) != fmt.Sprint(want) {
		t.Fatalf("files = %#v\nwant %#v", got.Files, want)
	}
	wantSkipped := []string{"dist/huge.bin (file too large)", "docs/long.txt (file too large)", "zz/late.bin (total-size cap)"}
	if strings.Join(got.Skipped, "|") != strings.Join(wantSkipped, "|") {
		t.Fatalf("skipped = %q, want %q", got.Skipped, wantSkipped)
	}
	// 5+7+15 bytes are in; long.txt (12) is fetched to classify it, but
	// huge.bin (over both caps) and late.bin (27+14 > 40) never are.
	if downloaded["blob-huge"] || downloaded["blob-late"] || !downloaded["blob-long"] {
		t.Fatalf("download set = %v", downloaded)
	}
}

// TestCheckoutFilesWithoutIncludeBinarySkipsBinaries is the pre-binary
// behavior: binaries are skipped, no file carries an encoding, and the
// pre-download check uses the text cap.
func TestCheckoutFilesWithoutIncludeBinarySkipsBinaries(t *testing.T) {
	srv, _, downloaded := checkoutTestServer(t)
	got := runTestCheckout(t, srv, backend.RepositoryCheckoutInput{MaxFiles: 10, MaxFileBytes: 8, MaxBinaryFileBytes: 16, MaxTotalBytes: 40})
	if len(got.Files) != 1 || got.Files[0] != (backend.RepositoryCommitFile{Path: "README.md", Content: "hello"}) {
		t.Fatalf("files = %#v, want only the text file", got.Files)
	}
	wantSkipped := []string{
		"assets/logo.png (binary)",
		"assets/wide.bin (file too large)",
		"dist/huge.bin (file too large)",
		"docs/long.txt (file too large)",
		"zz/late.bin (file too large)",
	}
	if strings.Join(got.Skipped, "|") != strings.Join(wantSkipped, "|") {
		t.Fatalf("skipped = %q, want %q", got.Skipped, wantSkipped)
	}
	for _, sha := range []string{"blob-wide", "blob-long", "blob-huge", "blob-late"} {
		if downloaded[sha] {
			t.Fatalf("%s is over the text cap but was downloaded", sha)
		}
	}
}

func TestBlobClientUsesLongerTimeout(t *testing.T) {
	b := New()
	cred := backend.Credential{Token: "token"}
	def, err := b.client(context.Background(), cred, "")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := b.blobClient(context.Background(), cred, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := def.Client().Timeout; got != githubRequestTimeout {
		t.Fatalf("default client timeout = %s, want %s", got, githubRequestTimeout)
	}
	if got := blob.Client().Timeout; got != githubBlobRequestTimeout || got <= githubRequestTimeout {
		t.Fatalf("blob client timeout = %s, want %s", got, githubBlobRequestTimeout)
	}
}
