/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

// Dev-only kro patches for the kind/Tilt environment. In a kind cluster the
// kro pod can't resolve the kcp front-proxy hostnames (kcp.localhost, …) and
// the local-runtime member needs a kind-internal kubeconfig. These are NOT
// helm chart values, so `make dev-kro-up-into` applies them after install; the
// operator replicates that here, gated entirely by env vars so production
// (where DNS just works) is unaffected:
//
//	INFRASTRUCTURE_KRO_HOSTALIASES_IP     e.g. 10.96.2.2 (the envoy gateway ClusterIP)
//	INFRASTRUCTURE_KRO_HOSTALIASES_NAMES  comma list, e.g. kcp.localhost,root.kcp.localhost
//	INFRASTRUCTURE_KRO_SELF_CLUSTER_KUBECONFIG  path to a kind --internal kubeconfig;
//	                                            written as the kro.run/cluster=true
//	                                            "self-cluster" Secret (local runtime member)
//
// When none are set this is a no-op.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// ApplyKroDevPatches applies the kind-only kro Deployment hostAliases and the
// self-cluster member Secret, if the corresponding env vars are set.
func ApplyKroDevPatches(ctx context.Context, cs kubernetes.Interface, namespace, releaseName string) error {
	if ip := os.Getenv("INFRASTRUCTURE_KRO_HOSTALIASES_IP"); ip != "" {
		names := splitCSV(os.Getenv("INFRASTRUCTURE_KRO_HOSTALIASES_NAMES"))
		if len(names) > 0 {
			patch := map[string]any{
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"hostAliases": []any{
								map[string]any{"ip": ip, "hostnames": names},
							},
						},
					},
				},
			}
			body, err := json.Marshal(patch)
			if err != nil {
				return fmt.Errorf("marshal hostAliases patch: %w", err)
			}
			if _, err := cs.AppsV1().Deployments(namespace).Patch(ctx, releaseName, types.StrategicMergePatchType, body, metav1.PatchOptions{}); err != nil {
				if !apierrors.IsNotFound(err) {
					return fmt.Errorf("patch kro hostAliases: %w", err)
				}
			}
		}
	}

	if path := os.Getenv("INFRASTRUCTURE_KRO_SELF_CLUSTER_KUBECONFIG"); path != "" {
		kc, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read self-cluster kubeconfig %s: %w", path, err)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "self-cluster",
				Namespace: namespace,
				Labels:    map[string]string{"kro.run/cluster": "true"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"kubeconfig": kc},
		}
		existing, err := cs.CoreV1().Secrets(namespace).Get(ctx, secret.Name, metav1.GetOptions{})
		switch {
		case err == nil:
			existing.Data = secret.Data
			existing.Labels = secret.Labels
			if _, uerr := cs.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
				return fmt.Errorf("update self-cluster Secret: %w", uerr)
			}
		case apierrors.IsNotFound(err):
			if _, cerr := cs.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
				return fmt.Errorf("create self-cluster Secret: %w", cerr)
			}
		default:
			return fmt.Errorf("get self-cluster Secret: %w", err)
		}
	}

	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
