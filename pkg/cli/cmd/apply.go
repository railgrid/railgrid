package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/yaml"
)

func newApplyCommand() *cobra.Command {
	var filename string

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply a resource from a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			if filename == "" {
				return fmt.Errorf("-f flag is required")
			}

			data, err := os.ReadFile(filename)
			if err != nil {
				return fmt.Errorf("reading file: %w", err)
			}

			// Parse YAML to unstructured
			obj := &unstructured.Unstructured{}
			if err := yaml.Unmarshal(data, &obj.Object); err != nil {
				return fmt.Errorf("parsing YAML: %w", err)
			}

			// Detect GVK
			gvk := obj.GroupVersionKind()
			if gvk.Kind == "" {
				return fmt.Errorf("kind is required in the resource")
			}

			gvr := gvkToGVR(gvk)

			// Create dynamic client
			dynClient, err := loadDynamicClient()
			if err != nil {
				return err
			}

			ctx := context.Background()
			namespace := obj.GetNamespace()
			if namespace == "" {
				namespace = "default"
			}

			var client = dynClient.Resource(gvr)

			// Try to get existing resource
			name := obj.GetName()
			var existing *unstructured.Unstructured
			if isNamespaced(gvk) {
				existing, err = client.Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			} else {
				existing, err = client.Get(ctx, name, metav1.GetOptions{})
			}

			if apierrors.IsNotFound(err) {
				// Create
				if isNamespaced(gvk) {
					_, err = client.Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
				} else {
					_, err = client.Create(ctx, obj, metav1.CreateOptions{})
				}
				if err != nil {
					return fmt.Errorf("creating %s/%s: %w", gvk.Kind, name, err)
				}
				fmt.Printf("%s/%s created\n", gvk.Kind, name)
			} else if err != nil {
				return fmt.Errorf("getting %s/%s: %w", gvk.Kind, name, err)
			} else {
				// Update
				obj.SetResourceVersion(existing.GetResourceVersion())
				if isNamespaced(gvk) {
					_, err = client.Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
				} else {
					_, err = client.Update(ctx, obj, metav1.UpdateOptions{})
				}
				if err != nil {
					return fmt.Errorf("updating %s/%s: %w", gvk.Kind, name, err)
				}
				fmt.Printf("%s/%s configured\n", gvk.Kind, name)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&filename, "filename", "f", "", "Path to resource YAML")

	return cmd
}

func gvkToGVR(gvk schema.GroupVersionKind) schema.GroupVersionResource {
	// Map kind to resource (lowercase plural)
	resource := ""
	switch gvk.Kind {
	case "VirtualWorkload":
		resource = "virtualworkloads"
	case "Site":
		resource = "sites"
	case "Placement":
		resource = "placements"
	case "User":
		resource = "users"
	default:
		// Simple pluralization
		resource = fmt.Sprintf("%ss", gvk.Kind)
	}
	return schema.GroupVersionResource{
		Group:    gvk.Group,
		Version:  gvk.Version,
		Resource: resource,
	}
}

func isNamespaced(gvk schema.GroupVersionKind) bool {
	switch gvk.Kind {
	case "Site", "User":
		return false
	default:
		return true
	}
}
