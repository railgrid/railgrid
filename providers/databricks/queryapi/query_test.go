// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTableSchemaProbeSQLQuotesQualifiedNameAndReadsNoRows(t *testing.T) {
	got, err := TableSchemaProbeSQL(TableRef{Catalog: "sales", Schema: "gold", Table: "order_history"})
	if err != nil {
		t.Fatalf("TableSchemaProbeSQL returned error: %v", err)
	}
	want := "SELECT * FROM `sales`.`gold`.`order_history` LIMIT 0"
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

func TestDescribeTableSQLRetainsLegacyDescribeSemantics(t *testing.T) {
	got, err := DescribeTableSQL(TableRef{Catalog: "sales", Schema: "gold", Table: "order_history"})
	if err != nil {
		t.Fatalf("DescribeTableSQL returned error: %v", err)
	}
	want := "DESCRIBE TABLE `sales`.`gold`.`order_history`"
	if got != want {
		t.Fatalf("DescribeTableSQL = %q, want legacy %q", got, want)
	}
}

func TestTableSchemaProbeSQLRejectsUnsafeIdentifier(t *testing.T) {
	_, err := TableSchemaProbeSQL(TableRef{Catalog: "sales", Schema: "gold", Table: "order_history\n drop table orders"})
	if err == nil {
		t.Fatal("TableSchemaProbeSQL returned nil error for unsafe identifier")
	}
	if !strings.Contains(err.Error(), "table") {
		t.Fatalf("error = %q, want table context", err.Error())
	}
}

func TestTableSchemaProbeSQLQuotesDelimitedIdentifiers(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "spaces and hyphens", value: "sales catalog", want: "sales catalog"},
		{name: "statement punctuation", value: "semi;colon", want: "semi;colon"},
		{name: "leading backtick", value: "`leading", want: "``leading"},
		{name: "trailing backtick", value: "trailing`", want: "trailing``"},
		{name: "internal backtick", value: "a`b", want: "a``b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TableSchemaProbeSQL(TableRef{Catalog: "sales", Schema: "gold-schema", Table: tt.value})
			if err != nil {
				t.Fatalf("TableSchemaProbeSQL returned error: %v", err)
			}
			want := "SELECT * FROM `sales`.`gold-schema`.`" + tt.want + "` LIMIT 0"
			if got != want {
				t.Fatalf("SQL = %q, want %q", got, want)
			}
		})
	}
}

func TestTableSchemaProbeSQLRejectsControlsAndMalformedDelimitedValues(t *testing.T) {
	for _, table := range []string{"order\n_history", "order\x00history", string([]byte{0xff, 'x'})} {
		if _, err := TableSchemaProbeSQL(TableRef{Catalog: "sales", Schema: "gold", Table: table}); err == nil {
			t.Fatalf("TableSchemaProbeSQL accepted malformed identifier %q", table)
		}
	}
}

func TestSelectTableSQLBuildsBoundedProjection(t *testing.T) {
	sql, err := SelectTableSQL(TableRef{Catalog: "sales", Schema: "gold", Table: "orders"}, []string{"order_id", "total"}, 25, []string{"order_id", "total"})
	if err != nil {
		t.Fatalf("SelectTableSQL returned error: %v", err)
	}
	if sql != "SELECT `order_id`, `total` FROM `sales`.`gold`.`orders` LIMIT 25" {
		t.Fatalf("sql = %q", sql)
	}
}

func TestSelectTableSQLRejectsControlsAndUnknownColumns(t *testing.T) {
	for _, projection := range [][]string{{"order_id\nDROP TABLE users"}, {"missing"}} {
		_, err := SelectTableSQL(TableRef{Catalog: "sales", Schema: "gold", Table: "orders"}, projection, 10, []string{"order_id"})
		if err == nil {
			t.Fatalf("SelectTableSQL accepted unsafe projection %#v", projection)
		}
		if projection[0] == "missing" {
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Code != ErrorCodeSchemaProjectionInvalid {
				t.Fatalf("unknown projection error = %T %v, want %q", err, err, ErrorCodeSchemaProjectionInvalid)
			}
			if validation.Message != `requested column "missing" is not present in the imported table schema` {
				t.Fatalf("unknown projection message = %q", validation.Message)
			}
		}
	}
	if _, err := SelectTableSQL(TableRef{Catalog: "sales", Schema: "gold", Table: "orders"}, nil, MaxQueryLimit+1, nil); err == nil {
		t.Fatal("SelectTableSQL accepted limit above fixed maximum")
	}
}

func TestNormalizeQueryRequestRequiresPinnedVersionAndBounds(t *testing.T) {
	if _, err := NormalizeQueryRequest(QueryTableRequest{TableRef: "orders"}); err == nil {
		t.Fatal("NormalizeQueryRequest accepted missing actionVersion")
	}
	request, err := NormalizeQueryRequest(QueryTableRequest{ActionVersion: ActionVersionV1, TableRef: "orders"})
	if err != nil {
		t.Fatalf("NormalizeQueryRequest returned error: %v", err)
	}
	if request.Limit != DefaultQueryLimit {
		t.Fatalf("default limit = %d, want %d", request.Limit, DefaultQueryLimit)
	}
	columns := make([]string, MaxQueryColumns+1)
	for i := range columns {
		columns[i] = "column_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	if _, err := NormalizeQueryRequest(QueryTableRequest{ActionVersion: ActionVersionV1, TableRef: "orders", Columns: columns}); err == nil {
		t.Fatal("NormalizeQueryRequest accepted more than the input column cap")
	}
}

func TestBoundQueryResultNormalizesRowsAndByteBudget(t *testing.T) {
	result := BoundQueryResult(QueryTableResult{
		ActionVersion: ActionVersionV1,
		TableRef:      "orders",
		Columns:       []QueryColumn{{Name: "order_id"}, {Name: "total"}},
		Rows: []map[string]any{
			{"order_id": 1, "extra": "discard"},
			{"order_id": 2},
		},
	})
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(result.Rows))
	}
	for _, row := range result.Rows {
		if len(row) != len(result.Columns) {
			t.Fatalf("row keys = %#v, columns = %#v", row, result.Columns)
		}
		if _, ok := row["extra"]; ok {
			t.Fatalf("extra row key was not removed: %#v", row)
		}
	}
	if result.Rows[1]["total"] != nil {
		t.Fatalf("missing column = %#v, want nil", result.Rows[1]["total"])
	}

	rows := make([]map[string]any, MaxQueryRows)
	for i := range rows {
		rows[i] = map[string]any{"value": strings.Repeat("x", 2_000)}
	}
	bounded := BoundQueryResult(QueryTableResult{
		ActionVersion: ActionVersionV1,
		TableRef:      "orders",
		Columns:       []QueryColumn{{Name: "value"}},
		Rows:          rows,
	})
	encoded, err := json.Marshal(bounded)
	if err != nil {
		t.Fatalf("marshal bounded result: %v", err)
	}
	if len(encoded) > MaxQueryBytes || len(bounded.Rows) >= len(rows) || !bounded.Truncated {
		t.Fatalf("bounded result bytes=%d rows=%d truncated=%v", len(encoded), len(bounded.Rows), bounded.Truncated)
	}
}

func TestBoundQueryResultCapsColumnsAndPreservesRetainedCells(t *testing.T) {
	columns := make([]QueryColumn, MaxQueryColumns+1)
	row := make(map[string]any, len(columns))
	for i := range columns {
		name := "column_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		columns[i] = QueryColumn{Name: name}
		row[name] = "value-" + name
	}
	result := BoundQueryResult(QueryTableResult{
		ActionVersion: ActionVersionV1,
		TableRef:      "orders",
		Columns:       columns,
		Rows:          []map[string]any{row},
	})
	if len(result.Columns) != MaxQueryColumns || !result.Truncated {
		t.Fatalf("columns=%d truncated=%v, want %d/truncated", len(result.Columns), result.Truncated, MaxQueryColumns)
	}
	for _, column := range result.Columns {
		value, ok := result.Rows[0][column.Name]
		if !ok || value != "value-"+column.Name {
			t.Fatalf("retained cell %q = %#v (present=%v), value was corrupted", column.Name, value, ok)
		}
	}
	if _, ok := result.Rows[0][columns[MaxQueryColumns].Name]; ok {
		t.Fatalf("row retained capped column %q: %#v", columns[MaxQueryColumns].Name, result.Rows[0])
	}
}
