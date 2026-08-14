// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/hostpolicy"
	"github.com/faroshq/provider-databricks/queryapi"
)

const (
	currentUserPath                  = "/api/2.0/current-user/me"
	warehousePathPrefix              = "/api/2.0/sql/warehouses/"
	tableSummariesPath               = "/api/2.1/unity-catalog/table-summaries"
	defaultAllowedWorkspaceHostError = "not an allowed Databricks workspace host"
)

// ConnectionValidator validates tenant-authored Databricks Connection
// resources without exposing the referenced credential outside the provider.
type ConnectionValidator interface {
	ValidateConnection(context.Context, ConnectionValidationTarget) (ConnectionValidationResult, error)
}

// WarehouseValidator validates tenant-authored Databricks Warehouse resources.
type WarehouseValidator interface {
	ValidateWarehouse(context.Context, WarehouseValidationTarget) (WarehouseValidationResult, error)
}

// TableValidator validates tenant-authored Databricks Table resources and
// returns schema metadata safe to cache on status.
type TableValidator interface {
	ValidateTable(context.Context, TableValidationTarget) (TableValidationResult, error)
}

// Validator is the provider's Databricks validation surface used by
// controllers. It is intentionally narrower than general statement execution.
type Validator interface {
	ConnectionValidator
	WarehouseValidator
	TableValidator
}

type ConnectionValidationTarget struct {
	Host        string
	AuthType    databricksv1alpha1.ConnectionAuthType
	BearerToken string
}

type ConnectionValidationResult struct {
	Principal   string
	WorkspaceID string
}

type WarehouseValidationTarget struct {
	Host        string
	WarehouseID string
	BearerToken string
}

type WarehouseValidationResult struct {
	Name  string
	State string
}

type TableValidationTarget = queryapi.TableTarget

type TableValidationResult struct {
	Columns []databricksv1alpha1.Column
}

// UnsupportedTableTypeError reports a Databricks table type that cannot be
// validated through the provider's bounded query_table/v1 contract.
type UnsupportedTableTypeError struct {
	TableType string
}

func (e UnsupportedTableTypeError) Error() string { return e.SafeStatusMessage() }

func (e UnsupportedTableTypeError) SafeStatusMessage() string {
	tableType := strings.ToUpper(strings.TrimSpace(e.TableType))
	if tableType == "" {
		tableType = "UNKNOWN"
	}
	return fmt.Sprintf("Databricks table type %q is not supported by query_table/v1; use a standard table or view", tableType)
}

type statusSafeError interface {
	SafeStatusMessage() string
}

var _ Validator = StatementClient{}
var _ Validator = Stub{}

func SafeStatusMessage(err error) string {
	if err == nil {
		return ""
	}
	var safe statusSafeError
	if errors.As(err, &safe) {
		if msg := strings.TrimSpace(safe.SafeStatusMessage()); msg != "" {
			return msg
		}
	}
	var statusErr httpStatusCoder
	if errors.As(err, &statusErr) {
		return fmt.Sprintf("databricks validation failed: HTTP %d", statusErr.HTTPStatusCode())
	}
	return err.Error()
}

// ValidateConnection performs the lightest useful PAT check: call Databricks'
// current-user endpoint with the token. Warehouse/table authorization is
// validated by their own resources.
func (c StatementClient) ValidateConnection(ctx context.Context, target ConnectionValidationTarget) (ConnectionValidationResult, error) {
	if target.AuthType != databricksv1alpha1.ConnectionAuthPAT {
		return ConnectionValidationResult{}, fmt.Errorf("unsupported authType %q", target.AuthType)
	}
	if strings.TrimSpace(target.BearerToken) == "" {
		return ConnectionValidationResult{}, fmt.Errorf("databricks bearer token is required")
	}
	endpoints, err := c.currentUserEndpoints(target.Host)
	if err != nil {
		return ConnectionValidationResult{}, err
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var notFoundErr error
	for _, endpoint := range endpoints {
		result, err := c.validateCurrentUser(ctx, client, endpoint, target.BearerToken)
		if err == nil {
			return result, nil
		}
		if isEndpointNotFound(err) {
			notFoundErr = err
			continue
		}
		return ConnectionValidationResult{}, err
	}
	if notFoundErr != nil {
		return ConnectionValidationResult{}, notFoundErr
	}
	return ConnectionValidationResult{}, fmt.Errorf("databricks current-user endpoint unavailable")
}

func (c StatementClient) validateCurrentUser(ctx context.Context, client *http.Client, endpoint, token string) (ConnectionValidationResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ConnectionValidationResult{}, fmt.Errorf("build current-user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ConnectionValidationResult{}, fmt.Errorf("validate databricks credential: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ConnectionValidationResult{}, currentUserHTTPError{
			statusCode: resp.StatusCode,
			status:     resp.Status,
			body:       strings.TrimSpace(string(body)),
		}
	}
	var payload map[string]any
	if err := decodeBoundedJSON(resp.Body, maxMetadataResponseBytes, &payload); err != nil {
		return ConnectionValidationResult{}, fmt.Errorf("decode current-user response: %w", err)
	}
	return ConnectionValidationResult{
		Principal: firstString(payload,
			"userName",
			"username",
			"user_name",
			"email",
			"displayName",
			"id",
		),
		WorkspaceID: firstString(payload, "workspace_id", "workspaceId", "workspaceID"),
	}, nil
}

// ValidateWarehouse checks that the token can see the configured SQL warehouse
// and returns the Databricks-reported state for faros status.
func (c StatementClient) ValidateWarehouse(ctx context.Context, target WarehouseValidationTarget) (WarehouseValidationResult, error) {
	if strings.TrimSpace(target.BearerToken) == "" {
		return WarehouseValidationResult{}, fmt.Errorf("databricks bearer token is required")
	}
	if strings.TrimSpace(target.WarehouseID) == "" {
		return WarehouseValidationResult{}, fmt.Errorf("databricks warehouse_id is required")
	}
	endpoint, err := c.warehouseEndpoint(target.Host, target.WarehouseID)
	if err != nil {
		return WarehouseValidationResult{}, err
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return WarehouseValidationResult{}, fmt.Errorf("build warehouse request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+target.BearerToken)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return WarehouseValidationResult{}, fmt.Errorf("validate databricks warehouse: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return WarehouseValidationResult{}, warehouseHTTPError{
			status:      resp.Status,
			statusCode:  resp.StatusCode,
			body:        strings.TrimSpace(string(body)),
			warehouseID: strings.TrimSpace(target.WarehouseID),
		}
	}
	var payload map[string]any
	if err := decodeBoundedJSON(resp.Body, maxMetadataResponseBytes, &payload); err != nil {
		return WarehouseValidationResult{}, fmt.Errorf("decode warehouse response: %w", err)
	}
	return WarehouseValidationResult{
		Name:  firstString(payload, "name"),
		State: firstString(payload, "state"),
	}, nil
}

// ValidateTable classifies the configured Unity Catalog object, then asks the
// referenced SQL warehouse for a zero-row SELECT manifest. The manifest is the
// same schema source used by query execution, so validation and query_table
// cannot silently disagree about the table's columns.
func (c StatementClient) ValidateTable(ctx context.Context, target TableValidationTarget) (TableValidationResult, error) {
	summary, err := c.getTableSummary(ctx, target)
	if err != nil {
		return TableValidationResult{}, err
	}
	if strings.EqualFold(strings.TrimSpace(summary.TableType), "METRIC_VIEW") {
		return TableValidationResult{}, UnsupportedTableTypeError{TableType: summary.TableType}
	}
	sql, err := queryapi.TableSchemaProbeSQL(target.Table)
	if err != nil {
		return TableValidationResult{}, err
	}
	result, err := c.executeStatement(ctx, target, sql)
	if err != nil {
		return TableValidationResult{}, err
	}
	columns := columnsFromStatementManifest(result)
	if len(columns) == 0 {
		return TableValidationResult{}, fmt.Errorf("databricks table schema probe returned no columns")
	}
	return TableValidationResult{Columns: columns}, nil
}

type tableSummary struct {
	FullName  string `json:"full_name"`
	TableType string `json:"table_type"`
}

type tableSummariesResponse struct {
	Tables        []tableSummary `json:"tables"`
	NextPageToken string         `json:"next_page_token"`
}

func (c StatementClient) getTableSummary(ctx context.Context, target TableValidationTarget) (tableSummary, error) {
	if strings.TrimSpace(target.Credential.BearerToken) == "" {
		return tableSummary{}, fmt.Errorf("databricks bearer token is required")
	}
	endpoint, fullName, err := c.tableSummariesEndpoint(target.Connection.Host, target.Table)
	if err != nil {
		return tableSummary{}, err
	}
	pageURL, err := url.Parse(endpoint)
	if err != nil {
		return tableSummary{}, fmt.Errorf("parse table summary endpoint: %w", err)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	seenTokens := make(map[string]struct{})
	pageToken := ""
	for page := 0; page < 100; page++ {
		query := pageURL.Query()
		if pageToken == "" {
			query.Del("page_token")
		} else {
			query.Set("page_token", pageToken)
		}
		pageURL.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL.String(), nil)
		if err != nil {
			return tableSummary{}, fmt.Errorf("build table summary request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+target.Credential.BearerToken)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return tableSummary{}, fmt.Errorf("get databricks table summary: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return tableSummary{}, tableSummaryHTTPError{statusCode: resp.StatusCode, status: resp.Status}
		}
		var summaries tableSummariesResponse
		decodeErr := decodeBoundedJSON(resp.Body, maxMetadataResponseBytes, &summaries)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return tableSummary{}, fmt.Errorf("decode table summary response: %w", decodeErr)
		}
		for _, summary := range summaries.Tables {
			if summary.FullName == fullName {
				return summary, nil
			}
		}
		if summaries.NextPageToken == "" {
			return tableSummary{}, tableSummaryHTTPError{statusCode: http.StatusNotFound, status: "404 Not Found"}
		}
		if _, repeated := seenTokens[summaries.NextPageToken]; repeated {
			return tableSummary{}, fmt.Errorf("databricks table summary pagination returned a repeated page token")
		}
		seenTokens[summaries.NextPageToken] = struct{}{}
		pageToken = summaries.NextPageToken
	}
	return tableSummary{}, fmt.Errorf("databricks table summary pagination exceeded 100 pages")
}

func columnsFromStatementManifest(resp statementResponse) []databricksv1alpha1.Column {
	columns := make([]databricksv1alpha1.Column, 0, len(resp.Manifest.Schema.Columns))
	for _, column := range resp.Manifest.Schema.Columns {
		name := strings.TrimSpace(column.Name)
		typ := strings.TrimSpace(column.TypeText)
		if typ == "" {
			typ = strings.TrimSpace(column.TypeName)
		}
		if name == "" || typ == "" {
			continue
		}
		columns = append(columns, databricksv1alpha1.Column{Name: name, Type: typ})
	}
	return columns
}

func currentUserEndpoints(host string) ([]string, error) {
	return StatementClient{}.currentUserEndpoints(host)
}

func (c StatementClient) currentUserEndpoints(host string) ([]string, error) {
	u, err := c.workspaceURL(host)
	if err != nil {
		return nil, err
	}
	basePath := strings.TrimRight(u.Path, "/")
	u.Path = basePath + currentUserPath
	primary := u.String()
	u.Path = basePath + "/api/2.0/preview/scim/v2/Me"
	return []string{primary, u.String()}, nil
}

func warehouseEndpoint(host, warehouseID string) (string, error) {
	return StatementClient{}.warehouseEndpoint(host, warehouseID)
}

func (c StatementClient) warehouseEndpoint(host, warehouseID string) (string, error) {
	u, err := c.workspaceURL(host)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + warehousePathPrefix + url.PathEscape(strings.TrimSpace(warehouseID))
	return u.String(), nil
}

func (c StatementClient) tableSummariesEndpoint(host string, ref queryapi.TableRef) (string, string, error) {
	if err := validateTableRef(ref); err != nil {
		return "", "", err
	}
	u, err := c.workspaceURL(host)
	if err != nil {
		return "", "", err
	}
	fullName := strings.Join([]string{ref.Catalog, ref.Schema, ref.Table}, ".")
	u.Path = strings.TrimRight(u.Path, "/") + tableSummariesPath
	query := u.Query()
	query.Set("catalog_name", ref.Catalog)
	query.Set("schema_name_pattern", escapeSQLLikeLiteral(ref.Schema))
	query.Set("table_name_pattern", escapeSQLLikeLiteral(ref.Table))
	query.Set("max_results", "1")
	query.Set("include_manifest_capabilities", "false")
	u.RawQuery = query.Encode()
	return u.String(), fullName, nil
}

func validateTableRef(ref queryapi.TableRef) error {
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "catalog", value: ref.Catalog},
		{label: "schema", value: ref.Schema},
		{label: "table", value: ref.Table},
	} {
		if err := queryapi.ValidateIdentifier(field.value); err != nil {
			return fmt.Errorf("%s %q: %w", field.label, field.value, err)
		}
	}
	return nil
}

func escapeSQLLikeLiteral(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func (c StatementClient) workspaceURL(host string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(strings.TrimRight(host, "/")))
	if err != nil {
		return nil, fmt.Errorf("parse databricks host %q: %w", host, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("databricks host %q must include scheme and host", host)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("databricks host %q must not include user info, query, or fragment", host)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("databricks host %q must be a workspace root URL", host)
	}
	hostname := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if hostname == "" {
		return nil, fmt.Errorf("databricks host %q must include host name", host)
	}
	port := u.Port()
	allowLoopbackHost := c.AllowInsecureWorkspaceHost && isLoopbackHost(hostname)
	if !c.schemeAllowed(u.Scheme, hostname) {
		return nil, fmt.Errorf("databricks host %q must use https", host)
	}
	if !allowLoopbackHost && !allowedWorkspaceHost(hostname, c.AllowedWorkspaceHostSuffixes) {
		return nil, fmt.Errorf("databricks host %q: %s", host, defaultAllowedWorkspaceHostError)
	}
	if port != "" && port != "443" && !allowLoopbackHost {
		return nil, fmt.Errorf("databricks host %q must use the default https port", host)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = hostname
	if port != "" {
		u.Host = net.JoinHostPort(hostname, port)
	}
	u.Path = ""
	return u, nil
}

func (c StatementClient) schemeAllowed(scheme, hostname string) bool {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "https" {
		return true
	}
	return c.AllowInsecureWorkspaceHost && scheme == "http" && isLoopbackHost(hostname)
}

func allowedWorkspaceHost(hostname string, configured []string) bool {
	return hostpolicy.AllowedWorkspaceHost(hostname, configured)
}

func isLoopbackHost(hostname string) bool {
	if hostname == "localhost" {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

type currentUserHTTPError struct {
	statusCode int
	status     string
	body       string
}

func (e currentUserHTTPError) HTTPStatusCode() int { return e.statusCode }

func (e currentUserHTTPError) Error() string {
	return "databricks credential validation failed: " + e.status
}

func (e currentUserHTTPError) SafeStatusMessage() string {
	return "databricks credential validation failed: " + e.status
}

type warehouseHTTPError struct {
	status      string
	statusCode  int
	body        string
	warehouseID string
}

type tableSummaryHTTPError struct {
	statusCode int
	status     string
}

func (e tableSummaryHTTPError) HTTPStatusCode() int { return e.statusCode }

func (e tableSummaryHTTPError) Error() string { return e.SafeStatusMessage() }

func (e tableSummaryHTTPError) SafeStatusMessage() string {
	return "databricks table summary validation failed: " + e.status
}

func (e warehouseHTTPError) HTTPStatusCode() int { return e.statusCode }

func (e warehouseHTTPError) Error() string {
	return e.SafeStatusMessage()
}

func (e warehouseHTTPError) SafeStatusMessage() string {
	message := "databricks warehouse validation failed: " + e.status
	if e.statusCode == http.StatusNotFound {
		message += " — no SQL warehouse with this ID in the workspace. The warehouse ID is the 16-character hex value from SQL Warehouses → Connection details (/sql/1.0/warehouses/<id>)"
		if warehouseIDLooksLikeOrgID(e.warehouseID) {
			message += "; a purely numeric value is usually the workspace org ID from the ?o= URL parameter, not a warehouse ID"
		}
	}
	return message
}

// warehouseIDLooksLikeOrgID reports whether the value is purely numeric —
// the shape of the ?o= workspace org ID users paste by mistake, as opposed to
// the 16-character hex of a real SQL warehouse ID.
func warehouseIDLooksLikeOrgID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isEndpointNotFound(err error) bool {
	var httpErr currentUserHTTPError
	return errors.As(err, &httpErr) && httpErr.statusCode == http.StatusNotFound
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := values[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case json.Number:
			return value.String()
		case float64:
			return fmt.Sprintf("%.0f", value)
		case int:
			return fmt.Sprint(value)
		case int64:
			return fmt.Sprint(value)
		}
	}
	return ""
}

func (Stub) ValidateConnection(_ context.Context, target ConnectionValidationTarget) (ConnectionValidationResult, error) {
	if strings.TrimSpace(target.Host) == "" {
		return ConnectionValidationResult{}, fmt.Errorf("databricks connection host is required")
	}
	if strings.TrimSpace(target.BearerToken) == "" {
		return ConnectionValidationResult{}, fmt.Errorf("databricks bearer token is required")
	}
	return ConnectionValidationResult{Principal: "stub", WorkspaceID: "stub"}, nil
}

func (Stub) ValidateWarehouse(_ context.Context, target WarehouseValidationTarget) (WarehouseValidationResult, error) {
	if strings.TrimSpace(target.Host) == "" {
		return WarehouseValidationResult{}, fmt.Errorf("databricks connection host is required")
	}
	if strings.TrimSpace(target.WarehouseID) == "" {
		return WarehouseValidationResult{}, fmt.Errorf("databricks warehouse_id is required")
	}
	if strings.TrimSpace(target.BearerToken) == "" {
		return WarehouseValidationResult{}, fmt.Errorf("databricks bearer token is required")
	}
	return WarehouseValidationResult{Name: target.WarehouseID, State: "RUNNING"}, nil
}

func (Stub) ValidateTable(_ context.Context, target TableValidationTarget) (TableValidationResult, error) {
	if strings.TrimSpace(target.Connection.Host) == "" {
		return TableValidationResult{}, fmt.Errorf("databricks connection host is required")
	}
	if strings.TrimSpace(target.Warehouse.WarehouseID) == "" {
		return TableValidationResult{}, fmt.Errorf("databricks warehouse_id is required")
	}
	if strings.TrimSpace(target.Credential.BearerToken) == "" {
		return TableValidationResult{}, fmt.Errorf("databricks bearer token is required")
	}
	if strings.TrimSpace(target.Table.Table) == "" {
		return TableValidationResult{}, fmt.Errorf("databricks table is required")
	}
	return TableValidationResult{Columns: []databricksv1alpha1.Column{
		{Name: "order_id", Type: "STRING", Comment: "Stub order identifier"},
		{Name: "total_amount", Type: "DOUBLE"},
	}}, nil
}
