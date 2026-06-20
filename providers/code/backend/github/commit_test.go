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

package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gogithub "github.com/google/go-github/v66/github"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
)

func TestCleanRepositoryPath(t *testing.T) {
	got, err := cleanRepositoryPath("src\\main.go")
	if err != nil {
		t.Fatalf("cleanRepositoryPath returned error: %v", err)
	}
	if got != "src/main.go" {
		t.Fatalf("clean path = %q, want src/main.go", got)
	}

	for _, path := range []string{"", "/etc/passwd", "../secret", "src/../secret"} {
		if _, err := cleanRepositoryPath(path); err == nil {
			t.Fatalf("cleanRepositoryPath(%q) returned nil error", path)
		}
	}
}

func TestGitTreeEntries(t *testing.T) {
	entries, paths, err := gitTreeEntries([]backend.RepositoryCommitFile{
		{Path: "b.txt", Content: "b"},
		{Path: "a.txt", Content: "a"},
	})
	if err != nil {
		t.Fatalf("gitTreeEntries returned error: %v", err)
	}
	if len(entries) != 2 || len(paths) != 2 {
		t.Fatalf("entry count = %d paths = %d, want 2/2", len(entries), len(paths))
	}
	if paths[0] != "a.txt" || paths[1] != "b.txt" {
		t.Fatalf("paths = %#v, want sorted paths", paths)
	}
	if entries[0].GetContent() != "a" || entries[1].GetContent() != "b" {
		t.Fatalf("entries were not ordered with their content")
	}
}

func TestGitTreeEntriesRejectsDuplicatePaths(t *testing.T) {
	_, _, err := gitTreeEntries([]backend.RepositoryCommitFile{
		{Path: "src/../app.go", Content: "a"},
		{Path: "app.go", Content: "b"},
	})
	if err == nil {
		t.Fatal("gitTreeEntries returned nil error for duplicate normalized paths")
	}
}

func TestShouldReuseHeadCommitWhenDesiredTreeAlreadyAtHead(t *testing.T) {
	parent := &gogithub.Commit{SHA: gogithub.String("abc123")}
	tree := &gogithub.Tree{SHA: gogithub.String("tree123")}
	if !shouldReuseHeadCommit(parent, tree, "tree123") {
		t.Fatal("shouldReuseHeadCommit returned false for matching head tree")
	}
	if shouldReuseHeadCommit(parent, tree, "other") {
		t.Fatal("shouldReuseHeadCommit returned true for a different tree")
	}
	if shouldReuseHeadCommit(nil, tree, "tree123") {
		t.Fatal("shouldReuseHeadCommit returned true without a parent commit")
	}
}

func TestCommitMessageWithIdempotencyKey(t *testing.T) {
	got := commitMessageWithIdempotencyKey("Initial app", "root:acme/demo")
	if got != "Initial app\n\nKedge-RepositoryCommit: root:acme/demo" {
		t.Fatalf("message = %q", got)
	}
	if got := commitMessageWithIdempotencyKey("Initial app", ""); got != "Initial app" {
		t.Fatalf("message with empty key = %q, want unchanged", got)
	}
}

func TestCommitMessageHasIdempotencyKey(t *testing.T) {
	message := "Initial app\n\nKedge-RepositoryCommit: root:acme/demo"
	if !commitMessageHasIdempotencyKey(message, "root:acme/demo") {
		t.Fatal("commitMessageHasIdempotencyKey returned false for matching trailer")
	}
	if commitMessageHasIdempotencyKey(message, "root:acme/other") {
		t.Fatal("commitMessageHasIdempotencyKey returned true for a different key")
	}
	if commitMessageHasIdempotencyKey("Initial app Kedge-RepositoryCommit: root:acme/demo", "root:acme/demo") {
		t.Fatal("commitMessageHasIdempotencyKey returned true for an inline substring")
	}
}

func TestCommitFilesRecoversWhenConcurrentUpdateAlreadyAppliedDesiredTree(t *testing.T) {
	const (
		baseTreeSHA    = "tree-base"
		desiredTreeSHA = "tree-desired"
		baseCommitSHA  = "commit-base"
		nextCommitSHA  = "commit-next"
		idempotencyKey = "commit-uid"
	)
	var getRefCalls int
	var updateRefCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/ref/heads/main" && r.Method == http.MethodGet {
			getRefCalls++
			w.Header().Set("Content-Type", "application/json")
			sha := baseCommitSHA
			if getRefCalls > 1 {
				sha = nextCommitSHA
			}
			_, _ = fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, sha)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/commits" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits/"+baseCommitSHA && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, baseCommitSHA, baseTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits/"+nextCommitSHA && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q,"html_url":"https://example.test/acme/widgets/commit/%s","message":"Update\n\n%s %s","tree":{"sha":%q}}`, nextCommitSHA, nextCommitSHA, repositoryCommitIdempotencyTrailer, idempotencyKey, desiredTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/trees" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q}`, desiredTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, nextCommitSHA, desiredTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/refs/heads/main" && r.Method == http.MethodPatch {
			updateRefCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Update is not a fast forward"}`))
			return
		}
		t.Fatalf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	got, err := New().CommitFiles(context.Background(),
		&codev1alpha1.Connection{Spec: codev1alpha1.ConnectionSpec{Owner: "acme", BaseURL: srv.URL}},
		backend.Credential{Token: "token"},
		&codev1alpha1.Repository{Spec: codev1alpha1.RepositorySpec{Name: "widgets", DefaultBranch: "main"}},
		backend.RepositoryCommitInput{
			Message:        "Update",
			IdempotencyKey: idempotencyKey,
			Files:          []backend.RepositoryCommitFile{{Path: "index.html", Content: "hello"}},
		},
	)
	if err != nil {
		t.Fatalf("CommitFiles returned error: %v", err)
	}
	if got.CommitSHA != nextCommitSHA {
		t.Fatalf("commit SHA = %q, want %q", got.CommitSHA, nextCommitSHA)
	}
	if len(got.Files) != 1 || got.Files[0] != "index.html" {
		t.Fatalf("files = %#v, want index.html", got.Files)
	}
	if got.Branch != "main" {
		t.Fatalf("branch = %q, want main", got.Branch)
	}
	if !strings.Contains(got.CommitURL, nextCommitSHA) {
		t.Fatalf("commit URL = %q, want SHA", got.CommitURL)
	}
	if updateRefCalls != 1 {
		t.Fatalf("update ref calls = %d, want 1", updateRefCalls)
	}
	if getRefCalls != 2 {
		t.Fatalf("get ref calls = %d, want initial + recovery", getRefCalls)
	}
}

func TestCommitFilesRetriesWhenConcurrentUpdateMovedBranchToDifferentTree(t *testing.T) {
	const (
		baseTreeSHA       = "tree-base"
		concurrentTreeSHA = "tree-concurrent"
		mergedTreeSHA     = "tree-merged"
		baseCommitSHA     = "commit-base"
		concurrentSHA     = "commit-concurrent"
		orphanCommitSHA   = "commit-orphan"
		retryCommitSHA    = "commit-retry"
	)
	var getRefCalls int
	var createTreeBases []string
	var createCommitParents []string
	var updateRefSHAs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/ref/heads/main" && r.Method == http.MethodGet {
			getRefCalls++
			w.Header().Set("Content-Type", "application/json")
			sha := baseCommitSHA
			if getRefCalls > 1 {
				sha = concurrentSHA
			}
			_, _ = fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, sha)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/commits" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits/"+baseCommitSHA && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, baseCommitSHA, baseTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits/"+concurrentSHA && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, concurrentSHA, concurrentTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/trees" && r.Method == http.MethodPost {
			body := mustReadRequestBody(t, r)
			if strings.Contains(body, baseTreeSHA) {
				createTreeBases = append(createTreeBases, baseTreeSHA)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"sha":"tree-orphan"}`)
				return
			}
			if strings.Contains(body, concurrentTreeSHA) {
				createTreeBases = append(createTreeBases, concurrentTreeSHA)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"sha":%q}`, mergedTreeSHA)
				return
			}
			t.Fatalf("unexpected CreateTree body: %s", body)
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits" && r.Method == http.MethodPost {
			body := mustReadRequestBody(t, r)
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(body, baseCommitSHA) {
				createCommitParents = append(createCommitParents, baseCommitSHA)
				_, _ = fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":"tree-orphan"}}`, orphanCommitSHA)
				return
			}
			if strings.Contains(body, concurrentSHA) {
				createCommitParents = append(createCommitParents, concurrentSHA)
				_, _ = fmt.Fprintf(w, `{"sha":%q,"html_url":"https://example.test/acme/widgets/commit/%s","tree":{"sha":%q}}`, retryCommitSHA, retryCommitSHA, mergedTreeSHA)
				return
			}
			t.Fatalf("unexpected CreateCommit body: %s", body)
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/refs/heads/main" && r.Method == http.MethodPatch {
			body := mustReadRequestBody(t, r)
			if strings.Contains(body, orphanCommitSHA) {
				updateRefSHAs = append(updateRefSHAs, orphanCommitSHA)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Update is not a fast forward"}`))
				return
			}
			if strings.Contains(body, retryCommitSHA) {
				updateRefSHAs = append(updateRefSHAs, retryCommitSHA)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, retryCommitSHA)
				return
			}
			t.Fatalf("unexpected UpdateRef body: %s", body)
		}
		t.Fatalf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	got, err := New().CommitFiles(context.Background(),
		&codev1alpha1.Connection{Spec: codev1alpha1.ConnectionSpec{Owner: "acme", BaseURL: srv.URL}},
		backend.Credential{Token: "token"},
		&codev1alpha1.Repository{Spec: codev1alpha1.RepositorySpec{Name: "widgets", DefaultBranch: "main"}},
		backend.RepositoryCommitInput{
			Message:        "Update",
			IdempotencyKey: "commit-uid",
			Files:          []backend.RepositoryCommitFile{{Path: "index.html", Content: "hello"}},
		},
	)
	if err != nil {
		t.Fatalf("CommitFiles returned error: %v", err)
	}
	if got.CommitSHA != retryCommitSHA {
		t.Fatalf("commit SHA = %q, want %q", got.CommitSHA, retryCommitSHA)
	}
	if strings.Join(createTreeBases, ",") != baseTreeSHA+","+concurrentTreeSHA {
		t.Fatalf("CreateTree bases = %#v, want base then concurrent tree", createTreeBases)
	}
	if strings.Join(createCommitParents, ",") != baseCommitSHA+","+concurrentSHA {
		t.Fatalf("CreateCommit parents = %#v, want base then concurrent commit", createCommitParents)
	}
	if strings.Join(updateRefSHAs, ",") != orphanCommitSHA+","+retryCommitSHA {
		t.Fatalf("UpdateRef SHAs = %#v, want orphan then retry commit", updateRefSHAs)
	}
}

func TestCommitFilesRetriesWhenConcurrentUpdateAlreadyAppliedSameTreeWithoutProof(t *testing.T) {
	const (
		baseTreeSHA     = "tree-base"
		desiredTreeSHA  = "tree-desired"
		baseCommitSHA   = "commit-base"
		orphanCommitSHA = "commit-orphan"
		concurrentSHA   = "commit-concurrent"
		retryCommitSHA  = "commit-retry"
	)
	var getRefCalls int
	var createCommitParents []string
	var updateRefSHAs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/ref/heads/main" && r.Method == http.MethodGet {
			getRefCalls++
			w.Header().Set("Content-Type", "application/json")
			sha := baseCommitSHA
			if getRefCalls > 1 {
				sha = concurrentSHA
			}
			_, _ = fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, sha)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/commits" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits/"+baseCommitSHA && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, baseCommitSHA, baseTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits/"+concurrentSHA && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q},"message":"Other automation wrote the same files"}`, concurrentSHA, desiredTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/trees" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q}`, desiredTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits" && r.Method == http.MethodPost {
			body := mustReadRequestBody(t, r)
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(body, baseCommitSHA) {
				createCommitParents = append(createCommitParents, baseCommitSHA)
				_, _ = fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, orphanCommitSHA, desiredTreeSHA)
				return
			}
			if strings.Contains(body, concurrentSHA) {
				createCommitParents = append(createCommitParents, concurrentSHA)
				_, _ = fmt.Fprintf(w, `{"sha":%q,"html_url":"https://example.test/acme/widgets/commit/%s","tree":{"sha":%q}}`, retryCommitSHA, retryCommitSHA, desiredTreeSHA)
				return
			}
			t.Fatalf("unexpected CreateCommit body: %s", body)
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/refs/heads/main" && r.Method == http.MethodPatch {
			body := mustReadRequestBody(t, r)
			if strings.Contains(body, orphanCommitSHA) {
				updateRefSHAs = append(updateRefSHAs, orphanCommitSHA)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Update is not a fast forward"}`))
				return
			}
			if strings.Contains(body, retryCommitSHA) {
				updateRefSHAs = append(updateRefSHAs, retryCommitSHA)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, retryCommitSHA)
				return
			}
			t.Fatalf("unexpected UpdateRef body: %s", body)
		}
		t.Fatalf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	got, err := New().CommitFiles(context.Background(),
		&codev1alpha1.Connection{Spec: codev1alpha1.ConnectionSpec{Owner: "acme", BaseURL: srv.URL}},
		backend.Credential{Token: "token"},
		&codev1alpha1.Repository{Spec: codev1alpha1.RepositorySpec{Name: "widgets", DefaultBranch: "main"}},
		backend.RepositoryCommitInput{
			Message: "Update",
			Files:   []backend.RepositoryCommitFile{{Path: "index.html", Content: "hello"}},
		},
	)
	if err != nil {
		t.Fatalf("CommitFiles returned error: %v", err)
	}
	if got.CommitSHA != retryCommitSHA {
		t.Fatalf("commit SHA = %q, want retried commit %q", got.CommitSHA, retryCommitSHA)
	}
	if strings.Join(createCommitParents, ",") != baseCommitSHA+","+concurrentSHA {
		t.Fatalf("CreateCommit parents = %#v, want base then concurrent commit", createCommitParents)
	}
	if strings.Join(updateRefSHAs, ",") != orphanCommitSHA+","+retryCommitSHA {
		t.Fatalf("UpdateRef SHAs = %#v, want orphan then retry commit", updateRefSHAs)
	}
}

func TestCommitFilesRecoversWhenConcurrentCreateRefAlreadyAppliedDesiredTree(t *testing.T) {
	const (
		desiredTreeSHA = "tree-desired"
		nextCommitSHA  = "commit-next"
		idempotencyKey = "commit-uid"
	)
	var getRefCalls int
	var createRefCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/ref/heads/main" && r.Method == http.MethodGet {
			getRefCalls++
			if getRefCalls == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":%q}}`, nextCommitSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits/"+nextCommitSHA && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q,"html_url":"https://example.test/acme/widgets/commit/%s","message":"Initial app\n\n%s %s","tree":{"sha":%q}}`, nextCommitSHA, nextCommitSHA, repositoryCommitIdempotencyTrailer, idempotencyKey, desiredTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/trees" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q}`, desiredTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/commits" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, nextCommitSHA, desiredTreeSHA)
			return
		}
		if r.URL.Path == "/api/v3/repos/acme/widgets/git/refs" && r.Method == http.MethodPost {
			createRefCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
			return
		}
		t.Fatalf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	got, err := New().CommitFiles(context.Background(),
		&codev1alpha1.Connection{Spec: codev1alpha1.ConnectionSpec{Owner: "acme", BaseURL: srv.URL}},
		backend.Credential{Token: "token"},
		&codev1alpha1.Repository{Spec: codev1alpha1.RepositorySpec{Name: "widgets", DefaultBranch: "main"}},
		backend.RepositoryCommitInput{
			Message:        "Initial app",
			IdempotencyKey: idempotencyKey,
			Files:          []backend.RepositoryCommitFile{{Path: "index.html", Content: "hello"}},
		},
	)
	if err != nil {
		t.Fatalf("CommitFiles returned error: %v", err)
	}
	if got.CommitSHA != nextCommitSHA {
		t.Fatalf("commit SHA = %q, want %q", got.CommitSHA, nextCommitSHA)
	}
	if createRefCalls != 1 {
		t.Fatalf("create ref calls = %d, want 1", createRefCalls)
	}
	if getRefCalls != 2 {
		t.Fatalf("get ref calls = %d, want initial miss + recovery", getRefCalls)
	}
}

func mustReadRequestBody(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return string(body)
}
