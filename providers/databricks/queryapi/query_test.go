// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
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
