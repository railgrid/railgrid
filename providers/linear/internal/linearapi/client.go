// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package linearapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const Endpoint = "https://api.linear.app/graphql"

type Client struct {
	HTTP     *http.Client
	endpoint string
	key      string
}

func New(key string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}, endpoint: Endpoint, key: key}
}

type Error struct {
	Status      int
	RateLimited bool
	Uncertain   bool
	RetryAfter  string
}

func (e *Error) Error() string {
	if e.RateLimited {
		return "Linear rate limited; retry reads later"
	}
	if e.Uncertain {
		return "Linear mutation outcome uncertain; inspect the existing operation"
	}
	return fmt.Sprintf("Linear request failed (HTTP %d)", e.Status)
}
func (c *Client) query(ctx context.Context, q string, v any, mutation bool, out any) error {
	body, err := json.Marshal(map[string]any{"query": q, "variables": v})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &Error{Uncertain: mutation}
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024+1))
	if err != nil || len(b) > 512*1024 {
		return &Error{Status: resp.StatusCode, Uncertain: mutation}
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(b, &envelope) != nil {
		return &Error{Status: resp.StatusCode, Uncertain: mutation}
	}
	limited := resp.StatusCode == 429
	for _, e := range envelope.Errors {
		if e.Extensions.Code == "RATELIMITED" {
			limited = true
		}
	}
	if resp.StatusCode != 200 || len(envelope.Errors) > 0 {
		return &Error{Status: resp.StatusCode, RateLimited: limited, Uncertain: mutation, RetryAfter: resp.Header.Get("Retry-After")}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return &Error{Status: 200, Uncertain: mutation}
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return &Error{Status: 200, Uncertain: mutation}
	}
	return nil
}

type PageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}
type Page[T any] struct {
	Nodes    []T      `json:"nodes"`
	PageInfo PageInfo `json:"pageInfo"`
}
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}
type State struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Team Team   `json:"team"`
}
type Issue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	UpdatedAt   string `json:"updatedAt"`
	Team        Team   `json:"team"`
	State       State  `json:"state"`
}
type CommentUser struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}
type CommentBotActor struct {
	ID      *string `json:"id"`
	Type    string  `json:"type"`
	Name    *string `json:"name"`
	SubType *string `json:"subType"`
}
type CommentExternalUser struct {
	ID string `json:"id"`
}
type Comment struct {
	ID           string               `json:"id"`
	Body         string               `json:"body"`
	IssueID      *string              `json:"issueId"`
	ParentID     *string              `json:"parentId"`
	CreatedAt    string               `json:"createdAt"`
	UpdatedAt    string               `json:"updatedAt"`
	EditedAt     *string              `json:"editedAt"`
	URL          string               `json:"url"`
	User         *CommentUser         `json:"user"`
	BotActor     *CommentBotActor     `json:"botActor"`
	ExternalUser *CommentExternalUser `json:"externalUser"`
	Issue        Issue                `json:"issue"`
}

const issueFields = `id identifier title description url updatedAt team { id name key } state { id name }`
const commentFields = `id body issueId parentId createdAt updatedAt editedAt url user{id name displayName} botActor{id type name subType} externalUser{id}`

func pageVars(first int, after string) map[string]any {
	if first < 1 || first > 50 {
		first = 25
	}
	v := map[string]any{"first": first}
	if after != "" {
		v["after"] = after
	}
	return v
}
func (c *Client) Teams(ctx context.Context, first int, after string) (Page[Team], error) {
	var d struct {
		Teams Page[Team] `json:"teams"`
	}
	err := c.query(ctx, `query($first:Int!,$after:String){teams(first:$first,after:$after){nodes{id name key} pageInfo{hasNextPage endCursor}}}`, pageVars(first, after), false, &d)
	return d.Teams, err
}
func (c *Client) States(ctx context.Context, team string, first int, after string) (Page[State], error) {
	var d struct {
		Team struct {
			States Page[State] `json:"states"`
		} `json:"team"`
	}
	v := pageVars(first, after)
	v["id"] = team
	err := c.query(ctx, `query($id:String!,$first:Int!,$after:String){team(id:$id){states(first:$first,after:$after){nodes{id name team{id}} pageInfo{hasNextPage endCursor}}}}`, v, false, &d)
	return d.Team.States, err
}
func (c *Client) Issues(ctx context.Context, team, query, since string, first int, after string) (Page[Issue], error) {
	var d struct {
		Issues Page[Issue] `json:"issues"`
	}
	v := pageVars(first, after)
	filter := map[string]any{"team": map[string]any{"id": map[string]any{"eq": team}}}
	if query != "" {
		filter["title"] = map[string]any{"containsIgnoreCase": query}
	}
	if since != "" {
		filter["updatedAt"] = map[string]any{"gte": since}
	}
	v["filter"] = filter
	err := c.query(ctx, `query($filter:IssueFilter!,$first:Int!,$after:String){issues(filter:$filter,first:$first,after:$after,orderBy:updatedAt){nodes{`+issueFields+`} pageInfo{hasNextPage endCursor}}}`, v, false, &d)
	return d.Issues, err
}
func (c *Client) Issue(ctx context.Context, id string) (Issue, error) {
	var d struct {
		Issue *Issue `json:"issue"`
	}
	err := c.query(ctx, `query($id:String!){issue(id:$id){`+issueFields+`}}`, map[string]any{"id": id}, false, &d)
	if err != nil {
		return Issue{}, err
	}
	if d.Issue == nil || d.Issue.ID == "" {
		return Issue{}, errors.New("linear issue unavailable")
	}
	return *d.Issue, nil
}
func (c *Client) Comments(ctx context.Context, id string, first int, after string) (Page[Comment], error) {
	var d struct {
		Issue struct {
			Comments Page[Comment] `json:"comments"`
		} `json:"issue"`
	}
	v := pageVars(first, after)
	v["id"] = id
	err := c.query(ctx, `query($id:String!,$first:Int!,$after:String){issue(id:$id){comments(first:$first,after:$after,orderBy:createdAt){nodes{`+commentFields+`} pageInfo{hasNextPage endCursor}}}}`, v, false, &d)
	return d.Issue.Comments, err
}
func (c *Client) CommentIssueID(ctx context.Context, id string) (string, error) {
	var d struct {
		Comment *Comment `json:"comment"`
	}
	err := c.query(ctx, `query($id:String!){comment(id:$id){issueId}}`, map[string]any{"id": id}, false, &d)
	if err != nil {
		return "", err
	}
	if d.Comment == nil || d.Comment.IssueID == nil || *d.Comment.IssueID == "" {
		return "", errors.New("linear comment unavailable")
	}
	return *d.Comment.IssueID, nil
}
func (c *Client) CommentReplies(ctx context.Context, id string, first int, after string) (Page[Comment], error) {
	var d struct {
		Comment *struct {
			Children Page[Comment] `json:"children"`
		} `json:"comment"`
	}
	v := pageVars(first, after)
	v["id"] = id
	err := c.query(ctx, `query($id:String!,$first:Int!,$after:String){comment(id:$id){children(first:$first,after:$after,orderBy:createdAt){nodes{`+commentFields+`} pageInfo{hasNextPage endCursor}}}}`, v, false, &d)
	if err != nil {
		return Page[Comment]{}, err
	}
	if d.Comment == nil {
		return Page[Comment]{}, errors.New("linear comment unavailable")
	}
	return d.Comment.Children, nil
}
func (c *Client) CreateIssue(ctx context.Context, input map[string]any) (Issue, error) {
	var d struct {
		Payload struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueCreate"`
	}
	err := c.query(ctx, `mutation($input:IssueCreateInput!){issueCreate(input:$input){success issue{`+issueFields+`}}}`, map[string]any{"input": input}, true, &d)
	if err == nil && (!d.Payload.Success || d.Payload.Issue.ID == "") {
		err = &Error{Status: 200, Uncertain: true}
	}
	return d.Payload.Issue, err
}
func (c *Client) UpdateIssue(ctx context.Context, id string, input map[string]any) (Issue, error) {
	var d struct {
		Payload struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueUpdate"`
	}
	err := c.query(ctx, `mutation($id:String!,$input:IssueUpdateInput!){issueUpdate(id:$id,input:$input){success issue{`+issueFields+`}}}`, map[string]any{"id": id, "input": input}, true, &d)
	if err == nil && (!d.Payload.Success || d.Payload.Issue.ID == "") {
		err = &Error{Status: 200, Uncertain: true}
	}
	return d.Payload.Issue, err
}
func (c *Client) AddComment(ctx context.Context, id, body string) (Comment, error) {
	var d struct {
		Payload struct {
			Success bool    `json:"success"`
			Comment Comment `json:"comment"`
		} `json:"commentCreate"`
	}
	err := c.query(ctx, `mutation($input:CommentCreateInput!){commentCreate(input:$input){success comment{`+commentFields+`}}}`, map[string]any{"input": map[string]any{"issueId": id, "body": body}}, true, &d)
	if err == nil && (!d.Payload.Success || d.Payload.Comment.ID == "") {
		err = &Error{Status: 200, Uncertain: true}
	}
	return d.Payload.Comment, err
}
