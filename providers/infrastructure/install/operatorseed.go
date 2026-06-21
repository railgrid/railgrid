// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package install

// Operator-mode kro seeding. Unlike SeedKroCluster (which mints a
// ServiceAccount token and builds a kubeconfig from a RuntimeIdentity),
// the operator is GIVEN the provider kubeconfig directly and simply
// copies it into the kro cluster's kcp-kubeconfig Secret — no token
// minting, no SA. It reuses the same host-rewrite (so the server URL
// resolves from inside the kro pod) and Deployment-bounce logic.

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// RetargetHostToWorkspace rewrites a kcp host URL so it terminates at
// /clusters/<workspacePath>. Idempotent — an existing /clusters/… segment
// is replaced rather than appended. Exported so both the operator's
// rest.Config retarget and the kro kubeconfig retarget share one
// implementation.
func RetargetHostToWorkspace(host, workspacePath string) (string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", fmt.Errorf("parse host %q: %w", host, err)
	}
	if idx := strings.Index(u.Path, "/clusters/"); idx >= 0 {
		// Already cluster-scoped. Leave a specific workspace (cluster ID, e.g.
		// the admin-portal-issued provider kubeconfig, or a workspace path)
		// untouched — only a "root"-scoped host is retargeted. Front proxies that
		// route by cluster ID 404 on a path form, so rewriting an already-scoped
		// kubeconfig breaks access.
		seg := strings.SplitN(strings.TrimPrefix(u.Path[idx:], "/clusters/"), "/", 2)[0]
		if seg != "" && seg != "root" {
			return host, nil
		}
		u.Path = u.Path[:idx]
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/clusters/" + workspacePath
	return u.String(), nil
}

// SeedKroClusterFromKubeconfig writes the provided kubeconfig into the kro
// cluster's kcp-kubeconfig Secret (KroSecretName in KroNamespace on the
// runtimeConfig cluster) and bounces the kro Deployment so it reloads. The
// cluster server in the kubeconfig is first retargeted to workspacePath (when
// set) so kro reads the provider workspace's APIExportEndpointSlice, then
// host-rewritten via rewriteHostForKro so the URL resolves from inside the kro
// pod. Auth (token or client cert) is preserved verbatim from the input.
func SeedKroClusterFromKubeconfig(ctx context.Context, runtimeConfig *rest.Config, kubeconfig []byte, workspacePath string) error {
	cfg, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return fmt.Errorf("load provider kubeconfig: %w", err)
	}

	// Rewrite the cluster(s) the kubeconfig references: retarget to the
	// provider workspace, then make the host reachable from the kro pod.
	for _, cluster := range cfg.Clusters {
		if cluster == nil {
			continue
		}
		if workspacePath != "" {
			retargeted, rerr := RetargetHostToWorkspace(cluster.Server, workspacePath)
			if rerr != nil {
				return fmt.Errorf("retarget kro kubeconfig server: %w", rerr)
			}
			cluster.Server = retargeted
		}
		server, insecure, herr := rewriteHostForKro(cluster.Server, cluster.CertificateAuthorityData)
		if herr != nil {
			return fmt.Errorf("rewrite host for kro: %w", herr)
		}
		cluster.Server = server
		if insecure {
			cluster.InsecureSkipTLSVerify = true
			cluster.CertificateAuthorityData = nil
			cluster.CertificateAuthority = ""
		}
	}

	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return fmt.Errorf("encode kro kubeconfig: %w", err)
	}

	cs, err := kubernetes.NewForConfig(runtimeConfig)
	if err != nil {
		return fmt.Errorf("runtime kubernetes client: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: KroSecretName, Namespace: KroNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"kubeconfig": out},
	}

	existing, err := cs.CoreV1().Secrets(KroNamespace).Get(ctx, KroSecretName, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Data = secret.Data
		if _, uerr := cs.CoreV1().Secrets(KroNamespace).Update(ctx, existing, metav1.UpdateOptions{}); uerr != nil {
			return fmt.Errorf("update kro Secret: %w", uerr)
		}
	case apierrors.IsNotFound(err):
		if _, cerr := cs.CoreV1().Secrets(KroNamespace).Create(ctx, secret, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return fmt.Errorf("create kro Secret: %w", cerr)
		}
	default:
		return fmt.Errorf("get existing kro Secret: %w", err)
	}

	// Bounce kro so it reloads the kubeconfig (best-effort: a missing
	// Deployment — chart not installed yet — is ignored).
	return restartKroDeployment(ctx, cs)
}
