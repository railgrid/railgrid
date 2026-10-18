// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package pullrequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/railgrid/provider-code/actions"
	api "github.com/railgrid/provider-code/apis/v1alpha1"
	"github.com/railgrid/provider-code/backend"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func textDigest(pr *api.PullRequest) string {
	data, _ := json.Marshal([]string{pr.Spec.RepositoryRef, pr.Spec.Title, pr.Spec.Body, pr.Spec.AttachmentRef})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (r *Reconciler) attachmentBody(ctx context.Context, c client.Client, host forge, conn *api.Connection, cred backend.Credential, repo *api.Repository, cluster string, pr *api.PullRequest) (string, error) {
	body := pr.Spec.Body
	if pr.Status.AttachmentRepositoryUID != string(repo.UID) {
		pr.Status.UploadedAttachments = nil
		pr.Status.AttachmentRepositoryUID = string(repo.UID)
	}
	matches := backend.AttachmentImages.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		if strings.Contains(body, "](artifacts/screenshots/") {
			return "", errors.New("unsupported screenshot Markdown; use simple inline PNG/JPEG paths")
		}
		return body, nil
	}
	if pr.Spec.AttachmentRef == "" {
		return "", errors.New("PR description references screenshots but no attachment artifact was provided")
	}
	uploader, ok := host.(backend.AttachmentPublisher)
	if !ok {
		return "", errors.New("git provider cannot upload screenshots")
	}
	snapshot, err := actions.FindStaged(ctx, r.Snapshots, cluster, pr.Spec.AttachmentRef)
	if err != nil {
		return "", err
	}
	if err := backend.ValidateAttachments(snapshot.Attachments); err != nil {
		return "", err
	}
	images := map[string]backend.Attachment{}
	for _, a := range snapshot.Attachments {
		images[a.Path] = a
	}
	// Keep only uploads that match the current bytes. This bounds status while
	// preserving completed uploads if a later image or PR update fails.
	kept := []api.PublishedAttachment{}
	for _, old := range pr.Status.UploadedAttachments {
		if a, ok := images[old.Path]; ok && a.Digest() == old.Digest {
			kept = append(kept, old)
		}
	}
	pr.Status.UploadedAttachments = kept
	for _, match := range matches {
		name := match[1]
		a, ok := images[name]
		if !ok {
			return "", fmt.Errorf("referenced screenshot %s was not exported", name)
		}
		published := ""
		for _, old := range pr.Status.UploadedAttachments {
			if old.Path == name && old.Digest == a.Digest() {
				published = old.URL
				break
			}
		}
		if published == "" {
			published, err = uploader.UploadAttachment(ctx, conn, cred, repo, a)
			if err != nil {
				return "", err
			}
			pr.Status.UploadedAttachments = append(pr.Status.UploadedAttachments, api.PublishedAttachment{Path: name, Digest: a.Digest(), URL: published})
			if err := c.Status().Update(ctx, pr); err != nil {
				return "", err
			}
		}
		body = strings.ReplaceAll(body, match[0], strings.Replace(match[0], "("+name+")", "("+published+")", 1))
	}
	if strings.Contains(body, "](artifacts/screenshots/") {
		return "", errors.New("unsupported screenshot Markdown; use simple inline PNG/JPEG paths")
	}
	return body, nil
}
