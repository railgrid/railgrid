/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenant

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// DefaultCredentialsNamespace is the namespace a Connection's secretRef
// resolves to when its Namespace field is empty. Overridable so an admin can
// push credential Secrets into a namespace tenants cannot write to.
func DefaultCredentialsNamespace() string {
	if v := os.Getenv("KEDGE_TENANT_CREDENTIALS_NAMESPACE"); v != "" {
		return v
	}
	return "default"
}

// DefaultTokenKey is the Secret data key holding a PAT when the Connection's
// secretRef.Key is empty.
const DefaultTokenKey = "token"

// ResolveToken reads the named Secret via the supplied dynamic client and
// returns the (base64-decoded) value under key. Empty namespace/key fall back
// to the conventions above.
//
// The dynamic client may be either a controller's VW-scoped per-cluster client
// (the normal path, authorized by the secrets permission claim) or the
// caller-token client from ClientFactory (the MCP/portal path). Maps 404 →
// ErrCredentialsMissing and 403 → ErrAPIBindingMissing.
func ResolveToken(ctx context.Context, dyn dynamic.Interface, namespace, name, key string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("connection secretRef.name is empty")
	}
	if namespace == "" {
		namespace = DefaultCredentialsNamespace()
	}
	if key == "" {
		key = DefaultTokenKey
	}
	obj, err := dyn.Resource(secretGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", ErrCredentialsMissing
		}
		if apierrors.IsForbidden(err) {
			return "", ErrAPIBindingMissing
		}
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	data, err := decodeSecretData(obj)
	if err != nil {
		return "", err
	}
	v, ok := data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s has no key %q", namespace, name, key)
	}
	return string(v), nil
}

// decodeSecretData materializes the Secret's data the same way the typed
// client does — unstructured holds base64 strings under .data and raw strings
// under .stringData.
func decodeSecretData(obj *unstructured.Unstructured) (map[string][]byte, error) {
	out := map[string][]byte{}
	if data, found, _ := unstructured.NestedMap(obj.Object, "data"); found {
		for k, v := range data {
			s, ok := v.(string)
			if !ok {
				continue
			}
			dec, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return nil, fmt.Errorf("decoding secret data key %q: %w", k, err)
			}
			out[k] = dec
		}
	}
	if sd, found, _ := unstructured.NestedMap(obj.Object, "stringData"); found {
		for k, v := range sd {
			if s, ok := v.(string); ok {
				out[k] = []byte(s)
			}
		}
	}
	return out, nil
}
