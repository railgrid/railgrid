// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
)

func (b *Backend) UploadAttachment(ctx context.Context, conn *api.Connection, cred backend.Credential, repo *api.Repository, a backend.Attachment) (string, error) {
	if err := backend.ValidateAttachments([]backend.Attachment{a}); err != nil {
		return "", err
	}
	if conn.Spec.BaseURL != "" {
		return "", errors.New("screenshot uploads currently support github.com only")
	}
	supportedToken := strings.HasPrefix(cred.Token, "ghp_") || strings.HasPrefix(cred.Token, "github_pat_") || strings.HasPrefix(cred.Token, "gho_") || strings.HasPrefix(cred.Token, "ghu_")
	if !supportedToken {
		return "", errors.New("screenshot uploads require a PAT or supported user OAuth token; installation tokens are unsupported")
	}
	c, err := b.collaborationClient(ctx, conn, cred, repo)
	if err != nil {
		return "", err
	}
	remote, response, err := c.Repositories.Get(ctx, owner(conn, repo), repo.Spec.Name)
	if err != nil {
		return "", classify(response, err)
	}
	if remote.GetID() == 0 || strconv.FormatInt(remote.GetID(), 10) != repo.Status.RepoID {
		return "", backend.ErrRepositoryIdentityConflict
	}
	return uploadAttachment(ctx, cred.Token, remote.GetID(), a, "https://uploads.github.com")
}

// The upload host is fixed by the backend, never by a staged artifact or caller.
func uploadAttachment(ctx context.Context, token string, repositoryID int64, a backend.Attachment, host string) (string, error) {
	query := url.Values{"name": {path.Base(a.Path)}, "content_type": {a.MediaType}, "repository_id": {strconv.FormatInt(repositoryID, 10)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/user-attachments/assets?"+query.Encode(), bytes.NewReader(a.Data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/octet-stream")
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return errors.New("attachment upload redirect refused")
	}}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub screenshot upload returned HTTP %d", response.StatusCode)
	}
	var result struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&result); err != nil {
		return "", err
	}
	u, err := url.Parse(result.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasPrefix(u.Path, "/user-attachments/assets/") || u.User != nil {
		return "", errors.New("GitHub returned an invalid attachment URL")
	}
	return result.URL, nil
}
