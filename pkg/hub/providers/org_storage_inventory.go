// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package providers

import (
	"fmt"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
)

func quotaGroupResource(key string) (schema.GroupResource, error) {
	name, ok := strings.CutPrefix(key, "count/")
	if !ok || name == "" {
		return schema.GroupResource{}, fmt.Errorf("unsupported provider quota key %q", key)
	}
	resource, group, hasGroup := strings.Cut(name, ".")
	if resource == "" || hasGroup && group == "" {
		return schema.GroupResource{}, fmt.Errorf("invalid provider quota key %q", key)
	}
	// Kubernetes resource plurals do not contain dots; preserve all group
	// segments after the first separator for compatibility with qualified groups.
	if hasGroup {
		group = strings.TrimPrefix(name, resource+".")
	}
	return schema.GroupResource{Group: group, Resource: resource}, nil
}
