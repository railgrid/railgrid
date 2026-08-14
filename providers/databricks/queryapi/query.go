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
	"unicode"
	"unicode/utf8"
)

const (
	// ActionVersionV1 is the versioned MCP/provider action contract. Callers
	// must send it explicitly so a future contract can be selected safely.
	ActionVersionV1 = "v1"
	// DefaultQueryLimit and MaxQueryLimit intentionally have the same fixed
	// value: omitted limits are bounded and callers cannot raise the cap.
	DefaultQueryLimit   = 100
	MaxQueryLimit       = 100
	MaxQueryRows        = 100
	MaxQueryColumns     = 64
	MaxIdentifierLength = 255
	// MaxQueryBytes is the serialized result budget. It applies to the result
	// object itself and is also used by the action envelope to keep the complete
	// wire response bounded.
	MaxQueryBytes = 64 * 1024
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

// NormalizeQueryControls applies the one input contract shared by HTTP
// actions, MCP, and the tenant executor. The returned slice is a copy so a
// caller cannot mutate a validated request after it crosses a boundary.
func NormalizeQueryControls(columns []string, limit int) ([]string, int, error) {
	if limit == 0 {
		limit = DefaultQueryLimit
	}
	if limit < 1 || limit > MaxQueryLimit {
		return nil, 0, fmt.Errorf("limit must be between 1 and %d", MaxQueryLimit)
	}
	if len(columns) > MaxQueryColumns {
		return nil, 0, fmt.Errorf("columns must contain at most %d entries", MaxQueryColumns)
	}
	normalized := make([]string, len(columns))
	seen := make(map[string]struct{}, len(columns))
	for i, column := range columns {
		if _, err := quoteIdent(column); err != nil {
			return nil, 0, fmt.Errorf("columns[%d]: invalid identifier", i)
		}
		if _, ok := seen[column]; ok {
			return nil, 0, fmt.Errorf("columns[%d]: duplicate identifier", i)
		}
		seen[column] = struct{}{}
		normalized[i] = column
	}
	return normalized, limit, nil
}

// BoundQueryResult enforces the provider's row, column, and serialized-byte
// limits at every boundary (backend, tenant adapter, and MCP output). Rows are
// rebuilt from the final column list, so every row has exactly the same keys.
// It is intentionally infallible for compatibility with existing callers; an
// unmarshalable row is discarded and marks the result truncated.
func BoundQueryResult(result QueryTableResult) QueryTableResult {
	return BoundQueryResultWithin(result, MaxQueryBytes)
}

// BoundQueryResultWithin applies the same shape and row/column limits as
// BoundQueryResult with a caller-provided serialized byte budget. Protocol
// adapters that duplicate or wrap this JSON should measure their final wire
// envelope as well; this helper only bounds the result object itself.
func BoundQueryResultWithin(result QueryTableResult, maxBytes int) QueryTableResult {
	if maxBytes < 1 || maxBytes > MaxQueryBytes {
		maxBytes = MaxQueryBytes
	}
	result = normalizeResultShape(result)
	for {
		encoded, err := json.Marshal(result)
		if err == nil && len(encoded) <= maxBytes {
			return result
		}
		if len(result.Rows) > 0 {
			result.Rows = result.Rows[:len(result.Rows)-1]
			result.Truncated = true
			continue
		}
		if len(result.Columns) > 0 {
			result.Columns = result.Columns[:len(result.Columns)-1]
			result.Rows = rowsForColumns(result.Rows, result.Columns)
			result.Truncated = true
			continue
		}
		// The fixed metadata fields are bounded by the request contract. If a
		// future caller violates that contract, return the smallest valid result
		// rather than emitting an unbounded or invalid response.
		return QueryTableResult{
			ActionVersion: ActionVersionV1,
			TableRef:      truncateString(result.TableRef, 253),
			Columns:       []QueryColumn{},
			Rows:          []map[string]any{},
			Truncated:     true,
		}
	}
}

func normalizeResultShape(result QueryTableResult) QueryTableResult {
	columns := make([]QueryColumn, 0, len(result.Columns))
	seenColumns := make(map[string]struct{}, len(result.Columns))
	for _, column := range result.Columns {
		if _, ok := seenColumns[column.Name]; ok {
			result.Truncated = true
			continue
		}
		seenColumns[column.Name] = struct{}{}
		columns = append(columns, column)
	}
	result.Columns = columns
	result.Rows = append([]map[string]any(nil), result.Rows...)
	if len(result.Columns) > MaxQueryColumns {
		result.Columns = result.Columns[:MaxQueryColumns]
		result.Truncated = true
	}
	if len(result.Rows) > MaxQueryRows {
		result.Rows = result.Rows[:MaxQueryRows]
		result.Truncated = true
	}
	allowed := make(map[string]struct{}, len(result.Columns))
	for _, column := range result.Columns {
		allowed[column.Name] = struct{}{}
	}
	for _, row := range result.Rows {
		for key := range row {
			if _, ok := allowed[key]; !ok {
				result.Truncated = true
				break
			}
		}
	}
	result.Rows = rowsForColumns(result.Rows, result.Columns)
	return result
}

func rowsForColumns(rows []map[string]any, columns []QueryColumn) []map[string]any {
	if len(rows) == 0 {
		return []map[string]any{}
	}
	keys := make([]string, 0, len(columns))
	seen := make(map[string]struct{}, len(columns))
	for i := range columns {
		if _, ok := seen[columns[i].Name]; ok {
			continue
		}
		seen[columns[i].Name] = struct{}{}
		keys = append(keys, columns[i].Name)
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		normalized := make(map[string]any, len(keys))
		for _, key := range keys {
			if value, ok := row[key]; ok {
				normalized[key] = value
			} else {
				normalized[key] = nil
			}
		}
		result = append(result, normalized)
	}
	return result
}

func truncateString(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
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

// ValidateIdentifier reports whether a Databricks catalog, schema, table, or
// projection column can be represented by the current bounded query contract.
// Discovery surfaces use this same rule to mark metadata that can be seen but
// cannot yet be queried through query_table/v1.
func ValidateIdentifier(value string) error {
	if _, err := quoteIdent(value); err != nil {
		return err
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
	columns, limit, err := NormalizeQueryControls(in.Columns, in.Limit)
	if err != nil {
		return QueryTableRequest{}, err
	}
	in.Columns = columns
	in.Limit = limit
	return in, nil
}

// TableSchemaProbeSQL constructs the zero-row statement used to validate a
// Table and obtain its schema manifest without reading table data.
func TableSchemaProbeSQL(ref TableRef) (string, error) {
	from, err := qualifiedTable(ref)
	if err != nil {
		return "", err
	}
	return "SELECT * FROM " + from + " LIMIT 0", nil
}

// DescribeTableSQL retains the original DESCRIBE statement contract for
// callers that explicitly need Databricks' table-description result rows.
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
	if len(projection) > MaxQueryColumns {
		return "", fmt.Errorf("projection must contain at most %d columns", MaxQueryColumns)
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
	if value == "" || utf8.RuneCountInString(value) > MaxIdentifierLength || !utf8.ValidString(value) {
		return "", fmt.Errorf("invalid identifier")
	}
	if identifierRE.MatchString(value) {
		return "`" + value + "`", nil
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("invalid identifier")
		}
	}
	return "`" + strings.ReplaceAll(value, "`", "``") + "`", nil
}
