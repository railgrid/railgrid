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

package appauth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/faroshq/faros/pkg/apiurl"
)

// NewKCPInstanceHostResolver returns an InstanceHostResolver that reads the
// referenced instance in its tenant workspace through the hub's admin kcp
// config and reports the host the provider stamped onto it.
//
// The host is taken from spec.values.expose.fqdn — the field the instance
// controller stamps on every exposed instance, i.e. on exactly the set of
// instances whose access gate can ever start this flow — falling back to
// status.host for resources that mirror it there instead. A missing object
// and a missing host are deliberately the same ErrInstanceNotPublished:
// authorize has already SAR-checked the caller against these coordinates, and
// collapsing the two keeps the error page from confirming which one it was.
//
// The instance's group version is discovered per call (one GET of /apis/<group>
// before the instance read). Sign-ins are per-user-per-app-session rare and
// already cost a SubjectAccessReview, so two extra reads beat carrying a
// discovery cache that can go stale across provider upgrades.
func NewKCPInstanceHostResolver(kcpConfig *rest.Config) (InstanceHostResolver, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("appauth: kcp config is required")
	}
	return func(ctx context.Context, ref InstanceRef) (string, error) {
		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, ref.Cluster)

		version, err := preferredVersion(ctx, cfg, ref.Group)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// The tenant workspace does not serve this group at all.
				return "", ErrInstanceNotPublished
			}
			return "", fmt.Errorf("discovering %s in cluster %s: %w", ref.Group, ref.Cluster, err)
		}

		client, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return "", fmt.Errorf("creating client for cluster %s: %w", ref.Cluster, err)
		}
		gvr := schema.GroupVersionResource{Group: ref.Group, Version: version, Resource: ref.Resource}
		inst, err := client.Resource(gvr).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return "", ErrInstanceNotPublished
			}
			return "", fmt.Errorf("reading %s/%s %s in cluster %s: %w", ref.Group, ref.Resource, ref.Name, ref.Cluster, err)
		}

		host := nestedTrimmedString(inst, "spec", "values", "expose", "fqdn")
		if host == "" {
			host = nestedTrimmedString(inst, "status", "host")
		}
		if host == "" {
			return "", ErrInstanceNotPublished
		}
		return host, nil
	}, nil
}

// preferredVersion resolves the tenant cluster's preferred served version of
// group with a single GET of /apis/<group>.
func preferredVersion(ctx context.Context, cfg *rest.Config, group string) (string, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("creating discovery client: %w", err)
	}
	raw, err := dc.RESTClient().Get().AbsPath("/apis/" + group).Do(ctx).Raw()
	if err != nil {
		return "", err
	}
	var apiGroup metav1.APIGroup
	if err := json.Unmarshal(raw, &apiGroup); err != nil {
		return "", fmt.Errorf("decoding /apis/%s: %w", group, err)
	}
	version := apiGroup.PreferredVersion.Version
	if version == "" && len(apiGroup.Versions) > 0 {
		version = apiGroup.Versions[0].Version
	}
	if version == "" {
		return "", fmt.Errorf("group %s serves no versions", group)
	}
	return version, nil
}

func nestedTrimmedString(obj *unstructured.Unstructured, fields ...string) string {
	v, _, _ := unstructured.NestedString(obj.Object, fields...)
	return strings.TrimSpace(v)
}
