// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"errors"
	"strings"
	"testing"
)

func TestDescribeTableSQLQuotesQualifiedName(t *testing.T) {
	got, err := DescribeTableSQL(TableRef{Catalog: "sales", Schema: "gold", Table: "order_history"})
	if err != nil {
		t.Fatalf("DescribeTableSQL returned error: %v", err)
	}
	want := "DESCRIBE TABLE `sales`.`gold`.`order_history`"
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

func TestDescribeTableSQLRejectsUnsafeIdentifier(t *testing.T) {
	_, err := DescribeTableSQL(TableRef{Catalog: "sales", Schema: "gold", Table: "order_history; drop table orders"})
	if err == nil {
		t.Fatal("DescribeTableSQL returned nil error for unsafe identifier")
	}
	if !strings.Contains(err.Error(), "table") {
		t.Fatalf("error = %q, want table context", err.Error())
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

func TestSelectTableSQLRejectsRawSQLAndUnknownColumns(t *testing.T) {
	for _, projection := range [][]string{{"order_id); DROP TABLE users; --"}, {"missing"}} {
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
}
