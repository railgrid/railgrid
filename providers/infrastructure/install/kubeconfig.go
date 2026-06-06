/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

// WriteKubeconfig builds a kubeconfig from a RuntimeIdentity and
// writes it to disk. The format matches what `kubectl --kubeconfig=…`
// expects so operators can poke at it directly when debugging the
// init/serve handoff.

import (
	"fmt"
	"os"
	"path/filepath"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/clientcmd"
)

// WriteKubeconfig serializes a clientcmdapi.Config built from id and
// writes it to path. The file is created with 0600 because it carries
// a bearer token; permissive permissions would let any local user
// hijack the SA's grants.
func WriteKubeconfig(path string, id *RuntimeIdentity) error {
	if id == nil {
		return fmt.Errorf("nil RuntimeIdentity")
	}

	cfg := &clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"provider-workspace": {
				Server:                   id.Server,
				CertificateAuthorityData: id.CAData,
				// TLS verification stays on; the admin config's CA
				// is propagated through so the minted kubeconfig
				// uses the same trust store. For a separate
				// front-proxy with a different CA, init can be
				// extended to read it from a flag — out of scope
				// for v1.
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

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return fmt.Errorf("write kubeconfig: %w", err)
	}
	// clientcmd.WriteToFile preserves existing perms; force 0600 in
	// case it landed on a path the user created with looser bits.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod kubeconfig: %w", err)
	}
	return nil
}
