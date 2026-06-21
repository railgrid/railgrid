/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package operator

import (
	"context"
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	sdkinstall "github.com/faroshq/provider-sdk/install"
)

// EnsureCatalogEntry registers the provider with the hub by applying its
// CatalogEntry (the embedded manifest) into the provider workspace, rewriting
// the ui/backend URLs to the serve Service the operator owns. This is what makes
// the provider appear in the catalog/portal — without it the workspace is
// bootstrapped but the provider is never listed.
func EnsureCatalogEntry(ctx context.Context, providerCfg *rest.Config, manifest []byte, serveURL string) error {
	if len(manifest) == 0 {
		return fmt.Errorf("no embedded CatalogEntry manifest")
	}

	var obj map[string]any
	if err := yaml.Unmarshal(manifest, &obj); err != nil {
		return fmt.Errorf("parse CatalogEntry manifest: %w", err)
	}
	if spec, ok := obj["spec"].(map[string]any); ok {
		// Point ui + backend at the in-cluster serve Service the operator manages.
		if serveURL != "" {
			if ui, ok := spec["ui"].(map[string]any); ok {
				ui["url"] = serveURL
			}
			if be, ok := spec["backend"].(map[string]any); ok {
				be["url"] = serveURL
			}
		}
		// Stamp the release version. The embedded manifest.yaml carries a
		// placeholder (spec.version: 0.1.0); the chart injects the real
		// .Chart.AppVersion (stamped by `helm package --app-version` at
		// release) via KEDGE_PROVIDER_VERSION so the portal shows the same
		// version as the chart-templated providers.
		if v := os.Getenv("KEDGE_PROVIDER_VERSION"); v != "" {
			spec["version"] = v
		}
	}
	out, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("encode CatalogEntry: %w", err)
	}

	// sdkinstall.ApplyCatalogEntry reads a file; round-trip through a temp file.
	f, err := os.CreateTemp("", "kedge-catalogentry-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(out); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	dyn, err := dynamic.NewForConfig(providerCfg)
	if err != nil {
		return err
	}
	return sdkinstall.ApplyCatalogEntry(ctx, dyn, f.Name())
}

// ServeServiceURL is the in-cluster URL of the serve Service the operator owns.
func ServeServiceURL(name string, port int32) string {
	if port == 0 {
		port = 8081
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", name, ServeNamespace, port)
}
