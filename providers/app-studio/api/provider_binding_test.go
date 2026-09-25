/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
)

// testProviders answers the binding lookup with a fixed provider name, the way
// a workspace that enabled exactly one copy of the dependency would.
func testProviders(provider string) providerLookup {
	return func(_ context.Context, _, _ string) (string, error) { return provider, nil }
}

// defaultTestProviders is the lookup every test server gets unless it cares
// which provider it resolved: the platform copy, under its usual name.
var defaultTestProviders = testProviders(infraDependencyName)
