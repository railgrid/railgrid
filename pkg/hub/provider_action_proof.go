// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/tenantaccess"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/browsersession"
	"github.com/railgrid/railgrid/pkg/hub/actionproof"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

var errAppAccessRuntimeAbsent = errors.New("project has no persisted runtime binding")

// providerActionProofBridge authenticates a person at the hub front door and
// passes only a narrow, expiring proof through kcp. The person's bearer remains
// on the hub -> kcp hop, where kcp authenticates it natively, and never reaches
// the provider. Proof redemption additionally requires that provider's own SA.
type providerActionProofBridge struct {
	keys         serviceaccounts.ProofKeySource
	registry     *providers.Registry
	identify     func(*http.Request) (browsersession.Identity, error)
	memberships  tenant.MembershipLookup
	clients      func(string) (dynamic.Interface, error)
	caller       func(context.Context, *http.Request, string) (authnv1.UserInfo, error)
	authenticate func(context.Context, *http.Request, actionproof.Claims) error
}

func newProviderActionProofBridge(config *rest.Config, keys serviceaccounts.ProofKeySource, registry *providers.Registry, identify func(*http.Request) (browsersession.Identity, error), memberships tenant.MembershipLookup) *providerActionProofBridge {
	b := &providerActionProofBridge{keys: keys, registry: registry, identify: identify, memberships: memberships}
	b.clients = func(cluster string) (dynamic.Interface, error) {
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster)
		cfg.Timeout = 15 * time.Second
		return dynamic.NewForConfig(cfg)
	}
	b.caller = func(ctx context.Context, r *http.Request, cluster string) (authnv1.UserInfo, error) {
		// Use the same credential kcp is about to authenticate, solely against
		// kcp's self-review endpoint. This obtains verified OIDC groups and
		// extras instead of deriving either from client-controlled headers.
		cfg := rest.AnonymousClientConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, cluster)
		cfg.BearerToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		cfg.Timeout = 10 * time.Second
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return authnv1.UserInfo{}, err
		}
		review, err := client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
		if err != nil {
			return authnv1.UserInfo{}, err
		}
		return review.Status.UserInfo, nil
	}
	b.authenticate = func(ctx context.Context, r *http.Request, claims actionproof.Claims) error {
		entry, ok := registry.GetForOrg(claims.OrgUUID, claims.Provider)
		if !ok || entry.OrgUUID != claims.ProviderOrgUUID || entry.WorkspaceCluster == "" {
			return fmt.Errorf("the proof's provider is unavailable")
		}
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, entry.WorkspaceCluster)
		cfg.Timeout = 10 * time.Second
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return err
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			return fmt.Errorf("provider authentication is required")
		}
		review, err := client.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		if !review.Status.Authenticated || review.Status.User.Username != providers.ProviderSAUsername {
			return fmt.Errorf("the bearer does not authenticate as the proof's provider")
		}
		return nil
	}
	return b
}

// wrap is installed only around the hub's kcp forwarder, not hub REST routes
// where a provider redeems an already-issued proof. Client-supplied proofs are
// always removed before minting, including requests that do not qualify.
func (b *providerActionProofBridge) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Del(actionproof.Header)
		route, err := dataplane.ParseSubresourceRequest(r)
		if err != nil || route.Group != "ai.railgrid.ai" || route.APIVersion != "v1alpha1" || (route.Resource != "projects" && route.Resource != "sessions") {
			next.ServeHTTP(w, r)
			return
		}
		person, err := b.identify(r)
		if err != nil {
			// ServiceAccounts keep their native kcp path, without acquiring a
			// person's identity or any additional hub privileges.
			next.ServeHTTP(w, r)
			return
		}
		claims, err := b.claims(r, route, person)
		if err != nil {
			writeDiscoveryError(w, http.StatusForbidden, "caller_context_denied", "The caller's access to this App Studio operation could not be verified.")
			return
		}
		proof, err := actionproof.Sign(r.Context(), b.keys, claims, time.Now())
		if err != nil {
			writeDiscoveryError(w, http.StatusServiceUnavailable, "caller_context_unavailable", "The hub could not establish a caller context. Retry the operation.")
			return
		}
		r.Header.Set(actionproof.Header, proof)
		next.ServeHTTP(w, r)
	})
}

func (b *providerActionProofBridge) claims(r *http.Request, route dataplane.SubresourceRequest, person browsersession.Identity) (actionproof.Claims, error) {
	claims := actionproof.Claims{UserID: person.UserID, RBACIdentity: person.RBACIdentity,
		ClusterID: route.ClusterID, Provider: "app-studio", Group: route.Group, Version: route.APIVersion,
		Resource: route.Resource, ParentName: route.Name, Verb: route.Verb, Method: r.Method}
	dyn, err := b.clients(route.ClusterID)
	if err != nil {
		return claims, err
	}
	path, err := tenantaccess.WorkspacePath(r.Context(), dyn)
	if err != nil {
		return claims, err
	}
	claims.OrgUUID, claims.WorkspaceUUID, _ = tenantaccess.ParseTenantPath(path)
	entry, ok := b.registry.GetForOrg(claims.OrgUUID, claims.Provider)
	if !ok || entry.Export == nil {
		return claims, fmt.Errorf("app-studio provider is unavailable")
	}
	claims.ProviderOrgUUID = entry.OrgUUID
	declared := false
	for _, resource := range entry.Export.Resources {
		if resource.Name != route.Resource || resource.APIVersion != route.Group+"/"+route.APIVersion {
			continue
		}
		for _, verb := range resource.Verbs {
			declared = declared || verb.Name == route.Verb
		}
	}
	if !declared {
		return claims, fmt.Errorf("undeclared App Studio operation")
	}
	if r.Method == http.MethodPost && route.Resource == "projects" && route.Tail == "" &&
		(route.Verb == "publishing-grants" || route.Verb == "preview-grants") {
		claims.InviteUser, err = actionProofInviteUser(r)
		if err != nil {
			return claims, err
		}
	}
	if r.Method == http.MethodPost && route.Resource == "projects" && route.Tail == "" && route.Verb == "authentication-grants" {
		claims.AppAuthUser, claims.AppAuthAppID, err = actionProofAppAuthGrant(r)
		if err != nil {
			return claims, err
		}
	}
	if route.Resource == "projects" {
		if operation, user, grant, ok, intentErr := actionProofAppAccessIntent(r, route); intentErr != nil {
			return claims, intentErr
		} else if ok {
			claims.AppAccessOperation = operation
			claims.AppAccessUser = user
			claims.AppAccessGrant = grant
			claims.AppAccessProject = route.Name
			claims.AppAccessGroup = "infrastructure.railgrid.ai"
			claims.AppAccessResource = "instances"
			if err := bindAppAccessTarget(r.Context(), dyn, route, &claims); err != nil {
				return claims, err
			}
		}
	}
	identity, err := b.caller(r.Context(), r, route.ClusterID)
	if err != nil || identity.Username == "" || identity.Username != person.RBACIdentity {
		return claims, fmt.Errorf("kcp did not confirm the caller identity")
	}
	claims.Groups = identity.Groups
	claims.Extra = make(map[string][]string, len(identity.Extra))
	for key, value := range identity.Extra {
		claims.Extra[key] = []string(value)
	}
	if err := b.validateScope(r.Context(), claims); err != nil {
		return claims, err
	}
	return claims, nil
}

// actionProofAppAccessIntent recognizes only the Project actions that back
// app-access grants. The target itself is resolved from the persisted Project
// below, so a caller cannot choose an arbitrary workspace RBAC object.
func actionProofAppAccessIntent(r *http.Request, route dataplane.SubresourceRequest) (operation, user, grant string, matched bool, err error) {
	if route.Resource != "projects" || route.Name == "" {
		return "", "", "", false, nil
	}
	switch route.Verb {
	case "publishing-grants", "preview-grants", "authentication-grants":
		if route.Tail == "" {
			switch r.Method {
			case http.MethodGet:
				return "list", "", "", true, nil
			case http.MethodPost:
				user, err = actionProofRequestedAppAccessUser(r)
				return "create", user, "", true, err
			}
			return "", "", "", false, nil
		}
		if (route.Verb == "authentication-grants" && r.Method == http.MethodDelete) ||
			((route.Verb == "publishing-grants" || route.Verb == "preview-grants") && r.Method == http.MethodPost) {
			return "revoke", "", route.Tail, true, nil
		}
	case "publishing", "preview":
		if route.Tail == "" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
			return "list", "", "", true, nil
		}
		if route.Tail == "" && r.Method == http.MethodDelete {
			return "reset", "", "", true, nil
		}
	}
	return "", "", "", false, nil
}

func actionProofRequestedAppAccessUser(r *http.Request) (string, error) {
	if r == nil || r.Body == nil {
		return "", fmt.Errorf("app-access grant request is empty")
	}
	const maxAppAccessIntentBytes = 16 << 10
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAppAccessIntentBytes+1))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > maxAppAccessIntentBytes {
		return "", fmt.Errorf("app-access grant request could not be read")
	}
	var intent struct {
		User   string `json:"user"`
		Invite bool   `json:"invite"`
		AppID  string `json:"appId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&intent) != nil || decoder.Decode(new(any)) != io.EOF ||
		intent.User == "" || intent.User != strings.TrimSpace(intent.User) {
		return "", fmt.Errorf("app-access grant request is invalid")
	}
	return intent.User, nil
}

// bindAppAccessTarget follows the fixed Project binding selected by the
// source action, then signs the persisted Project and Infrastructure object
// identities. Instance owner references make a deleted/recreated Project UID
// unable to inherit grants through a same-name resource.
func bindAppAccessTarget(ctx context.Context, client dynamic.Interface, route dataplane.SubresourceRequest, claims *actionproof.Claims) error {
	if client == nil || claims == nil {
		return fmt.Errorf("app-access target lookup is unavailable")
	}
	environment, bindingName := "", ""
	switch route.Verb {
	case "publishing-grants", "authentication-grants", "publishing":
		environment, bindingName = "production", "prod"
	case "preview-grants", "preview":
		environment, bindingName = "development", "dev"
	default:
		return fmt.Errorf("unsupported app-access Project action")
	}
	projectGVR := schema.GroupVersionResource{Group: "ai.railgrid.ai", Version: route.APIVersion, Resource: "projects"}
	project, err := client.Resource(projectGVR).Get(ctx, route.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading the addressed App Studio Project: %w", err)
	}
	if project.GetName() != route.Name || string(project.GetUID()) == "" {
		return fmt.Errorf("the addressed App Studio Project has no valid persisted identity")
	}
	target, version, err := projectAppAccessTarget(project, environment, bindingName)
	if errors.Is(err, errAppAccessRuntimeAbsent) && appAccessMayOmitAbsentRuntime(route, claims) {
		clearAppAccessTarget(claims)
		return nil
	}
	if err != nil {
		return err
	}
	instanceGVR := schema.GroupVersionResource{Group: claims.AppAccessGroup, Version: version, Resource: claims.AppAccessResource}
	instance, err := client.Resource(instanceGVR).Get(ctx, target, metav1.GetOptions{})
	if err != nil {
		if missingNamedObject(err, instanceGVR.GroupResource(), target) && appAccessMayOmitAbsentRuntime(route, claims) {
			clearAppAccessTarget(claims)
			return nil
		}
		return fmt.Errorf("reading the Project-managed Infrastructure Instance: %w", err)
	}
	if !projectOwnsAppAccessInstance(project, instance) || string(instance.GetUID()) == "" {
		return fmt.Errorf("the Infrastructure Instance is not owned by the addressed Project")
	}
	claims.AppAccessName = instance.GetName()
	claims.AppAccessUID = string(instance.GetUID())
	claims.AppAccessProjectUID = string(project.GetUID())
	return nil
}

func appAccessMayOmitAbsentRuntime(route dataplane.SubresourceRequest, claims *actionproof.Claims) bool {
	if claims == nil || route.Resource != "projects" || route.Tail != "" {
		return false
	}
	if route.Verb != "publishing" && route.Verb != "preview" {
		return false
	}
	return (claims.AppAccessOperation == "list" && (claims.Method == http.MethodGet || claims.Method == http.MethodPost)) ||
		(claims.AppAccessOperation == "reset" && claims.Method == http.MethodDelete)
}

func clearAppAccessTarget(claims *actionproof.Claims) {
	claims.AppAccessOperation = ""
	claims.AppAccessGroup = ""
	claims.AppAccessResource = ""
	claims.AppAccessName = ""
	claims.AppAccessUID = ""
	claims.AppAccessProject = ""
	claims.AppAccessProjectUID = ""
	claims.AppAccessUser = ""
	claims.AppAccessGrant = ""
}

func missingNamedObject(err error, resource schema.GroupResource, name string) bool {
	if !apierrors.IsNotFound(err) || name == "" {
		return false
	}
	status, ok := err.(apierrors.APIStatus)
	if !ok {
		return false
	}
	details := status.Status().Details
	return details != nil && details.Name == name && details.Group == resource.Group && details.Kind == resource.Resource
}

func projectAppAccessTarget(project *unstructured.Unstructured, environment, bindingName string) (string, string, error) {
	environments, found, err := unstructured.NestedSlice(project.Object, "spec", "environments")
	if err != nil {
		return "", "", fmt.Errorf("project environments are malformed: %w", err)
	}
	if !found {
		return "", "", errAppAccessRuntimeAbsent
	}
	var selectedEnvironment map[string]any
	for _, rawEnv := range environments {
		env, ok := rawEnv.(map[string]any)
		if !ok {
			return "", "", fmt.Errorf("project environment entry is malformed")
		}
		environmentName, ok := env["name"].(string)
		if !ok || strings.TrimSpace(environmentName) == "" || environmentName != strings.TrimSpace(environmentName) {
			return "", "", fmt.Errorf("project environment name is malformed")
		}
		if environmentName != environment {
			continue
		}
		if selectedEnvironment != nil {
			return "", "", fmt.Errorf("project has duplicate %s environments", environment)
		}
		selectedEnvironment = env
	}
	if selectedEnvironment == nil {
		return "", "", errAppAccessRuntimeAbsent
	}
	bindings, found, err := unstructured.NestedSlice(selectedEnvironment, "bindings")
	if err != nil {
		return "", "", fmt.Errorf("project %s environment bindings are malformed: %w", environment, err)
	}
	if !found {
		return "", "", errAppAccessRuntimeAbsent
	}
	var selected map[string]any
	for _, rawBinding := range bindings {
		candidate, ok := rawBinding.(map[string]any)
		if !ok {
			return "", "", fmt.Errorf("project environment binding entry is malformed")
		}
		candidateName, ok := candidate["name"].(string)
		if !ok || strings.TrimSpace(candidateName) == "" || candidateName != strings.TrimSpace(candidateName) {
			return "", "", fmt.Errorf("project environment binding name is malformed")
		}
		if candidateName == bindingName {
			if selected != nil {
				return "", "", fmt.Errorf("project has duplicate %s app Instance bindings", environment)
			}
			selected = candidate
		}
	}
	if selected == nil {
		return "", "", errAppAccessRuntimeAbsent
	}
	if selected["provider"] != "app-studio" || selected["kind"] != "providerResource" {
		return "", "", fmt.Errorf("project has an unsupported %s app Instance binding", environment)
	}
	ref, ok := selected["resourceRef"].(map[string]any)
	if !ok || ref["kind"] != "Instance" || ref["resource"] != "instances" {
		return "", "", fmt.Errorf("project app binding does not name an Infrastructure Instance")
	}
	gv, err := schema.ParseGroupVersion(stringValue(ref["apiVersion"]))
	if err != nil || gv.Group != "infrastructure.railgrid.ai" {
		return "", "", fmt.Errorf("project app binding does not name the Infrastructure API")
	}
	name := ""
	if rawName, exists := ref["name"]; exists {
		name, ok = rawName.(string)
		if !ok {
			return "", "", fmt.Errorf("project app binding Instance name is malformed")
		}
		name = strings.TrimSpace(name)
	}
	if name == "" {
		values, valuesFound, err := unstructured.NestedMap(selected, "values")
		if err != nil {
			return "", "", fmt.Errorf("project app binding values are malformed: %w", err)
		}
		if valuesFound {
			if rawName, exists := values["name"]; exists {
				name, ok = rawName.(string)
				if !ok {
					return "", "", fmt.Errorf("project app binding Instance name is malformed")
				}
				name = strings.TrimSpace(name)
			}
		}
	}
	if name == "" {
		name = project.GetName() + "-" + bindingName
	}
	if !actionproofValidResourceName(name) {
		return "", "", fmt.Errorf("project app binding has an invalid Instance name")
	}
	return name, gv.Version, nil
}

func projectOwnsAppAccessInstance(project, instance *unstructured.Unstructured) bool {
	if project == nil || instance == nil || project.GetUID() == "" || project.GetName() == "" || instance.GetName() == "" {
		return false
	}
	if instance.GetLabels()["app-studio.railgrid.ai/project"] != project.GetName() {
		return false
	}
	for _, owner := range instance.GetOwnerReferences() {
		if owner.APIVersion == "ai.railgrid.ai/v1alpha1" && owner.Kind == "Project" &&
			owner.Name == project.GetName() && owner.UID == project.GetUID() {
			return true
		}
	}
	return false
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func actionproofValidResourceName(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '.' {
			return false
		}
	}
	return value[0] != '-' && value[0] != '.' && value[len(value)-1] != '-' && value[len(value)-1] != '.'
}

// actionProofInviteUser binds the one invitation requested by the human. The
// provider receives the original body unchanged; hub redemption cannot
// substitute another recipient or turn a member-only share into an invite.
func actionProofInviteUser(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	const maxInviteRequestBytes = 64 << 10
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInviteRequestBytes+1))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > maxInviteRequestBytes {
		return "", fmt.Errorf("sharing request could not be read")
	}
	var intent struct {
		User   string `json:"user"`
		Invite bool   `json:"invite"`
	}
	if err := json.Unmarshal(body, &intent); err != nil {
		return "", fmt.Errorf("sharing request is invalid")
	}
	user := strings.TrimSpace(intent.User)
	if !intent.Invite || !strings.Contains(user, "@") {
		return "", nil
	}
	return user, nil
}

// actionProofAppAuthGrant binds the authentication-grants action to one
// existing platform account and the production audience represented by the
// addressed Project. The request body is restored for the provider handler.
func actionProofAppAuthGrant(r *http.Request) (string, string, error) {
	if r.Body == nil {
		return "", "", fmt.Errorf("authentication grant request is empty")
	}
	const maxAuthenticationGrantBodyBytes = 16 << 10
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAuthenticationGrantBodyBytes+1))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > maxAuthenticationGrantBodyBytes {
		return "", "", fmt.Errorf("authentication grant request could not be read")
	}
	var intent struct {
		User  string `json:"user"`
		AppID string `json:"appId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&intent) != nil || decoder.Decode(new(any)) != io.EOF {
		return "", "", fmt.Errorf("authentication grant request is invalid")
	}
	if intent.User == "" || intent.User != strings.TrimSpace(intent.User) || intent.AppID == "" || intent.AppID != strings.TrimSpace(intent.AppID) {
		return "", "", fmt.Errorf("authentication grant must name an existing user and this project's production audience")
	}
	return intent.User, intent.AppID, nil
}

func (b *providerActionProofBridge) validateScope(ctx context.Context, c actionproof.Claims) error {
	if c.Provider != "app-studio" || c.Group != "ai.railgrid.ai" || c.Version != "v1alpha1" || (c.Resource != "projects" && c.Resource != "sessions") {
		return fmt.Errorf("unsupported caller context")
	}
	if _, err := tenant.ResolveWorkspaceContext(ctx, b.memberships, c.UserID, c.OrgUUID, c.WorkspaceUUID); err != nil {
		return err
	}
	dyn, err := b.clients(c.ClusterID)
	if err != nil {
		return err
	}
	path, err := tenantaccess.WorkspacePath(ctx, dyn)
	if err != nil {
		return err
	}
	org, workspace, ok := tenantaccess.ParseTenantPath(path)
	if !ok || org != c.OrgUUID || workspace != c.WorkspaceUUID {
		return fmt.Errorf("workspace identity changed")
	}
	entry, ok := b.registry.GetForOrg(c.OrgUUID, c.Provider)
	if !ok || entry.OrgUUID != c.ProviderOrgUUID {
		return fmt.Errorf("provider identity changed")
	}
	bound, err := b.bound(ctx, c.ClusterID, entry)
	if err != nil || !bound {
		return fmt.Errorf("provider is not bound in this workspace")
	}
	caller := dataplane.ProxiedIdentity{User: c.RBACIdentity, Groups: c.Groups, Extra: c.Extra}
	methodVerb := map[string]string{http.MethodGet: "get", http.MethodPost: "create", http.MethodPut: "update", http.MethodPatch: "patch", http.MethodDelete: "delete"}[c.Method]
	if methodVerb == "" {
		return fmt.Errorf("unsupported operation method")
	}
	for _, attr := range []dataplane.ResourceAttributes{
		{Group: c.Group, Version: c.Version, Resource: c.Resource, Name: c.ParentName, Verb: "get"},
		{Group: c.Group, Version: c.Version, Resource: c.Resource, Name: c.ParentName, Subresource: c.Verb, Verb: methodVerb},
	} {
		allowed, err := dataplane.Authorize(ctx, dyn, caller, attr)
		if err != nil || !allowed {
			return fmt.Errorf("caller may not perform the addressed operation")
		}
	}
	parent, err := dyn.Resource(schema.GroupVersionResource{Group: c.Group, Version: c.Version, Resource: c.Resource}).Get(ctx, c.ParentName, metav1.GetOptions{})
	if err != nil || parent.GetDeletionTimestamp() != nil {
		return fmt.Errorf("the action's parent resource is unavailable")
	}
	return nil
}

func (b *providerActionProofBridge) verifier() *actionproof.Verifier {
	return &actionproof.Verifier{Keys: b.keys, Authenticate: b.authenticate, ValidateScope: b.validateScope}
}

func (b *providerActionProofBridge) bound(ctx context.Context, cluster string, entry providers.Provider) (bool, error) {
	dyn, err := b.clients(cluster)
	if err != nil {
		return false, err
	}
	bindings, err := dyn.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	for _, item := range bindings.Items {
		name, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "name")
		path, _, _ := unstructured.NestedString(item.Object, "spec", "reference", "export", "path")
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if name == entry.APIExportName && path == entry.APIExportPath && phase == "Bound" {
			return true, nil
		}
	}
	return false, nil
}

func actionProofResourceClient(config *rest.Config) func(actionproof.Claims) (dynamic.Interface, error) {
	return func(c actionproof.Claims) (dynamic.Interface, error) {
		cfg := rest.CopyConfig(config)
		cfg.Host = apiurl.KCPClusterURL(cfg.Host, c.ClusterID)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: c.RBACIdentity, Groups: c.Groups, Extra: c.Extra}
		cfg.Timeout = 15 * time.Second
		return dynamic.NewForConfig(cfg)
	}
}
