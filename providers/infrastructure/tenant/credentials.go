// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

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
)

var secretGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// secretName / secretNamespace are convention-bound (cloud-credentials
// in `default`) but overridable via env vars in case a tenant runs in
// a non-default setup or the platform admin wants to push the secret
// into a privileged namespace tenants can't write to.
func secretName() string {
	if v := os.Getenv("KEDGE_TENANT_CREDENTIALS_SECRET"); v != "" {
		return v
	}
	return "cloud-credentials"
}

func secretNamespace() string {
	if v := os.Getenv("KEDGE_TENANT_CREDENTIALS_NAMESPACE"); v != "" {
		return v
	}
	return "default"
}

// ResolveCloudCredentials reads the `cloud-credentials` Secret from
// the tenant's workspace via the APIExport-mediated dynamic client and
// returns its Data map (base64-decoded). Returns ErrCredentialsMissing
// for 404 (the tenant just hasn't created the Secret yet) and
// ErrAPIBindingMissing for 403 (the tenant hasn't accepted the
// secrets permission claim — Enable flow incomplete).
//
// The returned map's keys depend on the cloud — see
// docs/credentials.md for the convention each template author should
// adhere to (e.g. aws_access_key_id, gcp_service_account_json).
func ResolveCloudCredentials(ctx context.Context, factory *ClientFactory, clusterID, token string) (map[string][]byte, error) {
	dyn, err := factory.For(clusterID, token)
	if err != nil {
		return nil, err
	}
	obj, err := dyn.Resource(secretGVR).Namespace(secretNamespace()).
		Get(ctx, secretName(), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrCredentialsMissing
		}
		if apierrors.IsForbidden(err) {
			return nil, ErrAPIBindingMissing
		}
		return nil, fmt.Errorf("get cloud-credentials in cluster %q: %w", clusterID, err)
	}
	return decodeSecretData(obj)
}

// decodeSecretData mirrors how the typed Secret client materializes
// the Data field. Unstructured holds base64-encoded strings under
// .data (vs raw []byte on typed objects), so we decode here once.
// stringData is also flattened in — k8s merges it into Data
// server-side on write, but a fresh Get may surface either depending
// on how the secret was constructed.
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
