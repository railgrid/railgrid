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
	"os"
	"strings"
	"time"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/queryapi"
)

const (
	currentUserPath                  = "/api/2.0/current-user/me"
	warehousePathPrefix              = "/api/2.0/sql/warehouses/"
	allowedHostSuffixesEnv           = "DATABRICKS_ALLOWED_HOST_SUFFIXES"
	defaultAllowedWorkspaceHostError = "not an allowed Databricks workspace host"
)

var defaultAllowedWorkspaceHostSuffixes = []string{
	"cloud.databricks.com",
	"gcp.databricks.com",
	"azuredatabricks.net",
}

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
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
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
// and returns the Databricks-reported state for kedge status.
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
			status: resp.Status,
			body:   strings.TrimSpace(string(body)),
		}
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return WarehouseValidationResult{}, fmt.Errorf("decode warehouse response: %w", err)
	}
	return WarehouseValidationResult{
		Name:  firstString(payload, "name"),
		State: firstString(payload, "state"),
	}, nil
}

// ValidateTable checks that the token can describe the configured table through
// the referenced SQL warehouse and returns schema columns for status caching.
func (c StatementClient) ValidateTable(ctx context.Context, target TableValidationTarget) (TableValidationResult, error) {
	sql, err := queryapi.DescribeTableSQL(target.Table)
	if err != nil {
		return TableValidationResult{}, err
	}
	result, err := c.executeStatement(ctx, target, sql)
	if err != nil {
		return TableValidationResult{}, err
	}
	columns := columnsFromDescribeRows(rowsFromStatement(result))
	if len(columns) == 0 {
		return TableValidationResult{}, fmt.Errorf("databricks table describe returned no columns")
	}
	return TableValidationResult{Columns: columns}, nil
}

func columnsFromDescribeRows(rows []map[string]any) []databricksv1alpha1.Column {
	columns := make([]databricksv1alpha1.Column, 0, len(rows))
	for _, row := range rows {
		name := rowString(row, "col_name")
		if name == "" || strings.HasPrefix(name, "#") {
			break
		}
		typ := rowString(row, "data_type")
		if typ == "" {
			continue
		}
		columns = append(columns, databricksv1alpha1.Column{
			Name:    name,
			Type:    typ,
			Comment: rowString(row, "comment"),
		})
	}
	return columns
}

func rowString(row map[string]any, key string) string {
	value, ok := row[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
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
	for _, suffix := range workspaceHostSuffixes(configured) {
		if hostname == suffix || strings.HasSuffix(hostname, "."+suffix) {
			return true
		}
	}
	return false
}

func workspaceHostSuffixes(configured []string) []string {
	if len(configured) == 0 {
		configured = splitCSV(os.Getenv(allowedHostSuffixesEnv))
	}
	if len(configured) == 0 {
		configured = defaultAllowedWorkspaceHostSuffixes
	}
	out := make([]string, 0, len(configured))
	for _, suffix := range configured {
		suffix = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(suffix)), ".")
		if suffix != "" {
			out = append(out, suffix)
		}
	}
	return out
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
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

func (e currentUserHTTPError) Error() string {
	if e.body == "" {
		return "databricks credential validation failed: " + e.status
	}
	return fmt.Sprintf("databricks credential validation failed: %s: %s", e.status, e.body)
}

func (e currentUserHTTPError) SafeStatusMessage() string {
	return "databricks credential validation failed: " + e.status
}

type warehouseHTTPError struct {
	status string
	body   string
}

func (e warehouseHTTPError) Error() string {
	if e.body == "" {
		return "databricks warehouse validation failed: " + e.status
	}
	return fmt.Sprintf("databricks warehouse validation failed: %s: %s", e.status, e.body)
}

func (e warehouseHTTPError) SafeStatusMessage() string {
	return "databricks warehouse validation failed: " + e.status
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
