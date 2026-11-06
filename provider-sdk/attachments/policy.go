// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package attachments owns the shared assistant upload format and bounds.
package attachments

import (
	"bytes"
	"fmt"
	"mime"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	AttachmentMaxBytes         = 25 << 20
	AttachmentMaxImageBytes    = 8 << 20
	AttachmentMaxTextBytes     = 1 << 20
	AttachmentMaxFilenameBytes = 255
	AttachmentMaxIDBytes       = 128
	MaxAttachmentsPerTurn      = 8
	MaxBytesPerTurn            = 50 << 20
	InlineTextMaxBytes         = 32 << 10
	ReadTextMaxBytes           = 64 << 10
	CurrentImageMaxBytes       = 20 << 20
	DefaultWorkspaceMaxBytes   = 1 << 30
	DefaultDraftOwnerMaxBytes  = 128 << 20
	DefaultDraftOwnerMaxCount  = 64
	DefaultDraftRetention      = 24 * time.Hour
)

// Receipt binds immutable upload metadata to a conversation turn; it contains no bytes.
type Receipt struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"contentType"`
	SizeBytes   int64     `json:"sizeBytes"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ReceiptsMatch compares the entire immutable receipt, including normalized media type and digest.
func ReceiptsMatch(expected, actual Receipt) bool {
	return UploadMetadataMatches(expected, actual) && !actual.CreatedAt.IsZero() && expected.CreatedAt.Equal(actual.CreatedAt.UTC())
}

// UploadMetadataMatches recognizes a retry of the same upload before its
// receipt has been issued. CreatedAt is assigned by the server, so it must not
// make an otherwise identical retry conflict. Scope, actor, and bytes remain
// the storage adapter's responsibility. Accepted turns use ReceiptsMatch.
func UploadMetadataMatches(expected, actual Receipt) bool {
	contentType, err := NormalizeAttachmentContentType(actual.ContentType)
	if err != nil {
		return false
	}
	return expected.ID == actual.ID && expected.Filename == actual.Filename && expected.ContentType == contentType && expected.SizeBytes == actual.SizeBytes && strings.EqualFold(expected.SHA256, strings.TrimSpace(actual.SHA256))
}

// Attachment kinds. Images and text are model-visible content; any other
// file is stored as opaque bytes and reaches the model as metadata only (it
// can be placed into the project workspace with import_attachment).
const (
	AttachmentKindImage = "image"
	AttachmentKindText  = "text"
	AttachmentKindFile  = "file"
)

// AttachmentMaxContentTypeBytes bounds a stored media type.
const AttachmentMaxContentTypeBytes = 127

// AttachmentKind classifies a normalized content type.
func AttachmentKind(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png", "image/jpeg", "image/webp":
		return AttachmentKindImage
	case "text/plain", "text/markdown":
		return AttachmentKindText
	default:
		return AttachmentKindFile
	}
}

// NormalizeAttachmentContentType strips parameters and validates a
// "type/subtype" media type. Image and text types keep their dedicated
// validation; every other type is an opaque "file" attachment.
func NormalizeAttachmentContentType(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("attachment content type is required")
	}
	contentType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", fmt.Errorf("invalid attachment content type: %w", err)
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	major, minor, ok := strings.Cut(contentType, "/")
	if !ok || major == "" || minor == "" || len(contentType) > AttachmentMaxContentTypeBytes {
		return "", fmt.Errorf("unsupported attachment content type %q", contentType)
	}
	return contentType, nil
}

// AttachmentKindFor classifies an attachment by type and size: an image or
// text attachment beyond its model-visible bound is an opaque "file" (for
// example a 12 MiB hero image meant for public/), up to AttachmentMaxBytes.
func AttachmentKindFor(contentType string, size int64) string {
	kind := AttachmentKind(contentType)
	if kind != AttachmentKindFile && size > int64(AttachmentMaxBytesFor(kind)) {
		return AttachmentKindFile
	}
	return kind
}

// AttachmentMaxBytesFor returns the per-attachment bound for one kind.
func AttachmentMaxBytesFor(kind string) int {
	switch kind {
	case AttachmentKindImage:
		return AttachmentMaxImageBytes
	case AttachmentKindText:
		return AttachmentMaxTextBytes
	default:
		return AttachmentMaxBytes
	}
}

// ValidateAttachmentID applies the same path-safe bound used by durable
// attachment rows. Callers that accept a client-chosen upload ID should trim
// it first, then use this helper so malformed IDs fail as request validation
// rather than surfacing as a storage conflict.
func ValidateAttachmentID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || !utf8.ValidString(id) || len([]byte(id)) > AttachmentMaxIDBytes || id == "." || id == ".." {
		return fmt.Errorf("attachment ID must be a safe non-empty value of at most %d bytes", AttachmentMaxIDBytes)
	}
	for _, character := range id {
		if unicode.IsSpace(character) || unicode.IsControl(character) || strings.ContainsRune("/\\?#", character) {
			return fmt.Errorf("attachment ID must be a safe non-empty value of at most %d bytes", AttachmentMaxIDBytes)
		}
	}
	return nil
}

// ValidateAttachmentContent applies the format and size contract independently
// of HTTP. Image magic bytes prevent a mislabeled executable from becoming a
// downloadable image; text must be valid UTF-8 and use a .txt/.md filename.
// Any other ("file") type is opaque bytes: only its name and size are
// checked, because it is never interpreted — only stored and copied.
func ValidateAttachmentContent(filename, contentType string, data []byte) error {
	contentType, err := NormalizeAttachmentContentType(contentType)
	if err != nil {
		return err
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return fmt.Errorf("attachment filename is required")
	}
	if !utf8.ValidString(filename) || len([]byte(filename)) > AttachmentMaxFilenameBytes {
		return fmt.Errorf("attachment filename must be valid UTF-8 and at most %d bytes", AttachmentMaxFilenameBytes)
	}
	if strings.ContainsAny(filename, "/\\"+"\x00\r\n") || filename == "." || filename == ".." || path.Base(filename) != filename {
		return fmt.Errorf("attachment filename must be a single safe path segment")
	}
	if len(data) == 0 {
		return fmt.Errorf("attachment data is required")
	}
	kind := AttachmentKindFor(contentType, int64(len(data)))
	maxBytes := AttachmentMaxBytesFor(kind)
	if len(data) > maxBytes {
		return fmt.Errorf("attachment is %d bytes; maximum is %d", len(data), maxBytes)
	}
	if kind == AttachmentKindFile {
		return nil
	}

	switch contentType {
	case "image/png":
		if !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
			return fmt.Errorf("attachment data is not a PNG image")
		}
	case "image/jpeg":
		if len(data) < 3 || data[0] != 0xff || data[1] != 0xd8 || data[2] != 0xff {
			return fmt.Errorf("attachment data is not a JPEG image")
		}
	case "image/webp":
		if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
			return fmt.Errorf("attachment data is not a WebP image")
		}
	case "text/plain", "text/markdown":
		ext := strings.ToLower(path.Ext(filename))
		if ext != ".txt" && ext != ".md" {
			return fmt.Errorf("text attachments must use a .txt or .md filename")
		}
		if !utf8.Valid(data) {
			return fmt.Errorf("text attachment must be valid UTF-8")
		}
	}
	return nil
}
