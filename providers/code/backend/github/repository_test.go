/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	code "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRepositoryCreationIntent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		createOnly bool
		recordedID string
		exists     bool
		race       bool
		wantError  bool
		wantPosts  int
	}{
		{name: "collision", createOnly: true, exists: true, wantError: true},
		{name: "retry same remote", createOnly: true, recordedID: "42", exists: true},
		{name: "replacement remote", createOnly: true, recordedID: "41", exists: true, wantError: true},
		{name: "explicit import", exists: true},
		{name: "new repository", createOnly: true, wantPosts: 1},
		{name: "concurrent collision", createOnly: true, race: true, wantError: true, wantPosts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v3/repos/team/demo":
					if !tc.exists && (!tc.race || posts <= 0) {
						w.WriteHeader(404)
						_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
						return
					}
					_, _ = fmt.Fprint(w, `{"id":42,"name":"demo"}`)
				case r.Method == "POST" && r.URL.Path == "/api/v3/orgs/team/repos":
					posts++
					if tc.race {
						w.WriteHeader(422)
						_, _ = fmt.Fprint(w, `{"message":"name already exists"}`)
						return
					}
					w.WriteHeader(201)
					_, _ = fmt.Fprint(w, `{"id":42,"name":"demo"}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			repo := &code.Repository{Spec: code.RepositorySpec{Name: "demo"}, Status: code.RepositoryStatus{RepoID: tc.recordedID}}
			if tc.createOnly {
				repo.Annotations = map[string]string{createOnlyAnnotation: "true"}
			}
			conn := &code.Connection{Spec: code.ConnectionSpec{Owner: "team", BaseURL: srv.URL}}
			result, err := New().EnsureRepository(context.Background(), conn, backend.Credential{Token: "test"}, repo)
			if (err != nil) != tc.wantError {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if tc.wantError && (tc.exists || tc.race) && !errors.Is(err, backend.ErrRepositoryIdentityConflict) {
				t.Fatalf("collision must expose a recoverable identity conflict: %v", err)
			}
			if !tc.wantError && result.RepoID != "42" {
				t.Fatalf("remote ID = %q", result.RepoID)
			}
			if posts != tc.wantPosts {
				t.Fatalf("create calls=%d, want %d", posts, tc.wantPosts)
			}
		})
	}
}

func TestCreateOnlyCollisionCannotMutateRemote(t *testing.T) {
	for _, id := range []string{"", "41"} {
		t.Run("recordedID="+id, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v3/repos/team/demo" {
					t.Errorf("unsafe request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"id":42,"name":"demo"}`)
			}))
			defer srv.Close()
			conn := &code.Connection{Spec: code.ConnectionSpec{Owner: "team", BaseURL: srv.URL}}
			repo := &code.Repository{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{createOnlyAnnotation: "true"}}, Spec: code.RepositorySpec{Name: "demo"}, Status: code.RepositoryStatus{RepoID: id}}
			cred := backend.Credential{Token: "test"}
			err := New().DeleteRepository(context.Background(), conn, cred, repo)
			if (err != nil) != (id != "") {
				t.Fatalf("delete error=%v", err)
			}
			_, err = New().CommitFiles(context.Background(), conn, cred, repo, backend.RepositoryCommitInput{Files: []backend.RepositoryCommitFile{{Path: "README.md", Content: "do not overwrite"}}})
			if err == nil {
				t.Fatal("commit allowed into unowned repository")
			}
		})
	}
}

func TestCreateRaceConfirmationFailures(t *testing.T) {
	for _, statusCode := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(statusCode), func(t *testing.T) {
			gets, posts := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					gets++
					if gets == 1 {
						w.WriteHeader(404)
					} else {
						w.WriteHeader(statusCode)
					}
					_, _ = fmt.Fprint(w, `{"message":"lookup failed"}`)
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("unexpected %s", r.Method)
				}
				posts++
				w.WriteHeader(422)
				_, _ = fmt.Fprint(w, `{"message":"validation failed"}`)
			}))
			defer srv.Close()
			repo := &code.Repository{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{createOnlyAnnotation: "true"}}, Spec: code.RepositorySpec{Name: "demo"}}
			conn := &code.Connection{Spec: code.ConnectionSpec{Owner: "team", BaseURL: srv.URL}}
			_, err := New().EnsureRepository(context.Background(), conn, backend.Credential{Token: "test"}, repo)
			if err == nil || errors.Is(err, backend.ErrRepositoryIdentityConflict) {
				t.Fatalf("unconfirmed collision must not trigger rename: %v", err)
			}
			if statusCode != 404 && !strings.Contains(err.Error(), "confirm repository name collision") {
				t.Fatalf("lost lookup failure: %v", err)
			}
			if posts != 1 || gets != 2 {
				t.Fatalf("posts=%d gets=%d", posts, gets)
			}
		})
	}
}

func TestRepositoryRejectsNamesBeforeExternalCreation(t *testing.T) {
	for _, name := range []string{"E2E Invalid Name!", ".", "..", "", strings.Repeat("a", 101), "a/b"} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid name reached GitHub") }))
			defer srv.Close()
			conn := &code.Connection{Spec: code.ConnectionSpec{Owner: "team", BaseURL: srv.URL}}
			_, err := New().EnsureRepository(context.Background(), conn, backend.Credential{Token: "test"}, &code.Repository{Spec: code.RepositorySpec{Name: name}})
			if !errors.Is(err, backend.ErrInvalidRepositoryName) {
				t.Fatalf("expected invalid name error, got %v", err)
			}
		})
	}
}
