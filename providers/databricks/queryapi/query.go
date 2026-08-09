// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package queryapi exposes provider-owned Databricks table metadata helpers.
package queryapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	// ActionVersionV1 is the versioned MCP/provider action contract. Callers
	// must send it explicitly so a future contract can be selected safely.
	ActionVersionV1 = "v1"
	// DefaultQueryLimit and MaxQueryLimit intentionally have the same fixed
	// value: omitted limits are bounded and callers cannot raise the cap.
	DefaultQueryLimit = 100
	MaxQueryLimit     = 100
	MaxQueryRows      = 100
	MaxQueryColumns   = 64
	MaxQueryBytes     = 64 * 1024
	// ErrorCodeSchemaProjectionInvalid identifies a bounded projection that
	// cannot be satisfied by the imported Table schema. It is safe to expose
	// through the Provider Actions error envelope.
	ErrorCodeSchemaProjectionInvalid = "schema_projection_invalid"
)

var identifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var tableRefRE = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

type TableRef struct {
	Catalog string `json:"catalog"`
	Schema  string `json:"schema"`
	Table   string `json:"table"`
}

type ConnectionRef struct {
	Name     string `json:"name,omitempty"`
	Host     string `json:"host"`
	AuthType string `json:"authType"`
}

type WarehouseRef struct {
	Name        string `json:"name,omitempty"`
	WarehouseID string `json:"warehouseID"`
}

type Credential struct {
	BearerToken string `json:"-"`
}

type TableTarget struct {
	Table      TableRef      `json:"table"`
	Connection ConnectionRef `json:"connection"`
	Warehouse  WarehouseRef  `json:"warehouse"`
	Credential Credential    `json:"-"`
}

// QueryTableRequest is the public, typed v1 query_table contract. It contains
// only a Table resource name and bounded projection controls; callers cannot
// provide SQL, warehouse IDs, connection hosts, or credentials.
type QueryTableRequest struct {
	ActionVersion string
	TableRef      string
	Columns       []string
	Limit         int
}

// ValidationError is a typed, safe query validation failure. It intentionally
// contains no SQL, backend target, credential, or tenant information.
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "query validation failed"
	}
	return e.Message
}

type QueryColumn struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type QueryTableResult struct {
	ActionVersion string           `json:"actionVersion"`
	TableRef      string           `json:"tableRef"`
	Columns       []QueryColumn    `json:"columns"`
	Rows          []map[string]any `json:"rows"`
	Truncated     bool             `json:"truncated,omitempty"`
}

// BoundQueryResult enforces the provider's row, column, and serialized-byte
// limits at every boundary (backend, tenant adapter, and MCP output).
func BoundQueryResult(result QueryTableResult) QueryTableResult {
	if len(result.Columns) > MaxQueryColumns {
		result.Columns = result.Columns[:MaxQueryColumns]
		result.Truncated = true
	}
	if len(result.Rows) > MaxQueryRows {
		result.Rows = result.Rows[:MaxQueryRows]
		result.Truncated = true
	}
	used := 0
	rows := result.Rows[:0]
	for _, row := range result.Rows {
		encoded, err := json.Marshal(row)
		if err != nil || used+len(encoded) > MaxQueryBytes {
			result.Truncated = true
			break
		}
		used += len(encoded)
		rows = append(rows, row)
	}
	result.Rows = rows
	return result
}

func ValidateActionVersion(version string) error {
	if strings.TrimSpace(version) != ActionVersionV1 {
		return fmt.Errorf("unsupported actionVersion %q", version)
	}
	return nil
}

func ValidateTableRef(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 253 || !tableRefRE.MatchString(name) {
		return fmt.Errorf("invalid tableRef")
	}
	return nil
}

// NormalizeQueryRequest validates the typed action and applies the fixed
// default limit. No SQL text is accepted by this contract.
func NormalizeQueryRequest(in QueryTableRequest) (QueryTableRequest, error) {
	if err := ValidateActionVersion(in.ActionVersion); err != nil {
		return QueryTableRequest{}, err
	}
	in.TableRef = strings.TrimSpace(in.TableRef)
	if err := ValidateTableRef(in.TableRef); err != nil {
		return QueryTableRequest{}, err
	}
	if in.Limit == 0 {
		in.Limit = DefaultQueryLimit
	}
	if in.Limit < 1 || in.Limit > MaxQueryLimit {
		return QueryTableRequest{}, fmt.Errorf("limit must be between 1 and %d", MaxQueryLimit)
	}
	seen := make(map[string]struct{}, len(in.Columns))
	for i, column := range in.Columns {
		column = strings.TrimSpace(column)
		if _, err := quoteIdent(column); err != nil {
			return QueryTableRequest{}, fmt.Errorf("columns[%d]: invalid identifier", i)
		}
		if _, ok := seen[column]; ok {
			return QueryTableRequest{}, fmt.Errorf("columns[%d]: duplicate identifier", i)
		}
		seen[column] = struct{}{}
		in.Columns[i] = column
	}
	return in, nil
}

func DescribeTableSQL(ref TableRef) (string, error) {
	from, err := qualifiedTable(ref)
	if err != nil {
		return "", err
	}
	return "DESCRIBE TABLE " + from, nil
}

// SelectTableSQL constructs the only statement accepted by query_table. The
// projection is quoted identifier-by-identifier and the table is always the
// provider-resolved Table target. allowedColumns is the cached Table schema;
// when present, every requested column must be in that allowlist.
func SelectTableSQL(ref TableRef, projection []string, limit int, allowedColumns []string) (string, error) {
	if limit == 0 {
		limit = DefaultQueryLimit
	}
	if limit < 1 || limit > MaxQueryLimit {
		return "", fmt.Errorf("limit must be between 1 and %d", MaxQueryLimit)
	}
	from, err := qualifiedTable(ref)
	if err != nil {
		return "", err
	}
	allowed := make(map[string]struct{}, len(allowedColumns))
	for _, column := range allowedColumns {
		if strings.TrimSpace(column) != "" {
			allowed[column] = struct{}{}
		}
	}
	selectList := "*"
	if len(projection) > 0 {
		parts := make([]string, 0, len(projection))
		seen := make(map[string]struct{}, len(projection))
		for i, column := range projection {
			column = strings.TrimSpace(column)
			if _, err := quoteIdent(column); err != nil {
				return "", fmt.Errorf("columns[%d]: invalid identifier", i)
			}
			if len(allowed) > 0 {
				if _, ok := allowed[column]; !ok {
					return "", &ValidationError{
						Code:    ErrorCodeSchemaProjectionInvalid,
						Message: fmt.Sprintf("requested column %q is not present in the imported table schema", column),
					}
				}
			}
			if _, ok := seen[column]; ok {
				return "", fmt.Errorf("column %q is selected more than once", column)
			}
			seen[column] = struct{}{}
			quoted, _ := quoteIdent(column)
			parts = append(parts, quoted)
		}
		selectList = strings.Join(parts, ", ")
	}
	return fmt.Sprintf("SELECT %s FROM %s LIMIT %d", selectList, from, limit), nil
}

func qualifiedTable(ref TableRef) (string, error) {
	catalog, err := quoteIdent(ref.Catalog)
	if err != nil {
		return "", fmt.Errorf("catalog %q: %w", ref.Catalog, err)
	}
	schema, err := quoteIdent(ref.Schema)
	if err != nil {
		return "", fmt.Errorf("schema %q: %w", ref.Schema, err)
	}
	table, err := quoteIdent(ref.Table)
	if err != nil {
		return "", fmt.Errorf("table %q: %w", ref.Table, err)
	}
	return catalog + "." + schema + "." + table, nil
}

func quoteIdent(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !identifierRE.MatchString(value) {
		return "", fmt.Errorf("invalid identifier")
	}
	return "`" + value + "`", nil
}
