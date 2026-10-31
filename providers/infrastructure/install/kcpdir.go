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

// APIExport loads the generated export containing platform resources and claims.
// Bootstrap selects Templates virtual storage before publication; the Template
// controller adds per-template resources at runtime, preserved by the SDK merge.
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
	return export, nil
}
