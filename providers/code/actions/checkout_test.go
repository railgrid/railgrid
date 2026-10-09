// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-code/commitexec"
)

// checkoutFixture stands in for the checkout controller over the commit
// fixture: every RepositoryCheckout the provider creates immediately reports
// a stored bundle holding a text and a binary file. created records the CRs
// the provider wrote, so a test can assert who they were written as and that
// they were reclaimed.
type checkoutFixture struct {
	*commitFixture
	logo    string
	created []*unstructured.Unstructured
	phase   string
}

func newCheckoutFixture(t *testing.T) *checkoutFixture {
	t.Helper()
	f := &checkoutFixture{commitFixture: newCommitFixture(t), phase: string(api.RepositoryCheckoutPhaseSucceeded)}
	f.logo = base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 0x00})
	bundle, err := f.bundles.Put(context.Background(), testCluster, []commitbundle.File{
		{Path: "README.md", Content: "# product"},
		{Path: "public/logo.png", Content: f.logo, Encoding: commitbundle.EncodingBase64},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.provider.PrependReactor("create", "repositorycheckouts", func(action ktesting.Action) (bool, runtime.Object, error) {
		obj := action.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		f.created = append(f.created, obj.DeepCopy())
		obj.SetAnnotations(map[string]string{"kcp.io/cluster": testCluster})
		obj.Object["status"] = map[string]any{
			"phase":     f.phase,
			"ref":       "main",
			"commitSHA": strings.Repeat("a", 40),
			"bundleRef": map[string]any{"name": bundle.Name, "digest": bundle.Digest},
			"skipped":   []any{"dist/huge.bin (file too large)"},
			"conditions": []any{map[string]any{
				"type": api.ConditionReady, "status": "False", "reason": "RefNotFound", "message": "ref does not exist",
			}},
		}
		if err := f.provider.Tracker().Create(commitexec.RepositoryCheckoutsGVR, obj, "", metav1.CreateOptions{}); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	return f
}

// checkoutsLeft counts the RepositoryCheckouts the provider created that
// still exist: a checkout is transient, so the answer should be zero.
func (f *checkoutFixture) checkoutsLeft(t *testing.T) int {
	t.Helper()
	left := 0
	for _, created := range f.created {
		_, err := f.provider.Resource(commitexec.RepositoryCheckoutsGVR).Get(context.Background(), created.GetName(), metav1.GetOptions{})
		switch {
		case err == nil:
			left++
		case !apierrors.IsNotFound(err):
			t.Fatal(err)
		}
	}
	return left
}

// The whole point of the verb: the gate passes, the RepositoryCheckout is
// created AS THE PROVIDER (through the client the gate returned, not with
// any caller credential), the controller's bundle comes back as files, and
// both the CR and the bundle are reclaimed.
func TestCheckoutVerbReturnsTreeAndReclaimsRequest(t *testing.T) {
	f := newCheckoutFixture(t)
	response, envelope := f.invoke(t, Checkout, map[string]any{"repositoryUID": "repo-uid", "ref": "main", "binaryEncoding": "base64"})
	if response.Code != http.StatusOK || envelope.Error != nil {
		t.Fatalf("checkout → %d %s", response.Code, response.Body.String())
	}
	var out commitexec.CheckoutResult
	if err := json.Unmarshal(envelope.Result, &out); err != nil {
		t.Fatalf("result is not a checkout: %v", err)
	}
	if out.RepositoryRef != "product" || out.Ref != "main" || out.CommitSHA != strings.Repeat("a", 40) || out.Phase != string(api.RepositoryCheckoutPhaseSucceeded) {
		t.Fatalf("checkout = %+v", out)
	}
	want := []commitexec.CheckoutFile{
		{Path: "README.md", Content: "# product"},
		{Path: "public/logo.png", Content: f.logo, Encoding: "base64"},
	}
	if len(out.Files) != 2 || out.Files[0] != want[0] || out.Files[1] != want[1] {
		t.Fatalf("files = %#v, want %#v", out.Files, want)
	}
	if strings.Join(out.Skipped, "|") != "dist/huge.bin (file too large)" {
		t.Fatalf("skipped = %q", out.Skipped)
	}
	if len(f.created) != 1 {
		t.Fatalf("provider created %d RepositoryCheckouts, want 1", len(f.created))
	}
	created := f.created[0]
	if ref, _, _ := unstructured.NestedString(created.Object, "spec", "ref"); ref != "main" {
		t.Fatalf("created checkout spec.ref = %q", ref)
	}
	if got := created.GetAnnotations()[api.AnnotationCheckoutBinaryEncoding]; got != "base64" {
		t.Fatalf("binary-encoding annotation = %q, want base64", got)
	}
	if got := created.GetLabels()[api.LabelRepository]; got != "product" {
		t.Fatalf("repository label = %q", got)
	}
	if n := f.checkoutsLeft(t); n != 0 {
		t.Fatalf("%d RepositoryCheckouts left behind, want 0 (a checkout is transient)", n)
	}
}

// Without the base64 opt-in a binary file is skipped, never encoded, even
// though the controller's bundle holds it.
func TestCheckoutVerbWithoutOptInSkipsBinaries(t *testing.T) {
	f := newCheckoutFixture(t)
	response, envelope := f.invoke(t, Checkout, map[string]any{"repositoryUID": "repo-uid"})
	if response.Code != http.StatusOK || envelope.Error != nil {
		t.Fatalf("checkout → %d %s", response.Code, response.Body.String())
	}
	var out commitexec.CheckoutResult
	if err := json.Unmarshal(envelope.Result, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 1 || out.Files[0] != (commitexec.CheckoutFile{Path: "README.md", Content: "# product"}) {
		t.Fatalf("files = %#v, want only the text file", out.Files)
	}
	if strings.Join(out.Skipped, "|") != "dist/huge.bin (file too large)|public/logo.png (binary)" {
		t.Fatalf("skipped = %q", out.Skipped)
	}
	if _, set := f.created[0].GetAnnotations()[api.AnnotationCheckoutBinaryEncoding]; set {
		t.Fatal("checkout without binaryEncoding was annotated for binaries")
	}
}

func TestCheckoutVerbRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input map[string]any
		code  string
		want  int
	}{
		{name: "wrong repository uid", input: map[string]any{"repositoryUID": "other"}, code: "action_forbidden", want: http.StatusForbidden},
		{name: "unknown binary encoding", input: map[string]any{"repositoryUID": "repo-uid", "binaryEncoding": "hex"}, code: "invalid_action_input", want: http.StatusBadRequest},
		{name: "unknown input member", input: map[string]any{"repositoryUID": "repo-uid", "repository": "product"}, code: "invalid_action_input", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCheckoutFixture(t)
			response, envelope := f.invoke(t, Checkout, tc.input)
			if response.Code != tc.want || envelope.Error == nil || envelope.Error.Code != tc.code {
				t.Fatalf("checkout → %d %s, want %d %s", response.Code, response.Body.String(), tc.want, tc.code)
			}
			if len(f.created) != 0 {
				t.Fatal("a refused checkout created a RepositoryCheckout")
			}
		})
	}
}

// A checkout the controller settles as Failed carries the controller's Ready
// message, the one detail the consumer can act on, and is still reclaimed.
func TestCheckoutVerbReportsControllerFailure(t *testing.T) {
	f := newCheckoutFixture(t)
	f.phase = string(api.RepositoryCheckoutPhaseFailed)
	response, envelope := f.invoke(t, Checkout, map[string]any{"repositoryUID": "repo-uid", "ref": "missing"})
	if response.Code != http.StatusBadGateway || envelope.Error == nil || envelope.Error.Code != "checkout_failed" {
		t.Fatalf("checkout → %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(envelope.Error.Message, "ref does not exist") {
		t.Fatalf("error message = %q, want the controller's Ready message", envelope.Error.Message)
	}
	if n := f.checkoutsLeft(t); n != 0 {
		t.Fatalf("%d RepositoryCheckouts left behind after a failure", n)
	}
}

// The gate: a caller who cannot see the Repository is refused before the
// provider writes anything, with the contract's non-disclosing not-found.
func TestCheckoutVerbDeniedWithoutVisibility(t *testing.T) {
	f := newCheckoutFixture(t)
	f.visible = false
	response, envelope := f.invoke(t, Checkout, map[string]any{"repositoryUID": "repo-uid"})
	if response.Code != http.StatusNotFound || envelope.Error == nil || envelope.Error.Code != "action_not_found" {
		t.Fatalf("checkout → %d %s", response.Code, response.Body.String())
	}
	if len(f.created) != 0 {
		t.Fatal("an invisible repository was checked out")
	}
}
