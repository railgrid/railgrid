// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"log"
)

// runInitCmd is the one-shot bootstrap. For the code provider this is
// deliberately thin: the hub provisioner already materializes everything the
// CatalogEntry declares (sub-workspace, the four APIResourceSchemas, the
// APIExport, the provider ServiceAccount + minted kubeconfig). The only thing
// the provider's own multicluster manager additionally needs is an
// APIExportEndpointSlice for code.providers.kedge.faros.sh so apiexport.New can
// discover tenant workspaces.
//
// OPEN ITEM (resolve in PR B): confirm whether the hub provisioner already
// creates that APIExportEndpointSlice for provider APIExports. If it does,
// `init` can be dropped entirely and `serve` suffices. Until confirmed this
// stays a no-op skeleton so the binary keeps the init/serve shape its Helm
// chart and Makefile target expect.
func runInitCmd(_ context.Context) error {
	log.Printf("code-provider init: no-op (hub provisioner handles sub-workspace, schemas, APIExport, SA, kubeconfig). " +
		"See init_cmd.go OPEN ITEM re: APIExportEndpointSlice.")
	return nil
}
