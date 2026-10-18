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
	"regexp"
	"strings"

	"github.com/railgrid/provider-code/snapshotstore"
	"k8s.io/client-go/util/retry"

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
			return "", fmt.Errorf("%w: unsupported screenshot Markdown; use simple inline PNG/JPEG paths", backend.ErrAttachmentsUnsupported)
		}
		return body, nil
	}
	if pr.Spec.AttachmentRef == "" {
		return "", fmt.Errorf("%w: screenshot artifact not provided", backend.ErrAttachmentsUnsupported)
	}
	uploader, ok := host.(backend.AttachmentPublisher)
	if !ok {
		return "", fmt.Errorf("%w: git provider cannot upload screenshots", backend.ErrAttachmentsUnsupported)
	}
	snapshot, err := actions.FindStaged(ctx, r.Snapshots, cluster, pr.Spec.AttachmentRef)
	if err != nil {
		if errors.Is(err, snapshotstore.ErrNotFound) {
			return "", fmt.Errorf("%w: screenshot artifact expired or missing", backend.ErrAttachmentsUnsupported)
		}
		return "", err
	}
	if err := backend.ValidateAttachments(snapshot.Attachments); err != nil {
		return "", fmt.Errorf("%w: %v", backend.ErrAttachmentsUnsupported, err)
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
			return "", fmt.Errorf("%w: referenced screenshot %s was not exported", backend.ErrAttachmentsUnsupported, name)
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
			receipt := api.PublishedAttachment{Path: name, Digest: a.Digest(), URL: published}
			if err := recordAttachment(ctx, c, pr, string(repo.UID), receipt, images); err != nil {
				return "", err
			}
		}
		body = strings.ReplaceAll(body, match[0], strings.Replace(match[0], "("+name+")", "("+published+")", 1))
	}
	if strings.Contains(body, "](artifacts/screenshots/") {
		return "", fmt.Errorf("%w: unsupported screenshot Markdown; use simple inline PNG/JPEG paths", backend.ErrAttachmentsUnsupported)
	}
	return body, nil
}

// Retry the receipt write, not the completed external upload. Patch only this
// controller's attachment fields, retaining unrelated concurrent status edits.
func recordAttachment(ctx context.Context, c client.Client, pr *api.PullRequest, repoUID string, receipt api.PublishedAttachment, images map[string]backend.Attachment) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &api.PullRequest{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(pr), current); err != nil {
			return err
		}
		if current.UID != pr.UID {
			return fmt.Errorf("pull request identity changed while recording upload")
		}
		base := current.DeepCopy()
		kept := []api.PublishedAttachment{}
		if current.Status.AttachmentRepositoryUID == repoUID {
			for _, old := range current.Status.UploadedAttachments {
				if image, ok := images[old.Path]; ok && image.Digest() == old.Digest && old.Path != receipt.Path {
					kept = append(kept, old)
				}
			}
		}
		current.Status.AttachmentRepositoryUID = repoUID
		current.Status.UploadedAttachments = append(kept, receipt)
		if err := c.Status().Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		// Do not replace the spec being reconciled if a user edited it concurrently.
		pr.ResourceVersion = current.ResourceVersion
		pr.Status.UploadedAttachments = current.Status.UploadedAttachments
		pr.Status.AttachmentRepositoryUID = current.Status.AttachmentRepositoryUID
		return nil
	})
}

var localScreenshotMarkdown = regexp.MustCompile(`!?\[[^\]\r\n]*\]\(artifacts/screenshots/[^)\r\n]*\)`)

func withoutLocalScreenshots(body string) string {
	clean := localScreenshotMarkdown.ReplaceAllString(body, "")
	if clean != body {
		clean = strings.TrimSpace(clean) + "\n\nScreenshots are unavailable; live visual evidence has not been published."
	}
	return clean
}
