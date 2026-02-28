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

package builder

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
)

var edgeGVR = schema.GroupVersionResource{
	Group:    "kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "edges",
}

// markEdgeDisconnected patches an Edge's status to Connected=false,
// Phase=Disconnected on the hub.  It is called by the agent-proxy-v2 handler
// when the agent's revdial tunnel closes so that the hub's view of edge
// connectivity is accurate even when the agent process dies without sending a
// clean disconnect heartbeat.
//
// It is best-effort: errors are logged but not propagated.
func (p *virtualWorkspaces) markEdgeDisconnected(ctx context.Context, cluster, name string) {
	if p.kcpConfig == nil {
		return
	}

	cfg := rest.CopyConfig(p.kcpConfig)
	cfg.Host = kcp.AppendClusterPath(cfg.Host, cluster)

	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		p.logger.Error(err, "markEdgeDisconnected: failed to create dynamic client",
			"cluster", cluster, "edge", name)
		return
	}

	patch := []byte(`{"status":{"connected":false,"phase":"Disconnected"}}`)
	_, err = dynClient.Resource(edgeGVR).Patch(ctx, name,
		types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	if err != nil {
		p.logger.Error(err, "markEdgeDisconnected: failed to patch edge status",
			"cluster", cluster, "edge", name)
		return
	}

	p.logger.Info("Edge marked Disconnected on tunnel close",
		"cluster", cluster, "edge", name)
}
