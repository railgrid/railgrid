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
	"k8s.io/client-go/dynamic"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

func newGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get [resource]",
		Short: "Get resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resource := args[0]
			ctx := context.Background()

			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			switch resource {
			case "virtualworkloads", "vw":
				return listVirtualWorkloads(ctx, dynClient)
			case "placements":
				return listPlacements(ctx, dynClient)
			case "edges":
				return listEdges(ctx, dynClient)
			default:
				return fmt.Errorf("unknown resource type: %s", resource)
			}
		},
	}

	return cmd
}

func listVirtualWorkloads(ctx context.Context, dynClient dynamic.Interface) error {
	list, err := dynClient.Resource(kedgeclient.VirtualWorkloadGVR).Namespace("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing virtualworkloads: %w", err)
	}

	tw := newTabWriter(os.Stdout)
	printRow(tw, "NAME", "PHASE", "READY", "AVAILABLE", "AGE")

	for _, item := range list.Items {
		phase := getNestedString(item, "status", "phase")
		readyReplicas := getNestedInt(item, "status", "readyReplicas")
		availableReplicas := getNestedInt(item, "status", "availableReplicas")
		age := formatAge(item.GetCreationTimestamp().Time)
		printRow(tw, item.GetName(), formatStringOrDash(phase),
			fmt.Sprintf("%d", readyReplicas),
			fmt.Sprintf("%d", availableReplicas),
			age)
	}

	_ = tw.Flush()
	return nil
}

func listPlacements(ctx context.Context, dynClient dynamic.Interface) error {
	list, err := dynClient.Resource(kedgeclient.PlacementGVR).Namespace("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing placements: %w", err)
	}

	tw := newTabWriter(os.Stdout)
	printRow(tw, "NAME", "SITE", "PHASE", "READY", "AGE")

	for _, item := range list.Items {
		siteName := getNestedString(item, "spec", "siteName")
		phase := getNestedString(item, "status", "phase")
		readyReplicas := getNestedInt(item, "status", "readyReplicas")
		age := formatAge(item.GetCreationTimestamp().Time)
		printRow(tw, item.GetName(), siteName, phase, fmt.Sprintf("%d", readyReplicas), age)
	}

	_ = tw.Flush()
	return nil
}

func listEdges(ctx context.Context, dynClient dynamic.Interface) error {
	list, err := dynClient.Resource(kedgeclient.EdgeGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing edges: %w", err)
	}

	tw := newTabWriter(os.Stdout)
	printRow(tw, "NAME", "TYPE", "PHASE", "CONNECTED", "AGE")

	for _, item := range list.Items {
		edgeType := getNestedString(item, "spec", "type")
		phase := getNestedString(item, "status", "phase")
		connected, _, _ := unstructuredNestedBool(item.Object, "status", "connected")
		age := formatAge(item.GetCreationTimestamp().Time)
		printRow(tw, item.GetName(), formatStringOrDash(edgeType), formatStringOrDash(phase),
			fmt.Sprintf("%v", connected), age)
	}

	_ = tw.Flush()
	return nil
}

func getNestedString(u unstructured.Unstructured, fields ...string) string {
	val, found, err := unstructured.NestedString(u.Object, fields...)
	if err != nil || !found {
		return ""
	}
	return val
}

func getNestedInt(u unstructured.Unstructured, fields ...string) int64 {
	val, found, err := unstructured.NestedInt64(u.Object, fields...)
	if err != nil || !found {
		return 0
	}
	return val
}
