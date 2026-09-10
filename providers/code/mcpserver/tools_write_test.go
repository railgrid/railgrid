/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/commitbundle"
)

// commitFilesFixture returns a tenant client holding the demo-app Repository.
// Created RepositoryCommits get the kcp.io/cluster annotation and, when status
// is non-nil, that status, standing in for the controller.
func commitFilesFixture(t *testing.T, status map[string]any) (*dynamicfake.FakeDynamicClient, *commitbundle.FileStore) {
	t.Helper()
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       "Repository",
		"metadata": map[string]any{
			"name": "demo-app",
			"labels": map[string]any{
				"app-studio.ai.faros.sh/project": "demo-project",
			},
		},
		"spec": map[string]any{
			"connectionRef": "github",
			"name":          "demo-app",
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), repo)
	dyn.PrependReactor("create", "repositorycommits", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		obj := create.GetObject().(*unstructured.Unstructured).DeepCopy()
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["kcp.io/cluster"] = "logical-cluster"
		obj.SetAnnotations(annotations)
		if status != nil {
			obj.Object["status"] = runtime.DeepCopyJSON(status)
		}
		if err := dyn.Tracker().Create(repositoryCommitsGVR, obj, "", metav1.CreateOptions{}); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	store, err := commitbundle.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore returned error: %v", err)
	}
	return dyn, store
}

// runCommitFiles calls commitFiles with a short wait; the fake never changes
// the status the fixture wrote, so the result reflects it.
func runCommitFiles(dyn *dynamicfake.FakeDynamicClient, store *commitbundle.FileStore, in commitFilesInput) (commitFilesOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, out, err := commitFiles(ctx, dyn, store, "root:acme", in)
	return out, err
}

func TestCommitFilesCreatesRepositoryCommitRequest(t *testing.T) {
	dyn, store := commitFilesFixture(t, nil)
	out, err := runCommitFiles(dyn, store, commitFilesInput{
		RepositoryRef: "demo-app",
		Message:       "Initial app",
		Files: []commitFileInput{
			{Path: "package.json", Content: `{"private":true}`},
			{Path: "src/App.tsx", Content: "export default function App() { return null }"},
		},
		DeletePaths: []string{"src/legacy.ts"},
	})
	// No controller runs here, so the request is still pending when the wait ends.
	if err == nil || !strings.Contains(err.Error(), "did not finish within") || !strings.Contains(err.Error(), out.Name) {
		t.Fatalf("commitFiles error = %v, want an unfinished error naming %q", err, out.Name)
	}
	if out.Name == "" || out.BundleRef == "" || out.BundleDigest == "" {
		t.Fatalf("unexpected output: %#v", out)
	}
	if out.CommitSHA != "" {
		t.Fatalf("CommitSHA = %q, want empty before controller status", out.CommitSHA)
	}
	if strings.Join(out.DeletedPaths, ",") != "src/legacy.ts" {
		t.Fatalf("deleted paths = %v", out.DeletedPaths)
	}

	created, err := dyn.Resource(repositoryCommitsGVR).Get(context.Background(), out.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get RepositoryCommit returned error: %v", err)
	}
	if created.GetLabels()[codev1alpha1.LabelRepository] != "demo-app" {
		t.Fatalf("repository label = %q, want demo-app", created.GetLabels()[codev1alpha1.LabelRepository])
	}
	if created.GetLabels()["app-studio.ai.faros.sh/project"] != "demo-project" {
		t.Fatalf("project label was not copied: %#v", created.GetLabels())
	}
	if _, found, _ := unstructured.NestedSlice(created.Object, "spec", "files"); found {
		t.Fatal("RepositoryCommit spec unexpectedly stores file contents")
	}
	name, _, _ := unstructured.NestedString(created.Object, "spec", "source", "bundleRef", "name")
	digest, _, _ := unstructured.NestedString(created.Object, "spec", "source", "bundleRef", "digest")
	if name != out.BundleRef || digest != out.BundleDigest {
		t.Fatalf("bundle ref = %s/%s, want %s/%s", name, digest, out.BundleRef, out.BundleDigest)
	}
	scope, _, _ := unstructured.NestedString(created.Object, "spec", "source", "bundleRef", "scope")
	if scope != "" {
		t.Fatalf("RepositoryCommit spec exposed bundle scope %q", scope)
	}
	bundle, err := store.Get(context.Background(), "logical-cluster", out.BundleRef, out.BundleDigest)
	if err != nil {
		t.Fatalf("logical-cluster bundle lookup returned error: %v", err)
	}
	if len(bundle.Files) != 3 || !bundle.Files[2].Delete || bundle.Files[2].Path != "src/legacy.ts" {
		t.Fatalf("stored bundle = %#v", bundle.Files)
	}
	if _, err := store.Get(context.Background(), "root:acme", out.BundleRef, out.BundleDigest); err == nil {
		t.Fatal("staging bundle was not cleaned up")
	}
}

func TestCommitFilesResultByPhase(t *testing.T) {
	startedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	status := func(phase, reason, message string) map[string]any {
		return map[string]any{
			"phase":     phase,
			"startedAt": startedAt.Format(time.RFC3339),
			"commitSHA": "abc123",
			"conditions": []any{map[string]any{
				"type": codev1alpha1.ConditionReady, "status": "False", "reason": reason, "message": message,
			}},
		}
	}
	for _, tc := range []struct {
		name    string
		status  map[string]any
		wantErr []string
	}{
		{name: "succeeded", status: status("Succeeded", codev1alpha1.ReasonReady, "Commit succeeded.")},
		{name: "failed", status: status("Failed", codev1alpha1.ReasonError, "ensure repository: boom"), wantErr: []string{"failed: ensure repository: boom"}},
		{name: "rate limited", status: status("Running", codev1alpha1.ReasonRateLimited, "GitHub rate limit; retrying in 28s"), wantErr: []string{
			"queued behind a GitHub rate limit (GitHub rate limit; retrying in 28s)",
			"retries it until " + startedAt.Add(codev1alpha1.RepositoryCommitRateLimitWindow).Format(time.RFC3339),
			"watch RepositoryCommit",
		}},
		{name: "running", status: status("Running", codev1alpha1.ReasonReconciling, "Commit is running."), wantErr: []string{"did not finish within the 1m15s wait (phase Running)", "watch RepositoryCommit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn, store := commitFilesFixture(t, tc.status)
			out, err := runCommitFiles(dyn, store, commitFilesInput{
				RepositoryRef: "demo-app",
				Files:         []commitFileInput{{Path: "index.html", Content: "<h1>demo</h1>"}},
			})
			if len(tc.wantErr) == 0 {
				if err != nil || out.CommitSHA != "abc123" || out.Phase != "Succeeded" {
					t.Fatalf("commitFiles = %+v, %v; want success", out, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("commitFiles succeeded with phase %s", out.Phase)
			}
			for _, want := range append(tc.wantErr, strconv.Quote(out.Name)) {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestCommitFilesRejectsLongMessage(t *testing.T) {
	dyn, store := commitFilesFixture(t, map[string]any{"phase": "Succeeded"})
	files := []commitFileInput{{Path: "index.html", Content: "<h1>demo</h1>"}}
	// The limit counts characters, not bytes; surrounding whitespace is trimmed.
	_, err := runCommitFiles(dyn, store, commitFilesInput{RepositoryRef: "demo-app", Message: strings.Repeat("é", 513), Files: files})
	if want := "commit message is 513 characters; the limit is 512 — shorten the body"; err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "create" {
			t.Fatalf("over-long message created %s", action.GetResource().Resource)
		}
	}
	if entries, err := os.ReadDir(store.Dir()); err != nil || len(entries) != 0 {
		t.Fatalf("over-long message wrote bundle data: %v %v", entries, err)
	}

	message := strings.Repeat("é", 512)
	out, err := runCommitFiles(dyn, store, commitFilesInput{RepositoryRef: "demo-app", Message: "\n " + message + " \n", Files: files})
	if err != nil {
		t.Fatalf("512-character message rejected: %v", err)
	}
	created, err := dyn.Resource(repositoryCommitsGVR).Get(context.Background(), out.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedString(created.Object, "spec", "message"); got != message {
		t.Fatalf("stored message is not trimmed: %q", got)
	}
}

func TestCommitFilesAcceptsBase64Files(t *testing.T) {
	dyn, store := commitFilesFixture(t, map[string]any{"phase": "Succeeded", "commitSHA": "abc123"})
	logo := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}
	encoded := base64.StdEncoding.EncodeToString(logo)
	// A binary file may exceed the 2 MiB text cap.
	large := base64.StdEncoding.EncodeToString(make([]byte, commitbundle.MaxFileBytes+1))
	// The status is already terminal, so no wait; the longer deadline only
	// covers writing the multi-MiB bundle.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, out, err := commitFiles(ctx, dyn, store, "root:acme", commitFilesInput{
		RepositoryRef: "demo-app",
		Files: []commitFileInput{
			{Path: "public/logo.png", Content: encoded, Encoding: "base64"},
			{Path: "public/hero.jpg", Content: large, Encoding: "base64"},
			{Path: "index.html", Content: "<img src=logo.png>", Encoding: "utf-8"},
			{Path: "README.md", Content: "# demo"},
		},
	})
	if err != nil {
		t.Fatalf("commitFiles returned error: %v", err)
	}
	bundle, err := store.Get(context.Background(), "logical-cluster", out.BundleRef, out.BundleDigest)
	if err != nil {
		t.Fatalf("bundle lookup returned error: %v", err)
	}
	byPath := map[string]commitbundle.BundleFile{}
	for _, f := range bundle.Files {
		byPath[f.Path] = f
	}
	got := byPath["public/logo.png"]
	sum := sha256.Sum256(logo)
	if got.Encoding != "base64" || got.Content != encoded || got.Size != int64(len(logo)) || got.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("binary bundle file = %+v, want encoded content with decoded size/digest", got)
	}
	// The text path is unchanged: no encoding recorded, digest over the text.
	for path, content := range map[string]string{"index.html": "<img src=logo.png>", "README.md": "# demo"} {
		text := byPath[path]
		sum := sha256.Sum256([]byte(content))
		if text.Encoding != "" || text.Content != content || text.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Fatalf("text bundle file %s = %+v", path, text)
		}
	}
}

func TestCommitFilesRejectsBadEncodingAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		file commitFileInput
		want string
	}{
		{"unknown encoding", commitFileInput{Path: "a.bin", Content: "00ff", Encoding: "hex"}, `file "a.bin": unsupported encoding "hex"`},
		{"uppercase encoding", commitFileInput{Path: "a.txt", Content: "x", Encoding: "UTF-8"}, "unsupported encoding"},
		{"invalid base64", commitFileInput{Path: "a.bin", Content: "not base64!", Encoding: "base64"}, `file "a.bin" has invalid base64 content`},
		{"binary over cap", commitFileInput{Path: "a.bin", Content: base64.StdEncoding.EncodeToString(make([]byte, commitbundle.MaxBinaryFileBytes+1)), Encoding: "base64"}, `file "a.bin" is too large`},
		{"text over cap", commitFileInput{Path: "a.txt", Content: strings.Repeat("x", commitbundle.MaxFileBytes+1)}, `file "a.txt" is too large`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn, store := commitFilesFixture(t, map[string]any{"phase": "Succeeded"})
			_, err := runCommitFiles(dyn, store, commitFilesInput{RepositoryRef: "demo-app", Files: []commitFileInput{tc.file}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			for _, action := range dyn.Actions() {
				if action.GetVerb() == "create" {
					t.Fatalf("rejected file created %s", action.GetResource().Resource)
				}
			}
		})
	}
}

// TestCodeToolSchemas pins the wire contract clients gate on: commit_files
// advertises an encoding property on file items (so senders know base64 is
// accepted), checkout_repository advertises its binaryEncoding opt-in, and
// declares no output schema because it returns its result only as JSON text
// (no duplicated structuredContent).
func TestCodeToolSchemas(t *testing.T) {
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	registerWriteTools(srv, Deps{}, identity{})
	registerCheckoutTools(srv, Deps{}, identity{})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "commit_files":
			found++
			raw, _ := json.Marshal(tool.InputSchema)
			var schema struct {
				Properties struct {
					Files struct {
						Items struct {
							Properties map[string]struct {
								Type        any    `json:"type"`
								Description string `json:"description"`
							} `json:"properties"`
						} `json:"items"`
					} `json:"files"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			encoding, ok := schema.Properties.Files.Items.Properties["encoding"]
			if !ok || !strings.Contains(encoding.Description, "base64") || !strings.Contains(encoding.Description, "25 MiB") {
				t.Fatalf("commit_files file items lack a documented encoding property: %s", raw)
			}
		case "checkout_repository":
			found++
			if tool.OutputSchema != nil {
				t.Fatalf("checkout_repository declares an output schema: %v", tool.OutputSchema)
			}
			raw, _ := json.Marshal(tool.InputSchema)
			var schema struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			if prop, ok := schema.Properties["binaryEncoding"]; !ok || !strings.Contains(prop.Description, "base64") {
				t.Fatalf("checkout_repository lacks a documented binaryEncoding input: %s", raw)
			}
		}
	}
	if found != 2 {
		t.Fatalf("found %d of the 2 expected tools", found)
	}
}

func TestRepositoryCommitObjectKeepsAuthoritativeRepositoryLabel(t *testing.T) {
	repo := &codev1alpha1.Repository{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-app",
			Labels: map[string]string{
				codev1alpha1.LabelRepository:     "stale-repo",
				"app-studio.ai.faros.sh/project": "demo-project",
			},
		},
	}
	obj := repositoryCommitObject(repo, commitbundle.BundleRef{
		Name:   "bundle-123",
		Digest: "sha256:123",
	}, commitFilesInput{
		RepositoryRef: "demo-app",
		Message:       "Initial app",
	})
	if got := obj.GetLabels()[codev1alpha1.LabelRepository]; got != "demo-app" {
		t.Fatalf("repository label = %q, want demo-app", got)
	}
	if got := obj.GetLabels()["app-studio.ai.faros.sh/project"]; got != "demo-project" {
		t.Fatalf("project label = %q, want demo-project", got)
	}
	scope, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "bundleRef", "scope")
	if scope != "" {
		t.Fatalf("RepositoryCommit spec exposed bundle scope %q", scope)
	}
}

func TestRepositoryCommitBundleStorageScope(t *testing.T) {
	obj := &unstructured.Unstructured{}
	obj.SetAnnotations(map[string]string{"kcp.io/cluster": " logical-cluster "})
	if got := repositoryCommitBundleStorageScope("root:acme", obj); got != "logical-cluster" {
		t.Fatalf("scope = %q, want logical-cluster", got)
	}
	if got := repositoryCommitBundleStorageScope("root:acme", &unstructured.Unstructured{}); got != "" {
		t.Fatalf("fallback scope = %q, want empty", got)
	}
	if got := repositoryCommitBundleStorageScope("root:acme", nil); got != "" {
		t.Fatalf("nil fallback scope = %q, want empty", got)
	}
}

func TestRepositorySpecDefaultsAutoInit(t *testing.T) {
	spec := repositorySpec(createRepositoryInput{
		ConnectionRef: "github",
	}, "demo-app")
	if got := spec["autoInit"]; got != true {
		t.Fatalf("autoInit = %#v, want true", got)
	}

	no := false
	spec = repositorySpec(createRepositoryInput{
		ConnectionRef: "github",
		AutoInit:      &no,
	}, "demo-app")
	if _, ok := spec["autoInit"]; ok {
		t.Fatalf("autoInit present for explicit false: %#v", spec)
	}
}

func TestCommitObjectName(t *testing.T) {
	name := commitObjectName(strings.Repeat("a", 260), "sha256:1234567890abcdef", time.Unix(1, 2))
	if len(name) > 253 {
		t.Fatalf("name length = %d, want <= 253", len(name))
	}
	if !strings.Contains(name, "-commit-1234567890ab-") {
		t.Fatalf("name = %q, want digest suffix", name)
	}
}
