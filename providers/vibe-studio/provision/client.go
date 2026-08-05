// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package provision holds everything needed to bring a project's development
// sandbox to life: the infrastructure data-plane client, scaffold fetching,
// workspacePath routing, and the code provider's commit tool. It is shared by
// the HTTP layer (per-request, as the calling user) and the Session
// reconciler (in the background, as the project's ServiceAccount) — the only
// difference between them is which bearer token the Client carries.
package provision

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	VerbSync    = "sync"
	VerbProcess = "process"
	VerbLog     = "log"
	VerbRestart = "restart"
	// VerbProxy forwards a plain HTTP request to an instance's Service. It is
	// how the studio reaches its private search backend.
	VerbProxy = "proxy"

	callTimeout = 30 * time.Second
	maxResponse = 16 << 20
)

// Ref addresses one instance, optionally one of its components.
type Ref struct {
	Resource  string
	Name      string
	Component string
}

// File is one workspace-relative text file.
type File struct {
	Path    string
	Content string
}

// Client reaches tenant runtimes through the hub as some identity: the
// calling user (HTTP path) or a project ServiceAccount (reconciler path).
type Client struct {
	HubBase   string
	ClusterID string
	Token     string
	Insecure  bool

	http *http.Client
}

// NewClient builds a client for one workspace cluster and bearer token.
func NewClient(hubBase, clusterID, token string, insecure bool) *Client {
	return &Client{
		HubBase:   strings.TrimRight(hubBase, "/"),
		ClusterID: clusterID,
		Token:     token,
		Insecure:  insecure,
		http: &http.Client{
			Timeout: callTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, //nolint:gosec // dev opt-in, same knob as the heartbeat
			},
		},
	}
}

// Ready reports whether the client can address a runtime at all.
func (c *Client) Ready() bool {
	return c != nil && c.HubBase != "" && c.ClusterID != "" && c.Token != ""
}

func (c *Client) dataPlaneURL(ref Ref, verb string) (string, error) {
	if c.HubBase == "" {
		return "", fmt.Errorf("hub URL is not configured (KEDGE_HUB_URL)")
	}
	if c.ClusterID == "" {
		return "", fmt.Errorf("no workspace cluster on this call")
	}
	if ref.Resource == "" || ref.Name == "" {
		return "", fmt.Errorf("development target is incomplete (resource %q, name %q)", ref.Resource, ref.Name)
	}
	u := c.HubBase + "/services/providers/infrastructure/dataplane/clusters/" + url.PathEscape(c.ClusterID) +
		"/" + url.PathEscape(ref.Resource) + "/" + url.PathEscape(ref.Name)
	if ref.Component != "" {
		u += "/components/" + url.PathEscape(ref.Component)
	}
	return u + "/" + url.PathEscape(verb), nil
}

// GetPath issues a GET through a data-plane verb that proxies raw HTTP:
// subpath and query are appended to the verb, so a searxng instance's
// /search?q=… is reachable without exposing it to the internet.
func (c *Client) GetPath(ctx context.Context, ref Ref, verb, subpath string, query url.Values) ([]byte, int, error) {
	u, err := c.dataPlaneURL(ref, verb)
	if err != nil {
		return nil, 0, err
	}
	if subpath != "" {
		u += "/" + strings.TrimLeft(subpath, "/")
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	return body, resp.StatusCode, err
}

// Call issues one data-plane verb. A non-2xx status is returned, not an error
// — callers decide what a given status means.
func (c *Client) Call(ctx context.Context, ref Ref, verb, method string, payload []byte) ([]byte, int, error) {
	u, err := c.dataPlaneURL(ref, verb)
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("development data plane %s: %w", verb, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("development data plane %s: %w", verb, err)
	}
	return out, resp.StatusCode, nil
}
