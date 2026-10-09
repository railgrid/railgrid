/*
Copyright 2026 The Railgrid Authors.

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

package commitexec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/commitbundle"
)

// The checkout is the commit flow in reverse, and it has the same two
// projections: the repositories/checkout verb (actions/checkout.go), which
// kcp authorizes and the gate reads for, and the checkout_repository MCP tool
// (mcpserver/tools_checkout.go), which runs as the bearer it was handed. Both
// run this one executor: create a RepositoryCheckout, let the checkout
// controller read the tree through the git backend into a provider-owned
// bundle, return the bundle's files inline, and reclaim both. What differs
// between the callers is only WHO dyn acts as — the provider, through its
// export virtual workspace, for the verb; the caller, for the tool.

// RepositoryCheckoutsGVR is the transient read-request kind the executor
// creates and deletes.
var RepositoryCheckoutsGVR = codev1alpha1.SchemeGroupVersion.WithResource("repositorycheckouts")

// CheckoutTimeout bounds how long a checkout waits for the controller.
const CheckoutTimeout = 75 * time.Second

// CheckoutRequest is one read of a repository's tree.
type CheckoutRequest struct {
	// RepositoryRef names the managed Repository to read.
	RepositoryRef string
	// Ref is a branch, tag or commit SHA; empty reads the default branch.
	Ref string
	// BinaryEncoding is "" to skip binary files (they are listed in Skipped)
	// or commitbundle.EncodingBase64 to receive them base64-encoded.
	BinaryEncoding string
}

// CheckoutFile is one checked-out file. Encoding is omitted for UTF-8 text
// (Content is the text) and "base64" for binary files (Content is the RFC
// 4648 standard padded base64 of the bytes) — only ever emitted when the
// caller asked for base64.
type CheckoutFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

// CheckoutResult is the checkout as every projection returns it.
type CheckoutResult struct {
	RepositoryRef string         `json:"repositoryRef"`
	Name          string         `json:"name,omitempty"`
	Phase         string         `json:"phase,omitempty"`
	Ref           string         `json:"ref,omitempty"`
	CommitSHA     string         `json:"commitSHA,omitempty"`
	Files         []CheckoutFile `json:"files,omitempty"`
	Skipped       []string       `json:"skipped,omitempty"`
}

// ErrCheckoutTimeout is wrapped when the controller did not settle the
// checkout within CheckoutTimeout; the result carries the request's name.
var ErrCheckoutTimeout = errors.New("did not complete in time")

// ErrCheckoutFailed is wrapped when the controller settled the checkout as
// Failed; the error message ends with the Ready condition's message.
var ErrCheckoutFailed = errors.New("failed")

// Validate reports whether the request can be executed.
func (req *CheckoutRequest) Validate() error {
	if strings.TrimSpace(req.RepositoryRef) == "" {
		return fmt.Errorf("repositoryRef is required")
	}
	switch req.BinaryEncoding {
	case "", commitbundle.EncodingBase64:
		return nil
	default:
		return fmt.Errorf("unsupported binaryEncoding %q: use %q or omit it", req.BinaryEncoding, commitbundle.EncodingBase64)
	}
}

// Checkout reads the repository's tree as dyn, in dyn's cluster. The
// RepositoryCheckout it creates is transient and deleted once the result is
// collected (or the wait gives up), and the bundle the controller stored is
// reclaimed after it is read: unlike a commit, a checkout records no durable
// host-side effect and leaves no audit object behind.
//
// The partial result is returned alongside ErrCheckoutTimeout and
// ErrCheckoutFailed so a caller can name the request in its own message.
func Checkout(ctx context.Context, dyn dynamic.Interface, bundles commitbundle.Store, req CheckoutRequest) (CheckoutResult, error) {
	if bundles == nil {
		return CheckoutResult{}, fmt.Errorf("bundle store is unavailable")
	}
	req.RepositoryRef = strings.TrimSpace(req.RepositoryRef)
	if err := req.Validate(); err != nil {
		return CheckoutResult{}, err
	}
	includeBinary := req.BinaryEncoding == commitbundle.EncodingBase64
	if _, err := GetRepository(ctx, dyn, req.RepositoryRef); err != nil {
		return CheckoutResult{}, err
	}

	spec := map[string]any{"repositoryRef": req.RepositoryRef}
	putIf(spec, "ref", strings.TrimSpace(req.Ref))
	metadata := map[string]any{
		"name":   CheckoutObjectName(req.RepositoryRef, time.Now()),
		"labels": map[string]any{codev1alpha1.LabelRepository: req.RepositoryRef},
	}
	if includeBinary {
		metadata["annotations"] = map[string]any{codev1alpha1.AnnotationCheckoutBinaryEncoding: commitbundle.EncodingBase64}
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": codev1alpha1.SchemeGroupVersion.String(),
		"kind":       "RepositoryCheckout",
		"metadata":   metadata,
		"spec":       spec,
	}}
	created, err := dyn.Resource(RepositoryCheckoutsGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return CheckoutResult{}, fmt.Errorf("create RepositoryCheckout: RepositoryCheckout API is not available in this workspace; re-register the Code provider so repositorycheckouts.code.railgrid.ai is published: %w", err)
		}
		return CheckoutResult{}, fmt.Errorf("create RepositoryCheckout: %w", err)
	}
	out := CheckoutResult{
		RepositoryRef: req.RepositoryRef,
		Name:          created.GetName(),
		Phase:         string(codev1alpha1.RepositoryCheckoutPhasePending),
	}
	defer func() {
		_ = dyn.Resource(RepositoryCheckoutsGVR).Delete(context.WithoutCancel(ctx), created.GetName(), metav1.DeleteOptions{})
	}()

	waited, done, err := WaitForPhase(ctx, dyn, RepositoryCheckoutsGVR, "RepositoryCheckout", created.GetName(), CheckoutTimeout,
		PhaseIn(string(codev1alpha1.RepositoryCheckoutPhaseSucceeded), string(codev1alpha1.RepositoryCheckoutPhaseFailed)))
	if err != nil {
		return out, err
	}
	if !done {
		return out, fmt.Errorf("RepositoryCheckout %q %w", out.Name, ErrCheckoutTimeout)
	}
	phase, _, _ := unstructured.NestedString(waited.Object, "status", "phase")
	out.Phase = phase
	if ref, _, _ := unstructured.NestedString(waited.Object, "status", "ref"); ref != "" {
		out.Ref = ref
	}
	if sha, _, _ := unstructured.NestedString(waited.Object, "status", "commitSHA"); sha != "" {
		out.CommitSHA = sha
	}
	if skipped, _, _ := unstructured.NestedStringSlice(waited.Object, "status", "skipped"); len(skipped) > 0 {
		out.Skipped = skipped
	}
	if phase != string(codev1alpha1.RepositoryCheckoutPhaseSucceeded) {
		return out, fmt.Errorf("RepositoryCheckout %q %w: %s", out.Name, ErrCheckoutFailed, ConditionMessage(waited))
	}

	// The controller stored the bundle under the CR's cluster scope; read it
	// back and reclaim it.
	bundleScope := strings.TrimSpace(waited.GetAnnotations()["kcp.io/cluster"])
	bundleName, _, _ := unstructured.NestedString(waited.Object, "status", "bundleRef", "name")
	bundleDigest, _, _ := unstructured.NestedString(waited.Object, "status", "bundleRef", "digest")
	if bundleScope == "" || bundleName == "" {
		return out, fmt.Errorf("RepositoryCheckout %q succeeded but reported no bundle", out.Name)
	}
	bundle, err := bundles.Get(ctx, bundleScope, bundleName, bundleDigest)
	if err != nil {
		return out, fmt.Errorf("read checkout bundle: %w", err)
	}
	defer func() { _ = bundles.Delete(context.WithoutCancel(ctx), bundleScope, bundleName, bundleDigest) }()

	out.Files = make([]CheckoutFile, 0, len(bundle.Files))
	for _, f := range bundle.Files {
		if f.Encoding != "" && !includeBinary {
			// Safe by construction for callers that did not opt in: they
			// would write the encoded content as text, so they never see an
			// encoded file, whatever the bundle holds.
			out.Skipped = append(out.Skipped, f.Path+" (binary)")
			continue
		}
		out.Files = append(out.Files, CheckoutFile{Path: f.Path, Content: f.Content, Encoding: f.Encoding})
	}
	return out, nil
}

// CheckoutObjectName composes a per-request-unique RepositoryCheckout name.
func CheckoutObjectName(repositoryRef string, now time.Time) string {
	base := strings.Trim(repositoryRef, "-")
	if base == "" {
		base = "repository"
	}
	suffix := fmt.Sprintf("%x", now.UnixNano())
	maxBase := 253 - len("-checkout-") - len(suffix)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	if base == "" {
		base = "repository"
	}
	return base + "-checkout-" + suffix
}

// ConditionMessage returns the Ready condition's message, or a placeholder.
func ConditionMessage(obj *unstructured.Unstructured) string {
	if msg, ok := ReadyCondition(obj)["message"].(string); ok && msg != "" {
		return msg
	}
	return "unknown error"
}

// ReadyCondition returns the object's Ready condition, or nil.
func ReadyCondition(obj *unstructured.Unstructured) map[string]any {
	if obj == nil {
		return nil
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conds {
		if cond, ok := raw.(map[string]any); ok && cond["type"] == codev1alpha1.ConditionReady {
			return cond
		}
	}
	return nil
}
