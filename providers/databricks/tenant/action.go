// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/faroshq/provider-databricks/actions"
	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/controller/shared"
	"github.com/faroshq/provider-databricks/queryapi"
)

// ActionExecutor resolves a bound Table and executes query_table/v1 directly.
// Provider-owned resources and credential Secrets are read with the provider
// authority client; the forwarded caller token is used only for delegated
// authorization. It intentionally does not create a query object or persist
// result rows in control-plane status.
type ActionExecutor struct {
	factory    *ClientFactory
	identity   identity
	executor   backend.QueryExecutor
	authorizer TableAuthorizer
	// authorityClient is test-only dependency injection; production requests
	// obtain it from ClientFactory.AuthorityClient (the mcmanager manager).
	authorityClient client.Client
}

// TableAuthorizer performs caller-scoped authorization before the provider
// reads its dependency chain: a visibility gate (get on the Table) and a verb
// gate (create on the action's virtual subresource). Both reviews run with
// the forwarded caller token so kcp RBAC — including workload-identity action
// grants — is the deciding authority.
type TableAuthorizer interface {
	AuthorizeTable(context.Context, string, string, string) error
	AuthorizeTableAction(context.Context, string, string, string, string) error
}

// ActionExecutorForRoute returns a direct action executor for the request's
// bearer and the route's logical-cluster ID. The cluster comes from the
// action path — the platform data-plane grammar — never from proxy-injected
// headers. A nil factory still returns an executor so the endpoint can fail
// with a useful unavailable error.
func (f *ClientFactory) ActionExecutorForRoute(r *http.Request, clusterID string, executor backend.QueryExecutor) *ActionExecutor {
	return &ActionExecutor{
		factory:    f,
		identity:   identity{clusterID: strings.TrimSpace(clusterID), token: bearerToken(r)},
		executor:   executor,
		authorizer: f,
	}
}

// QueryTable resolves the provider-owned Table -> Warehouse -> Connection ->
// Secret chain through the provider authority manager and invokes the shared
// SQL executor. No resource writes occur on this path.
func (e *ActionExecutor) QueryTable(ctx context.Context, ref actions.ResourceRef, input actions.QueryInput) (result queryapi.QueryTableResult, retErr error) {
	defer func() {
		if retErr != nil {
			retErr = sanitizeActionError(retErr)
		}
	}()
	if e == nil || e.factory == nil {
		return queryapi.QueryTableResult{}, fmt.Errorf("tenant client unavailable")
	}
	if strings.TrimSpace(e.identity.clusterID) == "" {
		return queryapi.QueryTableResult{}, fmt.Errorf("tenant identity is missing")
	}
	if strings.TrimSpace(e.identity.token) == "" {
		return queryapi.QueryTableResult{}, fmt.Errorf("caller credential is missing")
	}
	if strings.TrimSpace(ref.Name) == "" {
		return queryapi.QueryTableResult{}, fmt.Errorf("resourceRef.name is required")
	}
	request, err := queryapi.NormalizeQueryRequest(queryapi.QueryTableRequest{
		ActionVersion: queryapi.ActionVersionV1,
		TableRef:      ref.Name,
		Columns:       append([]string(nil), input.Columns...),
		Limit:         input.Limit,
	})
	if err != nil {
		return queryapi.QueryTableResult{}, err
	}
	if e.authorizer != nil {
		if err := e.authorizer.AuthorizeTable(ctx, e.identity.clusterID, e.identity.token, request.TableRef); err != nil {
			return queryapi.QueryTableResult{}, err
		}
		if err := e.authorizer.AuthorizeTableAction(ctx, e.identity.clusterID, e.identity.token, request.TableRef, actions.ActionQueryName); err != nil {
			return queryapi.QueryTableResult{}, err
		}
	}
	providerClient := e.authorityClient
	if providerClient == nil {
		providerClient, err = e.factory.AuthorityClient(ctx, e.identity.clusterID)
		if err != nil {
			return queryapi.QueryTableResult{}, err
		}
	}
	tbl, err := shared.ResolveTable(ctx, providerClient, request.TableRef)
	if err != nil {
		return queryapi.QueryTableResult{}, err
	}
	wh, err := shared.ResolveWarehouse(ctx, providerClient, tbl.Spec.WarehouseRef)
	if err != nil {
		return queryapi.QueryTableResult{}, err
	}
	if strings.TrimSpace(tbl.Spec.ConnectionRef) == "" || tbl.Spec.ConnectionRef != wh.Spec.ConnectionRef {
		return queryapi.QueryTableResult{}, fmt.Errorf("table and warehouse connection references do not match")
	}
	if !shared.CurrentConditionTrue(tbl.Status.Conditions, tbl.Status.ObservedGeneration, tbl.Generation, databricksv1alpha1.ConditionReady) {
		return queryapi.QueryTableResult{}, fmt.Errorf("table is not ready")
	}
	if !shared.CurrentConditionTrue(wh.Status.Conditions, wh.Status.ObservedGeneration, wh.Generation, databricksv1alpha1.ConditionReady) {
		return queryapi.QueryTableResult{}, fmt.Errorf("warehouse is not ready")
	}
	conn, err := shared.ResolveConnection(ctx, providerClient, tbl.Spec.ConnectionRef)
	if err != nil {
		return queryapi.QueryTableResult{}, err
	}
	if !shared.CurrentConditionTrue(conn.Status.Conditions, conn.Status.ObservedGeneration, conn.Generation, databricksv1alpha1.ConditionValidated) ||
		!shared.CurrentConditionTrue(conn.Status.Conditions, conn.Status.ObservedGeneration, conn.Generation, databricksv1alpha1.ConditionReady) {
		return queryapi.QueryTableResult{}, fmt.Errorf("connection is not ready")
	}
	if conn.Spec.AuthType != databricksv1alpha1.ConnectionAuthPAT {
		return queryapi.QueryTableResult{}, fmt.Errorf("connection authType is unsupported")
	}
	token, err := shared.ResolveBearerToken(ctx, providerClient, conn)
	if err != nil {
		return queryapi.QueryTableResult{}, err
	}
	if e.executor == nil {
		return queryapi.QueryTableResult{}, fmt.Errorf("databricks query executor is unavailable")
	}
	allowed := make([]string, 0, len(tbl.Status.Columns))
	for _, column := range tbl.Status.Columns {
		allowed = append(allowed, column.Name)
	}
	if len(request.Columns) > 0 && len(allowed) == 0 {
		return queryapi.QueryTableResult{}, fmt.Errorf("table schema is not available for projection")
	}
	result, err = e.executor.ExecuteTableQuery(ctx, backend.QueryExecutionTarget{
		Table:       queryapi.TableRef{Catalog: tbl.Spec.Catalog, Schema: tbl.Spec.Schema, Table: tbl.Spec.Table},
		Connection:  queryapi.ConnectionRef{Name: conn.Name, Host: conn.Spec.Host, AuthType: string(conn.Spec.AuthType)},
		Warehouse:   queryapi.WarehouseRef{Name: wh.Name, WarehouseID: wh.Spec.WarehouseID},
		BearerToken: token, Projection: request.Columns, Limit: request.Limit, AllowedColumns: allowed,
	})
	if err != nil {
		return queryapi.QueryTableResult{}, err
	}
	// The backend executor only receives resolved SQL target details. Bind the
	// public result identity to the hub-injected Table resource, never to a
	// caller-controlled or backend-generated placeholder.
	result.ActionVersion = request.ActionVersion
	result.TableRef = request.TableRef
	return queryapi.BoundQueryResult(result), nil
}

// AuthorizeTable delegates the authorization check to the tenant caller's
// SelfSubjectAccessReview. The provider's own kubeconfig is never used for
// the review; it is scoped to the forwarded token and cluster. A successful
// SSAR both authenticates the caller and proves the exact table/get grant, so
// no caller-token TokenReview client is needed.
func (f *ClientFactory) AuthorizeTable(ctx context.Context, clusterID, token, name string) error {
	if f == nil {
		return fmt.Errorf("tenant authorization unavailable")
	}
	client, err := f.AuthorizationFor(clusterID, token)
	if err != nil {
		return err
	}
	accessReview, err := client.SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{
			Group: databricksv1alpha1.GroupName, Version: databricksv1alpha1.Version,
			Resource: "tables", Name: name, Verb: "get",
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("authorize table action: %w", err)
	}
	if !accessReview.Status.Allowed {
		reason := strings.TrimSpace(accessReview.Status.Reason)
		if reason == "" {
			reason = "caller is not allowed to read the imported table"
		}
		return apierrors.NewForbidden(databricksv1alpha1.SchemeGroupVersion.WithResource("tables").GroupResource(), name, fmt.Errorf("%s", reason))
	}
	return nil
}

// AuthorizeTableAction is the verb gate: an explicit create permission on the
// action's virtual subresource (e.g. tables/query_table), reviewed as the
// caller. This is where action grants are enforced — App Studio materializes
// a Project grant as exactly this RBAC rule on the workload identity, and a
// human needs the equivalent workspace permission. The subresource is not
// served by any API server; it exists purely as an RBAC coordinate, the same
// pattern the infrastructure data plane uses for instance exec.
func (f *ClientFactory) AuthorizeTableAction(ctx context.Context, clusterID, token, name, action string) error {
	if f == nil {
		return fmt.Errorf("tenant authorization unavailable")
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return fmt.Errorf("action name is required for authorization")
	}
	client, err := f.AuthorizationFor(clusterID, token)
	if err != nil {
		return err
	}
	accessReview, err := client.SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{
			Group: databricksv1alpha1.GroupName, Version: databricksv1alpha1.Version,
			Resource: "tables", Subresource: action, Name: name, Verb: "create",
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("authorize table action verb: %w", err)
	}
	if !accessReview.Status.Allowed {
		reason := strings.TrimSpace(accessReview.Status.Reason)
		if reason == "" {
			reason = "caller is not granted this action on the imported table"
		}
		return apierrors.NewForbidden(databricksv1alpha1.SchemeGroupVersion.WithResource("tables").GroupResource(), name, fmt.Errorf("%s", reason))
	}
	return nil
}

var _ TableAuthorizer = (*ClientFactory)(nil)
var _ actions.QueryExecutor = (*ActionExecutor)(nil)

type actionFailureMetadata interface {
	ActionFailureCode() string
	ActionFailureMessage() string
	ActionFailureStatus() int
	ActionFailureRetryable() bool
}

func gatewayFailureStatus(retryable bool) int {
	if retryable {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}

func sanitizeActionError(err error) error {
	if err == nil {
		return nil
	}
	var typed *actions.ActionError
	if errors.As(err, &typed) && typed != nil {
		copy := *typed
		if copy.Code == actions.ActionErrorCodeBackendFailure || copy.Code == actions.ActionErrorCodeActionFailed {
			copy.Status = gatewayFailureStatus(copy.Retryable)
		} else if copy.Status < http.StatusBadRequest || copy.Status > 599 {
			copy.Status = http.StatusBadGateway
		}
		return &copy
	}
	var metadata actionFailureMetadata
	if errors.As(err, &metadata) && metadata != nil {
		code := strings.TrimSpace(metadata.ActionFailureCode())
		message := strings.TrimSpace(metadata.ActionFailureMessage())
		status := metadata.ActionFailureStatus()
		if code == "" {
			code = actions.ActionErrorCodeBackendFailure
		}
		if message == "" || len(message) > 512 {
			message = "databricks action failed"
		}
		if code == actions.ActionErrorCodeBackendFailure || code == actions.ActionErrorCodeActionFailed {
			status = gatewayFailureStatus(metadata.ActionFailureRetryable())
		} else if status < http.StatusBadRequest || status > 599 {
			status = http.StatusBadGateway
		}
		return &actions.ActionError{Code: code, Message: message, Status: status, Retryable: metadata.ActionFailureRetryable(), Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &actions.ActionError{
			Code: actions.ActionErrorCodeActionTimeout, Message: "databricks action timed out",
			Retryable: true, Status: http.StatusGatewayTimeout, Cause: err,
		}
	}
	if errors.Is(err, context.Canceled) {
		return &actions.ActionError{
			Code: actions.ActionErrorCodeActionTimeout, Message: "databricks action was canceled",
			Retryable: true, Status: http.StatusGatewayTimeout, Cause: err,
		}
	}
	var validation *queryapi.ValidationError
	if errors.As(err, &validation) && validation != nil && validation.Code == queryapi.ErrorCodeSchemaProjectionInvalid {
		message := strings.TrimSpace(validation.Message)
		if message == "" || len(message) > 512 {
			message = "requested projection is not present in the imported table schema"
		}
		return &actions.ActionError{
			Code: actions.ActionErrorCodeSchemaProjectionInvalid, Message: message,
			Status: http.StatusBadRequest, Cause: err,
		}
	}
	if apierrors.IsNotFound(err) {
		return &actions.ActionError{
			Code: actions.ActionErrorCodeResourceNotFound, Message: "bound Databricks resource was not found",
			Status: http.StatusNotFound, Cause: err,
		}
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return &actions.ActionError{
			Code: actions.ActionErrorCodeResourceForbidden, Message: "caller is not allowed to read the bound Databricks resource",
			Status: http.StatusForbidden, Cause: err,
		}
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(message, "not ready") || strings.Contains(message, "not validated") ||
		strings.Contains(message, "schema is not available") || strings.Contains(message, "unsupported") {
		return &actions.ActionError{
			Code: actions.ActionErrorCodeResourceNotReady, Message: "bound Databricks resource is not ready",
			Status: http.StatusConflict, Cause: err,
		}
	}
	// Do not carry arbitrary backend text across the provider boundary. The
	// action handler will retain this typed code/message while the hub performs
	// its own allowlist and envelope identity checks.
	return &actions.ActionError{
		Code: actions.ActionErrorCodeBackendFailure, Message: "databricks action failed",
		Status: http.StatusBadGateway, Cause: err,
	}
}
