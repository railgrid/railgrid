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

package serviceaccounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	// WorkloadIdentityBootstrapAudience is the audience expected by the
	// provider-owned bootstrap attestor. It is never used for the minted Railgrid
	// capability.
	WorkloadIdentityBootstrapAudience = "railgrid-provider-actions-bootstrap"

	// WorkloadIdentityTokenAudience is the audience requested for the minted
	// runtime capability. KCP's embedded and deployed API servers issue and
	// validate ServiceAccount tokens for this issuer audience; using the
	// legacy proxy-only "railgrid" audience would make the token fail at the
	// provider's tenant API before the action reached its backend.
	WorkloadIdentityTokenAudience = "https://kcp.default.svc"

	// WorkloadIdentityTokenTTL is deliberately short. The TokenRequest API may
	// cap it further according to cluster policy; it must never be increased by
	// this package.
	WorkloadIdentityTokenTTL = 10 * time.Minute

	// LabelWorkloadIdentity marks service accounts created for the provider
	// action runtime. These accounts intentionally do not carry
	// LabelRailgridSA, so the ordinary user-managed service-account CRUD surface
	// cannot list, patch, or rotate them.
	LabelWorkloadIdentity = "railgrid.ai/workload-identity"

	// AnnotationWorkloadIdentityScope is a compact, non-secret audit marker.
	// The token itself is never persisted in an annotation or Secret.
	AnnotationWorkloadIdentityScope = "railgrid.ai/workload-identity-scope"

	// AnnotationWorkloadIdentityTenantPath binds a workload ServiceAccount to
	// the child workspace in which it was issued. The tenant resolver checks
	// this marker after an online TokenReview, preventing a valid Railgrid token
	// from being replayed with another tenant's selection headers.
	AnnotationWorkloadIdentityTenantPath = "railgrid.ai/workload-identity-tenant"

	// The remaining annotations carry the exact project identity tuple used to
	// derive the deterministic workload ServiceAccount name. They are retained
	// separately from the compact scope marker so an online verifier can load
	// and compare the live Project before authorizing an action invocation.
	AnnotationWorkloadIdentityProject     = "railgrid.ai/workload-identity-project"
	AnnotationWorkloadIdentityProjectUID  = "railgrid.ai/workload-identity-project-uid"
	AnnotationWorkloadIdentityEnvironment = "railgrid.ai/workload-identity-environment"
	AnnotationWorkloadIdentityInstance    = "railgrid.ai/workload-identity-instance"

	workloadIdentityNamePrefix = "railgrid-wi-"
	workloadIdentityRoleSuffix = "-access"

	// The project resource is owned by App Studio. Provider-resource rules are
	// derived from the verified Project environment and are never hard-coded.
	workloadProjectGroup    = "ai.railgrid.ai"
	workloadProjectResource = "projects"
)

// WorkloadIdentityScope is the verified identity tuple supplied by the
// bootstrap-attestation exchange. All fields participate in the deterministic
// ServiceAccount name, so a deleted-and-recreated project cannot inherit an
// old runtime identity when its UID changes.
type WorkloadIdentityScope struct {
	TenantPath        string
	Project           string
	ProjectUID        string
	Environment       string
	Instance          string
	ProviderResources []ProviderResourceScope
}

// ProviderResourceScope is one exact provider reference from the verified
// Project environment. The resulting ClusterRole contains GET on this GVR and
// resource name, plus — for each granted action — CREATE on the action's
// virtual subresource ({resource}/{action}). The subresource is an RBAC
// coordinate only (nothing serves it); the owning provider enforces it with a
// caller-scoped SSAR, the same pattern as the infrastructure data-plane exec
// verb. Materializing a Project action grant IS writing this rule; revoking
// it removes the rule.
type ProviderResourceScope struct {
	APIVersion string
	Kind       string
	Resource   string
	Name       string
	// Actions are the non-revoked action names (version-less: version pinning
	// lives in the grant's schema digest, not in RBAC) granted on this exact
	// resource.
	Actions []string
}

// WorkloadServiceAccountName returns the stable, DNS-safe ServiceAccount name
// for a verified identity tuple. It is intentionally hash-only: tenant and
// project names can contain information that should not be reflected in
// cluster-wide RBAC object names.
func WorkloadServiceAccountName(scope WorkloadIdentityScope) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		scope.TenantPath,
		scope.Project,
		scope.ProjectUID,
		scope.Environment,
		scope.Instance,
	}, "\x00")))
	return workloadIdentityNamePrefix + hex.EncodeToString(sum[:20])
}

// WorkloadIdentityRoleName returns the deterministic ClusterRole and
// ClusterRoleBinding name paired with a workload ServiceAccount.
func WorkloadIdentityRoleName(serviceAccountName string) string {
	return serviceAccountName + workloadIdentityRoleSuffix
}

// WorkloadIdentityShape returns the minter shape for a verified workload
// scope: its deterministic name, its identity annotations, and the GET-only
// Project/provider-resource rules derived from the Project environment.
//
// It is exported because the hub identity service drives BOTH attestation
// modes through one minter (scoped_identity.go): this function is the
// workload mode's shape, and nothing else computes it.
func WorkloadIdentityShape(scope WorkloadIdentityScope) ScopedIdentityShape {
	name := WorkloadServiceAccountName(scope)
	return ScopedIdentityShape{
		ServiceAccount: name,
		Annotations:    workloadIdentityAnnotations(scope),
		// The five identity annotations are immutable for the account's
		// lifetime; the scope marker is not, because it tracks the CURRENT
		// grants and must follow an added, revoked or reactivated integration
		// rather than bricking the runtime identity.
		ImmutableAnnotations: []string{
			AnnotationWorkloadIdentityTenantPath,
			AnnotationWorkloadIdentityProject,
			AnnotationWorkloadIdentityProjectUID,
			AnnotationWorkloadIdentityEnvironment,
			AnnotationWorkloadIdentityInstance,
		},
		Rules:    workloadIdentityRules(scope),
		TokenTTL: WorkloadIdentityTokenTTL,
	}
}

// There is deliberately no Manager.EnsureWorkloadIdentity any more. Minting a
// workload identity without RECORDING it is exactly what left workload
// identities uncollected, so the only way to obtain one is through
// pkg/hub/identity's EnsureWorkload, which writes the ScopedIdentity first.

// VerifyWorkloadServiceAccount performs online TokenReview validation in the
// selected child workspace and verifies that the reviewed ServiceAccount is a
// hub-managed workload identity bound to expectedTenantPath. JWT signatures
// and claims are intentionally never trusted offline.
// It accepts workload identities only: a delegated user identity is rejected,
// because this entry point has no proof key with which to establish one.
func VerifyWorkloadServiceAccount(ctx context.Context, cfg *rest.Config, token, expectedTenantPath string) (string, error) {
	username, _, err := VerifyWorkloadServiceAccountDetails(ctx, cfg, token, expectedTenantPath, nil)
	return username, err
}

// VerifyWorkloadServiceAccountDetails is the online workload identity
// verifier used by both tenant resolution and action-grant authorization. It
// returns the reviewed ServiceAccount only after TokenReview, audience,
// subject, label, tenant, and required identity annotations have all passed.
//
// proofKeys is consulted only for delegated user identities, whose annotations
// are tenant-writable and therefore mean nothing without the hub's keyed proof
// (see delegated_user_proof.go). A nil source rejects them outright; that is
// the right answer for any caller that is not the delegated-token path.
func VerifyWorkloadServiceAccountDetails(ctx context.Context, cfg *rest.Config, token, expectedTenantPath string, proofKeys ProofKeySource) (string, *corev1.ServiceAccount, error) {
	if cfg == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(expectedTenantPath) == "" {
		return "", nil, fmt.Errorf("workload token, tenant path, and workspace config are required")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("building workload TokenReview client: %w", err)
	}
	review, err := client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{
		Token: token, Audiences: []string{WorkloadIdentityTokenAudience},
	}}, metav1.CreateOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("reviewing workload token: %w", err)
	}
	if !review.Status.Authenticated || !containsString(review.Status.Audiences, WorkloadIdentityTokenAudience) {
		return "", nil, fmt.Errorf("workload token is not authenticated for audience %q", WorkloadIdentityTokenAudience)
	}
	parts := strings.Split(review.Status.User.Username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" || parts[2] != Namespace || parts[3] == "" {
		return "", nil, fmt.Errorf("workload token subject is not a default-namespace ServiceAccount")
	}
	name := parts[3]
	sa, err := client.CoreV1().ServiceAccounts(Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("getting workload ServiceAccount: %w", err)
	}
	if err := verifyWorkloadServiceAccountAnnotations(ctx, sa, expectedTenantPath, proofKeys); err != nil {
		return "", nil, err
	}
	return review.Status.User.Username, sa, nil
}

// ValidateWorkloadScope rejects a scope whose tuple is incomplete or whose
// provider resources carry wildcards or malformed action names, so an RBAC
// rule is never materialized from a shape the policy would not admit.
func ValidateWorkloadScope(scope WorkloadIdentityScope) error {
	for name, value := range map[string]string{
		"tenantPath":  scope.TenantPath,
		"project":     scope.Project,
		"projectUID":  scope.ProjectUID,
		"environment": scope.Environment,
		"instance":    scope.Instance,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s contains prohibited characters", name)
		}
	}
	for _, resource := range scope.ProviderResources {
		if strings.TrimSpace(resource.APIVersion) == "" || strings.TrimSpace(resource.Resource) == "" || strings.TrimSpace(resource.Name) == "" {
			return fmt.Errorf("provider resource scope requires apiVersion, resource, and name")
		}
		if resource.Resource == "*" || resource.Name == "*" || strings.ContainsAny(resource.Resource+resource.Name, "\r\n\x00") {
			return fmt.Errorf("provider resource scope contains a wildcard or prohibited character")
		}
		if _, err := schema.ParseGroupVersion(resource.APIVersion); err != nil {
			return fmt.Errorf("provider resource apiVersion is invalid: %w", err)
		}
		for _, action := range resource.Actions {
			if !workloadActionNameRE.MatchString(action) {
				return fmt.Errorf("provider action name %q is invalid", action)
			}
		}
	}
	return nil
}

// workloadActionNameRE matches the name half of a CatalogEntry action ID
// (the ID grammar is name/vN; RBAC carries the name only).
var workloadActionNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

func workloadIdentityAnnotations(scope WorkloadIdentityScope) map[string]string {
	return map[string]string{
		AnnotationWorkloadIdentityScope:       workloadScopeMarker(scope),
		AnnotationWorkloadIdentityTenantPath:  scope.TenantPath,
		AnnotationWorkloadIdentityProject:     scope.Project,
		AnnotationWorkloadIdentityProjectUID:  scope.ProjectUID,
		AnnotationWorkloadIdentityEnvironment: scope.Environment,
		AnnotationWorkloadIdentityInstance:    scope.Instance,
	}
}

func verifyWorkloadServiceAccountAnnotations(ctx context.Context, sa *corev1.ServiceAccount, expectedTenantPath string, proofKeys ProofKeySource) error {
	if sa == nil || sa.Labels[LabelWorkloadIdentity] != "true" || sa.Annotations[AnnotationWorkloadIdentityTenantPath] != expectedTenantPath {
		return fmt.Errorf("workload ServiceAccount is not bound to selected tenant")
	}
	// A delegated user identity is hub-managed and tenant-bound like a
	// workload identity, but carries a human user instead of a Project tuple.
	// It never has a scope marker, so an action-grant check comparing markers
	// fails closed on it — which is right: delegation reaches a provider's
	// data plane as the user, not the action runtime.
	//
	// Note the label alone is tenant-writable, so this branch is reachable by
	// anyone; it is also terminal, so a hand-made object cannot fall through
	// to the workload-identity checks below and be read as some other
	// identity. Without a valid hub proof it is rejected outright.
	if IsDelegatedUserServiceAccount(sa) {
		key, err := delegatedProofKey(ctx, proofKeys)
		if err != nil {
			return err
		}
		return verifyDelegatedUserAnnotations(key, sa)
	}
	for _, key := range []string{
		AnnotationWorkloadIdentityScope,
		AnnotationWorkloadIdentityProject,
		AnnotationWorkloadIdentityProjectUID,
		AnnotationWorkloadIdentityEnvironment,
		AnnotationWorkloadIdentityInstance,
	} {
		value := strings.TrimSpace(sa.Annotations[key])
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("workload ServiceAccount has incomplete identity annotations")
		}
	}
	if len(sa.Annotations[AnnotationWorkloadIdentityScope]) != sha256.Size*2 {
		return fmt.Errorf("workload ServiceAccount has malformed identity scope")
	}
	if _, err := hex.DecodeString(sa.Annotations[AnnotationWorkloadIdentityScope]); err != nil {
		return fmt.Errorf("workload ServiceAccount has malformed identity scope")
	}
	return nil
}

// workloadIdentityRules is the exact rule set a workload identity carries:
// GET on its own Project, GET on each verified provider resource, and one
// rule per granted action on that action's kcp custom subresource. The
// subresource IS the capability, so the rule carries dataplane.SubresourceVerbs:
// kcp authorizes a verb call by mapping the HTTP method onto the RBAC verb
// (GET → get, POST → create, an upgrade → its method), and which method a
// verb uses is the provider's transport detail, not a separate grant.
// Materializing a Project action grant IS writing this rule; revoking it
// removes the rule.
func workloadIdentityRules(scope WorkloadIdentityScope) []rbacv1.PolicyRule {
	wantRules := []rbacv1.PolicyRule{
		{
			APIGroups:     []string{workloadProjectGroup},
			Resources:     []string{workloadProjectResource},
			Verbs:         []string{"get"},
			ResourceNames: []string{scope.Project},
		},
	}
	providerRules := make([]rbacv1.PolicyRule, 0, len(scope.ProviderResources))
	for _, resource := range scope.ProviderResources {
		gv, _ := schema.ParseGroupVersion(resource.APIVersion)
		providerRules = append(providerRules, rbacv1.PolicyRule{
			APIGroups: []string{gv.Group}, Resources: []string{resource.Resource},
			Verbs: []string{"get"}, ResourceNames: []string{resource.Name},
		})
		actions := append([]string(nil), resource.Actions...)
		sort.Strings(actions)
		for _, action := range actions {
			providerRules = append(providerRules, rbacv1.PolicyRule{
				APIGroups: []string{gv.Group}, Resources: []string{resource.Resource + "/" + action},
				Verbs: append([]string(nil), dataplane.SubresourceVerbs...), ResourceNames: []string{resource.Name},
			})
		}
	}
	sort.Slice(providerRules, func(i, j int) bool {
		return strings.Join(providerRules[i].APIGroups, "/")+"/"+providerRules[i].Resources[0]+"/"+providerRules[i].ResourceNames[0] < strings.Join(providerRules[j].APIGroups, "/")+"/"+providerRules[j].Resources[0]+"/"+providerRules[j].ResourceNames[0]
	})
	return append(wantRules, providerRules...)
}

// ensureWorkloadClusterRoleBinding reconciles the hub-managed
// ClusterRoleBinding bindingName so that exactly the default-namespace
// ServiceAccount serviceAccount is bound to the ClusterRole roleName. Shared by
// the workload identity (which binds its own narrow ClusterRole) and the
// delegated user identity (which binds the role the workspace already grants
// its members).
func ensureWorkloadClusterRoleBinding(ctx context.Context, cs kubernetes.Interface, bindingName, roleName, serviceAccount string) error {
	bindings := cs.RbacV1().ClusterRoleBindings()
	wantSubjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: serviceAccount, Namespace: Namespace}}
	wantRoleRef := rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName}
	binding, err := bindings.Get(ctx, bindingName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		binding, err = bindings.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{
			Name:   bindingName,
			Labels: map[string]string{LabelWorkloadIdentity: "true"},
		}, Subjects: wantSubjects, RoleRef: wantRoleRef}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating workload ClusterRoleBinding: %w", err)
		}
		if err != nil {
			binding, err = bindings.Get(ctx, bindingName, metav1.GetOptions{})
		}
	}
	if err != nil {
		return fmt.Errorf("getting workload ClusterRoleBinding: %w", err)
	}
	if !reflect.DeepEqual(binding.Subjects, wantSubjects) || !reflect.DeepEqual(binding.RoleRef, wantRoleRef) || binding.Labels[LabelWorkloadIdentity] != "true" {
		updated := binding.DeepCopy()
		updated.Subjects = wantSubjects
		// RoleRef is immutable in Kubernetes. Delete/recreate only when the
		// existing binding points at something else; normal reconciliation uses
		// a metadata/subject update.
		if !reflect.DeepEqual(updated.RoleRef, wantRoleRef) {
			if err := bindings.Delete(ctx, bindingName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting stale workload ClusterRoleBinding: %w", err)
			}
			_, err := bindings.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{
				Name: bindingName, Labels: map[string]string{LabelWorkloadIdentity: "true"},
			}, Subjects: wantSubjects, RoleRef: wantRoleRef}, metav1.CreateOptions{})
			if err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("recreating workload ClusterRoleBinding: %w", err)
			}
		} else {
			if updated.Labels == nil {
				updated.Labels = map[string]string{}
			}
			updated.Labels[LabelWorkloadIdentity] = "true"
			if _, err := bindings.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("reconciling workload ClusterRoleBinding: %w", err)
			}
		}
	}
	return nil
}

func workloadScopeMarker(scope WorkloadIdentityScope) string {
	resources := append([]ProviderResourceScope(nil), scope.ProviderResources...)
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].APIVersion+"/"+resources[i].Resource+"/"+resources[i].Name < resources[j].APIVersion+"/"+resources[j].Resource+"/"+resources[j].Name
	})
	parts := []string{scope.TenantPath, scope.Project, scope.ProjectUID, scope.Environment, scope.Instance}
	for _, resource := range resources {
		parts = append(parts, resource.APIVersion, resource.Kind, resource.Resource, resource.Name)
		actions := append([]string(nil), resource.Actions...)
		sort.Strings(actions)
		parts = append(parts, actions...)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// WorkloadIdentityScopeMarker returns the deterministic scope annotation for
// a verified workload scope. It is exported for hub-side invocation
// authorization, which must compare the live Project-derived scope with the
// ServiceAccount's durable identity marker before forwarding an action.
func WorkloadIdentityScopeMarker(scope WorkloadIdentityScope) string {
	return workloadScopeMarker(scope)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
