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

package plugin

import (
	"fmt"
	"os"

	"sigs.k8s.io/kind/pkg/cluster"
)

// RunDelete deletes the development environment
func (o *DevOptions) RunDelete() error {
	// Delete hub cluster
	if err := o.deleteCluster(o.HubClusterName); err != nil {
		return err
	}

	// Delete all agent cluster(s).
	for _, agentName := range o.agentClusterNames() {
		if err := o.deleteCluster(agentName); err != nil {
			return err
		}
	}

	// The exported dev CA belongs to the deleted cluster.
	if err := os.Remove(o.devCAFile()); err != nil && !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Failed to remove %s: %v\n", o.devCAFile(), err)
	}

	// Also clean up the edge kubeconfig if it exists
	edgeKubeconfigPath := "edge-kubeconfig"
	if err := os.Remove(edgeKubeconfigPath); err != nil && !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Failed to remove edge kubeconfig file %s: %v\n", edgeKubeconfigPath, err)
	}

	return nil
}

func (o *DevOptions) deleteCluster(clusterName string) error {
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Deleting kind cluster %s\n", clusterName)
	provider := cluster.NewProvider()

	err := provider.Delete(clusterName, "")
	if err != nil {
		return err
	}

	kubeconfigPath := fmt.Sprintf("%s.kubeconfig", clusterName)
	if err := os.Remove(kubeconfigPath); err != nil && !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Failed to remove kubeconfig file %s: %v\n", kubeconfigPath, err)
	} else {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Removed kubeconfig file %s\n", kubeconfigPath)
	}

	return nil
}
