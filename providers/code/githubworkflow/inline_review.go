// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package githubworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
)

// Review locations are single diff lines; ranges and file-level comments are
// intentionally excluded from this bounded publication contract.
func validateReviewComments(comments []ReviewComment, count int) error {
	if len(comments) == 0 {
		return nil
	}
	if len(comments) > 50 || len(comments) != count {
		return errors.New("inline review requires one comment per finding, at most 50")
	}
	total := 0
	for _, c := range comments {
		if c.Path == "" || len(c.Path) > 1024 || path.IsAbs(c.Path) || path.Clean(c.Path) != c.Path || c.Path == ".." || strings.HasPrefix(c.Path, "../") || strings.Contains(c.Path, "\\") || strings.ContainsFunc(c.Path, unicode.IsControl) || c.Line <= 0 || (c.Side != "LEFT" && c.Side != "RIGHT") || strings.TrimSpace(c.Body) == "" || len(c.Body) > 8192 {
			return errors.New("invalid inline review path, line, side or bounded body")
		}
		total += len(c.Body) + len(c.Path)
	}
	if total > 65536 {
		return errors.New("inline review content exceeds 64 KiB")
	}
	return nil
}

func publishReview(ctx context.Context, c Client, e *api.GitHubEvent, head, body, marker string, comments []ReviewComment) (any, error) {
	if len(comments) == 0 {
		return publishText(ctx, c, e, head, body, marker)
	}
	if err := validateReviewComments(comments, len(comments)); err != nil {
		return nil, err
	}
	// Recover an unconfirmed post by its existing marker, but reject changed
	// content under the same key rather than silently treating it as a replay.
	raw, _ := json.Marshal(comments)
	sum := sha256.Sum256(raw)
	body += "\n\n<!-- railgrid-inline-content:" + hex.EncodeToString(sum[:]) + " -->"
	if err := validateDiffLocations(ctx, c, e, comments); err != nil {
		return nil, err
	}
	return publishTextWithComments(ctx, c, e, head, body, marker, false, comments)
}

type diffLocation struct {
	side string
	line int
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$`)

func patchLocations(patch string) (map[diffLocation]bool, error) {
	locations := map[diffLocation]bool{}
	if patch == "" {
		return nil, errors.New("GitHub did not provide a text patch")
	}
	oldLine, newLine, oldLeft, newLeft := 0, 0, 0, 0
	active := false
	lines := strings.Split(patch, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if active && (oldLeft != 0 || newLeft != 0) {
				return nil, errors.New("incomplete GitHub patch")
			}
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, errors.New("invalid patch hunk")
			}
			values := []int{0, 1, 0, 1}
			for index, number := range m[1:5] {
				if number == "" {
					continue
				}
				parsed, err := strconv.ParseInt(number, 10, 32)
				if err != nil {
					return nil, errors.New("patch hunk coordinate exceeds bounds")
				}
				values[index] = int(parsed)
			}
			oldLine, oldLeft, newLine, newLeft = values[0], values[1], values[2], values[3]
			if oldLine == 0 && oldLeft > 0 || newLine == 0 && newLeft > 0 {
				return nil, errors.New("invalid zero patch coordinate")
			}
			active = true
			continue
		}
		if line == "" && i == len(lines)-1 {
			continue
		}
		if strings.HasPrefix(line, "\\ No newline at end of file") {
			continue
		}
		if !active || len(line) == 0 {
			return nil, errors.New("invalid GitHub patch line")
		}
		switch line[0] {
		case ' ':
			// GitHub expects unchanged context on RIGHT; LEFT is deletion-only.
			locations[diffLocation{"RIGHT", newLine}] = true
			oldLine++
			newLine++
			oldLeft--
			newLeft--
		case '-':
			locations[diffLocation{"LEFT", oldLine}] = true
			oldLine++
			oldLeft--
		case '+':
			locations[diffLocation{"RIGHT", newLine}] = true
			newLine++
			newLeft--
		default:
			return nil, errors.New("invalid GitHub patch line")
		}
		if oldLeft < 0 || newLeft < 0 {
			return nil, errors.New("GitHub patch exceeds hunk bounds")
		}
	}
	if !active || oldLeft != 0 || newLeft != 0 {
		return nil, errors.New("incomplete GitHub patch")
	}
	return locations, nil
}

func validateDiffLocations(ctx context.Context, c Client, e *api.GitHubEvent, comments []ReviewComment) error {
	remaining := map[string][]ReviewComment{}
	for _, comment := range comments {
		remaining[comment.Path] = append(remaining[comment.Path], comment)
	}
	for page := 1; page <= 30; page++ {
		var files []struct {
			Filename string `json:"filename"`
			Patch    string `json:"patch"`
		}
		route := fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", e.Spec.Repository, e.Spec.PullRequest, page)
		if err := c.Do(ctx, http.MethodGet, route, nil, &files); err != nil {
			return err
		}
		for _, file := range files {
			findings, needed := remaining[file.Filename]
			if !needed {
				continue
			}
			locations, err := patchLocations(file.Patch)
			if err != nil {
				return fmt.Errorf("cannot validate inline locations for %s: %w", file.Filename, err)
			}
			for _, finding := range findings {
				if !locations[diffLocation{finding.Side, finding.Line}] {
					return fmt.Errorf("inline location %s:%d %s is outside the PR diff", finding.Path, finding.Line, finding.Side)
				}
			}
			delete(remaining, file.Filename)
		}
		if len(remaining) == 0 {
			return nil
		}
		if len(files) < 100 {
			break
		}
	}
	return errors.New("inline review file is absent from the bounded GitHub PR diff")
}
