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
	"log"
	"sync/atomic"
	"time"

	"github.com/faroshq/provider-linear/internal/engine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type Controller struct {
	Authority Authority
	Ready     atomic.Bool
}

func (c *Controller) Run(ctx context.Context) {
	for {
		err := c.Reconcile(ctx)
		c.Ready.Store(err == nil)
		if err != nil && ctx.Err() == nil {
			log.Print("Linear controller reconciliation incomplete")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
func (c *Controller) Reconcile(ctx context.Context) error {
	endpoints, err := c.Authority.Endpoints(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, endpoint := range endpoints {
		all, err := c.Authority.Client(endpoint, "*")
		if err != nil {
			return err
		}
		for _, gvr := range []schema.GroupVersionResource{engine.Connections, engine.Operations, engine.Events} {
			cursor := ""
			for {
				page, err := all.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 100, Continue: cursor})
				if err != nil {
					failures = append(failures, err)
					break
				}
				for i := range page.Items {
					u := &page.Items[i]
					cluster := u.GetAnnotations()["kcp.io/cluster"]
					cl, err := c.Authority.Client(endpoint, cluster)
					if err != nil {
						failures = append(failures, err)
						continue
					}
					e := engine.Engine{Client: cl}
					var fn func(context.Context, *unstructured.Unstructured) error
					switch gvr.Resource {
					case "connections":
						fn = e.Probe
					case "operations":
						fn = e.Reconcile
					case "events":
						fn = e.Prune
					}
					if err = fn(ctx, u); err != nil {
						failures = append(failures, err)
					}
				}
				cursor = page.GetContinue()
				if cursor == "" {
					break
				}
			}
		}
	}
	return errors.Join(failures...)
}
