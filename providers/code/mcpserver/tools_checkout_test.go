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

package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/commitbundle"
)

// checkoutFixture stands in for the checkout controller: every created
// RepositoryCheckout immediately reports a stored bundle holding a text and a
// binary file. annotations receives the annotations of the created CR.
func checkoutFixture(t *testing.T) (*dynamicfake.FakeDynamicClient, *commitbundle.FileStore, string, *map[string]string) {
	t.Helper()
	ctx := context.Background()
	store, err := commitbundle.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logo := base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 0x00})
	bundle, err := store.Put(ctx, "logical-cluster", []commitbundle.File{
		{Path: "README.md", Content: "# demo"},
		{Path: "public/logo.png", Content: logo, Encoding: commitbundle.EncodingBase64},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       "Repository",
		"metadata":   map[string]any{"name": "demo-app"},
		"spec":       map[string]any{"connectionRef": "github", "name": "demo-app"},
	}}
	annotations := map[string]string{}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), repo)
	dyn.PrependReactor("create", "repositorycheckouts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		annotations = obj.GetAnnotations()
		obj.SetAnnotations(map[string]string{"kcp.io/cluster": "logical-cluster"})
		obj.Object["status"] = map[string]any{
			"phase":     string(codev1alpha1.RepositoryCheckoutPhaseSucceeded),
			"ref":       "main",
			"commitSHA": "abc123",
			"bundleRef": map[string]any{"name": bundle.Name, "digest": bundle.Digest},
			"skipped":   []any{"dist/huge.bin (file too large)"},
		}
		if err := dyn.Tracker().Create(repositoryCheckoutsGVR, obj, "", metav1.CreateOptions{}); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	return dyn, store, logo, &annotations
}

// resultFiles renders the tool result and returns its files as raw JSON
// objects, asserting the tree is carried once, as text.
func resultFiles(t *testing.T, out checkoutRepositoryOutput) []map[string]any {
	t.Helper()
	res, err := checkoutToolResult(out)
	if err != nil {
		t.Fatal(err)
	}
	if res.StructuredContent != nil || len(res.Content) != 1 {
		t.Fatalf("result = %+v, want a single text block and no structured copy", res)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T, want text", res.Content[0])
	}
	var decoded struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		t.Fatalf("result text is not JSON: %v", err)
	}
	return decoded.Files
}

func TestCheckoutRepositoryWithoutOptInNeverEmitsEncoding(t *testing.T) {
	ctx := context.Background()
	dyn, store, _, annotations := checkoutFixture(t)
	// The fixture's bundle holds a binary file even though this caller did
	// not ask for one (as a misbehaving controller could): it must still
	// come back skipped, never encoded.
	_, out, err := checkoutRepository(ctx, dyn, store, checkoutRepositoryInput{RepositoryRef: "demo-app"})
	if err != nil {
		t.Fatalf("checkoutRepository returned error: %v", err)
	}
	if _, set := (*annotations)[codev1alpha1.AnnotationCheckoutBinaryEncoding]; set {
		t.Fatalf("checkout without binaryEncoding was annotated: %v", *annotations)
	}
	if len(out.Files) != 1 || out.Files[0] != (checkoutFileOutput{Path: "README.md", Content: "# demo"}) {
		t.Fatalf("files = %#v, want only the text file", out.Files)
	}
	if strings.Join(out.Skipped, "|") != "dist/huge.bin (file too large)|public/logo.png (binary)" {
		t.Fatalf("skipped = %q", out.Skipped)
	}
	for _, f := range resultFiles(t, out) {
		if _, has := f["encoding"]; has {
			t.Fatalf("file carries an encoding for a caller that did not opt in: %v", f)
		}
	}
}

func TestCheckoutRepositoryWithOptInReturnsBase64(t *testing.T) {
	ctx := context.Background()
	dyn, store, logo, annotations := checkoutFixture(t)
	_, out, err := checkoutRepository(ctx, dyn, store, checkoutRepositoryInput{RepositoryRef: "demo-app", BinaryEncoding: "base64"})
	if err != nil {
		t.Fatalf("checkoutRepository returned error: %v", err)
	}
	if got := (*annotations)[codev1alpha1.AnnotationCheckoutBinaryEncoding]; got != "base64" {
		t.Fatalf("binary-encoding annotation = %q, want base64", got)
	}
	want := []checkoutFileOutput{
		{Path: "README.md", Content: "# demo"},
		{Path: "public/logo.png", Content: logo, Encoding: "base64"},
	}
	if len(out.Files) != len(want) || out.Files[0] != want[0] || out.Files[1] != want[1] {
		t.Fatalf("files = %#v, want %#v", out.Files, want)
	}
	if len(out.Skipped) != 1 || out.CommitSHA != "abc123" {
		t.Fatalf("output = %+v", out)
	}
	files := resultFiles(t, out)
	if _, has := files[0]["encoding"]; has {
		t.Fatalf("text file carries an encoding: %v", files[0])
	}
	if files[1]["encoding"] != "base64" || files[1]["content"] != logo {
		t.Fatalf("binary file = %v", files[1])
	}
}

func TestCheckoutRepositoryRejectsUnknownBinaryEncoding(t *testing.T) {
	dyn, store, _, _ := checkoutFixture(t)
	_, _, err := checkoutRepository(context.Background(), dyn, store, checkoutRepositoryInput{RepositoryRef: "demo-app", BinaryEncoding: "hex"})
	if err == nil || !strings.Contains(err.Error(), `unsupported binaryEncoding "hex"`) {
		t.Fatalf("error = %v", err)
	}
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "create" {
			t.Fatal("rejected checkout created a RepositoryCheckout")
		}
	}
}
