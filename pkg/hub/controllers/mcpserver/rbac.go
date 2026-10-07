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

package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/railgrid/provider-sdk/dataplane"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	railgridv1alpha1 "github.com/railgrid/railgrid/apis/railgrid/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/mcpaggregate"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// roleNamePrefix prefixes the generated per-server ClusterRole name:
// railgrid:mcpserver:<MCPServer name>.
const roleNamePrefix = "railgrid:mcpserver:"

var (
	// readVerbs is granted on every bound provider resource.
	readVerbs = []string{"get", "list", "watch"}
	// writeVerbs is granted on every bound provider resource unless the server
	// is spec.readOnly.
	writeVerbs = []string{"create", "update", "patch", "delete"}
)

// dataPlaneGrant describes RBAC coordinates a provider data plane checks with
// a SubjectAccessReview as the caller instead of serving through the API
// server. They are declared on the provider export, while APIBindings only
// report the parent resource; this type records the server-owned allowlist of
// non-read-only coordinates an MCPServer may receive.
type dataPlaneGrant struct {
	// providerName and APIExport identity pin this exception to the public
	// platform provider that owns the reviewed data plane. A different export
	// may serve the same group/resource coordinate, but it cannot inherit the
	// curated writable capability.
	providerName  string
	apiExportPath string
	apiExportName string
	// resources, when non-empty, restricts the grant to those resources
	// (intersected with what the tenant actually bound). Empty means every
	// bound resource in the group, which is only correct when the data plane
	// really does serve all of them. Anything narrower must be listed, or the
	// grant widens itself as new resources join the group's APIExport.
	resources []string
	// subresources are the provider's kcp custom subresources (data-plane
	// verbs) granted with dataplane.SubresourceVerbs: the coordinate is the
	// capability, and kcp maps the HTTP method a verb uses onto the RBAC
	// verb. They invoke something (a shell, a job) and are dropped for
	// readOnly servers.
	subresources []string
}

// dataPlaneGrants is keyed by API group and is an explicit allowlist of
// non-read-only custom verbs. Every coordinate is granted with the verbs from
// dataplane.SubresourceVerbs after its declaration and exact APIExport identity
// are checked. The owning provider runs a SelfSubjectAccessReview for exactly
// {resource}/{verb} as the caller before serving it (provider-sdk/dataplane.Gate).
var dataPlaneGrants = map[string][]dataPlaneGrant{
	// providers/edges/internal/tunnel/grammar.go dataPlaneVerbs. The tunnel
	// serves kubectl (including delete and exec), an SSH shell and MCP under
	// these verbs, so none of them survives readOnly. ticket is for browsers,
	// and agent-token, ssh-credentials, runner-auth and runner-token are the
	// EDGE AGENT's own credential plane — each one hands over or hands back a
	// credential for the machine the caller has proved it is. An MCP token has
	// proved nothing of the sort, so none of them is ever granted here.
	"edges.railgrid.ai": {
		{providerName: "edges", apiExportPath: "root:railgrid:providers:edges", apiExportName: "edges.providers.railgrid.ai", resources: []string{"kubernetesclusters"}, subresources: []string{"k8s", "ssh", "mcp"}},
		{providerName: "edges", apiExportPath: "root:railgrid:providers:edges", apiExportName: "edges.providers.railgrid.ai", resources: []string{"linuxservers"}, subresources: []string{"k8s", "ssh"}},
		{providerName: "edges", apiExportPath: "root:railgrid:providers:edges", apiExportName: "edges.providers.railgrid.ai", resources: []string{"services"}, subresources: []string{"proxy", "mcp"}},
	},
	// providers/agents/api/dataplane.go routes(). The agents data plane serves
	// verbs on agents, runs, connections, schedules, triggers and
	// modelcredentials, but only the model-credential probes belong to an MCP
	// token: they answer "does this credential work?" and cost nothing but a
	// round-trip to the model endpoint. Everything else on that group either
	// spends the tenant's money (agents/chat, agents/run), resolves an
	// approval, or reaches a messaging platform, and none of those is a side
	// effect an AI client should be able to cause by itself.
	//
	// modelcredentials is named explicitly so binding a new resource in the
	// group never widens this to <newresource>/test.
	"agents.railgrid.ai": {{providerName: "agents", apiExportPath: "root:railgrid:providers:agents", apiExportName: "agents.railgrid.ai", resources: []string{"modelcredentials"}, subresources: []string{"test", "discover"}}},
	// The infrastructure data plane serves instances/{verb} as kcp custom
	// subresources (providers/infrastructure/dataplane/handler.go); exec is
	// the one an MCP token may hold. instances is the only resource the data
	// plane serves, so exec is granted on instances alone and never on
	// templates.
	"infrastructure.railgrid.ai": {{providerName: "infrastructure", apiExportPath: "root:railgrid:providers:infrastructure", apiExportName: "infrastructure.providers.railgrid.ai", resources: []string{"instances"}, subresources: []string{"exec"}}},
}

// privilegedResources are bound resources a generated MCPServer role NEVER
// grants, in any verb, read or write. The rule above widens itself as a
// provider's APIExport grows, which is right for ordinary provider objects and
// wrong for the few whose creation IS the privilege escalation.
//
// It is currently empty. The entry it used to hold, edges.railgrid.ai/addons,
// went away with the Addon kind; the same privilege now lives on the edge kinds
// themselves and is denied by privilegedWriteResources below, because an edge
// still has to be READABLE for the edge tools to work at all.
var privilegedResources = map[string]map[string]bool{}

// privilegedWriteResources are bound resources a generated MCPServer role may
// READ but never WRITE, whatever the server's own read-only setting.
//
// The edge host kinds are here because `spec.harness` is on them: updating an
// edge can ask a specific machine to start running a coding harness, which is
// arbitrary code execution on somebody's laptop or build box. That decision
// belongs to a human with workspace admin rights (whose wildcard still covers
// it) and to the machine's owner, who controls whether a harness is installed
// and which account it runs as. Handing it to every MCPServer token in the
// workspace — which is what "the tenant bound this resource" would otherwise
// mean — would let an AI client turn a machine into a code-execution host as a
// side effect of a tool call.
//
// Reads stay granted: listing edges and their status is most of what the edge
// tools do, and dropping the resource outright (what the Addon entry did) would
// break them. See docs/edge-harness.md.
var privilegedWriteResources = map[string]map[string]bool{
	"edges.railgrid.ai": {"linuxservers": true, "macosservers": true, "kubernetesclusters": true},
}

// dropPrivilegedResources removes the never-granted resources of one group.
func dropPrivilegedResources(group string, resources []string) []string {
	denied := privilegedResources[group]
	if len(denied) == 0 {
		return resources
	}
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		if !denied[r] {
			out = append(out, r)
		}
	}
	return out
}

// splitPrivilegedWrites divides resources into those a role may write and those
// it may only read.
func splitPrivilegedWrites(group string, resources []string) (writable, readOnly []string) {
	denied := privilegedWriteResources[group]
	if len(denied) == 0 {
		return resources, nil
	}
	for _, r := range resources {
		if denied[r] {
			readOnly = append(readOnly, r)
		} else {
			writable = append(writable, r)
		}
	}
	return writable, readOnly
}

// SubresourceGrant is one provider-declared action or custom verb, expressed
// as the RBAC coordinate a provider checks before invoking it: the
// provider-sdk/dataplane subresource verbs on <Resource>/<Name> in Group.
// ExportPath and ExportName identify the APIExport whose declaration owns the
// coordinate, so a different export serving the same group/resource cannot
// inherit it accidentally.
type SubresourceGrant struct {
	Group    string
	Resource string
	Name     string
	ReadOnly bool
	// AllowPrivilegedWrite is set only for coordinates in the existing
	// server-owned data-plane allowlist. Those deliberate capabilities remain
	// available on writable MCPServers even when the parent resource itself is
	// read-only to ordinary actions.
	AllowPrivilegedWrite bool
	APIExportPath        string
	APIExportName        string
}

// BoundResource carries the identity of the APIExport a tenant bound along
// with the bound resource. The APIBinding's status alone has only
// group/resource/schema identity; its export reference is needed to match a
// provider declaration without crossing an export boundary.
type BoundResource struct {
	apisv1alpha2.BoundAPIResource
	APIExportPath string
	APIExportName string
}

// buildRules derives the ClusterRole rules for one server: every resource the
// tenant has bound gets read (and, unless readOnly, write) verbs; provider
// subresources are added only when their exact APIExport and resource are
// bound; plus the read-only kcp/authz plumbing every tool path needs. Output is
// deterministic so reconcile-time comparison is stable.
func buildRules(bound []BoundResource, grants []SubresourceGrant, readOnly bool) []rbacv1.PolicyRule {
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

	// Provider subresource coordinates, keyed by group, only for resources
	// bound from the exact APIExport that declared them.
	subresourceSubs := map[string]map[string]struct{}{}
	for _, grant := range grants {
		if grant.Name == "" || grant.Resource == "" || grant.APIExportName == "" || grant.APIExportPath == "" {
			continue
		}
		if _, ok := byGroup[grant.Group][grant.Resource]; !ok {
			continue
		}
		if !boundToExport(bound, grant) {
			continue
		}
		// A provider subresource must not become a back door into a resource
		// the generated role refuses outright, nor a write into one it may
		// only read. Invocation is a write unless the provider explicitly
		// declared the coordinate read-only.
		if privilegedResources[grant.Group][grant.Resource] {
			continue
		}
		if neverMCPSubresources[grant.Group][grant.Resource][grant.Name] {
			continue
		}
		if grant.ReadOnly && neverReadOnlyMCPSubresources[grant.Group][grant.Resource][grant.Name] {
			continue
		}
		if privilegedWriteResources[grant.Group][grant.Resource] && !grant.ReadOnly && !grant.AllowPrivilegedWrite {
			continue
		}
		if readOnly && !grant.ReadOnly {
			continue
		}
		if subresourceSubs[grant.Group] == nil {
			subresourceSubs[grant.Group] = map[string]struct{}{}
		}
		subresourceSubs[grant.Group][grant.Resource+"/"+grant.Name] = struct{}{}
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
		// Privileged resources are dropped before anything else keys off the
		// list, so they cannot come back through a data-plane or action grant.
		resources := dropPrivilegedResources(g, sortedKeys(byGroup[g]))
		if len(resources) == 0 {
			continue
		}
		// A resource whose write IS the privilege escalation gets reads only,
		// in its own rule, so the grant cannot be widened by the server's
		// read-only setting being off.
		granted, readOnlyResources := splitPrivilegedWrites(g, resources)
		if len(granted) > 0 {
			rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: granted, Verbs: verbs})
		}
		if len(readOnlyResources) > 0 {
			rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: readOnlyResources, Verbs: append([]string{}, readVerbs...)})
		}

		if subs := subresourceSubs[g]; len(subs) > 0 {
			rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: sortedKeys(subs), Verbs: append([]string(nil), dataplane.SubresourceVerbs...)})
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

func boundToExport(bound []BoundResource, grant SubresourceGrant) bool {
	for _, b := range bound {
		if b.Group == grant.Group && b.Resource == grant.Resource &&
			b.APIExportPath == grant.APIExportPath && b.APIExportName == grant.APIExportName {
			return true
		}
	}
	return false
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
// nondeleting, Bound APIBindings and records each binding's APIExport identity.
// Bindings still being bound contribute nothing yet; the next reconcile picks
// them up.
func listBoundResources(ctx context.Context, kcp kcpclientset.Interface, tenantPath string) ([]BoundResource, error) {
	list, err := kcp.ApisV1alpha2().APIBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []BoundResource
	for i := range list.Items {
		binding := &list.Items[i]
		if binding.DeletionTimestamp != nil || binding.Status.Phase != apisv1alpha2.APIBindingPhaseBound {
			continue
		}
		var exportName, exportPath string
		if export := binding.Spec.Reference.Export; export != nil {
			exportName = export.Name
			exportPath = export.Path
			if exportPath == "" {
				exportPath = tenantPath
			}
		}
		for _, resource := range binding.Status.BoundResources {
			out = append(out, BoundResource{
				BoundAPIResource: resource,
				APIExportPath:    exportPath,
				APIExportName:    exportName,
			})
		}
	}
	return out, nil
}

// ensureMCPRBAC converges the generated ClusterRole and the ClusterRoleBinding
// pointing at it. RoleRef is immutable, so a binding left over from the
// cluster-admin era (or any other role) is deleted and recreated.
func ensureMCPRBAC(ctx context.Context, cs kubernetes.Interface, srv *railgridv1alpha1.MCPServer, owner metav1.OwnerReference, saName string, rules []rbacv1.PolicyRule) error {
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
	default:
		// Ownership is reconciled alongside the rules: a role whose owner
		// reference drifted or was stripped outlives its MCPServer, because
		// nothing else ever deletes it.
		changed := false
		if !equality.Semantic.DeepEqual(existing.Rules, rules) {
			existing.Rules = rules
			changed = true
		}
		if !equality.Semantic.DeepEqual(existing.OwnerReferences, role.OwnerReferences) {
			existing.OwnerReferences = role.OwnerReferences
			changed = true
		}
		if changed {
			if _, err := cs.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("updating ClusterRole %s: %w", roleName, err)
			}
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
		// RoleRef already points at the generated role, so the binding stays
		// and its mutable fields are converged in place. Subjects matter for
		// security — an extra subject left on the binding would hold the
		// MCPServer's permissions, and a wrong one silently breaks the token —
		// and the owner reference is what garbage-collects the binding.
		changed := false
		if !equality.Semantic.DeepEqual(got.Subjects, crb.Subjects) {
			got.Subjects = crb.Subjects
			changed = true
		}
		if !equality.Semantic.DeepEqual(got.OwnerReferences, crb.OwnerReferences) {
			got.OwnerReferences = crb.OwnerReferences
			changed = true
		}
		if changed {
			if _, err := cs.RbacV1().ClusterRoleBindings().Update(ctx, got, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("updating ClusterRoleBinding %s: %w", saName, err)
			}
		}
		return nil
	default:
		// RoleRef is immutable, so a binding pointing anywhere else (the
		// cluster-admin era, say) can only be replaced.
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

// actionNamePattern is the documented shape of an action's name, which is the
// subresource half of the {resource}/{action} coordinate. It mirrors the
// kubebuilder Pattern on ProviderAction.Name character for character; keep the
// two in step. The CRD rejects new objects that break it, but pattern validation
// never retro-validates objects that predate the marker, so the parser enforces
// the shape itself rather than trusting what is in storage.
var actionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

var customVerbNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// neverMCPSubresources lists credential-plane coordinates MCPServer tokens
// never receive automatically, even when a provider labels one read-only.
// Read-only describes resource mutation, not the authority of a returned
// credential. Edge agents use these routes to rotate or hand over machine
// credentials; Code's token actions can return a PAT/OAuth credential without
// narrowing its upstream permissions. Neither belongs in a generated MCP role.
// Legacy edge names remain denied so restoring a handler cannot widen the role.
var neverMCPSubresources = map[string]map[string]map[string]bool{
	"code.railgrid.ai": {
		"repositories": {"mint-clone-token": true},
		"connections":  {"mint-registry-token": true},
	},
	"edges.railgrid.ai": {
		"kubernetesclusters": {"agent-token": true, "runner-auth": true, "runner-token": true},
		"linuxservers":       {"agent-token": true, "ssh-credentials": true, "runner-auth": true, "runner-token": true},
		"macosservers":       {"agent-token": true, "runner-auth": true, "runner-token": true},
		"services":           {"ticket": true},
	},
}

// neverReadOnlyMCPSubresources preserves the old read-only role boundary for
// invocation routes that can open shells, proxy arbitrary traffic, or reach an
// edge MCP server. These verbs remain eligible for writable MCPServers only
// through the curated dataPlaneGrants entries, even if a declaration later
// labels one read-only by mistake.
var neverReadOnlyMCPSubresources = map[string]map[string]map[string]bool{
	"edges.railgrid.ai": {
		"kubernetesclusters": {"k8s": true, "ssh": true, "mcp": true},
		"linuxservers":       {"k8s": true, "ssh": true},
		"services":           {"proxy": true, "mcp": true},
	},
	"infrastructure.railgrid.ai": {
		"instances": {"exec": true},
	},
}

// registrySubresourceGrants projects provider declarations visible to the
// MCPServer's ServiceAccount. A tenant path is required so org-owned records
// shadow same-named platform providers before those org records are excluded:
// the SA cannot mint the delegated token required to federate an org provider.
//
// Every declared action is eligible subject to the usual ReadOnly checks in
// buildRules. Custom verbs are included only when their declaration explicitly
// says ReadOnly; non-read-only verbs remain limited to the curated
// dataPlaneGrants allowlist below. Provider exports need a known path, name and
// actual API group identity before any grant from them is considered.
func registrySubresourceGrants(reg *providers.Registry, tenantPath string) []SubresourceGrant {
	if reg == nil {
		return nil
	}
	orgUUID, _, ok := mcpaggregate.TenantFromPath(tenantPath)
	if !ok {
		return nil
	}

	grants := map[SubresourceGrant]struct{}{}
	for _, provider := range reg.ListForOrg(orgUUID) {
		if provider.OrgUUID != "" || provider.APIExportPath == "" || provider.APIExportName == "" ||
			provider.Export == nil || len(provider.APIGroups) == 0 {
			continue
		}
		actualGroups := make(map[string]struct{}, len(provider.APIGroups))
		for _, group := range provider.APIGroups {
			if group != "" {
				actualGroups[group] = struct{}{}
			}
		}

		for _, resource := range provider.Export.Resources {
			if resource.Name == "" {
				continue
			}
			gv, err := schema.ParseGroupVersion(resource.APIVersion)
			if err != nil || gv.Group == "" {
				continue
			}
			if _, ok := actualGroups[gv.Group]; !ok {
				continue
			}
			for _, action := range resource.Actions {
				name := strings.TrimSpace(action.Name)
				if name != action.Name || !actionNamePattern.MatchString(name) {
					continue
				}
				grant := SubresourceGrant{
					Group: gv.Group, Resource: resource.Name, Name: name,
					ReadOnly: action.ReadOnly, APIExportPath: provider.APIExportPath, APIExportName: provider.APIExportName,
				}
				if !neverMCPSubresources[grant.Group][grant.Resource][grant.Name] &&
					(!grant.ReadOnly || !neverReadOnlyMCPSubresources[grant.Group][grant.Resource][grant.Name]) {
					grants[grant] = struct{}{}
				}
			}
			for _, verb := range resource.Verbs {
				name := strings.TrimSpace(verb.Name)
				if !verb.ReadOnly || name != verb.Name || !customVerbNamePattern.MatchString(name) {
					continue
				}
				grant := SubresourceGrant{
					Group: gv.Group, Resource: resource.Name, Name: name,
					ReadOnly: true, APIExportPath: provider.APIExportPath, APIExportName: provider.APIExportName,
				}
				if !neverMCPSubresources[grant.Group][grant.Resource][grant.Name] &&
					!neverReadOnlyMCPSubresources[grant.Group][grant.Resource][grant.Name] {
					grants[grant] = struct{}{}
				}
			}
			// These non-read-only data-plane capabilities are deliberately
			// curated. Require the provider's own export to declare each
			// coordinate before granting it, and keep it tied to that export.
			for _, dp := range dataPlaneGrants[gv.Group] {
				if provider.Name != dp.providerName || provider.APIExportPath != dp.apiExportPath || provider.APIExportName != dp.apiExportName {
					continue
				}
				if len(dp.resources) > 0 && !slices.Contains(dp.resources, resource.Name) {
					continue
				}
				for _, name := range dp.subresources {
					if !declaresCustomVerb(resource.Verbs, name) || neverMCPSubresources[gv.Group][resource.Name][name] {
						continue
					}
					grants[SubresourceGrant{
						Group: gv.Group, Resource: resource.Name, Name: name, AllowPrivilegedWrite: true,
						APIExportPath: provider.APIExportPath, APIExportName: provider.APIExportName,
					}] = struct{}{}
				}
			}
		}
	}

	out := make([]SubresourceGrant, 0, len(grants))
	for grant := range grants {
		out = append(out, grant)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].APIExportPath != out[j].APIExportPath {
			return out[i].APIExportPath < out[j].APIExportPath
		}
		if out[i].APIExportName != out[j].APIExportName {
			return out[i].APIExportName < out[j].APIExportName
		}
		if out[i].ReadOnly != out[j].ReadOnly {
			return !out[i].ReadOnly
		}
		return !out[i].AllowPrivilegedWrite && out[j].AllowPrivilegedWrite
	})
	return out
}

func declaresCustomVerb(verbs []providersv1alpha1.ProviderVerb, name string) bool {
	for _, verb := range verbs {
		if verb.Name == name {
			return true
		}
	}
	return false
}
