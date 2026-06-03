/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package aggregatemcp

import (
	"k8s.io/klog/v2"

	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
)

// klogFromCfg returns the framework's logger when Deps is wired, or a
// fresh klog logger otherwise. The aggregator runs both in-process
// against builder.Deps (the real path) and from tests that omit it;
// this lets us log identically in both.
func klogFromCfg(deps *builder.Deps) klog.Logger {
	if deps != nil && deps.Logger.GetSink() != nil {
		return deps.Logger.WithName("provider-mcp-proxy")
	}
	return klog.Background().WithName("provider-mcp-proxy")
}
