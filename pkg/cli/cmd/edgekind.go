/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	railgridclient "github.com/railgrid/railgrid/pkg/client"
)

// edgeKindGVRs are the connectable kinds a `railgrid edge`/`railgrid agent` command
// may address by name. KubernetesCluster is tried first (the common case).
var edgeKindGVRs = []schema.GroupVersionResource{
	railgridclient.KubernetesClusterGVR,
	railgridclient.LinuxServerGVR,
	railgridclient.MacOSServerGVR,
}

func edgeGVRForKind(kind string) schema.GroupVersionResource {
	switch kind {
	case "LinuxServer":
		return railgridclient.LinuxServerGVR
	case "MacOSServer":
		return railgridclient.MacOSServerGVR
	default:
		return railgridclient.KubernetesClusterGVR
	}
}

// parseEdgeRef splits an edge reference into an optional kind qualifier and a
// name. Names are unique per kind but not across kinds, so a Kubernetes cluster
// and a Linux server may both be called "minis"; "server/minis" says which one.
// The qualifier accepts whichever spelling the user has in front of them: the
// type from `railgrid edge list` (server), the kind (linuxserver) or the
// resource (linuxservers).
func parseEdgeRef(ref string) (string, schema.GroupVersionResource, error) {
	qualifier, name, ok := strings.Cut(ref, "/")
	if !ok {
		return ref, schema.GroupVersionResource{}, nil
	}
	if name == "" {
		return "", schema.GroupVersionResource{}, fmt.Errorf("edge reference %q has no name after the %q qualifier", ref, qualifier)
	}
	for _, gvr := range edgeKindGVRs {
		edgeType := railgridclient.EdgeTypeForGVR(gvr)
		switch strings.ToLower(qualifier) {
		case edgeType,
			strings.ToLower(railgridclient.EdgeKindForType(edgeType)),
			gvr.Resource:
			return name, gvr, nil
		}
	}
	return "", schema.GroupVersionResource{}, fmt.Errorf("unknown edge type %q in reference %q (want kubernetes, server or macos)", qualifier, ref)
}

// edgeMatch is one connectable resource that carries a requested name, with the
// GVR it was found under.
type edgeMatch struct {
	obj *unstructured.Unstructured
	gvr schema.GroupVersionResource
}

// findEdgesByName returns every connectable resource called name, across all
// connectable kinds — more than one when the same name is used for, say, both a
// cluster and a server. Every kind has to be probed to notice that, so the
// probes go out together rather than costing one hub round trip each.
func findEdgesByName(ctx context.Context, dyn dynamic.Interface, name string) ([]edgeMatch, error) {
	type probe struct {
		obj *unstructured.Unstructured
		err error
	}
	probes := make([]probe, len(edgeKindGVRs))
	var wg sync.WaitGroup
	for i, gvr := range edgeKindGVRs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probes[i].obj, probes[i].err = dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		}()
	}
	wg.Wait()

	var matches []edgeMatch
	for i, gvr := range edgeKindGVRs {
		switch {
		case probes[i].err == nil:
			matches = append(matches, edgeMatch{obj: probes[i].obj, gvr: gvr})
		case !apierrors.IsNotFound(probes[i].err):
			// NotFound covers both "no such edge" and a kind this workspace's
			// edges API does not serve, so only a real failure (RBAC,
			// connectivity) stops the lookup.
			return nil, probes[i].err
		}
	}
	return matches, nil
}

// getEdgeByName fetches a connectable resource by reference across all
// connectable kinds (KubernetesCluster, LinuxServer, MacOSServer), returning the
// object and the GVR it was found under. The CLI addresses edges by name; the
// kind is discovered here. A name shared by several kinds is rejected rather
// than guessed — pass a qualified reference such as "server/minis".
func getEdgeByName(ctx context.Context, dyn dynamic.Interface, ref string) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	return getEdgeByNamePreferring(ctx, dyn, ref)
}

// getEdgeByNamePreferring resolves ref like getEdgeByName, except that when
// several kinds share the name the first preferred GVR with a match wins
// instead of the reference being reported as ambiguous. Callers pass the kinds
// they can actually act on, in order (`railgrid ssh` prefers LinuxServer), so
// that `railgrid ssh minis` reaches the server even when a cluster of that name
// exists too. A name that matches only kinds outside the preference list still
// resolves, so the caller can answer with its own "wrong kind of edge" message.
func getEdgeByNamePreferring(ctx context.Context, dyn dynamic.Interface, ref string, preferred ...schema.GroupVersionResource) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	name, qualified, err := parseEdgeRef(ref)
	if err != nil {
		return nil, schema.GroupVersionResource{}, err
	}
	if qualified.Resource != "" {
		u, err := dyn.Resource(qualified).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, qualified, err
		}
		return u, qualified, nil
	}

	matches, err := findEdgesByName(ctx, dyn, name)
	if err != nil {
		return nil, schema.GroupVersionResource{}, err
	}
	switch len(matches) {
	case 0:
		return nil, schema.GroupVersionResource{}, fmt.Errorf("edge %q not found (searched KubernetesCluster + LinuxServer + MacOSServer)", name)
	case 1:
		return matches[0].obj, matches[0].gvr, nil
	}
	for _, want := range preferred {
		for _, m := range matches {
			if m.gvr == want {
				return m.obj, m.gvr, nil
			}
		}
	}
	return nil, schema.GroupVersionResource{}, ambiguousEdgeError(name, matches)
}

// ambiguousEdgeError reports a name that several kinds answer to, and shows the
// qualified references that pick one.
func ambiguousEdgeError(name string, matches []edgeMatch) error {
	refs := make([]string, 0, len(matches))
	for _, m := range matches {
		refs = append(refs, railgridclient.EdgeTypeForGVR(m.gvr)+"/"+name)
	}
	return fmt.Errorf("edge %q is ambiguous: %d edges share that name; qualify it with its type — %s",
		name, len(matches), strings.Join(refs, " or "))
}

// listAllEdges lists every connectable resource across all kinds, merged.
func listAllEdges(ctx context.Context, dyn dynamic.Interface) ([]unstructured.Unstructured, error) {
	var items []unstructured.Unstructured
	served := 0
	for _, gvr := range edgeKindGVRs {
		list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			// A 404 for the resource means this workspace's edges API does
			// not serve that kind (an older provider without macosservers,
			// say); skip it. Only when no kind is served is edges disabled.
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("listing %s: %w", gvr.Resource, err)
		}
		served++
		// Some dynamic API responses omit per-item TypeMeta. Preserve the GVR
		// that produced each item so callers can still render a MacOSServer or
		// LinuxServer correctly instead of falling back to Kubernetes.
		for i := range list.Items {
			if list.Items[i].GetKind() == "" {
				list.Items[i].SetKind(railgridclient.EdgeKindForType(railgridclient.EdgeTypeForGVR(gvr)))
			}
			if list.Items[i].GetAPIVersion() == "" {
				list.Items[i].SetAPIVersion(gvr.GroupVersion().String())
			}
		}
		items = append(items, list.Items...)
	}
	if served == 0 {
		return nil, errEdgesNotEnabled
	}
	return items, nil
}

// duplicateEdgeNames returns the names that more than one of the listed edges
// carries — the ones that have to be qualified to address a single edge.
func duplicateEdgeNames(items []unstructured.Unstructured) map[string]bool {
	seen := map[string]int{}
	for i := range items {
		seen[items[i].GetName()]++
	}
	dupes := map[string]bool{}
	for name, n := range seen {
		if n > 1 {
			dupes[name] = true
		}
	}
	return dupes
}

// errEdgesNotEnabled is returned when the edges API is absent from the
// workspace: the provider has not been enabled there.
var errEdgesNotEnabled = fmt.Errorf("the edges provider is not enabled in this workspace (no edges.railgrid.ai API); enable it in the console's Providers page, then retry")
