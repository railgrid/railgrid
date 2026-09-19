// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// runtimeDynamicClient builds a dynamic client for the kro runtime cluster —
// the cluster the kro backend authors RGDs on and the Instance controller
// materializes instances on — and returns the rest.Config it was built from
// (both controllers run a watch cache over it). Resolution: explicit
// KRO_KUBECONFIG, else the pod's in-cluster config (the operator's
// in-cluster-runtime mode). Errors when neither is available (dev/stub-only),
// so the caller keeps the Instance controller off rather than pointing it at
// the wrong cluster. Returns the source for logging.
func runtimeDynamicClient() (dynamic.Interface, *rest.Config, string, error) {
	var cfg *rest.Config
	var src string
	if p := os.Getenv("KRO_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, nil, "", fmt.Errorf("loading KRO_KUBECONFIG: %w", err)
		}
		cfg, src = c, "KRO_KUBECONFIG="+p
	} else if c, err := rest.InClusterConfig(); err == nil {
		cfg, src = c, "in-cluster"
	} else {
		return nil, nil, "", fmt.Errorf("KRO_KUBECONFIG unset and not running in a pod")
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, "", fmt.Errorf("runtime dynamic client: %w", err)
	}
	return dyn, cfg, src, nil
}
