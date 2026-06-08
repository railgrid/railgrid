/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

// Runtime kubeconfig assembly.
//
// WriteKubeconfig writes the minted runtime kubeconfig to a local file
// (used by dev runs and the legacy init→serve handoff on disk).
//
// WriteKubeconfigToSecret writes the same kubeconfig into a Secret in the
// host cluster — the cluster the provider Deployment runs in. This is the
// path the Helm init container uses: it bootstraps kcp with an admin
// kubeconfig, then drops the low-privilege runtime kubeconfig into the
// Secret the runtime container mounts. Because the runtime token is a
// long-lived (legacy) SA token, the Secret never needs re-minting.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// RuntimeKubeconfigSecretKey is the data key under which the runtime
// kubeconfig is stored in its Secret. Must match the Helm chart's
// volume item key (deployment.yaml mounts items[key=kubeconfig]).
const RuntimeKubeconfigSecretKey = "kubeconfig"

// buildRuntimeKubeconfig assembles the clientcmd Config both writers share.
// TLS verification stays on; the admin config's CA is propagated through so
// the minted kubeconfig uses the same trust store.
func buildRuntimeKubeconfig(id *RuntimeIdentity) *clientcmdapi.Config {
	return &clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"provider-workspace": {
				Server:                   id.Server,
				CertificateAuthorityData: id.CAData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			id.ServiceAccount: {
				Token: id.Token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"provider": {
				Cluster:   "provider-workspace",
				AuthInfo:  id.ServiceAccount,
				Namespace: id.Namespace,
			},
		},
		CurrentContext: "provider",
	}
}

// WriteKubeconfig serializes the runtime kubeconfig built from id and writes
// it to path. The file is created with 0600 because it carries a bearer
// token; permissive permissions would let any local user hijack the SA's
// grants.
func WriteKubeconfig(path string, id *RuntimeIdentity) error {
	if id == nil {
		return fmt.Errorf("nil RuntimeIdentity")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	if err := clientcmd.WriteToFile(*buildRuntimeKubeconfig(id), path); err != nil {
		return fmt.Errorf("write kubeconfig: %w", err)
	}
	// clientcmd.WriteToFile preserves existing perms; force 0600 in case it
	// landed on a path the user created with looser bits.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod kubeconfig: %w", err)
	}
	return nil
}

// WriteKubeconfigToSecret writes the runtime kubeconfig built from id into the
// named Secret in the host cluster reachable via hostConfig. Idempotent:
// updates the Secret's data if it already exists, creates it otherwise. The
// runtime container mounts this Secret read-only (optional volume), so a brief
// window before the data lands is tolerated by the provider (it serves catalog
// reads and 502s broker writes until the kubeconfig appears).
func WriteKubeconfigToSecret(ctx context.Context, hostConfig *rest.Config, namespace, name string, id *RuntimeIdentity) error {
	if id == nil {
		return fmt.Errorf("nil RuntimeIdentity")
	}
	data, err := clientcmd.Write(*buildRuntimeKubeconfig(id))
	if err != nil {
		return fmt.Errorf("marshal kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(hostConfig)
	if err != nil {
		return fmt.Errorf("host kubernetes client: %w", err)
	}

	desired := map[string][]byte{RuntimeKubeconfigSecretKey: data}
	existing, err := cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Data = desired
		if _, err := cs.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update Secret %s/%s: %w", namespace, name, err)
		}
		return nil
	case apierrors.IsNotFound(err):
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       desired,
		}
		if _, err := cs.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create Secret %s/%s: %w", namespace, name, err)
		}
		return nil
	default:
		return fmt.Errorf("get Secret %s/%s: %w", namespace, name, err)
	}
}
