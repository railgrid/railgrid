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
			case "edges":
				return listEdges(ctx, dynClient)
			case "workloads", "vw":
				return listWorkloads(ctx, dynClient)
			case "placements":
				return listPlacements(ctx, dynClient)
			default:
				return fmt.Errorf("unknown resource type: %s (try: edges, workloads, placements)", resource)
			}
		},
	}

	return cmd
}

func listEdges(ctx context.Context, dynClient dynamic.Interface) error {
	items, err := listAllEdges(ctx, dynClient)
	if err != nil {
		return err
	}

	tw := newTabWriter(os.Stdout)
	printRow(tw, "NAME", "TYPE", "PHASE", "CONNECTED", "AGE")

	for _, item := range items {
		edgeType := "kubernetes"
		if item.GetKind() == "LinuxServer" {
			edgeType = "server"
		}
		phase := getNestedString(item, "status", "phase")
		connected, _, _ := unstructuredNestedBool(item.Object, "status", "connected")
		age := formatAge(item.GetCreationTimestamp().Time)
		printRow(tw, item.GetName(), formatStringOrDash(edgeType), formatStringOrDash(phase),
			fmt.Sprintf("%v", connected), age)
	}

	_ = tw.Flush()
	return nil
}

func listWorkloads(ctx context.Context, dyn dynamic.Interface) error {
	list, err := dyn.Resource(kedgeclient.WorkloadGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing workloads: %w", err)
	}
	tw := newTabWriter(os.Stdout)
	printRow(tw, "NAME", "IMAGE", "PHASE", "READY", "AGE")
	for _, item := range list.Items {
		image := getNestedString(item, "spec", "simple", "image")
		phase := getNestedString(item, "status", "phase")
		ready := getNestedInt(item, "status", "readyReplicas")
		replicas := getNestedInt(item, "spec", "replicas")
		age := formatAge(item.GetCreationTimestamp().Time)
		printRow(tw, item.GetName(), formatStringOrDash(image), formatStringOrDash(phase),
			fmt.Sprintf("%d/%d", ready, replicas), age)
	}
	_ = tw.Flush()
	return nil
}

func listPlacements(ctx context.Context, dyn dynamic.Interface) error {
	list, err := dyn.Resource(kedgeclient.PlacementGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing placements: %w", err)
	}
	tw := newTabWriter(os.Stdout)
	printRow(tw, "NAME", "EDGE", "PHASE", "READY", "AGE")
	for _, item := range list.Items {
		edge := getNestedString(item, "spec", "edgeName")
		phase := getNestedString(item, "status", "phase")
		ready := getNestedInt(item, "status", "readyReplicas")
		age := formatAge(item.GetCreationTimestamp().Time)
		printRow(tw, item.GetName(), formatStringOrDash(edge), formatStringOrDash(phase),
			fmt.Sprintf("%d", ready), age)
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
