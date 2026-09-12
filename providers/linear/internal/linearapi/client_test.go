// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package linearapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransportErrorsNeverReplayWritesOrLeakCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		rate   bool
	}{
		{"graphql", 200, `{"errors":[{"message":"secret-key should never escape"}]}`, false},
		{"rate", 429, `{"errors":[{"extensions":{"code":"RATELIMITED"}}]}`, true},
		{"malformed", 200, `not json`, false},
		{"revoked", 401, `{"errors":[{}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "secret-key" {
					t.Error("API key header changed")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New("secret-key")
			c.endpoint = srv.URL
			_, err := c.CreateIssue(context.Background(), map[string]any{"title": "approved"})
			var apiErr *Error
			if !errors.As(err, &apiErr) || !apiErr.Uncertain || apiErr.RateLimited != tc.rate {
				t.Fatalf("error=%v", err)
			}
			if calls != 1 || strings.Contains(err.Error(), "secret-key") {
				t.Fatal("unsafe retry or leaked secret")
			}
		})
	}
}
func TestReadPaginationAndRedirectProtection(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/stolen", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"teams":{"nodes":[{"id":"team","name":"Team"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}`))
	}))
	defer srv.Close()
	c := New("key")
	c.endpoint = srv.URL
	page, err := c.Teams(context.Background(), 25, "")
	if err != nil || len(page.Nodes) != 1 || !page.PageInfo.HasNextPage || page.PageInfo.EndCursor != "next" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	c.endpoint = srv.URL + "/redirect"
	_, err = c.Teams(context.Background(), 25, "")
	if err == nil || calls != 2 {
		t.Fatal("followed redirect")
	}
}
