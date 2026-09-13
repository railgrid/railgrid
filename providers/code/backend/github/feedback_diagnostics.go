// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	gh "github.com/google/go-github/v66/github"
)

// Diagnostic text is untrusted data. Reject incomplete or oversized observations
// instead of silently truncating the information a consumer uses for decisions.
func diagnosticText(text string) error {
	if len(text) > 16384 || !utf8.ValidString(text) || strings.ContainsAny(text, "\x00\x1b") {
		return errors.New("invalid or oversized diagnostic text")
	}
	return nil
}

func checkDiagnostic(ctx context.Context, c *gh.Client, owner, repo string, check *gh.CheckRun, text string) (string, error) {
	if err := diagnosticText(text); err != nil {
		return "", err
	}
	expected := check.GetOutput().GetAnnotationsCount()
	if expected < 0 || expected > 1000 {
		return "", errors.New("annotation count exceeds limit")
	}
	if expected == 0 {
		return text, nil
	}
	count := 0
	for page := 1; page <= 20; page++ {
		annotations, response, err := c.Checks.ListCheckRunAnnotations(ctx, owner, repo, check.GetID(), &gh.ListOptions{Page: page, PerPage: 50})
		if err != nil {
			return "", classify(response, err)
		}
		if len(annotations) > 50 {
			return "", errors.New("annotation page exceeds limit")
		}
		for _, annotation := range annotations {
			if annotation.GetPath() == "" || annotation.GetStartLine() < 1 || annotation.GetEndLine() < annotation.GetStartLine() {
				return "", errors.New("invalid annotation location")
			}
			text += fmt.Sprintf("\n\n%s:%d-%d [%s] %s\n%s\n%s", annotation.GetPath(), annotation.GetStartLine(), annotation.GetEndLine(), annotation.GetAnnotationLevel(), annotation.GetTitle(), annotation.GetMessage(), annotation.GetRawDetails())
			if err := diagnosticText(text); err != nil {
				return "", err
			}
			count++
		}
		if response == nil || response.NextPage == 0 {
			if count != expected {
				return "", errors.New("incomplete annotation listing")
			}
			return text, nil
		}
	}
	return "", errors.New("annotation pagination exceeds limit")
}

func reviewDiagnostic(ctx context.Context, c *gh.Client, owner, repo string, number int, review *gh.PullRequestReview, commit string) (string, error) {
	text := review.GetBody()
	if err := diagnosticText(text); err != nil {
		return "", err
	}
	seen := map[int64]bool{}
	for page := 1; page <= 20; page++ {
		comments, response, err := c.PullRequests.ListReviewComments(ctx, owner, repo, number, review.GetID(), &gh.ListOptions{Page: page, PerPage: 50})
		if err != nil {
			return "", classify(response, err)
		}
		if len(comments) > 50 {
			return "", errors.New("review comment page exceeds limit")
		}
		for _, comment := range comments {
			if comment.GetID() <= 0 || seen[comment.GetID()] || comment.GetPullRequestReviewID() != review.GetID() || !objectID.MatchString(comment.GetCommitID()) {
				return "", errors.New("invalid review comment identity")
			}
			seen[comment.GetID()] = true
			if comment.GetCommitID() != commit {
				continue
			}
			if comment.GetPath() == "" || comment.GetLine() < 0 {
				return "", errors.New("invalid review comment location")
			}
			text += fmt.Sprintf("\n\n%s:%d\n%s", comment.GetPath(), comment.GetLine(), comment.GetBody())
			if err := diagnosticText(text); err != nil {
				return "", err
			}
		}
		if response == nil || response.NextPage == 0 {
			return text, nil
		}
	}
	return "", errors.New("review comment pagination exceeds limit")
}
