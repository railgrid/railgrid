/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sharedstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/browsersession"
	"github.com/faroshq/faros/pkg/hub/appauth"
)

// AppCodeKind is the shared-store collection holding published-app
// authorization codes.
const AppCodeKind = "faros-appcode"

// AppCodeStore adapts Store to appauth.CodeStore, so a code minted on the
// replica that served the browser's authorize hop can be redeemed on whichever
// replica the access proxy reaches for the exchange.
//
// Single-use is enforced by Store.Take's resource-version-guarded delete: two
// concurrent exchanges of one code produce exactly one success, no matter which
// replicas run them.
type AppCodeStore struct {
	store *Store
}

// NewAppCodeStore builds the shared authorization-code store. config must
// target the workspace holding the entries.
func NewAppCodeStore(config *rest.Config, namespace string) (*AppCodeStore, error) {
	store, err := New(config, namespace, AppCodeKind)
	if err != nil {
		return nil, err
	}
	return &AppCodeStore{store: store}, nil
}

// Store exposes the underlying collection so the leader can sweep it.
func (s *AppCodeStore) Store() *Store { return s.store }

// storedCode is the wire form of an authorization code record.
type storedCode struct {
	Cluster      string    `json:"cluster"`
	Group        string    `json:"group"`
	Resource     string    `json:"resource"`
	Name         string    `json:"name"`
	RedirectHost string    `json:"redirectHost"`
	UserID       string    `json:"userID"`
	Email        string    `json:"email,omitempty"`
	DisplayName  string    `json:"displayName,omitempty"`
	RBACIdentity string    `json:"rbacIdentity,omitempty"`
	Issuer       string    `json:"issuer,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	AuthType     string    `json:"authType,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

func (s *AppCodeStore) Put(ctx context.Context, code string, record appauth.CodeRecord) error {
	value, err := json.Marshal(storedCode{
		Cluster:      record.Ref.Cluster,
		Group:        record.Ref.Group,
		Resource:     record.Ref.Resource,
		Name:         record.Ref.Name,
		RedirectHost: record.RedirectHost,
		UserID:       record.Identity.UserID,
		Email:        record.Identity.Email,
		DisplayName:  record.Identity.Name,
		RBACIdentity: record.Identity.RBACIdentity,
		Issuer:       record.Identity.Issuer,
		Subject:      record.Identity.Subject,
		AuthType:     record.Identity.AuthType,
		ExpiresAt:    record.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("encoding app authorization code: %w", err)
	}
	return s.store.Put(ctx, code, value, record.ExpiresAt)
}

func (s *AppCodeStore) Take(ctx context.Context, code string) (appauth.CodeRecord, bool) {
	value, err := s.store.Take(ctx, code)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			// A store failure must not be reported as "code accepted"; log it
			// so an outage is distinguishable from an expired code.
			klog.FromContext(ctx).Error(err, "Redeeming app authorization code failed")
		}
		return appauth.CodeRecord{}, false
	}
	var stored storedCode
	if err := json.Unmarshal(value, &stored); err != nil {
		return appauth.CodeRecord{}, false
	}
	return appauth.CodeRecord{
		Ref: appauth.InstanceRef{
			Cluster:  stored.Cluster,
			Group:    stored.Group,
			Resource: stored.Resource,
			Name:     stored.Name,
		},
		RedirectHost: stored.RedirectHost,
		Identity: browsersession.Identity{
			UserID:       stored.UserID,
			Email:        stored.Email,
			Name:         stored.DisplayName,
			RBACIdentity: stored.RBACIdentity,
			Issuer:       stored.Issuer,
			Subject:      stored.Subject,
			AuthType:     stored.AuthType,
		},
		ExpiresAt: stored.ExpiresAt,
	}, true
}
