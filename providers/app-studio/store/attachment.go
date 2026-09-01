// Copyright 2026 The Faros Authors.
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

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// AttachmentMaxBytes is the hard upper bound for one binary upload. The
	// HTTP layer applies this limit before reading the request body, while the
	// store repeats it for non-HTTP callers.
	AttachmentMaxBytes = 8 << 20
	// AttachmentMaxTextBytes keeps text attachments suitable for prompt
	// selection and prevents a text upload from becoming an unbounded transcript.
	AttachmentMaxTextBytes     = 1 << 20
	AttachmentMaxFilenameBytes = 255
	AttachmentMaxIDBytes       = 128
	// DefaultAttachmentWorkspaceMaxBytes is the finite hosting protection for
	// all attachment bytes in one workspace. A configured value of zero means
	// unlimited workspace storage.
	DefaultAttachmentWorkspaceMaxBytes = 1 << 30
	// DefaultAttachmentDraftProjectMaxBytes and
	// DefaultAttachmentDraftProjectMaxCount protect one project from abandoned
	// upload drafts. Bound attachments are governed only by the workspace
	// quota, because their lifetime is tied to a durable conversation turn.
	DefaultAttachmentDraftProjectMaxBytes = 64 << 20
	DefaultAttachmentDraftProjectMaxCount = 64
	// AttachmentProjectMaxBytes and AttachmentProjectMaxCount are retained as
	// source-compatible aliases for callers that used the former project-wide
	// quota. They now describe draft-only abuse limits.
	AttachmentProjectMaxBytes = DefaultAttachmentDraftProjectMaxBytes
	AttachmentProjectMaxCount = DefaultAttachmentDraftProjectMaxCount
	// DefaultAttachmentDraftRetention bounds uncommitted composer uploads. A
	// caller can shorten this through provider configuration, but cannot make a
	// draft immortal by omitting an expiry.
	DefaultAttachmentDraftRetention = 24 * time.Hour
)

var (
	ErrAttachmentNotFound        = errors.New("attachment not found")
	ErrAttachmentConflict        = errors.New("attachment already exists")
	ErrAttachmentForbidden       = errors.New("attachment ownership check failed")
	ErrAttachmentImmutable       = errors.New("attachment is immutable")
	ErrAttachmentReceiptMismatch = errors.New("attachment receipt does not match stored metadata")
	ErrAttachmentQuotaExceeded   = errors.New("attachment quota exceeded")
	ErrAttachmentProjectDeleted  = errors.New("project attachment storage is closed")
)

// AttachmentStorageFinalizer is held by the Project controller while its
// attachment scope is being closed. Keeping the name in the storage package
// prevents the API and controller from drifting onto different finalizers.
const AttachmentStorageFinalizer = "ai.faros.sh/attachment-storage"

// AttachmentQuota controls attachment storage admission. WorkspaceMaxBytes
// applies to bound and draft rows across every project in a workspace. The
// draft limits apply only to unbound rows in the current project. A zero limit
// means unlimited for that dimension.
type AttachmentQuota struct {
	WorkspaceMaxBytes    int64
	DraftProjectMaxBytes int64
	DraftProjectMaxCount int
}

// DefaultAttachmentQuota returns the hosting-safe defaults. Draft limits are
// deliberately retained as a separate abuse guard from the workspace budget.
func DefaultAttachmentQuota() AttachmentQuota {
	return AttachmentQuota{
		WorkspaceMaxBytes:    DefaultAttachmentWorkspaceMaxBytes,
		DraftProjectMaxBytes: DefaultAttachmentDraftProjectMaxBytes,
		DraftProjectMaxCount: DefaultAttachmentDraftProjectMaxCount,
	}
}

// ParseAttachmentQuotaBytes parses a non-negative byte quota. Plain decimal
// bytes and IEC suffixes (Ki, Mi, Gi, Ti) are accepted so environment values
// remain readable while retaining exact integer semantics.
func ParseAttachmentQuotaBytes(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("attachment quota is empty")
	}
	valuePart, multiplier := raw, int64(1)
	for suffix, factor := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40} {
		if len(raw) > len(suffix) && strings.EqualFold(raw[len(raw)-len(suffix):], suffix) {
			valuePart = strings.TrimSpace(raw[:len(raw)-len(suffix)])
			multiplier = factor
			break
		}
	}
	value, err := strconv.ParseInt(valuePart, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("attachment quota must be a non-negative byte count or IEC value")
	}
	if value > int64(^uint64(0)>>1)/multiplier {
		return 0, fmt.Errorf("attachment quota is too large")
	}
	return value * multiplier, nil
}

func validateAttachmentQuota(quota AttachmentQuota) error {
	if quota.WorkspaceMaxBytes < 0 {
		return fmt.Errorf("attachment workspace quota cannot be negative")
	}
	if quota.DraftProjectMaxBytes < 0 {
		return fmt.Errorf("attachment draft project byte quota cannot be negative")
	}
	if quota.DraftProjectMaxCount < 0 {
		return fmt.Errorf("attachment draft project count quota cannot be negative")
	}
	return nil
}

// AttachmentQuotaConfigurer is implemented by stores that can apply the
// provider's startup quota configuration.
type AttachmentQuotaConfigurer interface {
	ConfigureAttachmentQuota(AttachmentQuota) error
}

// AttachmentBindingReconciler repairs rows left bound without a durable
// owning conversation turn. It is called during provider startup before new
// assistant work is admitted.
type AttachmentBindingReconciler interface {
	ReconcileAttachmentBindings(context.Context) error
}

// Attachment is an immutable project-scoped blob and its receipt metadata.
// Data is omitted from JSON because API responses expose a receipt separately;
// DataEncrypted/DataKeyID are persistence-internal and are retained so the
// envelope-encryption decorator can round-trip through the backing store.
type Attachment struct {
	ID            string     `json:"id"`
	ProjectName   string     `json:"projectName,omitempty"`
	ProjectUID    string     `json:"projectUID,omitempty"`
	ActorID       string     `json:"actorID"`
	Filename      string     `json:"filename"`
	ContentType   string     `json:"contentType"`
	SizeBytes     int64      `json:"sizeBytes"`
	SHA256        string     `json:"sha256"`
	Draft         bool       `json:"draft"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	Data          []byte     `json:"-"`
	DataEncrypted bool       `json:"-"`
	DataKeyID     string     `json:"-"`
	// BindingID identifies the durable assistant run that retained this draft.
	// DraftExpiresAt preserves its prior expiry so a compensated run start can
	// atomically restore the complete batch to its retryable draft state.
	BindingID      string     `json:"-"`
	DraftExpiresAt *time.Time `json:"-"`
}

// AttachmentReceipt is the client-carried, immutable metadata used to bind a
// later assistant turn to an upload. It intentionally has no bytes.
type AttachmentReceipt struct {
	ID          string
	Filename    string
	ContentType string
	SizeBytes   int64
	SHA256      string
	CreatedAt   time.Time
}

// AttachmentStore is deliberately separate from Store. This lets existing
// message-only test doubles continue to compile while a production Store can
// expose both capabilities.
type AttachmentStore interface {
	CreateAttachment(context.Context, Scope, Attachment) (Attachment, error)
	ListAttachments(context.Context, Scope) ([]Attachment, error)
	GetAttachment(context.Context, Scope, string) (Attachment, error)
	BindAttachment(context.Context, Scope, AttachmentReceipt, string) (Attachment, error)
	BindAttachments(context.Context, Scope, []AttachmentReceipt, string, string) ([]Attachment, error)
	RollbackAttachmentBinding(context.Context, Scope, string) error
	DeleteAttachment(context.Context, Scope, string, string) error
	DeleteProjectAttachments(context.Context, Scope) error
	DeleteExpiredAttachments(context.Context, time.Time) (int64, error)
}

// CreateAttachmentIdempotent admits a client-chosen upload ID while making a
// retry after an ambiguous HTTP response safe. The returned boolean is true
// only when this call inserted a new row. Existing rows are matched by the
// authenticated actor and immutable content metadata; lifecycle timestamps
// and binding state are deliberately excluded because a retry can arrive
// after the original upload has been admitted to a turn.
//
// The helper performs a read before create for the common retry path, then
// repeats the read after a conflict or quota race. This keeps the behavior
// available to existing AttachmentStore implementations while preserving the
// database transaction/locking semantics of their CreateAttachment method.
func CreateAttachmentIdempotent(ctx context.Context, attachments AttachmentStore, scope Scope, candidate Attachment) (Attachment, bool, error) {
	if attachments == nil {
		return Attachment{}, false, ErrAttachmentNotFound
	}
	prepared, err := prepareAttachment(scope, candidate)
	if err != nil {
		return Attachment{}, false, err
	}
	matchExisting := func(existing Attachment) (Attachment, bool, error) {
		if !attachmentUploadMetadataMatches(existing, prepared) {
			return Attachment{}, false, fmt.Errorf("%w: client upload ID is already used for different content", ErrAttachmentConflict)
		}
		return existing, false, nil
	}

	existing, err := attachments.GetAttachment(ctx, scope, prepared.ID)
	switch {
	case err == nil:
		return matchExisting(existing)
	case !errors.Is(err, ErrAttachmentNotFound):
		return Attachment{}, false, err
	}

	created, err := attachments.CreateAttachment(ctx, scope, prepared)
	if err == nil {
		return created, true, nil
	}
	// A concurrent upload can win the row lock, or can fill the workspace
	// quota before this request reaches its INSERT. Recover the canonical row
	// whenever the chosen ID now exists; otherwise preserve the original error.
	if !errors.Is(err, ErrAttachmentConflict) && !errors.Is(err, ErrAttachmentQuotaExceeded) {
		return Attachment{}, false, err
	}
	existing, lookupErr := attachments.GetAttachment(ctx, scope, prepared.ID)
	if lookupErr != nil {
		return Attachment{}, false, err
	}
	return matchExisting(existing)
}

func attachmentUploadMetadataMatches(existing, candidate Attachment) bool {
	return existing.ActorID == candidate.ActorID &&
		existing.Filename == candidate.Filename &&
		existing.ContentType == candidate.ContentType &&
		existing.SizeBytes == candidate.SizeBytes &&
		strings.EqualFold(existing.SHA256, candidate.SHA256) &&
		bytes.Equal(existing.Data, candidate.Data)
}

// VerifyAttachmentReceipt performs the authoritative server-side admission
// check for a client-carried receipt. Callers must provide the same complete
// project Scope and authenticated actor used for the turn; matching only the
// client-supplied ID would permit cross-project or stale metadata references.
func VerifyAttachmentReceipt(ctx context.Context, attachments AttachmentStore, scope Scope, receipt AttachmentReceipt, actor string) (Attachment, error) {
	if attachments == nil {
		return Attachment{}, ErrAttachmentNotFound
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return Attachment{}, ErrAttachmentForbidden
	}
	attachment, err := attachments.GetAttachment(ctx, scope, receipt.ID)
	if err != nil {
		return Attachment{}, err
	}
	if err := validateAttachmentReceipt(attachment, receipt, actor); err != nil {
		return Attachment{}, err
	}
	return attachment, nil
}

func validateAttachmentReceipt(attachment Attachment, receipt AttachmentReceipt, actor string) error {
	if attachment.ActorID != actor {
		return ErrAttachmentForbidden
	}
	contentType, err := NormalizeAttachmentContentType(receipt.ContentType)
	if err != nil {
		return ErrAttachmentReceiptMismatch
	}
	if receipt.CreatedAt.IsZero() ||
		attachment.Filename != receipt.Filename ||
		attachment.ContentType != contentType ||
		attachment.SizeBytes != receipt.SizeBytes ||
		!strings.EqualFold(attachment.SHA256, strings.TrimSpace(receipt.SHA256)) ||
		(!receipt.CreatedAt.IsZero() && !attachment.CreatedAt.Equal(receipt.CreatedAt.UTC())) {
		return ErrAttachmentReceiptMismatch
	}
	return nil
}

// NormalizeAttachmentContentType strips parameters and validates the small
// media-type allowlist supported by the MVP upload contract.
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
	switch contentType {
	case "image/png", "image/jpeg", "image/webp", "text/plain", "text/markdown":
		return contentType, nil
	default:
		return "", fmt.Errorf("unsupported attachment content type %q", contentType)
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
	maxBytes := AttachmentMaxBytes
	if strings.HasPrefix(contentType, "text/") {
		maxBytes = AttachmentMaxTextBytes
	}
	if len(data) > maxBytes {
		return fmt.Errorf("attachment is %d bytes; maximum is %d", len(data), maxBytes)
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

func prepareAttachment(scope Scope, attachment Attachment) (Attachment, error) {
	if err := scope.validate(); err != nil {
		return Attachment{}, err
	}
	attachment.ID = strings.TrimSpace(attachment.ID)
	attachment.ActorID = strings.TrimSpace(attachment.ActorID)
	attachment.Filename = strings.TrimSpace(attachment.Filename)
	attachment.ContentType = strings.ToLower(strings.TrimSpace(attachment.ContentType))
	attachment.SHA256 = strings.ToLower(strings.TrimSpace(attachment.SHA256))
	if err := ValidateAttachmentID(attachment.ID); err != nil {
		return Attachment{}, err
	}
	if attachment.ActorID == "" {
		return Attachment{}, fmt.Errorf("attachment actor is required")
	}
	if !utf8.ValidString(attachment.ActorID) {
		return Attachment{}, fmt.Errorf("attachment actor must be valid UTF-8")
	}
	contentType, err := NormalizeAttachmentContentType(attachment.ContentType)
	if err != nil {
		return Attachment{}, err
	}
	attachment.ContentType = contentType
	if attachment.CreatedAt.IsZero() {
		attachment.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	} else {
		// PostgreSQL timestamps round-trip at microsecond precision. Canonicalize
		// every backend before issuing a receipt so the client never carries
		// nanoseconds that durable admission cannot reproduce.
		attachment.CreatedAt = attachment.CreatedAt.UTC().Truncate(time.Microsecond)
	}
	if attachment.ExpiresAt != nil {
		expires := attachment.ExpiresAt.UTC().Truncate(time.Microsecond)
		attachment.ExpiresAt = &expires
	}
	if !attachment.Draft && attachment.ExpiresAt != nil {
		return Attachment{}, fmt.Errorf("non-draft attachments cannot expire")
	}
	if attachment.Draft && attachment.ExpiresAt == nil {
		return Attachment{}, fmt.Errorf("draft attachments require an expiry")
	}
	attachment.ProjectName, attachment.ProjectUID = scope.ProjectName, scope.ProjectUID
	if attachment.SizeBytes <= 0 {
		return Attachment{}, fmt.Errorf("attachment size must be positive")
	}
	if len(attachment.SHA256) != sha256.Size*2 {
		return Attachment{}, fmt.Errorf("attachment sha256 must be a hexadecimal digest")
	}
	if _, err := hex.DecodeString(attachment.SHA256); err != nil {
		return Attachment{}, fmt.Errorf("attachment sha256 must be hexadecimal: %w", err)
	}
	if attachment.DataEncrypted != (strings.TrimSpace(attachment.DataKeyID) != "") {
		return Attachment{}, fmt.Errorf("attachment encryption metadata is inconsistent")
	}
	if attachment.DataEncrypted {
		if len(attachment.Data) == 0 {
			return Attachment{}, fmt.Errorf("encrypted attachment data is required")
		}
	} else {
		if int64(len(attachment.Data)) != attachment.SizeBytes {
			return Attachment{}, fmt.Errorf("attachment size does not match data")
		}
		if err := ValidateAttachmentContent(attachment.Filename, attachment.ContentType, attachment.Data); err != nil {
			return Attachment{}, err
		}
		digest := sha256.Sum256(attachment.Data)
		if !strings.EqualFold(attachment.SHA256, hex.EncodeToString(digest[:])) {
			return Attachment{}, fmt.Errorf("attachment sha256 does not match data")
		}
	}
	return cloneAttachment(attachment), nil
}

func cloneAttachment(attachment Attachment) Attachment {
	attachment.Data = append([]byte(nil), attachment.Data...)
	if attachment.ExpiresAt != nil {
		expires := *attachment.ExpiresAt
		attachment.ExpiresAt = &expires
	}
	if attachment.DraftExpiresAt != nil {
		expires := *attachment.DraftExpiresAt
		attachment.DraftExpiresAt = &expires
	}
	return attachment
}

func validateStoredAttachmentData(attachment Attachment) error {
	if attachment.DataEncrypted {
		return nil
	}
	if int64(len(attachment.Data)) != attachment.SizeBytes {
		return fmt.Errorf("stored attachment size does not match receipt")
	}
	if err := ValidateAttachmentContent(attachment.Filename, attachment.ContentType, attachment.Data); err != nil {
		return fmt.Errorf("stored attachment content is invalid: %w", err)
	}
	digest := sha256.Sum256(attachment.Data)
	if !strings.EqualFold(attachment.SHA256, hex.EncodeToString(digest[:])) {
		return fmt.Errorf("stored attachment sha256 does not match receipt")
	}
	return nil
}

func attachmentExpired(attachment Attachment, before time.Time) bool {
	return attachment.Draft && attachment.ExpiresAt != nil && !attachment.ExpiresAt.After(before.UTC())
}
