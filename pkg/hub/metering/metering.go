/*
Copyright 2026 The Railgrid Authors.

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

// Package metering is how the hub finds the contrib-metering deployment and
// lets the admin area read it.
//
// Metering is a standalone deployment, not a provider: nothing registers it
// with the hub. What the hub and metering share is kcp, so discovery goes
// through kcp. Metering's init publishes, in the workspace it owns
// (--metering-workspace), a ConfigMap "default/metering-endpoints" with the
// URL of its controller (portal at /, API under /api/, /readyz) and a Secret
// "default/metering-admin" with its operator token. The hub, as kcp-admin,
// polls for them: present means installed. The admin REST then serves
// GET /api/admin/metering (installed, version, ready) and
// GET /api/admin/metering/{collection}, a read-only proxy of the metering
// API with the token injected, so the portal's admin pages render metering
// data behind the hub's own admin gate and never see that token.
package metering

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	endpointsConfigMap = "metering-endpoints"
	adminTokenSecret   = "metering-admin"
	// pollInterval paces discovery. Metering is installed once and rarely
	// moves; a poll keeps an uninstalled platform free of the watch errors an
	// informer on a missing workspace would log forever.
	pollInterval = 30 * time.Second
	// collections is the read-only surface the proxy exposes, exactly the
	// collections metering's own API serves.
	collectionsCSV = "plans,entitlements,accounts,membershipreports,meteringconfigs"
)

// Endpoints is what the hub learned about the deployment.
type Endpoints struct {
	PortalURL  string `json:"portalURL"`
	WebhookURL string `json:"webhookURL,omitempty"`
	AuditURL   string `json:"auditURL,omitempty"`
	Version    string `json:"version,omitempty"`
	Workspace  string `json:"workspace"`
	token      string
}

// Discovery polls the metering workspace for the published objects.
type Discovery struct {
	client    kubernetes.Interface
	workspace string
	logger    klog.Logger

	mu      sync.RWMutex
	current *Endpoints
	checked bool
}

// NewDiscovery builds a Discovery from the hub's kcp config (any cluster
// path; it is re-scoped to workspace).
func NewDiscovery(base *rest.Config, workspace string, logger klog.Logger) (*Discovery, error) {
	cfg := rest.CopyConfig(base)
	host := cfg.Host
	if i := strings.Index(host, "/clusters/"); i != -1 {
		host = host[:i]
	}
	cfg.Host = strings.TrimRight(host, "/") + "/clusters/" + workspace
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metering discovery client: %w", err)
	}
	return &Discovery{client: client, workspace: workspace, logger: logger}, nil
}

// Run polls until ctx ends. Never fatal: a missing workspace means "not
// installed" until it is, and a workspace that cannot be read right now
// keeps the last answer (see refresh).
func (d *Discovery) Run(ctx context.Context) {
	for {
		d.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

// Refresh re-reads the published objects now (the admin area's manual
// refresh) and returns the result.
func (d *Discovery) Refresh(ctx context.Context) (Endpoints, bool) {
	d.refresh(ctx)
	return d.Current()
}

// refresh reads the published objects once. Only a definite answer moves
// the state: the ConfigMap present, or kcp saying it is not there (NotFound,
// or Forbidden on a workspace the hub cannot see). Any other failure, a
// timeout or a kcp hiccup, is transient and keeps the last answer: a gate
// that reads "installed" (the free-organizations cap, plan assignment) must
// not open because one poll failed while the deployment and every
// allowance in it are still there.
func (d *Discovery) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cm, err := d.client.CoreV1().ConfigMaps(metav1.NamespaceDefault).Get(ctx, endpointsConfigMap, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			d.set(nil)
			return
		}
		d.logger.V(2).Info("metering discovery: endpoints not readable, keeping the last answer", "workspace", d.workspace, "error", err.Error())
		return
	}
	e := &Endpoints{
		PortalURL:  strings.TrimRight(cm.Data["portalURL"], "/"),
		WebhookURL: cm.Data["webhookURL"],
		AuditURL:   cm.Data["auditURL"],
		Version:    cm.Data["version"],
		Workspace:  d.workspace,
	}
	if e.PortalURL == "" {
		d.set(nil)
		return
	}
	if s, err := d.client.CoreV1().Secrets(metav1.NamespaceDefault).Get(ctx, adminTokenSecret, metav1.GetOptions{}); err == nil {
		e.token = strings.TrimSpace(string(s.Data["token"]))
	}
	d.set(e)
}

func (d *Discovery) set(e *Endpoints) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if (d.current == nil) != (e == nil) {
		if e == nil {
			d.logger.Info("metering deployment no longer discovered", "workspace", d.workspace)
		} else {
			d.logger.Info("metering deployment discovered", "workspace", d.workspace, "portal", e.PortalURL, "version", e.Version, "tokenPublished", e.token != "")
		}
	}
	d.current, d.checked = e, true
}

// Current returns the last discovery result; false when metering is not
// installed (or not yet looked for).
func (d *Discovery) Current() (Endpoints, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.current == nil {
		return Endpoints{}, false
	}
	return *d.current, true
}

// Installed says whether a deployment is discovered and whether that is a
// definite answer: known is false until the first poll has reached kcp. A
// gate that must fail closed treats "not known" as installed.
func (d *Discovery) Installed() (installed, known bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.current != nil, d.checked
}

// Handler serves the admin-area view of metering. Mount it under the admin
// subrouter: the gate is the caller's.
type Handler struct {
	discovery *Discovery
	client    *http.Client
	// writer, when set, enables the plan and entitlement writes (admin.go).
	writer *Writer
}

// NewHandler builds a Handler.
func NewHandler(d *Discovery) *Handler {
	return &Handler{discovery: d, client: &http.Client{Timeout: 15 * time.Second}}
}

// Register adds the routes to r (a router already rooted at /api/admin).
func (h *Handler) Register(r *mux.Router) {
	r.HandleFunc("/metering", h.status).Methods(http.MethodGet)
	r.HandleFunc("/metering/{collection}", h.proxy).Methods(http.MethodGet)
	if h.writer != nil {
		h.registerWrites(r)
	}
}

type statusDTO struct {
	Installed bool   `json:"installed"`
	Workspace string `json:"workspace"`
	// Ready is the controller's /readyz, probed now; false with Reason when
	// the deployment is discovered but not answering.
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
	// Collections the proxy serves, so the portal need not hardcode them.
	Collections []string   `json:"collections"`
	Endpoints   *Endpoints `json:"endpoints,omitempty"`
	// Writable is true when the hub can edit plans and entitlements here.
	Writable bool `json:"writable"`
	// DefaultPlan is the plan new organizations are placed on.
	DefaultPlan string `json:"defaultPlan,omitempty"`
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	e, ok := h.discovery.Current()
	if r.URL.Query().Get("refresh") == "true" {
		e, ok = h.discovery.Refresh(r.Context())
	}
	dto := statusDTO{Installed: ok, Workspace: h.discovery.workspace, Collections: strings.Split(collectionsCSV, ","), Writable: h.writer != nil}
	if h.writer != nil {
		dto.DefaultPlan = h.writer.defaultPlan
	}
	if ok {
		dto.Endpoints = &e
		dto.Ready, dto.Reason = h.ready(r.Context(), e)
	}
	writeJSON(w, http.StatusOK, dto)
}

func (h *Handler) ready(ctx context.Context, e Endpoints) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.PortalURL+"/readyz", nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false, "controller unreachable: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("readyz %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return true, ""
}

// proxy forwards GET /api/admin/metering/{collection} to the metering API
// with the published token. Read-only and allow-listed: the metering API is
// the one that grows writes, and they get their own hub routes with their
// own audit when they do.
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request) {
	collection := mux.Vars(r)["collection"]
	if !strings.Contains(","+collectionsCSV+",", ","+collection+",") {
		http.NotFound(w, r)
		return
	}
	e, ok := h.discovery.Current()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "metering is not installed"})
		return
	}
	if e.token == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "metering published no operator token; set the chart's admin.tokenSecretRef"})
		return
	}
	u, err := url.Parse(e.PortalURL + "/api/" + collection)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Accept", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "metering unreachable: " + err.Error()})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
