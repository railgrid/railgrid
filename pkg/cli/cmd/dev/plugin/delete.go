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

package plugin

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"

	"sigs.k8s.io/kind/pkg/cluster"
)

// RunDelete deletes the development environment
func (o *DevOptions) RunDelete() error {
	// Delete hub cluster
	if err := o.deleteCluster(o.HubClusterName); err != nil {
		return err
	}

	// Delete agent cluster
	if err := o.deleteCluster(o.AgentClusterName); err != nil {
		return err
	}

	// Also clean up the site kubeconfig if it exists
	siteKubeconfigPath := "site-kubeconfig"
	if err := os.Remove(siteKubeconfigPath); err != nil && !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Failed to remove site kubeconfig file %s: %v\n", siteKubeconfigPath, err)
	}

	return o.cleanupHostEntries()
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

func (o *DevOptions) cleanupHostEntries() error {
	if err := removeHostEntry("kedge.localhost"); err != nil {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Failed to remove host entry: %v\n", err)
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: Could not automatically remove host entry. Please run:\n")
		if runtime.GOOS == "windows" {
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Remove '127.0.0.1 kedge.localhost' line from C:\\Windows\\System32\\drivers\\etc\\hosts\n")
		} else {
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "  sudo sed -i '/127.0.0.1 kedge.localhost/d' /etc/hosts\n")
		}
	} else {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Removed host entry kedge.localhost\n")
	}
	return nil
}

func removeHostEntry(hostname string) error {
	hostsPath := getHostsPath()

	file, err := os.Open(hostsPath)
	if err != nil {
		return fmt.Errorf("failed to open hosts file: %w", err)
	}
	defer func() { _ = file.Close() }()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, hostname) || !strings.Contains(line, "127.0.0.1") {
			lines = append(lines, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read hosts file: %w", err)
	}

	content := strings.Join(lines, "\n")
	if err := os.WriteFile(hostsPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write hosts file: %w", err)
	}

	return nil
}
