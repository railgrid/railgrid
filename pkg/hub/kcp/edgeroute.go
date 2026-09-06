/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/kcppaths"
)

// Edge routing for org-owned providers.
//
// A self-hosted provider's control half dials out — it reaches kcp itself, so
// Templates and Instances reconcile with no help. Its DATA half is the problem:
// /services/providers/{name}/** is a hub→provider call, and a provider in a
// tenant's own cluster has no address the hub can dial. The edge tunnel is the
// transport for that one hop (docs/byo-provider-edge-transport.md).
//
// Routing must not be readable from anything the provider controls, or a
// tenant could point platform traffic wherever they liked (E-1). So the edge is
// recorded by the HUB, at registration, as annotations on the provider
// Workspace — an object in the org's tree that only the hub writes.
const (
	// EdgeRouteWorkspaceAnnotation is the team workspace holding the edge.
	EdgeRouteWorkspaceAnnotation = "edges.faros.sh/route-workspace"
	// EdgeRouteEdgeAnnotation is the KubernetesCluster edge's name.
	EdgeRouteEdgeAnnotation = "edges.faros.sh/route-edge"
)

// edgeServiceGVR is the edges provider's Service — the reverse-proxy record the
// agent turns into an HTTP hop inside the tenant's cluster.
var edgeServiceGVR = schema.GroupVersionResource{
	Group: "edges.faros.sh", Version: "v1alpha1", Resource: "services",
}

// EdgeRoute is where a self-hosted provider's backend is reached: which
// workspace holds the edge, which edge, and the hub-owned Service standing in
// front of the provider inside the tenant's cluster.
type EdgeRoute struct {
	// WorkspaceUUID is the team workspace holding the edge and the Service.
	WorkspaceUUID string
	// EdgeName is the KubernetesCluster edge whose agent carries the tunnel.
	EdgeName string
	// ServiceName is the hub-owned edges.faros.sh/Service. Derived, never read
	// from tenant input.
	ServiceName string
	// Cluster is WorkspaceUUID's kcp logical-cluster ID — what the edges proxy
	// path addresses. Empty until ResolveProviderEdgeRoute has filled it in.
	Cluster string
}

// ProviderEdgeServiceName is the hub-owned Service standing in front of one
// org-owned provider. Derived from the provider name so the hub can always
// name it without a lookup, and prefixed so it cannot collide with a Service a
// tenant created for an appliance.
func ProviderEdgeServiceName(providerName string) string { return "provider-" + providerName }

// RecordProviderEdgeBinding stamps the chosen edge onto the provider Workspace.
//
// Called at registration, after the edge has been verified connected. Writing
// it here rather than on the CatalogEntry is the point: the CatalogEntry is
// authored by the tenant's own chart, and routing read from tenant-authored
// fields is routing the tenant controls.
func (b *Bootstrapper) RecordProviderEdgeBinding(ctx context.Context, orgUUID, providerName, wsUUID, edgeName string) error {
	if orgUUID == "" || providerName == "" || wsUUID == "" || edgeName == "" {
		return fmt.Errorf("RecordProviderEdgeBinding: orgUUID, providerName, wsUUID and edgeName are required")
	}
	parent, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.OrgProvidersParent(orgUUID)))
	if err != nil {
		return fmt.Errorf("creating org providers workspace client: %w", err)
	}
	// The workspace is usually seconds old here and its own controllers are
	// still writing to it, so a read-modify-write races them. Re-read inside
	// the retry: a stale object would only lose the race again.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ws, err := parent.Resource(workspaceGVR).Get(ctx, providerName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		annotations := ws.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		if annotations[EdgeRouteWorkspaceAnnotation] == wsUUID && annotations[EdgeRouteEdgeAnnotation] == edgeName {
			return nil
		}
		annotations[EdgeRouteWorkspaceAnnotation] = wsUUID
		annotations[EdgeRouteEdgeAnnotation] = edgeName
		ws.SetAnnotations(annotations)
		_, err = parent.Resource(workspaceGVR).Update(ctx, ws, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return fmt.Errorf("recording edge binding on org provider workspace %s: %w", providerName, err)
	}
	klog.FromContext(ctx).Info("Recorded provider edge binding",
		"org", orgUUID, "provider", providerName, "workspace", wsUUID, "edge", edgeName)
	return nil
}

// GetProviderEdgeRoute reads the binding back. Returns nil when the provider
// has none — a provider registered before edge transport existed, which stays
// on its declared backend URL rather than becoming unroutable.
func (b *Bootstrapper) GetProviderEdgeRoute(ctx context.Context, orgUUID, providerName string) (*EdgeRoute, error) {
	if orgUUID == "" || providerName == "" {
		return nil, fmt.Errorf("GetProviderEdgeRoute: orgUUID and providerName are required")
	}
	parent, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.OrgProvidersParent(orgUUID)))
	if err != nil {
		return nil, fmt.Errorf("creating org providers workspace client: %w", err)
	}
	ws, err := parent.Resource(workspaceGVR).Get(ctx, providerName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting org provider workspace %s: %w", providerName, err)
	}
	annotations := ws.GetAnnotations()
	wsUUID, edge := annotations[EdgeRouteWorkspaceAnnotation], annotations[EdgeRouteEdgeAnnotation]
	if wsUUID == "" || edge == "" {
		return nil, nil
	}
	return &EdgeRoute{
		WorkspaceUUID: wsUUID,
		EdgeName:      edge,
		ServiceName:   ProviderEdgeServiceName(providerName),
	}, nil
}

// ResolveProviderEdgeRoute returns the usable edge route for an org-owned
// provider, reconciling the hub-owned Service on the way.
//
// Implements providers.EdgeRouteResolver. Returns (nil, nil) when the provider
// has no recorded binding — one registered before edge transport existed, which
// keeps its old behaviour rather than becoming abruptly unroutable.
//
// backendURL is the address the provider published about itself; it is parsed
// and validated as cluster DNS, never trusted as-is. A backend the hub cannot
// make sense of is an error, not a silent fallback: falling back would dial an
// address inside the tenant's cluster from the hub.
func (b *Bootstrapper) ResolveProviderEdgeRoute(ctx context.Context, orgUUID, providerName, backendURL string) (*EdgeRoute, error) {
	route, err := b.GetProviderEdgeRoute(ctx, orgUUID, providerName)
	if err != nil || route == nil {
		return nil, err
	}
	target, err := ParseClusterServiceTarget(backendURL)
	if err != nil {
		return nil, fmt.Errorf("provider %s: %w", providerName, err)
	}
	if err := b.EnsureProviderEdgeService(ctx, orgUUID, *route, *target); err != nil {
		return nil, err
	}
	cluster, err := b.workspaceCluster(ctx, orgUUID, route.WorkspaceUUID)
	if err != nil {
		return nil, err
	}
	route.Cluster = cluster
	return route, nil
}

// workspaceCluster resolves a team workspace's kcp logical-cluster ID, which is
// what the edges proxy path addresses.
func (b *Bootstrapper) workspaceCluster(ctx context.Context, orgUUID, wsUUID string) (string, error) {
	orgClient, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.OrgPath(orgUUID)))
	if err != nil {
		return "", fmt.Errorf("creating org workspace client: %w", err)
	}
	ws, err := orgClient.Resource(workspaceGVR).Get(ctx, wsUUID, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting workspace %s in org %s: %w", wsUUID, orgUUID, err)
	}
	cluster, _, _ := unstructured.NestedString(ws.Object, "spec", "cluster")
	if cluster == "" {
		return "", fmt.Errorf("workspace %s in org %s has no spec.cluster", wsUUID, orgUUID)
	}
	return cluster, nil
}

// ClusterServiceTarget is a Kubernetes Service inside a tenant's cluster,
// parsed from an address the provider published about itself.
type ClusterServiceTarget struct {
	Name      string
	Namespace string
	Port      int32
	Scheme    string
}

// ParseClusterServiceTarget reads a provider's declared backend URL as a
// cluster-DNS address and refuses anything else.
//
// The provider's own CatalogEntry is the only thing that knows where it listens
// in the tenant's cluster — the chart's fullname and namespace are its choice,
// not something the hub can predict (an operator-mode install lands on
// `<release>-<chart>.<serve-ns>.svc`, not the `<name>.faros-provider-<name>.svc`
// a hub-side guess would produce). So the address is read from there but not
// trusted: it must be a `.svc` / `.svc.cluster.local` name, so the worst a
// tenant can aim this at is something inside the cluster the tunnel already
// terminates in. An IP, an external host, or a bare name is refused rather than
// silently proxied.
func ParseClusterServiceTarget(backendURL string) (*ClusterServiceTarget, error) {
	if strings.TrimSpace(backendURL) == "" {
		return nil, fmt.Errorf("provider declares no backend URL")
	}
	u, err := url.Parse(backendURL)
	if err != nil {
		return nil, fmt.Errorf("parsing backend URL %q: %w", backendURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("backend URL %q is not http(s)", backendURL)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("backend URL %q has no host", backendURL)
	}
	// Cluster DNS only. This is the whole validation: <svc>.<ns>.svc[.cluster.local]
	labels := strings.Split(strings.TrimSuffix(strings.TrimSuffix(host, "."), ".cluster.local"), ".")
	if len(labels) != 3 || labels[2] != "svc" || labels[0] == "" || labels[1] == "" {
		return nil, fmt.Errorf("backend URL host %q is not a Kubernetes cluster-DNS name (<service>.<namespace>.svc[.cluster.local]); "+
			"an org-owned provider is reached through its edge, so its backend must be a Service inside that cluster", host)
	}

	port := int32(80)
	if u.Scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, perr := strconv.ParseInt(p, 10, 32)
		if perr != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("backend URL %q has an invalid port", backendURL)
		}
		port = int32(n)
	}
	return &ClusterServiceTarget{Name: labels[0], Namespace: labels[1], Port: port, Scheme: u.Scheme}, nil
}

// EnsureProviderEdgeService reconciles the hub-owned edges.faros.sh/Service
// that fronts one org-owned provider.
//
// spec.auth is "passthrough": a provider backend authorizes the END USER, so
// substituting the Service's own token would collapse per-user RBAC into
// "anyone who can reach the tunnel" (E-5). The object is hub-owned and fully
// overwritten on every reconcile, so a tenant editing its target has that edit
// reverted rather than silently redirecting platform traffic (E-4).
func (b *Bootstrapper) EnsureProviderEdgeService(ctx context.Context, orgUUID string, route EdgeRoute, target ClusterServiceTarget) error {
	if orgUUID == "" || route.WorkspaceUUID == "" || route.EdgeName == "" || route.ServiceName == "" {
		return fmt.Errorf("EnsureProviderEdgeService: orgUUID and a complete route are required")
	}
	wsClient, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.WorkspacePath(orgUUID, route.WorkspaceUUID)))
	if err != nil {
		return fmt.Errorf("creating edge workspace client: %w", err)
	}

	desired := map[string]any{
		"edgeRef":   map[string]any{"kind": "KubernetesCluster", "name": route.EdgeName},
		"targetRef": map[string]any{"namespace": target.Namespace, "name": target.Name},
		"port":      int64(target.Port),
		"scheme":    target.Scheme,
		"type":      "generic",
		"auth":      "passthrough",
	}
	labels := map[string]any{
		"edges.faros.sh/edge":      route.EdgeName,
		"providers.faros.sh/owned": "true",
	}

	existing, err := wsClient.Resource(edgeServiceGVR).Get(ctx, route.ServiceName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "edges.faros.sh/v1alpha1",
			"kind":       "Service",
			"metadata":   map[string]any{"name": route.ServiceName, "labels": labels},
			"spec":       desired,
		}}
		if _, cerr := wsClient.Resource(edgeServiceGVR).Create(ctx, obj, metav1.CreateOptions{}); cerr != nil && !errors.IsAlreadyExists(cerr) {
			return fmt.Errorf("creating provider edge Service %s: %w", route.ServiceName, cerr)
		}
		klog.FromContext(ctx).Info("Created hub-owned provider edge Service",
			"org", orgUUID, "workspace", route.WorkspaceUUID, "service", route.ServiceName,
			"target", fmt.Sprintf("%s.%s.svc:%d", target.Name, target.Namespace, target.Port))
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting provider edge Service %s: %w", route.ServiceName, err)
	}

	if current, _, _ := unstructured.NestedMap(existing.Object, "spec"); specMatches(current, desired) {
		return nil
	}
	if err := unstructured.SetNestedMap(existing.Object, desired, "spec"); err != nil {
		return fmt.Errorf("setting spec on provider edge Service %s: %w", route.ServiceName, err)
	}
	existing.SetLabels(mergeLabels(existing.GetLabels(), labels))
	if _, err := wsClient.Resource(edgeServiceGVR).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating provider edge Service %s: %w", route.ServiceName, err)
	}
	klog.FromContext(ctx).Info("Reconciled hub-owned provider edge Service",
		"org", orgUUID, "workspace", route.WorkspaceUUID, "service", route.ServiceName)
	return nil
}

// specMatches reports whether every field the hub owns already has the desired
// value. Compared field-by-field rather than by whole-object equality so that
// status-adjacent or defaulted fields the API server added do not cause an
// update on every reconcile.
func specMatches(current, desired map[string]any) bool {
	if current == nil {
		return false
	}
	for k, want := range desired {
		got, ok := current[k]
		if !ok {
			return false
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return false
		}
	}
	return true
}

func mergeLabels(existing map[string]string, want map[string]any) map[string]string {
	if existing == nil {
		existing = map[string]string{}
	}
	for k, v := range want {
		existing[k] = fmt.Sprint(v)
	}
	return existing
}
