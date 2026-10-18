// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
)

// Attachment carries screenshot bytes in the staged snapshot, never in a CR.
type Attachment struct {
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
	Data      []byte `json:"data"`
}

func (a Attachment) Digest() string { sum := sha256.Sum256(a.Data); return hex.EncodeToString(sum[:]) }

var attachmentPath = regexp.MustCompile(`^artifacts/screenshots/[A-Za-z0-9][A-Za-z0-9._-]*\.(png|jpg|jpeg)$`)

// AttachmentImages is the crawl-version Markdown contract: inline images with simple local paths.
var AttachmentImages = regexp.MustCompile(`!\[[^\]\r\n]*\]\((artifacts/screenshots/[A-Za-z0-9][A-Za-z0-9._-]*\.(?:png|jpg|jpeg))\)`)

func ValidateAttachments(images []Attachment) error {
	if len(images) > 8 {
		return errors.New("at most 8 screenshots are supported")
	}
	seen := map[string]bool{}
	total := 0
	for _, a := range images {
		if !attachmentPath.MatchString(a.Path) || seen[a.Path] {
			return errors.New("invalid or duplicate screenshot path")
		}
		seen[a.Path] = true
		total += len(a.Data)
		if len(a.Data) == 0 || len(a.Data) > 2<<20 || total > 8<<20 {
			return errors.New("screenshots exceed limits: 2 MiB each, 8 MiB total")
		}
		media := http.DetectContentType(a.Data)
		if media != a.MediaType || (media != "image/png" && media != "image/jpeg") {
			return errors.New("screenshot must be PNG or JPEG")
		}
	}
	return nil
}

// AttachmentPublisher uploads images using the repository connection's credential.
type AttachmentPublisher interface {
	UploadAttachment(context.Context, *api.Connection, Credential, *api.Repository, Attachment) (string, error)
}
