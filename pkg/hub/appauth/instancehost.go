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

package appauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/tenantaccess"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/browsersession"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
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

// NewKCPIdentityAppResolver returns a generic descriptor resolver for
// Railgrid Auth. It reads only unstructured instance metadata, so appauth does
// not depend on the Infrastructure or App Studio API packages.
//
// The app audience is derived from a live App Studio Project and its matching
// spec environment binding. The Instance's mutable Railgrid Auth values must
// match that descriptor, and its UID and published origin are checked again
// when the one-use code is exchanged.
func NewKCPIdentityAppResolver(kcpConfig *rest.Config) (IdentityAppResolver, error) {
	if kcpConfig == nil {
		return nil, fmt.Errorf("appauth: kcp config is required")
	}
	return func(ctx context.Context, ref InstanceRef) (IdentityApp, error) {
		if ref.Group != "infrastructure.railgrid.ai" || ref.Resource != "instances" {
			return IdentityApp{}, ErrInstanceNotPublished
		}
		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, ref.Cluster)

		version, err := preferredVersion(ctx, cfg, ref.Group)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return IdentityApp{}, ErrInstanceNotPublished
			}
			return IdentityApp{}, fmt.Errorf("discovering %s in cluster %s: %w", ref.Group, ref.Cluster, err)
		}
		client, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return IdentityApp{}, fmt.Errorf("creating client for cluster %s: %w", ref.Cluster, err)
		}
		gvr := schema.GroupVersionResource{Group: ref.Group, Version: version, Resource: ref.Resource}
		inst, err := client.Resource(gvr).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return IdentityApp{}, ErrInstanceNotPublished
			}
			return IdentityApp{}, fmt.Errorf("reading %s/%s %s in cluster %s: %w", ref.Group, ref.Resource, ref.Name, ref.Cluster, err)
		}

		projectName, projectUID, err := identityProjectOwner(inst)
		if err != nil {
			return IdentityApp{}, ErrInstanceNotPublished
		}
		projectVersion, err := preferredVersion(ctx, cfg, "ai.railgrid.ai")
		if err != nil {
			if apierrors.IsNotFound(err) {
				return IdentityApp{}, ErrInstanceNotPublished
			}
			return IdentityApp{}, fmt.Errorf("discovering ai.railgrid.ai in cluster %s: %w", ref.Cluster, err)
		}
		project, err := client.Resource(schema.GroupVersionResource{
			Group: "ai.railgrid.ai", Version: projectVersion, Resource: "projects",
		}).Get(ctx, projectName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return IdentityApp{}, ErrInstanceNotPublished
			}
			return IdentityApp{}, fmt.Errorf("reading App Studio Project %s in cluster %s: %w", projectName, ref.Cluster, err)
		}

		templateName := nestedTrimmedString(project, "spec", "template", "name")
		if templateName == "" {
			return IdentityApp{}, ErrInstanceNotPublished
		}
		template, err := client.Resource(schema.GroupVersionResource{
			Group: ref.Group, Version: version, Resource: "templates",
		}).Get(ctx, templateName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return IdentityApp{}, ErrInstanceNotPublished
			}
			return IdentityApp{}, fmt.Errorf("reading Infrastructure Template %s in cluster %s: %w", templateName, ref.Cluster, err)
		}

		app, err := identityAppFromProjectBinding(ref, inst, project, projectName, projectUID, template)
		if err != nil {
			return IdentityApp{}, ErrInstanceNotPublished
		}
		return app, nil
	}, nil
}

// identityProjectOwner returns the App Studio Project coordinates stamped by
// the Project controller. The owner reference only locates the Project; the
// caller must still validate the live Project UID and its spec binding before
// treating the Instance as an App Studio application.
func identityProjectOwner(inst *unstructured.Unstructured) (string, string, error) {
	if inst == nil {
		return "", "", ErrInstanceNotPublished
	}
	var name, uid string
	projectOwners := 0
	for _, owner := range inst.GetOwnerReferences() {
		if owner.Kind != "Project" {
			continue
		}
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err != nil || gv.Group != "ai.railgrid.ai" || gv.Version != "v1alpha1" {
			return "", "", ErrInstanceNotPublished
		}
		projectOwners++
		if owner.Controller == nil || !*owner.Controller || strings.TrimSpace(owner.Name) == "" || strings.TrimSpace(string(owner.UID)) == "" {
			return "", "", ErrInstanceNotPublished
		}
		name, uid = strings.TrimSpace(owner.Name), string(owner.UID)
	}
	if projectOwners != 1 {
		return "", "", ErrInstanceNotPublished
	}
	if label := strings.TrimSpace(inst.GetLabels()["app-studio.railgrid.ai/project"]); label != "" && label != name {
		return "", "", ErrInstanceNotPublished
	}
	return name, uid, nil
}

// identityAppFromProjectBinding validates the persisted App Studio ownership
// chain before returning a descriptor. AppID and Environment come from the
// live Project UID and the matching spec environment binding; mutable Instance
// values are only accepted when they exactly match that authority.
func identityAppFromProjectBinding(ref InstanceRef, inst, project *unstructured.Unstructured, ownerName, ownerUID string, template *unstructured.Unstructured) (IdentityApp, error) {
	invalid := func() (IdentityApp, error) { return IdentityApp{}, ErrInstanceNotPublished }
	if ref.Group != "infrastructure.railgrid.ai" || ref.Resource != "instances" || inst == nil || project == nil || template == nil {
		return invalid()
	}
	if inst.GetKind() != "Instance" || project.GetKind() != "Project" || template.GetKind() != "Template" {
		return invalid()
	}
	if inst.GetDeletionTimestamp() != nil || project.GetName() == "" || project.GetName() != ownerName || project.GetUID() == "" || string(project.GetUID()) != ownerUID || project.GetDeletionTimestamp() != nil {
		return invalid()
	}
	projectVersion, err := schema.ParseGroupVersion(project.GetAPIVersion())
	if err != nil || projectVersion.Group != "ai.railgrid.ai" {
		return invalid()
	}
	instanceVersion, err := schema.ParseGroupVersion(inst.GetAPIVersion())
	if err != nil || instanceVersion.Group != ref.Group {
		return invalid()
	}
	if template.GetName() != nestedTrimmedString(project, "spec", "template", "name") || template.GetDeletionTimestamp() != nil {
		return invalid()
	}
	if templateVersion, err := schema.ParseGroupVersion(template.GetAPIVersion()); err != nil || templateVersion.Group != ref.Group {
		return invalid()
	}

	enabled, found, err := unstructured.NestedBool(project.Object, "spec", "authentication", "enabled")
	if err != nil || !found || !enabled {
		return invalid()
	}
	templateName := nestedTrimmedString(project, "spec", "template", "name")
	if templateName == "" || nestedTrimmedString(inst, "spec", "template") != templateName || !templateSupportsIdentity(template) {
		return invalid()
	}

	environment, ok := identityBindingEnvironment(ref, inst, project, instanceVersion.Version)
	if !ok {
		return invalid()
	}
	appID := string(project.GetUID()) + "/" + environment
	if !validAppID(appID) {
		return invalid()
	}

	runtimeEnabled, enabledFound, enabledErr := unstructured.NestedBool(inst.Object, "spec", "values", "railgridAuth")
	runtimeAppID, appIDFound, appIDErr := unstructured.NestedString(inst.Object, "spec", "values", "railgridAuthAppID")
	runtimeEnvironment, environmentFound, environmentErr := unstructured.NestedString(inst.Object, "spec", "values", "railgridAuthEnvironment")
	runtimeUID := nestedTrimmedString(inst, "spec", "values", "railgridAuthInstanceUID")
	instanceUID := string(inst.GetUID())
	if enabledErr != nil || appIDErr != nil || environmentErr != nil || !enabledFound || !runtimeEnabled || !appIDFound || !environmentFound ||
		strings.TrimSpace(runtimeAppID) != appID || strings.TrimSpace(runtimeEnvironment) != environment || instanceUID == "" || runtimeUID != instanceUID {
		return invalid()
	}

	origin := nestedTrimmedString(inst, "status", "url")
	if origin == "" {
		host := nestedTrimmedString(inst, "spec", "values", "expose", "fqdn")
		if host == "" {
			host = nestedTrimmedString(inst, "status", "host")
		}
		if host != "" {
			origin = "https://" + host
		}
	}
	canonical, err := canonicalPublishedOrigin(origin)
	if err != nil {
		return invalid()
	}
	return IdentityApp{
		InstanceUID:     instanceUID,
		AppID:           appID,
		Environment:     environment,
		PublishedOrigin: canonical,
		Enabled:         true,
	}, nil
}

// identityBindingEnvironment finds the unique persisted Project spec binding
// that targets this flattened Infrastructure Instance. Status is deliberately
// ignored: only spec grants the Project ownership relationship.
func identityBindingEnvironment(ref InstanceRef, inst, project *unstructured.Unstructured, instanceVersion string) (string, bool) {
	environments, found, err := unstructured.NestedSlice(project.Object, "spec", "environments")
	if err != nil || !found {
		return "", false
	}
	instanceName := strings.TrimSpace(inst.GetName())
	if instanceName == "" || instanceName != ref.Name {
		return "", false
	}
	matches := 0
	matchedEnvironment := ""
	for _, rawEnvironment := range environments {
		env, ok := rawEnvironment.(map[string]any)
		if !ok {
			return "", false
		}
		environment, ok := env["name"].(string)
		if !ok || strings.TrimSpace(environment) == "" {
			return "", false
		}
		rawBindings, found := env["bindings"]
		if !found {
			continue
		}
		bindings, ok := rawBindings.([]any)
		if !ok {
			return "", false
		}
		for _, rawBinding := range bindings {
			binding, ok := rawBinding.(map[string]any)
			if !ok {
				return "", false
			}
			if stringField(binding, "provider") != "app-studio" || stringField(binding, "kind") != "providerResource" || stringField(binding, "name") == "" {
				continue
			}
			resourceRef, ok := binding["resourceRef"].(map[string]any)
			if !ok {
				continue
			}
			apiVersion := stringField(resourceRef, "apiVersion")
			gv, err := schema.ParseGroupVersion(apiVersion)
			if err != nil || gv.Group != ref.Group || gv.Version != instanceVersion {
				continue
			}
			if stringField(resourceRef, "resource") != ref.Resource || stringField(resourceRef, "kind") != inst.GetKind() {
				continue
			}
			resolvedName := stringField(resourceRef, "name")
			if resolvedName == "" {
				if values, ok := binding["values"].(map[string]any); ok {
					resolvedName = stringField(values, "name")
				}
			}
			if resolvedName == "" {
				resolvedName = strings.TrimSpace(project.GetName()) + "-" + stringField(binding, "name")
			}
			if resolvedName != instanceName {
				continue
			}
			matches++
			matchedEnvironment = strings.TrimSpace(environment)
		}
	}
	if matches != 1 || matchedEnvironment == "" {
		return "", false
	}
	return matchedEnvironment, true
}

func templateSupportsIdentity(template *unstructured.Unstructured) bool {
	properties, found, err := unstructured.NestedMap(template.Object, "spec", "schema", "properties")
	if err != nil || !found {
		return false
	}
	for name, expectedType := range map[string]string{
		"railgridAuth":            "boolean",
		"railgridAuthAppID":       "string",
		"railgridAuthEnvironment": "string",
		"railgridAuthInstanceUID": "string",
	} {
		property, ok := properties[name].(map[string]any)
		if !ok || stringField(property, "type") != expectedType {
			return false
		}
	}
	components, found, err := unstructured.NestedMap(template.Object, "spec", "development", "components")
	if err != nil || !found {
		return false
	}
	apiComponent, ok := components["api"].(map[string]any)
	return ok && strings.TrimSpace(stringField(apiComponent, "workspacePath")) != ""
}

func stringField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

// NewKCPIdentityWorkspaceMemberResolver builds the development-environment
// membership check. It resolves the tenant path from the workspace's logical
// cluster and checks the hub's UserMembershipIndex, including the existing
// rule that an organization admin is an implicit workspace admin.
func NewKCPIdentityWorkspaceMemberResolver(kcpConfig *rest.Config, memberships tenant.MembershipLookup) (IdentityWorkspaceMember, error) {
	if kcpConfig == nil || memberships == nil {
		return nil, fmt.Errorf("appauth: kcp config and membership lookup are required")
	}
	return func(ctx context.Context, identity browsersession.Identity, ref InstanceRef) (bool, error) {
		if identity.UserID == "" {
			return false, nil
		}
		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, ref.Cluster)
		dyn, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return false, fmt.Errorf("creating workspace membership client: %w", err)
		}
		path, err := tenantaccess.WorkspacePath(ctx, dyn)
		if err != nil {
			return false, fmt.Errorf("resolving workspace path: %w", err)
		}
		orgUUID, workspaceUUID, ok := tenantaccess.ParseTenantPath(path)
		if !ok || orgUUID == "" || workspaceUUID == "" {
			return false, fmt.Errorf("workspace %q is not an organization child workspace", ref.Cluster)
		}
		index, err := memberships.GetUserMembershipIndex(ctx, identity.UserID)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("reading workspace membership index: %w", err)
		}
		for _, entry := range index.Spec.Entries {
			if entry.OrgUUID != orgUUID || entry.SoftDeletedAt != nil {
				continue
			}
			if entry.WorkspaceUUID == workspaceUUID ||
				(entry.WorkspaceUUID == "" && entry.Role == "admin") {
				return true, nil
			}
		}
		return false, nil
	}, nil
}

// canonicalPublishedOrigin accepts an HTTPS app URL, discards its path, and
// returns the canonical origin. An empty or non-HTTPS value fails closed.
func canonicalPublishedOrigin(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInstanceNotPublished
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid published app URL")
	}
	return canonicalHTTPSOrigin(u)
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
