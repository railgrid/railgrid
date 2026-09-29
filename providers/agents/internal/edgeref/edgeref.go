// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package edgeref is this provider's whole knowledge of the edges provider's
// API: the coordinates of an edge and of the Service a harness is published as,
// and how to read the harness off that Service.
//
// The literals are spelled here rather than imported from
// providers/edges/apis/v1alpha1 for the same reason pkg/runner/client does it:
// what this provider may see of a foreign group is declared under its own
// manifest spec.requires and reaches a workspace only once an admin accepted it,
// so the dependency is a CLAIM, not a Go import — and linking another provider's
// controller API package would make the two modules move together for no gain.
// They must stay in step with providers/edges/apis/v1alpha1 (GroupName, Version,
// the Service and host-edge kinds, ServiceStatus.harness) and with the
// spec.requires entries in providers/agents/manifest.yaml.
//
// It is ALSO the one place the two names of a harness meet the edges naming
// convention. A discovered runner Service is named "<edge>-<type>", and for a
// runner the type is the harness SELECTOR ("claude"), while what the runner
// advertises is the harness's own name ("claude-code"). Conflating those has
// already cost two bugs, so the selector arrives here from
// llm.HarnessSelector and nothing in this package invents either name.
package edgeref

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// GroupName is the edges provider's API group.
	GroupName = "edges.railgrid.ai"
	// Version is the version its kinds are served at.
	Version = "v1alpha1"

	// KindLinuxServer and KindMacOSServer are the HOST edge kinds — the only
	// ones a runner can be enrolled on, because a runner is a process on a
	// machine. They match client.EdgeKindLinuxServer / EdgeKindMacOSServer.
	KindLinuxServer = "LinuxServer"
	KindMacOSServer = "MacOSServer"

	// KindService is the published HTTP service on a host edge.
	KindService = "Service"

	// ResourceLinuxServers, ResourceMacOSServers and ResourceServices are the
	// resources the agents manifest requires.
	ResourceLinuxServers = "linuxservers"
	ResourceMacOSServers = "macosservers"
	ResourceServices     = "services"

	// VerbProxy is the data-plane verb the edges provider serves on a Service,
	// and the coordinate a grant to reach a runner is expressed on. It is a
	// constant here because two callers must spell it identically: the identity
	// that asks for the grant and the client that renders the path.
	VerbProxy = "proxy"
)

// EdgeGVK is the coordinate of a host edge kind. ok is false for anything that
// is not a host edge, which is how a KubernetesCluster edgeRef is refused as a
// harness host rather than 404ing later.
func EdgeGVK(kind string) (schema.GroupVersionKind, bool) {
	switch strings.TrimSpace(kind) {
	case KindLinuxServer, KindMacOSServer:
		return schema.GroupVersionKind{Group: GroupName, Version: Version, Kind: strings.TrimSpace(kind)}, true
	default:
		return schema.GroupVersionKind{}, false
	}
}

// EdgeResource is the resource name for a host edge kind.
func EdgeResource(kind string) string {
	switch strings.TrimSpace(kind) {
	case KindLinuxServer:
		return ResourceLinuxServers
	case KindMacOSServer:
		return ResourceMacOSServers
	default:
		return ""
	}
}

// ServiceGVK is the coordinate of an edges Service.
func ServiceGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: GroupName, Version: Version, Kind: KindService}
}

// ServiceGVR is the same coordinate as a resource, for a dynamic client.
func ServiceGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: ResourceServices}
}

// RunnerServiceName is the discovered Service a harness is published as, and the
// runner identity behind it.
//
// They are the same string on purpose: edges names a discovered Service
// "<edge>-<type>" and the agent names the runner it supervises
// "<edge>-<harness>" (pkg/agent/harnessplane child.runnerID). One function so
// this provider cannot spell the two differently and then fail the client's
// identity check against a runner that is exactly right.
func RunnerServiceName(edge, selector string) string {
	edge, selector = strings.TrimSpace(edge), strings.TrimSpace(selector)
	if edge == "" || selector == "" {
		return ""
	}
	return edge + "-" + selector
}

// HarnessStatus is the harness half of a runner Service's status, as the edges
// provider parsed it out of the runner's capabilities response.
type HarnessStatus struct {
	// Name is what the harness ADVERTISES.
	Name string
	// Version is the executable's version on the host.
	Version string
	// Ready is the HARNESS's readiness, which is not the Service's: a runner
	// answers runner/v1 and refuses every attempt when its harness is missing or
	// version-pinned wrong, so a Ready Service with an unready harness is a real
	// state and the one worth reporting.
	Ready bool
	// Reasons say why Ready is false.
	Reasons []string
}

// HarnessStatusOf reads status.harness off a Service. ok is false when the
// Service reports none, which is every Service that is not a runner.
func HarnessStatusOf(service *unstructured.Unstructured) (HarnessStatus, bool) {
	if service == nil {
		return HarnessStatus{}, false
	}
	raw, found, err := unstructured.NestedMap(service.Object, "status", "harness")
	if err != nil || !found || raw == nil {
		return HarnessStatus{}, false
	}
	out := HarnessStatus{}
	out.Name, _, _ = unstructured.NestedString(service.Object, "status", "harness", "name")
	out.Version, _, _ = unstructured.NestedString(service.Object, "status", "harness", "version")
	out.Ready, _, _ = unstructured.NestedBool(service.Object, "status", "harness", "ready")
	out.Reasons, _, _ = unstructured.NestedStringSlice(service.Object, "status", "harness", "reasons")
	return out, true
}
