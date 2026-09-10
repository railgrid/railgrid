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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/commitbundle"
)

var repositoryCheckoutsGVR = codev1alpha1.SchemeGroupVersion.WithResource("repositorycheckouts")

type checkoutRepositoryInput struct {
	RepositoryRef  string `json:"repositoryRef" jsonschema:"Name of the managed Repository CR to read"`
	Ref            string `json:"ref,omitempty" jsonschema:"Branch, tag, or commit SHA; defaults to the repository default branch"`
	BinaryEncoding string `json:"binaryEncoding,omitempty" jsonschema:"Set to base64 to also receive binary files (images, fonts, archives) as {path, content, encoding: base64} with content the RFC 4648 standard padded base64 of the bytes; limits then are 25 MiB per binary file and 48 MiB in total. Omit it and binary files are skipped and listed in skipped, text files are limited to 16 MiB in total, and no file carries an encoding field"`
}

// checkoutFileOutput is one checked-out file. Encoding is omitted for UTF-8
// text (content is the text) and "base64" for binary files (content is the
// RFC 4648 standard padded base64 of the bytes) — only ever emitted when the
// caller asked for binaryEncoding=base64.
type checkoutFileOutput struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

type checkoutRepositoryOutput struct {
	RepositoryRef string               `json:"repositoryRef"`
	Name          string               `json:"name,omitempty"`
	Phase         string               `json:"phase,omitempty"`
	Ref           string               `json:"ref,omitempty"`
	CommitSHA     string               `json:"commitSHA,omitempty"`
	Files         []checkoutFileOutput `json:"files,omitempty"`
	Skipped       []string             `json:"skipped,omitempty"`
}

// registerCheckoutTools wires the repository-read tool: the commit flow in
// reverse. The tool creates a RepositoryCheckout CR AS THE CALLER, the
// controller reads the tree through the git backend into a provider-owned
// bundle, and the tool returns the bundle's files inline (then reclaims it).
//
// The result is returned ONLY as JSON text content, with no structuredContent
// (hence the untyped output): with a typed output the SDK would carry the
// whole tree twice — once as structuredContent and again as a text copy —
// and a checkout can hold up to 48 MiB of files. Every consumer reads the
// text block (App Studio prefers it; the faros CLI falls back to it).
func registerCheckoutTools(srv *mcp.Server, deps Deps, ident identity) {
	yes := true
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "checkout_repository",
		Title:       "Read a repository's files",
		Description: "Read the tree of a managed Repository at a ref (default branch by default) and return the files inline as JSON text {repositoryRef, name, phase, ref, commitSHA, files:[{path, content, encoding?}], skipped}. Text files are UTF-8 (256 KiB each, 500 files). Binary files are skipped unless binaryEncoding=base64, which returns them with encoding=base64. Files over a limit are skipped and listed. Used to hydrate an App Studio workspace or import an existing repository.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true, OpenWorldHint: &yes},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in checkoutRepositoryInput) (*mcp.CallToolResult, any, error) {
		dyn, err := tenantClient(deps, ident)
		if err != nil {
			return nil, nil, err
		}
		_, out, err := checkoutRepository(ctx, dyn, deps.Bundles, in)
		if err != nil {
			return nil, nil, err
		}
		res, err := checkoutToolResult(out)
		return res, nil, err
	})
}

// checkoutToolResult renders the checkout as a single JSON text block.
func checkoutToolResult(out checkoutRepositoryOutput) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode checkout result: %w", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
}

func checkoutRepository(ctx context.Context, dyn dynamic.Interface, bundles commitbundle.Store, in checkoutRepositoryInput) (*mcp.CallToolResult, checkoutRepositoryOutput, error) {
	if bundles == nil {
		return nil, checkoutRepositoryOutput{}, fmt.Errorf("bundle store is unavailable")
	}
	in.RepositoryRef = strings.TrimSpace(in.RepositoryRef)
	if in.RepositoryRef == "" {
		return nil, checkoutRepositoryOutput{}, fmt.Errorf("repositoryRef is required")
	}
	var includeBinary bool
	switch in.BinaryEncoding {
	case "":
	case commitbundle.EncodingBase64:
		includeBinary = true
	default:
		return nil, checkoutRepositoryOutput{}, fmt.Errorf("unsupported binaryEncoding %q: use %q or omit it", in.BinaryEncoding, commitbundle.EncodingBase64)
	}
	if _, err := getRepository(ctx, dyn, in.RepositoryRef); err != nil {
		return nil, checkoutRepositoryOutput{}, err
	}

	spec := map[string]any{"repositoryRef": in.RepositoryRef}
	putIf(spec, "ref", in.Ref)
	metadata := map[string]any{
		"name":   checkoutObjectName(in.RepositoryRef, time.Now()),
		"labels": map[string]any{codev1alpha1.LabelRepository: in.RepositoryRef},
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
	created, err := dyn.Resource(repositoryCheckoutsGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, checkoutRepositoryOutput{}, fmt.Errorf("create RepositoryCheckout: RepositoryCheckout API is not available in this workspace; re-register the Code provider so repositorycheckouts.code.faros.sh is published: %w", err)
		}
		return nil, checkoutRepositoryOutput{}, fmt.Errorf("create RepositoryCheckout: %w", err)
	}
	out := checkoutRepositoryOutput{
		RepositoryRef: in.RepositoryRef,
		Name:          created.GetName(),
		Phase:         string(codev1alpha1.RepositoryCheckoutPhasePending),
	}
	// A checkout is a transient read request; the CR is deleted once the
	// result is collected (or the wait gives up) rather than kept as audit —
	// unlike commits, it records no durable host-side effect.
	defer func() {
		_ = dyn.Resource(repositoryCheckoutsGVR).Delete(context.WithoutCancel(ctx), created.GetName(), metav1.DeleteOptions{})
	}()

	waited, err := waitRepositoryCheckout(ctx, dyn, created.GetName(), 75*time.Second)
	if err != nil {
		return nil, out, err
	}
	if waited == nil {
		return nil, out, fmt.Errorf("RepositoryCheckout %q did not complete in time", out.Name)
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
		return nil, out, fmt.Errorf("RepositoryCheckout %q failed: %s", out.Name, repositoryCommitConditionMessage(waited))
	}

	// The controller stored the bundle under the CR's cluster scope; read it
	// back and reclaim it.
	bundleScope := strings.TrimSpace(waited.GetAnnotations()["kcp.io/cluster"])
	bundleName, _, _ := unstructured.NestedString(waited.Object, "status", "bundleRef", "name")
	bundleDigest, _, _ := unstructured.NestedString(waited.Object, "status", "bundleRef", "digest")
	if bundleScope == "" || bundleName == "" {
		return nil, out, fmt.Errorf("RepositoryCheckout %q succeeded but reported no bundle", out.Name)
	}
	bundle, err := bundles.Get(ctx, bundleScope, bundleName, bundleDigest)
	if err != nil {
		return nil, out, fmt.Errorf("read checkout bundle: %w", err)
	}
	defer func() { _ = bundles.Delete(context.WithoutCancel(ctx), bundleScope, bundleName, bundleDigest) }()

	out.Files = make([]checkoutFileOutput, 0, len(bundle.Files))
	for _, f := range bundle.Files {
		if f.Encoding != "" && !includeBinary {
			// Safe by construction for callers that did not opt in: they
			// would write the encoded content as text, so they never see an
			// encoded file, whatever the bundle holds.
			out.Skipped = append(out.Skipped, f.Path+" (binary)")
			continue
		}
		out.Files = append(out.Files, checkoutFileOutput{Path: f.Path, Content: f.Content, Encoding: f.Encoding})
	}
	return nil, out, nil
}

// checkoutObjectName composes a per-request-unique RepositoryCheckout name.
func checkoutObjectName(repositoryRef string, now time.Time) string {
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

// waitRepositoryCheckout polls until the checkout reaches a terminal phase.
// A timeout returns (nil, nil) — never a non-terminal object — so the caller
// surfaces "did not complete in time" rather than a misleading failure.
func waitRepositoryCheckout(ctx context.Context, dyn dynamic.Interface, name string, timeout time.Duration) (*unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		obj, err := dyn.Resource(repositoryCheckoutsGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil
			}
			return nil, fmt.Errorf("get RepositoryCheckout %q: %w", name, err)
		}
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		if phase == string(codev1alpha1.RepositoryCheckoutPhaseSucceeded) || phase == string(codev1alpha1.RepositoryCheckoutPhaseFailed) {
			return obj, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-ticker.C:
		}
	}
}
