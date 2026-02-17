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

// Package confighelpers provides bootstrap helpers inspired by
// github.com/kcp-dev/kcp/config/helpers. We maintain a local copy to avoid
// pulling in the full kcp server module which has dependency conflicts with
// our k8s versions.
package confighelpers

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// TransformFileFunc transforms a resource file before being applied to the cluster.
type TransformFileFunc func(bs []byte) ([]byte, error)

// Option allows to customize the bootstrap process.
type Option struct {
	// TransformFile is a function that transforms a resource file before being applied to the cluster.
	TransformFile TransformFileFunc
}

// ReplaceOption creates an Option that performs string replacement on resource files.
func ReplaceOption(pairs ...string) Option {
	return Option{
		TransformFile: func(bs []byte) ([]byte, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("odd number of arguments: %v", pairs)
			}
			for i := 0; i < len(pairs); i += 2 {
				bs = bytes.ReplaceAll(bs, []byte(pairs[i]), []byte(pairs[i+1]))
			}
			return bs, nil
		},
	}
}

const annotationCreateOnly = "bootstrap.kcp.io/create-only"

// Bootstrap reads all YAML files from an embed.FS and applies them to the
// cluster using a discovery-based REST mapper. It retries continuously until
// all resources are successfully applied or the context is cancelled.
func Bootstrap(ctx context.Context, discoveryClient discovery.DiscoveryInterface, dynamicClient dynamic.Interface, fs embed.FS, opts ...Option) error {
	cache := memory.NewMemCacheClient(discoveryClient)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	transformers := make([]TransformFileFunc, 0, len(opts))
	for _, opt := range opts {
		if opt.TransformFile != nil {
			transformers = append(transformers, opt.TransformFile)
		}
	}

	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		if err := createResourcesFromFS(ctx, dynamicClient, mapper, fs, transformers...); err != nil {
			klog.FromContext(ctx).V(2).Info("Failed to bootstrap resources, retrying", "err", err)
			cache.Invalidate()
			return false, nil
		}
		return true, nil
	})
}

// createResourcesFromFS reads all YAML files from an embed.FS and applies them.
func createResourcesFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, fs embed.FS, transformers ...TransformFileFunc) error {
	files, err := fs.ReadDir(".")
	if err != nil {
		return err
	}

	var errs []error
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if err := createResourceFromFS(ctx, client, mapper, f.Name(), fs, transformers...); err != nil {
			errs = append(errs, err)
		}
	}
	return utilerrors.NewAggregate(errs)
}

// createResourceFromFS reads a single YAML file (possibly multi-document),
// applies transformers, and upserts each resource.
func createResourceFromFS(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, filename string, fs embed.FS, transformers ...TransformFileFunc) error {
	raw, err := fs.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", filename, err)
	}
	if len(raw) == 0 {
		return nil
	}

	d := kubeyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	var errs []error
	for i := 1; ; i++ {
		doc, err := d.Read()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		for _, transformer := range transformers {
			doc, err = transformer(doc)
			if err != nil {
				return err
			}
		}

		if err := upsertResource(ctx, client, mapper, doc); err != nil {
			errs = append(errs, fmt.Errorf("failed to create resource %s doc %d: %w", filename, i, err))
		}
	}
	return utilerrors.NewAggregate(errs)
}

// upsertResource deserializes a single YAML document and creates or updates the resource.
func upsertResource(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, raw []byte) error {
	logger := klog.FromContext(ctx)

	// Convert YAML to JSON, then to unstructured.
	jsonData, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return fmt.Errorf("converting YAML to JSON: %w", err)
	}

	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(jsonData, &u.Object); err != nil {
		return fmt.Errorf("unmarshaling JSON: %w", err)
	}

	gvk := u.GroupVersionKind()
	m, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("could not get REST mapping for %s: %w", gvk, err)
	}

	_, err = client.Resource(m.Resource).Namespace(u.GetNamespace()).Create(ctx, u, metav1.CreateOptions{})
	if err == nil {
		logger.V(2).Info("Created resource", "kind", gvk.Kind, "name", u.GetName())
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}

	// Already exists — check if we should skip update.
	existing, err := client.Resource(m.Resource).Namespace(u.GetNamespace()).Get(ctx, u.GetName(), metav1.GetOptions{})
	if err != nil {
		return err
	}

	if _, exists := existing.GetAnnotations()[annotationCreateOnly]; exists {
		logger.V(4).Info("Skipping update of create-only resource", "kind", gvk.Kind, "name", u.GetName())
		return nil
	}

	u.SetResourceVersion(existing.GetResourceVersion())
	if _, err = client.Resource(m.Resource).Namespace(u.GetNamespace()).Update(ctx, u, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("could not update %s %s: %w", gvk.Kind, u.GetName(), err)
	}
	logger.V(2).Info("Updated resource", "kind", gvk.Kind, "name", u.GetName())
	return nil
}
