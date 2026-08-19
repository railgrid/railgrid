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
	"context"
	cryptoRand "crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var ErrProjectThumbnailNotFound = errors.New("project thumbnail not found")

// ProjectThumbnail is derived presentation data. It is intentionally outside
// the Project API and repository so screenshot churn never mutates desired
// state or source history.
type ProjectThumbnail struct {
	CommitSHA       string
	CommitCreatedAt time.Time
	CommitOrder     string
	Variant         string
	ContentType     string
	SHA256          string
	Data            []byte
	DataEncrypted   bool
	DataKeyID       string
	CapturedAt      time.Time
}

// ProjectThumbnailMetadata is the lightweight gallery projection. It omits
// image bytes and encryption details so project listings never read or decrypt
// the binary payload that the browser fetches separately.
type ProjectThumbnailMetadata struct {
	CommitSHA       string
	CommitCreatedAt time.Time
	CommitOrder     string
	Variant         string
	ContentType     string
	SHA256          string
	CapturedAt      time.Time
}

// ProjectThumbnailStore is optional so message-store decorators and test
// doubles do not gain a presentation concern. Production and the explicit
// in-memory development store implement it.
type ProjectThumbnailStore interface {
	GetProjectThumbnailMetadata(context.Context, Scope) (ProjectThumbnailMetadata, error)
	GetProjectThumbnail(context.Context, Scope) (ProjectThumbnail, error)
	PutProjectThumbnail(context.Context, Scope, ProjectThumbnail) error
	DeleteProjectThumbnail(context.Context, Scope) error
}

func validateProjectThumbnail(scope Scope, thumbnail ProjectThumbnail) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(thumbnail.CommitSHA) == "" || strings.TrimSpace(thumbnail.CommitOrder) == "" || strings.TrimSpace(thumbnail.Variant) == "" || strings.TrimSpace(thumbnail.SHA256) == "" || len(thumbnail.Data) == 0 {
		return fmt.Errorf("thumbnail commit, order, variant, digest, and image data are required")
	}
	if thumbnail.CommitCreatedAt.IsZero() {
		return fmt.Errorf("thumbnail commit creation time is required")
	}
	if thumbnail.ContentType != "image/png" && thumbnail.ContentType != "image/jpeg" {
		return fmt.Errorf("unsupported thumbnail content type %q", thumbnail.ContentType)
	}
	if thumbnail.DataEncrypted != (strings.TrimSpace(thumbnail.DataKeyID) != "") {
		return fmt.Errorf("thumbnail encryption metadata is inconsistent")
	}
	if thumbnail.CapturedAt.IsZero() {
		return fmt.Errorf("thumbnail capture time is required")
	}
	return nil
}

func projectThumbnailMetadata(in ProjectThumbnail) ProjectThumbnailMetadata {
	return ProjectThumbnailMetadata{
		CommitSHA: in.CommitSHA, CommitCreatedAt: in.CommitCreatedAt, CommitOrder: in.CommitOrder,
		Variant: in.Variant, ContentType: in.ContentType, SHA256: in.SHA256, CapturedAt: in.CapturedAt,
	}
}

func projectThumbnailNewer(leftCreatedAt time.Time, leftOrder string, rightCreatedAt time.Time, rightOrder string) bool {
	if leftCreatedAt.After(rightCreatedAt) {
		return true
	}
	if leftCreatedAt.Before(rightCreatedAt) {
		return false
	}
	return strings.TrimSpace(leftOrder) > strings.TrimSpace(rightOrder)
}

func (s *MemoryStore) GetProjectThumbnailMetadata(_ context.Context, scope Scope) (ProjectThumbnailMetadata, error) {
	if err := scope.validate(); err != nil {
		return ProjectThumbnailMetadata{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	thumbnail, ok := s.projectThumbnails[scope]
	if !ok {
		return ProjectThumbnailMetadata{}, ErrProjectThumbnailNotFound
	}
	return projectThumbnailMetadata(thumbnail), nil
}

func cloneProjectThumbnail(in ProjectThumbnail) ProjectThumbnail {
	in.Data = append([]byte(nil), in.Data...)
	return in
}

func (s *MemoryStore) GetProjectThumbnail(_ context.Context, scope Scope) (ProjectThumbnail, error) {
	if err := scope.validate(); err != nil {
		return ProjectThumbnail{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	thumbnail, ok := s.projectThumbnails[scope]
	if !ok {
		return ProjectThumbnail{}, ErrProjectThumbnailNotFound
	}
	return cloneProjectThumbnail(thumbnail), nil
}

func (s *MemoryStore) PutProjectThumbnail(_ context.Context, scope Scope, thumbnail ProjectThumbnail) error {
	if err := validateProjectThumbnail(scope, thumbnail); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projectThumbnails == nil {
		s.projectThumbnails = map[Scope]ProjectThumbnail{}
	}
	if current, ok := s.projectThumbnails[scope]; ok && projectThumbnailNewer(current.CommitCreatedAt, current.CommitOrder, thumbnail.CommitCreatedAt, thumbnail.CommitOrder) {
		return nil
	}
	s.projectThumbnails[scope] = cloneProjectThumbnail(thumbnail)
	return nil
}

func (s *PostgresStore) GetProjectThumbnailMetadata(ctx context.Context, scope Scope) (ProjectThumbnailMetadata, error) {
	if s == nil || s.db == nil {
		return ProjectThumbnailMetadata{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return ProjectThumbnailMetadata{}, err
	}
	var metadata ProjectThumbnailMetadata
	err := s.db.QueryRowContext(ctx, `
		SELECT commit_sha, commit_created_at, commit_order, variant, content_type, sha256, captured_at
		FROM app_studio_project_thumbnails
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID).Scan(
		&metadata.CommitSHA, &metadata.CommitCreatedAt, &metadata.CommitOrder, &metadata.Variant,
		&metadata.ContentType, &metadata.SHA256, &metadata.CapturedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectThumbnailMetadata{}, ErrProjectThumbnailNotFound
	}
	if err != nil {
		return ProjectThumbnailMetadata{}, fmt.Errorf("get project thumbnail metadata: %w", err)
	}
	return metadata, nil
}

func (s *MemoryStore) DeleteProjectThumbnail(_ context.Context, scope Scope) error {
	if err := scope.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.projectThumbnails, scope)
	return nil
}

func (s *PostgresStore) GetProjectThumbnail(ctx context.Context, scope Scope) (ProjectThumbnail, error) {
	if s == nil || s.db == nil {
		return ProjectThumbnail{}, fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return ProjectThumbnail{}, err
	}
	var thumbnail ProjectThumbnail
	err := s.db.QueryRowContext(ctx, `
		SELECT commit_sha, commit_created_at, commit_order, variant, content_type, sha256, image_bytes, image_encrypted, image_key_id, captured_at
		FROM app_studio_project_thumbnails
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID).Scan(
		&thumbnail.CommitSHA, &thumbnail.CommitCreatedAt, &thumbnail.CommitOrder, &thumbnail.Variant, &thumbnail.ContentType, &thumbnail.SHA256,
		&thumbnail.Data, &thumbnail.DataEncrypted, &thumbnail.DataKeyID, &thumbnail.CapturedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectThumbnail{}, ErrProjectThumbnailNotFound
	}
	if err != nil {
		return ProjectThumbnail{}, fmt.Errorf("get project thumbnail: %w", err)
	}
	return thumbnail, nil
}

func (s *PostgresStore) PutProjectThumbnail(ctx context.Context, scope Scope, thumbnail ProjectThumbnail) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store is nil")
	}
	if err := validateProjectThumbnail(scope, thumbnail); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_studio_project_thumbnails (
			org_uuid, workspace_uuid, project_name, project_uid, commit_sha, commit_created_at, commit_order, variant, content_type, sha256,
			image_bytes, image_encrypted, image_key_id, captured_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid) DO UPDATE SET
			commit_sha = EXCLUDED.commit_sha, commit_created_at = EXCLUDED.commit_created_at, commit_order = EXCLUDED.commit_order,
			variant = EXCLUDED.variant, content_type = EXCLUDED.content_type,
			sha256 = EXCLUDED.sha256, image_bytes = EXCLUDED.image_bytes, image_encrypted = EXCLUDED.image_encrypted,
			image_key_id = EXCLUDED.image_key_id, captured_at = EXCLUDED.captured_at
		WHERE (app_studio_project_thumbnails.commit_created_at, app_studio_project_thumbnails.commit_order)
			<= (EXCLUDED.commit_created_at, EXCLUDED.commit_order)
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		thumbnail.CommitSHA, thumbnail.CommitCreatedAt, thumbnail.CommitOrder, thumbnail.Variant, thumbnail.ContentType, thumbnail.SHA256,
		thumbnail.Data, thumbnail.DataEncrypted, thumbnail.DataKeyID, thumbnail.CapturedAt)
	if err != nil {
		return fmt.Errorf("put project thumbnail: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteProjectThumbnail(ctx context.Context, scope Scope) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store is nil")
	}
	if err := scope.validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM app_studio_project_thumbnails
		WHERE org_uuid = $1 AND workspace_uuid = $2 AND project_name = $3 AND project_uid = $4
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID)
	if err != nil {
		return fmt.Errorf("delete project thumbnail: %w", err)
	}
	return nil
}

func (s *encryptedStore) GetProjectThumbnail(ctx context.Context, scope Scope) (ProjectThumbnail, error) {
	thumbnailStore, ok := s.inner.(ProjectThumbnailStore)
	if !ok {
		return ProjectThumbnail{}, ErrProjectThumbnailNotFound
	}
	thumbnail, err := thumbnailStore.GetProjectThumbnail(ctx, scope)
	if err != nil || !thumbnail.DataEncrypted {
		return thumbnail, err
	}
	aead := s.keys[thumbnail.DataKeyID]
	if aead == nil {
		return ProjectThumbnail{}, fmt.Errorf("project thumbnail uses unknown encryption key %q", thumbnail.DataKeyID)
	}
	if len(thumbnail.Data) < aead.NonceSize() {
		return ProjectThumbnail{}, fmt.Errorf("encrypted project thumbnail is too short")
	}
	plaintext, err := aead.Open(nil, thumbnail.Data[:aead.NonceSize()], thumbnail.Data[aead.NonceSize():], projectThumbnailAAD(scope, thumbnail))
	if err != nil {
		return ProjectThumbnail{}, fmt.Errorf("decrypt project thumbnail: %w", err)
	}
	thumbnail.Data = plaintext
	thumbnail.DataEncrypted = false
	thumbnail.DataKeyID = ""
	return thumbnail, nil
}

func (s *encryptedStore) GetProjectThumbnailMetadata(ctx context.Context, scope Scope) (ProjectThumbnailMetadata, error) {
	thumbnailStore, ok := s.inner.(ProjectThumbnailStore)
	if !ok {
		return ProjectThumbnailMetadata{}, ErrProjectThumbnailNotFound
	}
	return thumbnailStore.GetProjectThumbnailMetadata(ctx, scope)
}

func (s *encryptedStore) PutProjectThumbnail(ctx context.Context, scope Scope, thumbnail ProjectThumbnail) error {
	thumbnailStore, ok := s.inner.(ProjectThumbnailStore)
	if !ok {
		return fmt.Errorf("inner store does not support project thumbnails")
	}
	if err := validateProjectThumbnail(scope, thumbnail); err != nil {
		return err
	}
	aead := s.keys[s.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(cryptoRand.Reader, nonce); err != nil {
		return fmt.Errorf("generate project thumbnail nonce: %w", err)
	}
	thumbnail.Data = append(nonce, aead.Seal(nil, nonce, thumbnail.Data, projectThumbnailAAD(scope, thumbnail))...)
	thumbnail.DataEncrypted = true
	thumbnail.DataKeyID = s.active
	return thumbnailStore.PutProjectThumbnail(ctx, scope, thumbnail)
}

func projectThumbnailAAD(scope Scope, thumbnail ProjectThumbnail) []byte {
	return []byte(strings.Join([]string{
		scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID,
		thumbnail.CommitSHA, thumbnail.SHA256,
	}, "\x00"))
}

func (s *encryptedStore) DeleteProjectThumbnail(ctx context.Context, scope Scope) error {
	thumbnailStore, ok := s.inner.(ProjectThumbnailStore)
	if !ok {
		return nil
	}
	return thumbnailStore.DeleteProjectThumbnail(ctx, scope)
}
