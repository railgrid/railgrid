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

package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	farosv1alpha1 "github.com/faroshq/faros/apis/faros/v1alpha1"
	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
	"github.com/faroshq/faros/pkg/apiurl"
	"github.com/faroshq/faros/pkg/kcppaths"
)

// roleNamePrefix prefixes the generated per-server ClusterRole name:
// faros:mcpserver:<MCPServer name>.
const roleNamePrefix = "faros:mcpserver:"

var (
	// readVerbs is granted on every bound provider resource.
	readVerbs = []string{"get", "list", "watch"}
	// writeVerbs is granted on every bound provider resource unless the server
	// is spec.readOnly.
	writeVerbs = []string{"create", "update", "patch", "delete"}
)

// dataPlaneGrant describes RBAC coordinates a provider data plane checks with
// a SubjectAccessReview as the caller instead of serving through the API
// server. They exist purely as authorization coordinates, so the tenant's
// APIBindings never list them and they have to be spelled out here.
type dataPlaneGrant struct {
	// verbs are extra verbs granted on the bound resources themselves. They
	// gate access to a data plane rather than a mutation of the object, so
	// they survive readOnly: without them the read-only tools cannot reach
	// the data plane at all.
	verbs []string
	// subresources are virtual subresources granted with "create". They
	// invoke something (a shell, a job) and are dropped for readOnly servers.
	subresources []string
}

// dataPlaneGrants is keyed by API group of the bound resources.
var dataPlaneGrants = map[string]dataPlaneGrant{
	// The edges tunnel authorizes verb "proxy" on the edge object before
	// serving its k8s/ssh/mcp subresources (providers/edges/internal/tunnel).
	"edges.faros.sh": {verbs: []string{"proxy"}},
	// The infrastructure data plane authorizes "create" on
	// <instance resource>/exec before running a command in a dev instance
	// (providers/infrastructure/dataplane/authorizer.go).
	"infrastructure.faros.sh": {subresources: []string{"exec"}},
}

// ActionGrant is one provider action from the platform catalog, expressed as
// the RBAC coordinate a provider checks before invoking it: "create" on
// <Resource>/<Name> in Group (e.g. tables/query_table).
type ActionGrant struct {
	Group    string
	Resource string
	// Name is the action name without its version suffix — the subresource
	// the provider's SelfSubjectAccessReview names.
	Name string
	// ReadOnly mirrors the catalog's declaration; read-only actions stay
	// granted on readOnly servers.
	ReadOnly bool
}

// ActionGrantSource lists the action grants declared by platform providers.
type ActionGrantSource func(ctx context.Context) ([]ActionGrant, error)

// buildRules derives the ClusterRole rules for one server: every resource the
// tenant has bound gets read (and, unless readOnly, write) verbs; data-plane
// coordinates and catalog actions are added for bound resources only; plus the
// read-only kcp/authz plumbing every tool path needs. Output is deterministic
// so reconcile-time comparison is stable.
func buildRules(bound []apisv1alpha2.BoundAPIResource, actions []ActionGrant, readOnly bool) []rbacv1.PolicyRule {
	byGroup := map[string]map[string]struct{}{}
	for _, b := range bound {
		if b.Resource == "" {
			continue
		}
		if byGroup[b.Group] == nil {
			byGroup[b.Group] = map[string]struct{}{}
		}
		byGroup[b.Group][b.Resource] = struct{}{}
	}

	// Action coordinates, keyed by group, only for resources that are bound.
	actionSubs := map[string]map[string]struct{}{}
	for _, a := range actions {
		if a.Name == "" || a.Resource == "" {
			continue
		}
		if _, ok := byGroup[a.Group][a.Resource]; !ok {
			continue
		}
		if readOnly && !a.ReadOnly {
			continue
		}
		if actionSubs[a.Group] == nil {
			actionSubs[a.Group] = map[string]struct{}{}
		}
		actionSubs[a.Group][a.Resource+"/"+a.Name] = struct{}{}
	}

	groups := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)

	verbs := append([]string{}, readVerbs...)
	if !readOnly {
		verbs = append(verbs, writeVerbs...)
	}

	var rules []rbacv1.PolicyRule
	for _, g := range groups {
		resources := sortedKeys(byGroup[g])
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: resources, Verbs: verbs})

		if dp, ok := dataPlaneGrants[g]; ok {
			if len(dp.verbs) > 0 {
				rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: resources, Verbs: dp.verbs})
			}
			if !readOnly && len(dp.subresources) > 0 {
				subs := make([]string, 0, len(resources)*len(dp.subresources))
				for _, r := range resources {
					for _, s := range dp.subresources {
						subs = append(subs, r+"/"+s)
					}
				}
				rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: subs, Verbs: []string{"create"}})
			}
		}
		if subs := actionSubs[g]; len(subs) > 0 {
			rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: sortedKeys(subs), Verbs: []string{"create"}})
		}
	}

	// Path lookup: tools resolve the workspace path from the LogicalCluster.
	rules = append(rules, rbacv1.PolicyRule{
		APIGroups: []string{"core.kcp.io"}, Resources: []string{"logicalclusters"}, Verbs: readVerbs,
	})
	// Provider data planes and action gates run a SelfSubjectAccessReview as
	// the caller; the review only ever answers for the token itself.
	rules = append(rules, rbacv1.PolicyRule{
		APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews"}, Verbs: []string{"create"},
	})
	return rules
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// listBoundResources collects status.boundResources across the tenant's
// APIBindings. Bindings still being bound contribute nothing yet; the next
// reconcile picks them up.
func listBoundResources(ctx context.Context, kcp kcpclientset.Interface) ([]apisv1alpha2.BoundAPIResource, error) {
	list, err := kcp.ApisV1alpha2().APIBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []apisv1alpha2.BoundAPIResource
	for i := range list.Items {
		out = append(out, list.Items[i].Status.BoundResources...)
	}
	return out, nil
}

// ensureMCPRBAC converges the generated ClusterRole and the ClusterRoleBinding
// pointing at it. RoleRef is immutable, so a binding left over from the
// cluster-admin era (or any other role) is deleted and recreated.
func ensureMCPRBAC(ctx context.Context, cs kubernetes.Interface, srv *farosv1alpha1.MCPServer, owner metav1.OwnerReference, saName string, rules []rbacv1.PolicyRule) error {
	roleName := roleNamePrefix + srv.Name
	if rules == nil {
		rules = []rbacv1.PolicyRule{}
	}

	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, OwnerReferences: []metav1.OwnerReference{owner}},
		Rules:      rules,
	}
	existing, err := cs.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := cs.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ensuring ClusterRole %s: %w", roleName, err)
		}
	case err != nil:
		return fmt.Errorf("getting ClusterRole %s: %w", roleName, err)
	case !equality.Semantic.DeepEqual(existing.Rules, rules):
		existing.Rules = rules
		if _, err := cs.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating ClusterRole %s: %w", roleName, err)
		}
	}

	roleRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: roleName}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: saName, OwnerReferences: []metav1.OwnerReference{owner}},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: mcpIdentityNamespace}},
		RoleRef:    roleRef,
	}
	got, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, saName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("getting ClusterRoleBinding %s: %w", saName, err)
	case got.RoleRef == roleRef:
		return nil
	default:
		klog.FromContext(ctx).Info("replacing MCPServer ClusterRoleBinding with a different roleRef", "binding", saName, "from", got.RoleRef.Name, "to", roleName)
		if err := cs.RbacV1().ClusterRoleBindings().Delete(ctx, saName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting stale ClusterRoleBinding %s: %w", saName, err)
		}
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensuring ClusterRoleBinding %s: %w", saName, err)
	}
	return nil
}

var catalogEntryGVR = schema.GroupVersionResource{
	Group: providersv1alpha1.GroupName, Version: providersv1alpha1.Version, Resource: "catalogentries",
}

// catalogActionGrants returns an ActionGrantSource that reads the platform
// providers' CatalogEntries from the system providers workspace. Only platform
// providers federate into the aggregate (see the enumerator in server.go), so
// org-owned catalogs are not consulted.
func catalogActionGrants(kcpConfig *rest.Config) ActionGrantSource {
	return func(ctx context.Context) ([]ActionGrant, error) {
		if kcpConfig == nil {
			return nil, nil
		}
		cfg := rest.CopyConfig(kcpConfig)
		cfg.Host = apiurl.KCPClusterURL(kcpConfig.Host, kcppaths.SystemProviders)
		dyn, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("building system providers client: %w", err)
		}
		list, err := dyn.Resource(catalogEntryGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing CatalogEntries in %s: %w", kcppaths.SystemProviders, err)
		}
		var out []ActionGrant
		for i := range list.Items {
			var entry providersv1alpha1.CatalogEntry
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &entry); err != nil {
				return nil, fmt.Errorf("decoding CatalogEntry %s: %w", list.Items[i].GetName(), err)
			}
			out = append(out, actionGrantsFromSpec(entry.Spec.Actions)...)
		}
		return out, nil
	}
}

// actionGrantsFromSpec maps catalog action declarations to RBAC coordinates.
// Action IDs are "<name>/<version>"; the provider reviews the unversioned
// name as the subresource.
func actionGrantsFromSpec(actions []providersv1alpha1.ProviderActionSpec) []ActionGrant {
	out := make([]ActionGrant, 0, len(actions))
	for _, a := range actions {
		name, _, _ := strings.Cut(strings.TrimSpace(a.ID), "/")
		if name == "" || a.BoundResource.Resource == "" {
			continue
		}
		gv, err := schema.ParseGroupVersion(a.BoundResource.APIVersion)
		if err != nil {
			continue
		}
		out = append(out, ActionGrant{
			Group:    gv.Group,
			Resource: a.BoundResource.Resource,
			Name:     name,
			ReadOnly: a.ReadOnly,
		})
	}
	return out
}
