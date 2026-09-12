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
	"encoding/json"
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

func TestCommentsDecodeAttributionAndCreatedOrdering(t *testing.T) {
	const wantQuery = `query($id:String!,$first:Int!,$after:String){issue(id:$id){comments(first:$first,after:$after,orderBy:createdAt){nodes{id body issueId parentId createdAt updatedAt editedAt url user{id name displayName} botActor{id type name subType} externalUser{id}} pageInfo{hasNextPage endCursor}}}}`
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Query     string `json:"query"`
			Variables struct {
				ID    string `json:"id"`
				First int    `json:"first"`
				After string `json:"after"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Query != wantQuery {
			t.Errorf("query = %q, want %q", request.Query, wantQuery)
		}
		if request.Variables.ID != "issue-1" || request.Variables.First != 2 || request.Variables.After != "cursor-1" {
			t.Errorf("variables = %+v, want issue-1/2/cursor-1", request.Variables)
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"comments":{"nodes":[{"id":"comment-1","body":"first","issueId":"issue-1","parentId":"comment-0","createdAt":"2026-09-12T10:00:00Z","updatedAt":"2026-09-12T10:01:00Z","editedAt":"2026-09-12T10:02:00Z","url":"https://linear.app/acme/issue/ENG-1#comment-1","user":{"id":"user-1","name":"Ada Lovelace","displayName":"Ada"},"botActor":null,"externalUser":null},{"id":"comment-2","body":"second","issueId":"issue-1","parentId":null,"createdAt":"2026-09-12T10:03:00Z","updatedAt":"2026-09-12T10:03:00Z","editedAt":null,"url":"https://linear.app/acme/issue/ENG-1#comment-2","user":null,"botActor":{"id":null,"type":"workflow","name":null,"subType":null},"externalUser":{"id":"external-1"}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-2"}}}}}`))
	}))
	defer srv.Close()
	c := New("key")
	c.endpoint = srv.URL
	page, err := c.Comments(context.Background(), "issue-1", 2, "cursor-1")
	if err != nil {
		t.Fatalf("Comments() error = %v", err)
	}
	if calls != 1 || len(page.Nodes) != 2 || !page.PageInfo.HasNextPage || page.PageInfo.EndCursor != "cursor-2" {
		t.Fatalf("page = %+v, calls = %d", page, calls)
	}
	first := page.Nodes[0]
	if first.IssueID == nil || *first.IssueID != "issue-1" || first.ParentID == nil || *first.ParentID != "comment-0" || first.EditedAt == nil || *first.EditedAt != "2026-09-12T10:02:00Z" {
		t.Fatalf("first comment metadata = %+v", first)
	}
	if first.User == nil || first.User.ID != "user-1" || first.User.Name != "Ada Lovelace" || first.User.DisplayName != "Ada" || first.BotActor != nil || first.ExternalUser != nil {
		t.Fatalf("first comment attribution = %+v", first)
	}
	second := page.Nodes[1]
	if second.ParentID != nil || second.EditedAt != nil || second.User != nil || second.BotActor == nil || second.ExternalUser == nil {
		t.Fatalf("second comment nullable metadata = %+v", second)
	}
	if second.BotActor.ID != nil || second.BotActor.Type != "workflow" || second.BotActor.Name != nil || second.BotActor.SubType != nil || second.ExternalUser.ID != "external-1" {
		t.Fatalf("second comment attribution = %+v", second)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"parentId", "editedAt", "user"} {
		if string(fields[field]) != "null" {
			t.Errorf("%s = %s, want null", field, fields[field])
		}
	}
}

func TestCommentRepliesValidateParentAndDecodePage(t *testing.T) {
	const wantParentQuery = `query($id:String!){comment(id:$id){issueId}}`
	const wantRepliesQuery = `query($id:String!,$first:Int!,$after:String){comment(id:$id){children(first:$first,after:$after,orderBy:createdAt){nodes{id body issueId parentId createdAt updatedAt editedAt url user{id name displayName} botActor{id type name subType} externalUser{id}} pageInfo{hasNextPage endCursor}}}}`
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		switch request.Query {
		case wantParentQuery:
			if request.Variables["id"] != "comment-1" {
				t.Errorf("parent variables = %+v", request.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"comment":{"issueId":"issue-1"}}}`))
		case wantRepliesQuery:
			if request.Variables["id"] != "comment-1" || request.Variables["first"] != float64(2) || request.Variables["after"] != "cursor-1" {
				t.Errorf("reply variables = %+v", request.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"comment":{"children":{"nodes":[{"id":"reply-1","body":"reply","issueId":"issue-1","parentId":"comment-1","createdAt":"2026-09-12T12:00:00Z","updatedAt":"2026-09-12T12:00:00Z","editedAt":null,"url":"https://linear.app/acme/issue/ENG-1#comment-reply-1","user":null,"botActor":null,"externalUser":null}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-2"}}}}}`))
		default:
			t.Errorf("unexpected query %q", request.Query)
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	c := New("key")
	c.endpoint = srv.URL
	issueID, err := c.CommentIssueID(context.Background(), "comment-1")
	if err != nil || issueID != "issue-1" {
		t.Fatalf("CommentIssueID() = %q, error = %v", issueID, err)
	}
	page, err := c.CommentReplies(context.Background(), "comment-1", 2, "cursor-1")
	if err != nil || calls != 2 || len(page.Nodes) != 1 || page.Nodes[0].ID != "reply-1" || !page.PageInfo.HasNextPage || page.PageInfo.EndCursor != "cursor-2" {
		t.Fatalf("CommentReplies() = %+v, calls = %d, error = %v", page, calls, err)
	}
}

func TestAddCommentReturnsAttributionMetadata(t *testing.T) {
	const wantQuery = `mutation($input:CommentCreateInput!){commentCreate(input:$input){success comment{id body issueId parentId createdAt updatedAt editedAt url user{id name displayName} botActor{id type name subType} externalUser{id}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string `json:"query"`
			Variables struct {
				Input struct {
					IssueID string `json:"issueId"`
					Body    string `json:"body"`
				} `json:"input"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Query != wantQuery {
			t.Errorf("query = %q, want %q", request.Query, wantQuery)
		}
		if request.Variables.Input.IssueID != "issue-1" || request.Variables.Input.Body != "created" {
			t.Errorf("input = %+v, want issue-1/created", request.Variables.Input)
		}
		_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true,"comment":{"id":"comment-created","body":"created","issueId":"issue-1","parentId":null,"createdAt":"2026-09-12T11:00:00Z","updatedAt":"2026-09-12T11:00:00Z","editedAt":null,"url":"https://linear.app/acme/issue/ENG-1#comment-created","user":{"id":"user-1","name":"Ada Lovelace","displayName":"Ada"},"botActor":null,"externalUser":null}}}}`))
	}))
	defer srv.Close()
	c := New("key")
	c.endpoint = srv.URL
	comment, err := c.AddComment(context.Background(), "issue-1", "created")
	if err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	if comment.ID != "comment-created" || comment.Body != "created" || comment.IssueID == nil || *comment.IssueID != "issue-1" || comment.EditedAt != nil {
		t.Fatalf("comment = %+v", comment)
	}
	if comment.User == nil || comment.User.DisplayName != "Ada" || comment.BotActor != nil || comment.ExternalUser != nil {
		t.Fatalf("comment attribution = %+v", comment)
	}
}
