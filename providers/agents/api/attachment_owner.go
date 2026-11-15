// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var selfSubjectReviews = schema.GroupVersionResource{
	Group: "authentication.k8s.io", Version: "v1", Resource: "selfsubjectreviews",
}

// resolveAuthenticatedKCPSubject asks the caller-authenticated API server for
// the Kubernetes principal attached to this bearer. The hub's browser user ID
// is a Railgrid User UUID and is not necessarily the username KCP stores on a
// data-plane request, so it cannot own upload bytes or session grants directly.
func (s *Server) resolveAuthenticatedKCPSubject(ctx context.Context, id identity) (string, error) {
	if s == nil || s.tenant == nil || strings.TrimSpace(id.clusterID) == "" || strings.TrimSpace(id.token) == "" {
		return "", errors.New("a current caller bearer is required to resolve Agent attachment ownership")
	}
	key := subjectCacheKey(id.clusterID, id.token)
	if subject, ok := s.subjects.get(key, time.Now()); ok {
		return subject, nil
	}
	subject, err := s.reviewKCPSubject(ctx, id)
	if err != nil {
		return "", err
	}
	s.subjects.put(key, subject, time.Now())
	return subject, nil
}

func (s *Server) reviewKCPSubject(ctx context.Context, id identity) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	scope, err := s.tenant.For(id.clusterID, id.token)
	if err != nil {
		return "", fmt.Errorf("build caller identity lookup: %w", err)
	}
	request := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authentication.k8s.io/v1",
		"kind":       "SelfSubjectReview",
		"spec":       map[string]any{},
	}}
	response, err := scope.Dynamic().Resource(selfSubjectReviews).Create(ctx, request, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("resolve current caller identity: %w", err)
	}
	username, found, err := unstructured.NestedString(response.Object, "status", "userInfo", "username")
	username = strings.TrimSpace(username)
	if err != nil || !found || username == "" || username == "system:anonymous" {
		return "", errors.New("the current caller identity response is incomplete")
	}
	return username, nil
}

// subjectCacheTTL bounds how long a bearer's resolved username is reused. The
// cached value is only an identity label for ownership; every kcp call the
// turn makes still presents the bearer, so a revoked token fails there.
const (
	subjectCacheTTL     = time.Minute
	subjectCacheEntries = 1024
)

type subjectCacheEntry struct {
	subject string
	expires time.Time
}

// subjectCache is keyed by a digest of the cluster and bearer, never the
// bearer itself.
type subjectCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]subjectCacheEntry
}

func subjectCacheKey(clusterID, token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(clusterID + "\x00" + token))
}

func (c *subjectCache) get(key [sha256.Size]byte, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !now.Before(entry.expires) {
		delete(c.entries, key)
		return "", false
	}
	return entry.subject, true
}

func (c *subjectCache) put(key [sha256.Size]byte, subject string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[[sha256.Size]byte]subjectCacheEntry{}
	}
	if len(c.entries) >= subjectCacheEntries {
		for k, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= subjectCacheEntries {
			c.entries = map[[sha256.Size]byte]subjectCacheEntry{}
		}
	}
	c.entries[key] = subjectCacheEntry{subject: subject, expires: now.Add(subjectCacheTTL)}
}

// resumeAttachmentCaller is evaluated before claiming a paused run. A failed
// caller lookup leaves both the run and its decision available for retry.
func (s *Server) resumeAttachmentCaller(ctx context.Context, checkpoint runCheckpoint, access runAccess) (string, error) {
	if strings.TrimSpace(checkpoint.AttachmentOwner) == "" {
		return "", nil
	}
	if access.AttachmentCaller != "" {
		return access.AttachmentCaller, nil
	}
	if strings.TrimSpace(access.CallerUser) == "" {
		// An unattended recovery replays the already accepted owner binding.
		return "", nil
	}
	if strings.TrimSpace(access.HubToken) != "" {
		return s.resolveAuthenticatedKCPSubject(ctx, identity{clusterID: access.ClusterID, user: access.CallerUser, token: access.HubToken})
	}
	// A data-plane approval carries the canonical kcp username from its gate.
	return strings.TrimSpace(access.CallerUser), nil
}
