/*
Copyright 2026 The Faros Authors.

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

package kcp

// Edge install targets: the KubernetesCluster edges an Org could install a
// self-hosted provider onto.
//
// An org-owned provider's backend is reached over the edge tunnel, because the
// hub cannot dial into a tenant's cluster — see
// docs/byo-provider-edge-transport.md. That makes "is there a connected edge"
// a precondition of registration rather than something to discover afterwards,
// and this file is where the hub answers it.
//
// The hub reads the tenant's edges DIRECTLY with its kcp-admin client rather
// than through the edges provider. It already does this for every other
// workspace-scoped operation (EnsureProviderEdgeProxyGrant and friends), and
// going through the provider would make provider availability a precondition
// of *asking the question* — the opposite of failing fast.

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/faroshq/faros/pkg/kcppaths"
)

// kubernetesClusterGVR is the edges provider's cluster-kind edge. Only this
// kind can host a provider: a LinuxServer edge has no cluster DNS and no
// Kubernetes Service to route a provider backend to.
var kubernetesClusterGVR = schema.GroupVersionResource{
	Group: "edges.faros.sh", Version: "v1alpha1", Resource: "kubernetesclusters",
}

// EdgeInstallTarget is one candidate cluster for a self-hosted provider,
// flattened across the Org's team workspaces.
//
// Connected is the field that decides eligibility. An edge whose agent has
// never dialed in, or whose tunnel has dropped, is a cluster the hub cannot
// reach — installing a provider there produces a backend nothing can call.
type EdgeInstallTarget struct {
	// Workspace is the team workspace UUID the edge lives in.
	Workspace string
	// WorkspaceDisplayName is that workspace's human label, when it has one.
	WorkspaceDisplayName string
	// Name is the KubernetesCluster's metadata.name.
	Name string
	// Connected reports an active agent tunnel (status.connected).
	Connected bool
	// Phase is status.phase, e.g. "AwaitingAgent" or "Connected". Carried so
	// the UI can explain a not-Connected edge rather than just greying it out.
	Phase string
	// AgentVersion is the faros build running on the agent, when reported.
	AgentVersion string
}

// ListEdgeInstallTargets returns every KubernetesCluster edge across the Org's
// team workspaces, connected or not.
//
// Workspaces where the edges provider is not enabled contribute nothing and are
// not an error: the resource simply is not served there, which kcp reports as
// NotFound or a no-match. That is the ordinary state of a workspace that never
// onboarded an edge, so treating it as a failure would make the whole listing
// fail for the one Org most likely to be asking.
//
// Non-eligible edges are returned too, deliberately. "You have an edge but its
// agent is not connected" and "you have no edge at all" need different
// remedies, and the caller can only tell them apart if it sees both.
func (b *Bootstrapper) ListEdgeInstallTargets(ctx context.Context, orgUUID string) ([]EdgeInstallTarget, error) {
	if orgUUID == "" {
		return nil, fmt.Errorf("ListEdgeInstallTargets: orgUUID is required")
	}
	workspaces, err := b.listChildTeamWorkspaceRefs(ctx, orgUUID)
	if err != nil {
		return nil, err
	}

	out := []EdgeInstallTarget{}
	for _, ws := range workspaces {
		wsClient, err := dynamic.NewForConfig(configForPath(b.config, childWorkspacePath(orgUUID, ws.name)))
		if err != nil {
			return nil, fmt.Errorf("creating client for workspace %s: %w", ws.name, err)
		}
		list, err := wsClient.Resource(kubernetesClusterGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			if edgesNotServed(err) {
				continue
			}
			return nil, fmt.Errorf("listing edges in workspace %s: %w", ws.name, err)
		}
		for i := range list.Items {
			out = append(out, edgeInstallTarget(&list.Items[i], ws))
		}
	}
	// Connected first, then by workspace and name: the list is rendered as a
	// picker and the usable entries belong at the top.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Connected != out[j].Connected {
			return out[i].Connected
		}
		if out[i].Workspace != out[j].Workspace {
			return out[i].Workspace < out[j].Workspace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// GetEdgeInstallTarget resolves one edge by (workspace, name), returning
// (nil, nil) when it does not exist or the edges provider is not enabled in
// that workspace. Both cases mean the same thing to a caller checking a
// precondition — there is no such target — and distinguishing them in the
// error would leak whether a workspace has edges enabled to a caller who named
// a workspace they cannot otherwise see.
func (b *Bootstrapper) GetEdgeInstallTarget(ctx context.Context, orgUUID, wsUUID, name string) (*EdgeInstallTarget, error) {
	if orgUUID == "" || wsUUID == "" || name == "" {
		return nil, fmt.Errorf("GetEdgeInstallTarget: orgUUID, wsUUID and name are required")
	}
	wsClient, err := dynamic.NewForConfig(configForPath(b.config, childWorkspacePath(orgUUID, wsUUID)))
	if err != nil {
		return nil, fmt.Errorf("creating client for workspace %s: %w", wsUUID, err)
	}
	got, err := wsClient.Resource(kubernetesClusterGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if edgesNotServed(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting edge %s in workspace %s: %w", name, wsUUID, err)
	}
	display, err := b.GetWorkspaceDisplayName(ctx, orgUUID, wsUUID)
	if err != nil {
		// A missing label is cosmetic; the caller is checking reachability.
		display = ""
	}
	target := edgeInstallTarget(got, workspaceRef{name: wsUUID, displayName: display})
	return &target, nil
}

// workspaceRef is a team workspace's UUID plus its display label, read in one
// List so the edge listing does not re-Get each workspace for a string.
type workspaceRef struct {
	name        string
	displayName string
}

// listChildTeamWorkspaceRefs is ListChildTeamWorkspaces carrying display names.
func (b *Bootstrapper) listChildTeamWorkspaceRefs(ctx context.Context, orgUUID string) ([]workspaceRef, error) {
	orgClient, err := dynamic.NewForConfig(configForPath(b.config, kcppaths.OrgPath(orgUUID)))
	if err != nil {
		return nil, fmt.Errorf("creating org workspace client: %w", err)
	}
	list, err := orgClient.Resource(workspaceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing child Workspaces in org %s: %w", orgUUID, err)
	}
	out := make([]workspaceRef, 0, len(list.Items))
	for i := range list.Items {
		w := &list.Items[i]
		// Same exclusion ListChildTeamWorkspaces makes: the `providers`
		// container is not a team workspace and holds no edges.
		if w.GetName() == kcppaths.OrgProvidersWorkspaceName {
			continue
		}
		display, _, _ := unstructured.NestedString(w.Object, "metadata", "annotations", WorkspaceDisplayNameAnnotation)
		out = append(out, workspaceRef{name: w.GetName(), displayName: display})
	}
	return out, nil
}

func edgeInstallTarget(u *unstructured.Unstructured, ws workspaceRef) EdgeInstallTarget {
	connected, _, _ := unstructured.NestedBool(u.Object, "status", "connected")
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	agentVersion, _, _ := unstructured.NestedString(u.Object, "status", "agentVersion")
	return EdgeInstallTarget{
		Workspace:            ws.name,
		WorkspaceDisplayName: ws.displayName,
		Name:                 u.GetName(),
		Connected:            connected,
		Phase:                phase,
		AgentVersion:         agentVersion,
	}
}

// edgesNotServed reports whether err means "edges.faros.sh is not available in
// this workspace" rather than a real failure. kcp answers a request for an
// unbound API with a 404 on the resource path; a client that built its mapping
// from discovery answers with a no-match instead.
func edgesNotServed(err error) bool {
	return errors.IsNotFound(err) || meta.IsNoMatchError(err)
}
