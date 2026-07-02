// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"context"
	"errors"
	"strings"
)

type TableResolver interface {
	ListTables(ctx context.Context) (map[string]TableRef, error)
	GetTable(ctx context.Context, name string) (TableRef, bool, error)
}

type StaticTableResolver map[string]TableRef

func (r StaticTableResolver) ListTables(context.Context) (map[string]TableRef, error) {
	out := make(map[string]TableRef, len(r))
	for name, ref := range r {
		out[name] = ref
	}
	return out, nil
}

func (r StaticTableResolver) GetTable(_ context.Context, name string) (TableRef, bool, error) {
	ref, ok := r[name]
	return ref, ok, nil
}

type UnavailableResolver struct {
	Message string
}

func (r UnavailableResolver) ListTables(context.Context) (map[string]TableRef, error) {
	return nil, r.err()
}

func (r UnavailableResolver) GetTable(context.Context, string) (TableRef, bool, error) {
	return TableRef{}, false, r.err()
}

func (r UnavailableResolver) err() error {
	if strings.TrimSpace(r.Message) == "" {
		return errors.New("table resolver unavailable")
	}
	return errors.New(r.Message)
}
