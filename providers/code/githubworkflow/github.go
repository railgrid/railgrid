// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package githubworkflow contains the deterministic GitHub command protocol.
package githubworkflow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var RepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Parse accepts an exact command on the first line. Unrelated prose, quoted
// examples, edited comments and model interpretations are not commands.
func Parse(body string) (mode string, addressed bool) {
	line := strings.TrimSpace(strings.SplitN(strings.ReplaceAll(body, "\r\n", "\n"), "\n", 2)[0])
	switch line {
	case "/railgrid review":
		return "review", true
	case "/railgrid review-fix":
		return "review-fix", true
	}
	return "", line == "/railgrid" || strings.HasPrefix(line, "/railgrid ")
}

func Signature(key, body []byte) string {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
func Verify(key, body []byte, signature string) bool {
	if len(key) < 32 || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	actual, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	expected, _ := hex.DecodeString(Signature(key, body))
	return hmac.Equal(actual, expected)
}

// Client reaches only GitHub's API, never a URL supplied in a webhook body.
type Client struct {
	HTTP     *http.Client
	Token    string
	BotLogin string
}

func (c Client) Do(ctx context.Context, method, path string, body, out any) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return fmt.Errorf("invalid GitHub API path")
	}
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.github.com"+path, input)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "railgrid-agents")
	client := &http.Client{Timeout: 8 * time.Second}
	if c.HTTP != nil {
		copy := *c.HTTP
		client = &copy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return fmt.Errorf("GitHub credential redirects forbidden") }
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub API request failed")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub API returned HTTP %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("reading GitHub response")
	}
	if len(raw) >= 4<<20 {
		return fmt.Errorf("GitHub response exceeds limit")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("invalid GitHub JSON response")
	}
	return nil
}

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}
type Repo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}
type Comment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User User   `json:"user"`
}
type Event struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository Repo `json:"repository"`
	Sender     User `json:"sender"`
	Issue      struct {
		Number      int64            `json:"number"`
		PullRequest *json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Comment Comment `json:"comment"`
}
type PullRequest struct {
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Number         int64  `json:"number"`
	State          string `json:"state"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	Head           struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo Repo   `json:"repo"`
	} `json:"head"`
	Base struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo Repo   `json:"repo"`
	} `json:"base"`
}

// Maintainer checks current permissions, not author_association or role text
// embedded in the webhook. Custom roles based on maintain/admin are included.
func (c Client) Maintainer(ctx context.Context, repo, login string) (bool, error) {
	var p struct {
		Permission string `json:"permission"`
		User       struct {
			Permissions struct {
				Admin    bool `json:"admin"`
				Maintain bool `json:"maintain"`
			} `json:"permissions"`
		} `json:"user"`
	}
	if err := c.Do(ctx, "GET", "/repos/"+repo+"/collaborators/"+url.PathEscape(login)+"/permission", nil, &p); err != nil {
		return false, err
	}
	return p.Permission == "admin" || p.Permission == "maintain" || p.User.Permissions.Admin || p.User.Permissions.Maintain, nil
}
