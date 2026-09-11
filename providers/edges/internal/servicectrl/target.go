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

package servicectrl

import (
	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
)

// Connectable kinds accepted by Service.edgeRef. An empty kind is the Go-side
// representation of the CRD's LinuxServer default.
const (
	linuxServerKind       = "LinuxServer"
	macOSServerKind       = "MacOSServer"
	kubernetesClusterKind = "KubernetesCluster"
)

// isKube reports whether a Service lives on a KubernetesCluster edge.
func isKube(es *edgesv1alpha1.Service) bool {
	return es.Spec.EdgeRef.Kind == kubernetesClusterKind
}

// connResource returns the tunnel ConnManager resource segment for the edge
// kind a Service references. It must match the keys the tunnel package uses.
func connResource(es *edgesv1alpha1.Service) string {
	switch es.Spec.EdgeRef.Kind {
	case "", linuxServerKind:
		return edgesv1alpha1.LinuxServerResource
	case macOSServerKind:
		return edgesv1alpha1.MacOSServerResource
	case kubernetesClusterKind:
		return edgesv1alpha1.KubernetesClusterResource
	default:
		// Admission enforces the enum, but controllers must also fail closed
		// when handed an object written before the rule or through a bypass.
		return ""
	}
}

func supportedEdgeKind(es *edgesv1alpha1.Service) bool {
	return connResource(es) != ""
}

// targetHost is the agent-side address of the service. It must stay in lockstep
// with the tunnel's serviceView.targetHost (service_proxy.go) so the validation
// probe reaches the same host the proxy does:
//   - spec.host wins on either edge kind — dial the address directly (loopback,
//     or a device on the edge's LAN like a UniFi console at 192.168.1.1);
//   - otherwise cluster DNS ({name}.{namespace}.svc) for a KubernetesCluster
//     edge with a targetRef;
//   - otherwise the host loopback (a LinuxServer or MacOSServer edge's own
//     agent host).
func targetHost(es *edgesv1alpha1.Service) string {
	if es.Spec.Host != "" {
		return es.Spec.Host
	}
	if isKube(es) && es.Spec.TargetRef != nil {
		return es.Spec.TargetRef.Name + "." + es.Spec.TargetRef.Namespace + ".svc"
	}
	return "127.0.0.1"
}
