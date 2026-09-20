/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	sdkinstall "github.com/railgrid/provider-sdk/install"
)

// DefaultKCPDir is where the image bakes deploy/chart/files.
const DefaultKCPDir = "/etc/railgrid/kcp"

// KCPDir resolves the directory holding the provider's generated APIExport.
func KCPDir() string {
	if dir := os.Getenv("RAILGRID_KCP_DIR"); dir != "" {
		return dir
	}
	return DefaultKCPDir
}

// APIExport loads the generated APIExport shell this provider applies at
// bootstrap, with per-installation identity hashes stamped on.
//
// Unlike every other railgrid provider, infrastructure has no apigen output:
// its APIResourceSchemas are minted at runtime from the embedded CRDs, and the
// Templates entry uses CachedResource virtual storage that only exists once the
// CachedResource has an identityHash. So the generated file carries an EMPTY
// spec.resources and PlatformSchemaInAPIExport fills it in afterwards —
// sdkinstall.ApplyAPIExport merges rather than clobbers, precisely so the two
// writers can coexist.
//
// What the file does carry is the export's name and its permission claims,
// taken from manifest.yaml by codegen. That is the whole point: a claim is
// written in one place for this provider too, even though the resources are
// not generated.
func APIExport(kcpDir string) (*unstructured.Unstructured, error) {
	if kcpDir == "" {
		kcpDir = KCPDir()
	}
	path := filepath.Join(kcpDir, sdkinstall.APIExportFileName)
	export, err := sdkinstall.LoadAPIExport(path)
	if err != nil {
		return nil, err
	}
	if name := export.GetName(); name != APIExportName {
		return nil, fmt.Errorf("%s declares APIExport %q, want %q; re-run make codegen-infrastructure-provider", path, name, APIExportName)
	}
	hashes, err := sdkinstall.ParseIdentityHashes(os.Getenv("RAILGRID_IDENTITY_HASHES"))
	if err != nil {
		return nil, err
	}
	if err := sdkinstall.StampIdentityHashes(export, hashes); err != nil {
		return nil, err
	}
	return export, nil
}
