/*
Copyright 2026 The Faros Authors.

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
	"net/url"
	"strings"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v66/github"

	"github.com/faroshq/provider-code/backend"
)

func forbiddenResponse() *gogithub.Response {
	req := httptest.NewRequest(http.MethodGet, "https://api.github.com/repos/acme/demo", nil)
	return &gogithub.Response{Response: &http.Response{StatusCode: http.StatusForbidden, Request: req, Header: http.Header{}}}
}

func TestClassifyRateLimits(t *testing.T) {
	resp := forbiddenResponse()
	reset := time.Now().Add(28 * time.Second).Truncate(time.Second)
	retryAfter := 90 * time.Second
	transport := &backend.RateLimitError{RetryAt: reset, Err: errors.New("HTTP 429")}
	for _, tc := range []struct {
		name string
		resp *gogithub.Response
		err  error
		// want is the expected RetryAt; zero means "about now + wantAfter".
		want      time.Time
		wantAfter time.Duration
	}{
		{name: "transport deadline", err: &url.Error{Op: "Post", URL: "https://api.github.com/x", Err: transport}, want: reset},
		{name: "go-github preflight", resp: resp, err: &gogithub.RateLimitError{Rate: gogithub.Rate{Reset: gogithub.Timestamp{Time: reset}}, Response: resp.Response, Message: "API rate limit exceeded"}, want: reset.Add(time.Second)},
		{name: "go-github preflight without reset", resp: resp, err: &gogithub.RateLimitError{Response: resp.Response}, wantAfter: defaultRateLimitRetryAfter},
		{name: "secondary with retry-after", resp: resp, err: &gogithub.AbuseRateLimitError{Response: resp.Response, RetryAfter: &retryAfter}, wantAfter: retryAfter},
		{name: "secondary without retry-after", resp: resp, err: &gogithub.AbuseRateLimitError{Response: resp.Response}, wantAfter: defaultRateLimitRetryAfter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now()
			err := classify(tc.resp, tc.err)
			var limited *backend.RateLimitError
			if !errors.As(err, &limited) {
				t.Fatalf("classify(%v) = %v, want *backend.RateLimitError", tc.err, err)
			}
			if !tc.want.IsZero() {
				if !limited.RetryAt.Equal(tc.want) {
					t.Fatalf("RetryAt = %s, want %s", limited.RetryAt, tc.want)
				}
			} else if limited.RetryAt.Before(before.Add(tc.wantAfter)) || limited.RetryAt.After(time.Now().Add(tc.wantAfter)) {
				t.Fatalf("RetryAt = %s, want now + %s", limited.RetryAt, tc.wantAfter)
			}
			if msg := err.Error(); strings.Contains(msg, "scope") || !strings.Contains(msg, "github: rate limited, resets in") {
				t.Fatalf("message = %q, want a rate limit message", msg)
			}
		})
	}
	// The transport error keeps its request context unchanged.
	wrapped := &url.Error{Op: "Post", URL: "https://api.github.com/x", Err: transport}
	if got := classify(nil, wrapped); got != error(wrapped) {
		t.Fatalf("transport rate limit rewritten: %v", got)
	}
}

func TestClassifyStatusErrors(t *testing.T) {
	cause := errors.New("boom")
	forbidden := classify(forbiddenResponse(), cause)
	if !strings.HasPrefix(forbidden.Error(), "github: forbidden — token lacks the required scope (403): ") || !errors.Is(forbidden, cause) {
		t.Fatalf("403 = %v", forbidden)
	}
	if strings.Contains(forbidden.Error(), "rate") {
		t.Fatalf("plain 403 mentions rate limiting: %v", forbidden)
	}
	var limited *backend.RateLimitError
	if errors.As(forbidden, &limited) {
		t.Fatal("plain 403 classified as rate limit")
	}
	unauthorized := forbiddenResponse()
	unauthorized.StatusCode = http.StatusUnauthorized
	if got := classify(unauthorized, cause); !strings.HasPrefix(got.Error(), "github: credential rejected (401): ") {
		t.Fatalf("401 = %v", got)
	}
	if got := classify(nil, cause); got != cause {
		t.Fatalf("other error rewritten: %v", got)
	}
}

// An exhausted budget reported on a successful response makes go-github fail
// the next request locally with a synthetic 403. It must surface as a rate limit.
func TestClassifyGoGitHubPreflightFromExhaustedBudget(t *testing.T) {
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
		_, _ = w.Write([]byte(`{"login":"alice"}`))
	}))
	defer srv.Close()
	b := New()
	c, err := b.client(context.Background(), backend.Credential{Token: "mock"}, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Users.Get(context.Background(), ""); err != nil {
		t.Fatalf("first request: %v", err)
	}
	_, resp, err := c.Users.Get(context.Background(), "")
	var preflight *gogithub.RateLimitError
	if !errors.As(err, &preflight) || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("second request = %v (resp %v), want go-github preflight 403", err, resp)
	}
	assertRetryAt(t, classify(resp, err), reset.Add(time.Second))
}
