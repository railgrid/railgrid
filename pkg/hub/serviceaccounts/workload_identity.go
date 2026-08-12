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

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	corev1typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

const (
	// WorkloadIdentityBootstrapAudience is the audience expected by the
	// provider-owned bootstrap attestor. It is never used for the minted Faros
	// capability.
	WorkloadIdentityBootstrapAudience = "faros-provider-actions-bootstrap"

	// WorkloadIdentityTokenAudience is the audience requested for the minted
	// runtime capability. KCP's embedded and deployed API servers issue and
	// validate ServiceAccount tokens for this issuer audience; using the
	// legacy proxy-only "faros" audience would make the token fail at the
	// provider's tenant API before the action reached its backend.
	WorkloadIdentityTokenAudience = "https://kcp.default.svc"

	// WorkloadIdentityTokenTTL is deliberately short. The TokenRequest API may
	// cap it further according to cluster policy; it must never be increased by
	// this package.
	WorkloadIdentityTokenTTL = 10 * time.Minute

	// LabelWorkloadIdentity marks service accounts created for the provider
	// action runtime. These accounts intentionally do not carry
	// LabelFarosSA, so the ordinary user-managed service-account CRUD surface
	// cannot list, patch, or rotate them.
	LabelWorkloadIdentity = "faros.sh/workload-identity"

	// AnnotationWorkloadIdentityScope is a compact, non-secret audit marker.
	// The token itself is never persisted in an annotation or Secret.
	AnnotationWorkloadIdentityScope = "faros.sh/workload-identity-scope"

	// AnnotationWorkloadIdentityTenantPath binds a workload ServiceAccount to
	// the child workspace in which it was issued. The tenant resolver checks
	// this marker after an online TokenReview, preventing a valid Faros token
	// from being replayed with another tenant's selection headers.
	AnnotationWorkloadIdentityTenantPath = "faros.sh/workload-identity-tenant"

	// The remaining annotations carry the exact project identity tuple used to
	// derive the deterministic workload ServiceAccount name. They are retained
	// separately from the compact scope marker so an online verifier can load
	// and compare the live Project before authorizing an action invocation.
	AnnotationWorkloadIdentityProject     = "faros.sh/workload-identity-project"
	AnnotationWorkloadIdentityProjectUID  = "faros.sh/workload-identity-project-uid"
	AnnotationWorkloadIdentityEnvironment = "faros.sh/workload-identity-environment"
	AnnotationWorkloadIdentityInstance    = "faros.sh/workload-identity-instance"

	workloadIdentityNamePrefix = "faros-wi-"
	workloadIdentityRoleSuffix = "-access"

	// The project resource is owned by App Studio. Provider-resource rules are
	// derived from the verified Project environment and are never hard-coded.
	workloadProjectGroup    = "ai.faros.sh"
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

// WorkloadIdentityToken is the short-lived capability returned by
// EnsureWorkloadIdentity. ServiceAccountName is diagnostic metadata only; the
// token is the sole credential returned to the caller.
type WorkloadIdentityToken struct {
	Token              string
	ExpiresAt          time.Time
	ServiceAccountName string
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

// EnsureWorkloadIdentity creates or reconciles the narrowly scoped workload
// ServiceAccount and its GET-only RBAC, then mints a fresh audience-bound
// TokenRequest. It deliberately does not call Create/IssueToken: those APIs
// retain their legacy human-managed semantics and cluster-admin binding.
func (m *Manager) EnsureWorkloadIdentity(ctx context.Context, orgUUID, wsUUID string, scope WorkloadIdentityScope) (*WorkloadIdentityToken, error) {
	if err := validateWorkloadScope(scope); err != nil {
		return nil, err
	}
	if strings.TrimSpace(orgUUID) == "" || strings.TrimSpace(wsUUID) == "" {
		return nil, fmt.Errorf("orgUUID and wsUUID are required")
	}

	cs, err := m.clientset(orgUUID, wsUUID)
	if err != nil {
		return nil, err
	}

	name := WorkloadServiceAccountName(scope)
	_, err = ensureWorkloadServiceAccount(ctx, cs, name, scope)
	if err != nil {
		return nil, err
	}
	if err := ensureWorkloadRBAC(ctx, cs, name, scope); err != nil {
		return nil, err
	}

	expirationSeconds := int64(WorkloadIdentityTokenTTL / time.Second)
	request := &authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{
		Audiences:         []string{WorkloadIdentityTokenAudience},
		ExpirationSeconds: &expirationSeconds,
	}}
	issued, err := cs.CoreV1().ServiceAccounts(Namespace).CreateToken(ctx, name, request, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("issuing workload identity token: %w", err)
	}
	if strings.TrimSpace(issued.Status.Token) == "" || issued.Status.ExpirationTimestamp.IsZero() {
		return nil, fmt.Errorf("workload identity token response was incomplete")
	}
	// A misbehaving API server must not turn this path into a longer-lived
	// credential than policy allows. A shorter expiration is valid because
	// cluster admission may cap TokenRequest TTLs.
	if issued.Status.ExpirationTimestamp.After(time.Now().Add(WorkloadIdentityTokenTTL + time.Second)) {
		return nil, fmt.Errorf("workload identity token exceeds maximum lifetime")
	}

	return &WorkloadIdentityToken{
		Token:              issued.Status.Token,
		ExpiresAt:          issued.Status.ExpirationTimestamp.Time,
		ServiceAccountName: name,
	}, nil
}

// VerifyWorkloadServiceAccount performs online TokenReview validation in the
// selected child workspace and verifies that the reviewed ServiceAccount is a
// hub-managed workload identity bound to expectedTenantPath. JWT signatures
// and claims are intentionally never trusted offline.
func VerifyWorkloadServiceAccount(ctx context.Context, cfg *rest.Config, token, expectedTenantPath string) (string, error) {
	username, _, err := VerifyWorkloadServiceAccountDetails(ctx, cfg, token, expectedTenantPath)
	return username, err
}

// VerifyWorkloadServiceAccountDetails is the online workload identity
// verifier used by both tenant resolution and action-grant authorization. It
// returns the reviewed ServiceAccount only after TokenReview, audience,
// subject, label, tenant, and required identity annotations have all passed.
func VerifyWorkloadServiceAccountDetails(ctx context.Context, cfg *rest.Config, token, expectedTenantPath string) (string, *corev1.ServiceAccount, error) {
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
	if err := verifyWorkloadServiceAccountAnnotations(sa, expectedTenantPath); err != nil {
		return "", nil, err
	}
	return review.Status.User.Username, sa, nil
}

func validateWorkloadScope(scope WorkloadIdentityScope) error {
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

func ensureWorkloadServiceAccount(ctx context.Context, cs kubernetes.Interface, name string, scope WorkloadIdentityScope) (*corev1.ServiceAccount, error) {
	sas := cs.CoreV1().ServiceAccounts(Namespace)
	sa, err := sas.Get(ctx, name, metav1.GetOptions{})
	wantAnnotations := workloadIdentityAnnotations(scope)
	if apierrors.IsNotFound(err) {
		sa, err = sas.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   Namespace,
			Labels:      map[string]string{LabelWorkloadIdentity: "true"},
			Annotations: wantAnnotations,
		}}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating workload ServiceAccount: %w", err)
		}
		if err == nil {
			return sa, nil
		}
		sa, err = sas.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("getting workload ServiceAccount: %w", err)
	}
	// The identity of the ServiceAccount is the five-field tuple (which also
	// determines its hashed name). The scope marker, by contrast, is a
	// snapshot of the CURRENT grants — it changes whenever an integration is
	// added, revoked, or reactivated, and must reconcile on re-exchange so a
	// grant change never bricks the runtime identity.
	if sa.Labels[LabelWorkloadIdentity] != "true" || sa.Annotations[AnnotationWorkloadIdentityTenantPath] != wantAnnotations[AnnotationWorkloadIdentityTenantPath] {
		return nil, fmt.Errorf("ServiceAccount %q is already bound to a different workload identity", name)
	}
	if err := reconcileWorkloadIdentityAnnotations(ctx, sas, sa, wantAnnotations); err != nil {
		return nil, err
	}
	return sa, nil
}

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

func verifyWorkloadServiceAccountAnnotations(sa *corev1.ServiceAccount, expectedTenantPath string) error {
	if sa == nil || sa.Labels[LabelWorkloadIdentity] != "true" || sa.Annotations[AnnotationWorkloadIdentityTenantPath] != expectedTenantPath {
		return fmt.Errorf("workload ServiceAccount is not bound to selected tenant")
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

func reconcileWorkloadIdentityAnnotations(ctx context.Context, sas corev1typed.ServiceAccountInterface, sa *corev1.ServiceAccount, want map[string]string) error {
	if sa.Annotations == nil {
		sa.Annotations = map[string]string{}
	}
	changed := false
	for key, value := range want {
		// The five identity annotations are immutable for the SA's lifetime;
		// the scope marker reconciles because it tracks the current grants.
		if key != AnnotationWorkloadIdentityScope {
			if existing, ok := sa.Annotations[key]; ok && existing != "" && existing != value {
				return fmt.Errorf("ServiceAccount %q has conflicting workload annotation %q", sa.Name, key)
			}
		}
		if sa.Annotations[key] != value {
			sa.Annotations[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := sas.Update(ctx, sa, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("reconciling workload ServiceAccount annotations: %w", err)
	}
	return nil
}

func ensureWorkloadRBAC(ctx context.Context, cs kubernetes.Interface, serviceAccount string, scope WorkloadIdentityScope) error {
	roleName := WorkloadIdentityRoleName(serviceAccount)
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
		// One create rule per granted action, on the action's virtual
		// subresource. The owning provider enforces this exact coordinate with
		// a caller-scoped SSAR before serving the verb.
		actions := append([]string(nil), resource.Actions...)
		sort.Strings(actions)
		for _, action := range actions {
			providerRules = append(providerRules, rbacv1.PolicyRule{
				APIGroups: []string{gv.Group}, Resources: []string{resource.Resource + "/" + action},
				Verbs: []string{"create"}, ResourceNames: []string{resource.Name},
			})
		}
	}
	sort.Slice(providerRules, func(i, j int) bool {
		return strings.Join(providerRules[i].APIGroups, "/")+"/"+providerRules[i].Resources[0]+"/"+providerRules[i].ResourceNames[0] < strings.Join(providerRules[j].APIGroups, "/")+"/"+providerRules[j].Resources[0]+"/"+providerRules[j].ResourceNames[0]
	})
	wantRules = append(wantRules, providerRules...)

	roles := cs.RbacV1().ClusterRoles()
	role, err := roles.Get(ctx, roleName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		role, err = roles.Create(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: map[string]string{LabelWorkloadIdentity: "true"},
		}, Rules: wantRules}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating workload ClusterRole: %w", err)
		}
		if err != nil {
			role, err = roles.Get(ctx, roleName, metav1.GetOptions{})
		}
	}
	if err != nil {
		return fmt.Errorf("getting workload ClusterRole: %w", err)
	}
	if !reflect.DeepEqual(role.Rules, wantRules) || role.Labels[LabelWorkloadIdentity] != "true" {
		updated := role.DeepCopy()
		updated.Rules = wantRules
		if updated.Labels == nil {
			updated.Labels = map[string]string{}
		}
		updated.Labels[LabelWorkloadIdentity] = "true"
		if _, err := roles.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("reconciling workload ClusterRole: %w", err)
		}
	}

	bindings := cs.RbacV1().ClusterRoleBindings()
	wantSubjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: serviceAccount, Namespace: Namespace}}
	binding, err := bindings.Get(ctx, roleName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		binding, err = bindings.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: map[string]string{LabelWorkloadIdentity: "true"},
		}, Subjects: wantSubjects, RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     roleName,
		}}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating workload ClusterRoleBinding: %w", err)
		}
		if err != nil {
			binding, err = bindings.Get(ctx, roleName, metav1.GetOptions{})
		}
	}
	if err != nil {
		return fmt.Errorf("getting workload ClusterRoleBinding: %w", err)
	}
	wantRoleRef := rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName}
	if !reflect.DeepEqual(binding.Subjects, wantSubjects) || !reflect.DeepEqual(binding.RoleRef, wantRoleRef) || binding.Labels[LabelWorkloadIdentity] != "true" {
		updated := binding.DeepCopy()
		updated.Subjects = wantSubjects
		// RoleRef is immutable in Kubernetes. Delete/recreate only when the
		// existing binding points at something else; normal reconciliation uses
		// a metadata/subject update.
		if !reflect.DeepEqual(updated.RoleRef, wantRoleRef) {
			if err := bindings.Delete(ctx, roleName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting stale workload ClusterRoleBinding: %w", err)
			}
			_, err := bindings.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{
				Name: roleName, Labels: map[string]string{LabelWorkloadIdentity: "true"},
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
