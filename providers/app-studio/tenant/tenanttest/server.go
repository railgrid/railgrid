/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tenanttest serves an in-memory stand-in for the hub's kcp proxy so
// tests can point tenant.NewClient at a real HTTP endpoint. It speaks enough
// of the Kubernetes REST API for unstructured objects — GET, LIST (label and
// name/namespace field selectors), POST, PUT, merge PATCH (main and status
// subresources) and DELETE with preconditions — under
// /clusters/{id}/apis/{group}/{version}[/namespaces/{ns}]/{resource}[/{name}[/status]]
// and /clusters/{id}/api/{version}/..., and answers failures with metav1.Status
// bodies whose reason/code map back onto apierrors on the client side.
package tenanttest

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/yaml"

	"github.com/faroshq/provider-app-studio/tenant"
)

// Request is one HTTP request the server handled, for assertions on what the
// client sent (selectors, delete preconditions, bearer identity).
type Request struct {
	Method    string
	Path      string
	Query     url.Values
	Body      []byte
	Headers   http.Header
	Cluster   string
	Bearer    string
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
	// Subresource is "status" for status requests, else "".
	Subresource string
}

// Server is an httptest.Server fronting an in-memory object store keyed by
// GroupVersionResource. Resource types become "served" when they are seeded
// or registered; requests for anything else get the same 404 kcp returns for
// an API that is not bound in the workspace.
type Server struct {
	// URL is the hub base URL to hand to tenant.NewClient.
	URL string

	t      testing.TB
	srv    *httptest.Server
	mu     sync.Mutex
	store  map[schema.GroupVersionResource]map[string]*unstructured.Unstructured
	reqs   []Request
	nextRV int64
}

// NewServer starts a fake proxy and closes it at test cleanup. The test is
// skipped when no loopback listener is available.
func NewServer(t testing.TB) *Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	s := &Server{
		t:      t,
		store:  map[schema.GroupVersionResource]map[string]*unstructured.Unstructured{},
		nextRV: 1,
	}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serveHTTP))
	s.srv.Listener = listener
	s.srv.Start()
	s.URL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// Client returns a tenant.Client targeting this server.
func (s *Server) Client() *tenant.Client {
	return tenant.NewClient(s.URL, false)
}

// Register marks resource types as served without seeding objects, so LIST
// returns an empty list and GET a per-object NotFound instead of the
// "API not served" 404.
func (s *Server) Register(gvrs ...schema.GroupVersionResource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, gvr := range gvrs {
		if s.store[gvr] == nil {
			s.store[gvr] = map[string]*unstructured.Unstructured{}
		}
	}
}

// Add seeds (or replaces) objects under gvr exactly as given, assigning a
// resourceVersion and uid when the object has none.
func (s *Server) Add(gvr schema.GroupVersionResource, objs ...*unstructured.Unstructured) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store[gvr] == nil {
		s.store[gvr] = map[string]*unstructured.Unstructured{}
	}
	for _, obj := range objs {
		if obj.GetName() == "" {
			s.t.Fatalf("tenanttest: seeded %s object has no name", gvr.Resource)
		}
		stored := obj.DeepCopy()
		if stored.GetUID() == "" {
			stored.SetUID(uuid.NewUUID())
		}
		if stored.GetResourceVersion() == "" {
			stored.SetResourceVersion(s.bumpRVLocked())
		}
		s.store[gvr][key(stored.GetNamespace(), stored.GetName())] = stored
	}
}

// Set replaces the stored object regardless of resourceVersion (a test-side
// out-of-band write), keeping the object's own resourceVersion if it has one.
func (s *Server) Set(gvr schema.GroupVersionResource, obj *unstructured.Unstructured) {
	s.Add(gvr, obj)
}

// Remove deletes an object from the store without going through HTTP.
func (s *Server) Remove(gvr schema.GroupVersionResource, namespace, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if objs := s.store[gvr]; objs != nil {
		delete(objs, key(namespace, name))
	}
}

// Get returns a copy of the stored object, or nil when absent.
func (s *Server) Get(gvr schema.GroupVersionResource, namespace, name string) *unstructured.Unstructured {
	s.mu.Lock()
	defer s.mu.Unlock()
	obj := s.store[gvr][key(namespace, name)]
	if obj == nil {
		return nil
	}
	return obj.DeepCopy()
}

// List returns copies of every stored object under gvr.
func (s *Server) List(gvr schema.GroupVersionResource) []unstructured.Unstructured {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]unstructured.Unstructured, 0, len(s.store[gvr]))
	for _, obj := range s.store[gvr] {
		out = append(out, *obj.DeepCopy())
	}
	return out
}

// Requests returns every request handled so far, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.reqs))
	copy(out, s.reqs)
	return out
}

// RequestsFor filters Requests by method and resource.
func (s *Server) RequestsFor(method string, gvr schema.GroupVersionResource) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Method == method && r.GVR == gvr {
			out = append(out, r)
		}
	}
	return out
}

// ObjectFromYAML decodes a YAML or JSON document into an unstructured object.
func ObjectFromYAML(t testing.TB, doc string) *unstructured.Unstructured {
	t.Helper()
	obj := map[string]any{}
	if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
		t.Fatalf("tenanttest: decode object: %v", err)
	}
	return &unstructured.Unstructured{Object: obj}
}

func key(namespace, name string) string { return namespace + "/" + name }

func (s *Server) bumpRVLocked() string {
	rv := strconv.FormatInt(s.nextRV, 10)
	s.nextRV++
	return rv
}

type route struct {
	cluster     string
	gvr         schema.GroupVersionResource
	namespace   string
	name        string
	subresource string
}

func parseRoute(path string) (route, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "clusters" {
		return route{}, false
	}
	r := route{cluster: parts[1]}
	rest := parts[2:]
	switch rest[0] {
	case "api":
		if len(rest) < 3 {
			return route{}, false
		}
		r.gvr.Version = rest[1]
		rest = rest[2:]
	case "apis":
		if len(rest) < 4 {
			return route{}, false
		}
		r.gvr.Group, r.gvr.Version = rest[1], rest[2]
		rest = rest[3:]
	default:
		return route{}, false
	}
	if rest[0] == "namespaces" {
		if len(rest) < 3 {
			return route{}, false
		}
		r.namespace = rest[1]
		rest = rest[2:]
	}
	r.gvr.Resource = rest[0]
	rest = rest[1:]
	if len(rest) > 0 {
		r.name = rest[0]
		rest = rest[1:]
	}
	if len(rest) > 0 {
		if rest[0] != "status" || len(rest) > 1 {
			return route{}, false
		}
		r.subresource = "status"
	}
	return r, true
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeStatus(w, apierrors.NewBadRequest("read body: "+err.Error()))
		return
	}
	rt, ok := parseRoute(r.URL.Path)
	if !ok {
		writeStatus(w, &apierrors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure, Code: http.StatusNotFound, Reason: metav1.StatusReasonNotFound,
			Message: "the server could not find the requested resource",
		}})
		return
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearer == "" || bearer == r.Header.Get("Authorization") {
		writeStatus(w, apierrors.NewUnauthorized("missing bearer token"))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, Request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: body, Headers: r.Header.Clone(),
		Cluster: rt.cluster, Bearer: bearer, GVR: rt.gvr, Namespace: rt.namespace, Name: rt.name, Subresource: rt.subresource,
	})

	objs := s.store[rt.gvr]
	if objs == nil {
		writeStatus(w, &apierrors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure, Code: http.StatusNotFound, Reason: metav1.StatusReasonNotFound,
			Message: "the server could not find the requested resource",
		}})
		return
	}
	gr := rt.gvr.GroupResource()

	switch {
	case r.Method == http.MethodGet && rt.name == "":
		s.list(w, r, rt, objs)
	case r.Method == http.MethodGet:
		obj := objs[key(rt.namespace, rt.name)]
		if obj == nil {
			writeStatus(w, apierrors.NewNotFound(gr, rt.name))
			return
		}
		writeObject(w, http.StatusOK, obj)
	case r.Method == http.MethodPost:
		s.create(w, rt, objs, body)
	case r.Method == http.MethodPut:
		s.update(w, rt, objs, body)
	case r.Method == http.MethodPatch:
		s.patch(w, r, rt, objs, body)
	case r.Method == http.MethodDelete:
		s.delete(w, rt, objs, body)
	default:
		writeStatus(w, apierrors.NewMethodNotSupported(gr, r.Method))
	}
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, rt route, objs map[string]*unstructured.Unstructured) {
	selector := labels.Everything()
	if raw := r.URL.Query().Get("labelSelector"); raw != "" {
		parsed, err := labels.Parse(raw)
		if err != nil {
			writeStatus(w, apierrors.NewBadRequest("labelSelector: "+err.Error()))
			return
		}
		selector = parsed
	}
	fieldSelector := fields.Everything()
	if raw := r.URL.Query().Get("fieldSelector"); raw != "" {
		parsed, err := fields.ParseSelector(raw)
		if err != nil {
			writeStatus(w, apierrors.NewBadRequest("fieldSelector: "+err.Error()))
			return
		}
		for _, req := range parsed.Requirements() {
			if req.Field != "metadata.name" && req.Field != "metadata.namespace" {
				writeStatus(w, apierrors.NewBadRequest("fieldSelector: unsupported field "+req.Field))
				return
			}
		}
		fieldSelector = parsed
	}
	items := make([]any, 0, len(objs))
	for _, obj := range objs {
		if rt.namespace != "" && obj.GetNamespace() != rt.namespace {
			continue
		}
		if !selector.Matches(labels.Set(obj.GetLabels())) {
			continue
		}
		if !fieldSelector.Matches(fields.Set{"metadata.name": obj.GetName(), "metadata.namespace": obj.GetNamespace()}) {
			continue
		}
		items = append(items, obj.DeepCopy().Object)
	}
	list := map[string]any{
		"apiVersion": rt.gvr.GroupVersion().String(),
		"kind":       "List",
		"metadata":   map[string]any{"resourceVersion": strconv.FormatInt(s.nextRV-1, 10)},
		"items":      items,
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) create(w http.ResponseWriter, rt route, objs map[string]*unstructured.Unstructured, body []byte) {
	gr := rt.gvr.GroupResource()
	obj, err := decodeObject(body)
	if err != nil {
		writeStatus(w, apierrors.NewBadRequest(err.Error()))
		return
	}
	if rt.namespace != "" {
		if ns := obj.GetNamespace(); ns != "" && ns != rt.namespace {
			writeStatus(w, apierrors.NewBadRequest(fmt.Sprintf("the namespace of the object (%s) does not match the namespace on the request (%s)", ns, rt.namespace)))
			return
		}
		obj.SetNamespace(rt.namespace)
	}
	if obj.GetName() == "" && obj.GetGenerateName() != "" {
		obj.SetName(obj.GetGenerateName() + strings.ToLower(string(uuid.NewUUID())[:5]))
	}
	if obj.GetName() == "" {
		writeStatus(w, apierrors.NewInvalid(schema.GroupKind{Group: rt.gvr.Group, Kind: obj.GetKind()}, "", nil))
		return
	}
	if obj.GetResourceVersion() != "" {
		writeStatus(w, apierrors.NewBadRequest("resourceVersion should not be set on objects to be created"))
		return
	}
	k := key(obj.GetNamespace(), obj.GetName())
	if objs[k] != nil {
		writeStatus(w, apierrors.NewAlreadyExists(gr, obj.GetName()))
		return
	}
	if obj.GetUID() == "" {
		obj.SetUID(uuid.NewUUID())
	}
	obj.SetResourceVersion(s.bumpRVLocked())
	if ts := obj.GetCreationTimestamp(); ts.IsZero() {
		obj.SetCreationTimestamp(metav1.Now())
	}
	objs[k] = obj
	writeObject(w, http.StatusCreated, obj)
}

func (s *Server) update(w http.ResponseWriter, rt route, objs map[string]*unstructured.Unstructured, body []byte) {
	gr := rt.gvr.GroupResource()
	current := objs[key(rt.namespace, rt.name)]
	if current == nil {
		writeStatus(w, apierrors.NewNotFound(gr, rt.name))
		return
	}
	desired, err := decodeObject(body)
	if err != nil {
		writeStatus(w, apierrors.NewBadRequest(err.Error()))
		return
	}
	if desired.GetName() != rt.name {
		writeStatus(w, apierrors.NewBadRequest(fmt.Sprintf("the name of the object (%s) does not match the name on the URL (%s)", desired.GetName(), rt.name)))
		return
	}
	if rv := desired.GetResourceVersion(); rv != "" && rv != current.GetResourceVersion() {
		writeStatus(w, apierrors.NewConflict(gr, rt.name, fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again")))
		return
	}
	if uid := desired.GetUID(); uid != "" && uid != current.GetUID() {
		writeStatus(w, apierrors.NewConflict(gr, rt.name, fmt.Errorf("Precondition failed: UID in precondition: %s, UID in object meta: %s", uid, current.GetUID())))
		return
	}
	stored := s.mergeWriteLocked(current, desired, rt.subresource == "status")
	objs[key(rt.namespace, rt.name)] = stored
	writeObject(w, http.StatusOK, stored)
}

func (s *Server) patch(w http.ResponseWriter, r *http.Request, rt route, objs map[string]*unstructured.Unstructured, body []byte) {
	gr := rt.gvr.GroupResource()
	current := objs[key(rt.namespace, rt.name)]
	if current == nil {
		writeStatus(w, apierrors.NewNotFound(gr, rt.name))
		return
	}
	switch types.PatchType(r.Header.Get("Content-Type")) {
	case types.MergePatchType, types.StrategicMergePatchType:
	default:
		writeStatus(w, apierrors.NewGenericServerResponse(http.StatusUnsupportedMediaType, "PATCH", gr, rt.name, "unsupported patch type "+r.Header.Get("Content-Type"), 0, false))
		return
	}
	patch := map[string]any{}
	if err := json.Unmarshal(body, &patch); err != nil {
		writeStatus(w, apierrors.NewBadRequest("decode merge patch: "+err.Error()))
		return
	}
	desired := &unstructured.Unstructured{Object: mergePatch(current.DeepCopy().Object, patch)}
	if rv := desired.GetResourceVersion(); rv != "" && rv != current.GetResourceVersion() {
		writeStatus(w, apierrors.NewConflict(gr, rt.name, fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again")))
		return
	}
	stored := s.mergeWriteLocked(current, desired, rt.subresource == "status")
	objs[key(rt.namespace, rt.name)] = stored
	writeObject(w, http.StatusOK, stored)
}

// mergeWriteLocked applies kube's subresource split: a main-resource write
// keeps the stored status, a status write keeps everything but status.
func (s *Server) mergeWriteLocked(current, desired *unstructured.Unstructured, status bool) *unstructured.Unstructured {
	var stored *unstructured.Unstructured
	if status {
		stored = current.DeepCopy()
		if st, ok := desired.Object["status"]; ok {
			stored.Object["status"] = st
		} else {
			delete(stored.Object, "status")
		}
	} else {
		stored = desired.DeepCopy()
		if st, ok := current.Object["status"]; ok {
			stored.Object["status"] = st
		} else {
			delete(stored.Object, "status")
		}
	}
	stored.SetUID(current.GetUID())
	stored.SetCreationTimestamp(current.GetCreationTimestamp())
	stored.SetResourceVersion(s.bumpRVLocked())
	return stored
}

func (s *Server) delete(w http.ResponseWriter, rt route, objs map[string]*unstructured.Unstructured, body []byte) {
	gr := rt.gvr.GroupResource()
	current := objs[key(rt.namespace, rt.name)]
	if current == nil {
		writeStatus(w, apierrors.NewNotFound(gr, rt.name))
		return
	}
	var opts metav1.DeleteOptions
	if len(body) > 0 {
		if err := json.Unmarshal(body, &opts); err != nil {
			writeStatus(w, apierrors.NewBadRequest("decode delete options: "+err.Error()))
			return
		}
	}
	if pre := opts.Preconditions; pre != nil {
		if pre.UID != nil && *pre.UID != current.GetUID() {
			writeStatus(w, apierrors.NewConflict(gr, rt.name, fmt.Errorf("Precondition failed: UID in precondition: %s, UID in object meta: %s", *pre.UID, current.GetUID())))
			return
		}
		if pre.ResourceVersion != nil && *pre.ResourceVersion != current.GetResourceVersion() {
			writeStatus(w, apierrors.NewConflict(gr, rt.name, fmt.Errorf("Precondition failed: ResourceVersion in precondition: %s, ResourceVersion in object meta: %s", *pre.ResourceVersion, current.GetResourceVersion())))
			return
		}
	}
	delete(objs, key(rt.namespace, rt.name))
	writeJSON(w, http.StatusOK, metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusSuccess,
		Details:  &metav1.StatusDetails{Name: rt.name, Group: gr.Group, Kind: gr.Resource, UID: current.GetUID()},
	})
}

// mergePatch applies RFC 7386 JSON merge patch semantics to target in place.
func mergePatch(target map[string]any, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, v := range patch {
		if v == nil {
			delete(target, k)
			continue
		}
		pv, pok := v.(map[string]any)
		tv, tok := target[k].(map[string]any)
		if pok && tok {
			target[k] = mergePatch(tv, pv)
			continue
		}
		if pok {
			target[k] = mergePatch(map[string]any{}, pv)
			continue
		}
		target[k] = v
	}
	return target
}

func decodeObject(body []byte) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(body); err != nil {
		return nil, fmt.Errorf("decode object: %w", err)
	}
	return obj, nil
}

func writeObject(w http.ResponseWriter, code int, obj *unstructured.Unstructured) {
	writeJSON(w, code, obj.Object)
}

func writeStatus(w http.ResponseWriter, err *apierrors.StatusError) {
	status := err.ErrStatus
	status.TypeMeta = metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}
	if status.Status == "" {
		status.Status = metav1.StatusFailure
	}
	code := int(status.Code)
	if code == 0 {
		code = http.StatusInternalServerError
	}
	writeJSON(w, code, status)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
