// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package authority

import (
	"context"
	"errors"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const Export = "linear.providers.faros.sh"

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Authority struct{ Config *rest.Config }

func (a Authority) Endpoints(ctx context.Context) ([]string, error) {
	c, err := dynamic.NewForConfig(a.Config)
	if err != nil {
		return nil, err
	}
	u, err := c.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices"}).Get(ctx, Export, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	items, _, _ := unstructured.NestedSlice(u.Object, "status", "endpoints")
	var urls []string
	for _, i := range items {
		m, ok := i.(map[string]any)
		if !ok {
			continue
		}
		s, _ := m["url"].(string)
		if s != "" {
			urls = append(urls, strings.TrimRight(s, "/"))
		}
	}
	if len(urls) == 0 {
		return nil, errors.New("linear export has no ready endpoints")
	}
	return urls, nil
}
func (a Authority) Client(endpoint, cluster string) (dynamic.Interface, error) {
	if cluster != "*" && !identifier.MatchString(cluster) {
		return nil, errors.New("invalid logical cluster")
	}
	cfg := rest.CopyConfig(a.Config)
	cfg.Host = endpoint + "/clusters/" + cluster
	cfg.Timeout = 20e9
	return dynamic.NewForConfig(cfg)
}
func (a Authority) Tenant(ctx context.Context, cluster, connection string) (dynamic.Interface, error) {
	endpoints, err := a.Endpoints(ctx)
	if err != nil {
		return nil, err
	}
	for _, endpoint := range endpoints {
		c, err := a.Client(endpoint, cluster)
		if err != nil {
			return nil, err
		}
		_, err = c.Resource(schema.GroupVersionResource{Group: Export, Version: "v1alpha1", Resource: "connections"}).Get(ctx, connection, metav1.GetOptions{})
		if err == nil {
			return c, nil
		}
	}
	return nil, errors.New("connection unavailable in bound tenant")
}
