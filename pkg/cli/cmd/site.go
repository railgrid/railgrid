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

package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

func newSiteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "site",
		Short: "Manage sites",
	}

	cmd.AddCommand(
		newSiteCreateCommand(),
		newSiteListCommand(),
		newSiteGetCommand(),
	)

	return cmd
}

func newSiteCreateCommand() *cobra.Command {
	var labels map[string]string
	var provider, region string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a site",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			site := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": kedgeclient.SiteGVR.Group + "/" + kedgeclient.SiteGVR.Version,
					"kind":       "Site",
					"metadata": map[string]interface{}{
						"name": name,
					},
					"spec": map[string]interface{}{
						"displayName": name,
					},
				},
			}

			if len(labels) > 0 {
				lbls := make(map[string]interface{}, len(labels))
				for k, v := range labels {
					lbls[k] = v
				}
				site.Object["metadata"].(map[string]interface{})["labels"] = lbls
			}
			if provider != "" {
				site.Object["spec"].(map[string]interface{})["provider"] = provider
			}
			if region != "" {
				site.Object["spec"].(map[string]interface{})["region"] = region
			}

			_, err = dynClient.Resource(kedgeclient.SiteGVR).Create(ctx, site, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("creating site %q: %w", name, err)
			}

			fmt.Printf("Site %q created.\n", name)
			return nil
		},
	}

	cmd.Flags().StringToStringVar(&labels, "labels", nil, "Labels for this site (key=value pairs)")
	cmd.Flags().StringVar(&provider, "provider", "", "Provider (e.g. aws, gcp, onprem, edge)")
	cmd.Flags().StringVar(&region, "region", "", "Region")

	return cmd
}

func newSiteListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all connected sites",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			list, err := dynClient.Resource(kedgeclient.SiteGVR).List(ctx, metav1.ListOptions{})
			if err != nil {
				return fmt.Errorf("listing sites: %w", err)
			}

			if len(list.Items) == 0 {
				fmt.Println("No sites found.")
				return nil
			}

			tw := newTabWriter(os.Stdout)
			printRow(tw, "NAME", "STATUS", "K8S VERSION", "PROVIDER", "REGION", "AGE")

			for _, item := range list.Items {
				phase := getNestedString(item, "status", "phase")
				k8sVersion := getNestedString(item, "status", "kubernetesVersion")
				provider := getNestedString(item, "spec", "provider")
				region := getNestedString(item, "spec", "region")
				age := formatAge(item.GetCreationTimestamp().Time)
				printRow(tw, item.GetName(), formatStringOrDash(phase), formatStringOrDash(k8sVersion),
					formatStringOrDash(provider), formatStringOrDash(region), age)
			}

			tw.Flush()
			return nil
		},
	}
}

func newSiteGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get [name]",
		Short: "Get site details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			site, err := dynClient.Resource(kedgeclient.SiteGVR).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("getting site %q: %w", name, err)
			}

			phase := getNestedString(*site, "status", "phase")
			k8sVersion := getNestedString(*site, "status", "kubernetesVersion")
			provider := getNestedString(*site, "spec", "provider")
			region := getNestedString(*site, "spec", "region")
			tunnelConnected, _, _ := unstructuredNestedBool(site.Object, "status", "tunnelConnected")
			lastHeartbeat := getNestedString(*site, "status", "lastHeartbeatTime")

			fmt.Printf("Name:              %s\n", site.GetName())
			fmt.Printf("Status:            %s\n", formatStringOrDash(phase))
			fmt.Printf("Provider:          %s\n", formatStringOrDash(provider))
			fmt.Printf("Region:            %s\n", formatStringOrDash(region))
			fmt.Printf("Kubernetes:        %s\n", formatStringOrDash(k8sVersion))
			fmt.Printf("Tunnel Connected:  %v\n", tunnelConnected)
			fmt.Printf("Last Heartbeat:    %s\n", formatStringOrDash(lastHeartbeat))
			fmt.Printf("Created:           %s\n", site.GetCreationTimestamp().Format("2006-01-02 15:04:05"))

			// Print labels if any
			labels := site.GetLabels()
			if len(labels) > 0 {
				fmt.Println("Labels:")
				for k, v := range labels {
					fmt.Printf("  %s=%s\n", k, v)
				}
			}

			return nil
		},
	}
}

func unstructuredNestedBool(obj map[string]interface{}, fields ...string) (bool, bool, error) {
	val, found, err := unstructuredNestedField(obj, fields...)
	if err != nil || !found {
		return false, found, err
	}
	b, ok := val.(bool)
	if !ok {
		return false, true, fmt.Errorf("expected bool, got %T", val)
	}
	return b, true, nil
}

func unstructuredNestedField(obj map[string]interface{}, fields ...string) (interface{}, bool, error) {
	var val interface{} = obj
	for _, field := range fields {
		m, ok := val.(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		val, ok = m[field]
		if !ok {
			return nil, false, nil
		}
	}
	return val, true, nil
}
