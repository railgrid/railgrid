/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tenanttest is an in-memory stand-in for the hub's kcp proxy: an
// http.Handler that speaks enough of the kube REST API for a dynamic client
// to get, list, create, update, merge-patch and delete unstructured objects
// under /clusters/{id}/api/v1/... and /clusters/{id}/apis/{group}/{version}/...
//
// Tests seed it with objects, point a tenant.Client at an httptest.Server
// wrapping it, and inspect what was written. Intercept lets a test fail
// specific requests with a metav1.Status the dynamic client turns back into
// the matching apierrors.StatusError.
package tenanttest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Request is one decoded API request, as handed to Intercept and as recorded
// in Writes.
type Request struct {
	// Verb is one of get, list, create, update, patch, delete.
	Verb        string
	ClusterID   string
	GVR         schema.GroupVersionResource
	Namespace   string
	Name        string
	Subresource string
	// Object is the decoded body of a create, update, or merge patch; nil
	// for reads and deletes.
	Object *unstructured.Unstructured
}

// Server is the in-memory API server. The zero value is not usable; call New.
type Server struct {
	mu      sync.Mutex
	objects map[string]*unstructured.Unstructured
	rv      int64
	writes  []Request

	// Intercept, when set, sees every request before it is served. Returning
	// a non-nil Status short-circuits the request with that Status (its Code
	// becomes the HTTP status), which the dynamic client surfaces as the
	// matching apierrors.StatusError.
	Intercept func(req Request) *metav1.Status
}

// New returns an empty server.
func New() *Server {
	return &Server{objects: map[string]*unstructured.Unstructured{}}
}

// Add seeds objects, deriving the resource from each object's Kind
// (lower-cased plural). Seeded objects get a uid and resourceVersion if they
// have none.
func (s *Server) Add(objs ...*unstructured.Unstructured) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range objs {
		gvk := o.GroupVersionKind()
		gvr := schema.GroupVersionResource{Group: gvk.Group, Version: gvk.Version, Resource: strings.ToLower(gvk.Kind) + "s"}
		s.store(gvr, o)
	}
}

// Get returns a copy of the stored object, or nil.
func (s *Server) Get(gvr schema.GroupVersionResource, namespace, name string) *unstructured.Unstructured {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.objects[key(gvr, namespace, name)]; o != nil {
		return o.DeepCopy()
	}
	return nil
}

// Writes returns every create, update, and patch the server accepted, in
// order, each with the request body as sent.
func (s *Server) Writes() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.writes...)
}

func key(gvr schema.GroupVersionResource, namespace, name string) string {
	return gvr.String() + "|" + namespace + "|" + name
}

// store assigns identity fields and saves o (caller holds the lock).
func (s *Server) store(gvr schema.GroupVersionResource, o *unstructured.Unstructured) *unstructured.Unstructured {
	s.rv++
	o.SetResourceVersion(strconv.FormatInt(s.rv, 10))
	if o.GetUID() == "" {
		o.SetUID(types.UID(fmt.Sprintf("uid-%d", s.rv)))
	}
	s.objects[key(gvr, o.GetNamespace(), o.GetName())] = o.DeepCopy()
	return o
}

// parse decodes the request path into a Request. It returns an error Status
// for paths that are not a resource URL.
func parse(r *http.Request) (Request, *metav1.Status) {
	req := Request{}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "clusters" {
		req.ClusterID = parts[1]
		parts = parts[2:]
	}
	switch {
	case len(parts) >= 2 && parts[0] == "api":
		req.GVR.Version = parts[1]
		parts = parts[2:]
	case len(parts) >= 3 && parts[0] == "apis":
		req.GVR.Group, req.GVR.Version = parts[1], parts[2]
		parts = parts[3:]
	default:
		st := apierrors.NewBadRequest("not a resource path: " + r.URL.Path).ErrStatus
		return req, &st
	}
	if len(parts) >= 2 && parts[0] == "namespaces" {
		req.Namespace = parts[1]
		parts = parts[2:]
	}
	if len(parts) == 0 {
		st := apierrors.NewBadRequest("missing resource in path: " + r.URL.Path).ErrStatus
		return req, &st
	}
	req.GVR.Resource = parts[0]
	if len(parts) > 1 {
		req.Name = parts[1]
	}
	if len(parts) > 2 {
		req.Subresource = parts[2]
	}
	switch r.Method {
	case http.MethodGet:
		req.Verb = "list"
		if req.Name != "" {
			req.Verb = "get"
		}
	case http.MethodPost:
		req.Verb = "create"
	case http.MethodPut:
		req.Verb = "update"
	case http.MethodPatch:
		req.Verb = "patch"
	case http.MethodDelete:
		req.Verb = "delete"
	default:
		st := apierrors.NewMethodNotSupported(req.GVR.GroupResource(), r.Method).ErrStatus
		return req, &st
	}
	return req, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, st := parse(r)
	if st != nil {
		writeStatus(w, st)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if req.Verb == "create" || req.Verb == "update" || (req.Verb == "patch" && isMergePatch(r)) {
		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(body, &obj.Object); err != nil {
			st := apierrors.NewBadRequest("decoding body: " + err.Error()).ErrStatus
			writeStatus(w, &st)
			return
		}
		req.Object = obj
	}

	if s.Intercept != nil {
		if st := s.Intercept(req); st != nil {
			writeStatus(w, st)
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	gr := req.GVR.GroupResource()
	k := key(req.GVR, req.Namespace, req.Name)

	switch req.Verb {
	case "get":
		o := s.objects[k]
		if o == nil {
			st := apierrors.NewNotFound(gr, req.Name).ErrStatus
			writeStatus(w, &st)
			return
		}
		writeJSON(w, http.StatusOK, o.Object)

	case "list":
		sel := labels.Everything()
		if ls := r.URL.Query().Get("labelSelector"); ls != "" {
			parsed, err := labels.Parse(ls)
			if err != nil {
				st := apierrors.NewBadRequest("labelSelector: " + err.Error()).ErrStatus
				writeStatus(w, &st)
				return
			}
			sel = parsed
		}
		items := []any{}
		for _, o := range s.objects {
			ogvk := o.GroupVersionKind()
			if ogvk.Group != req.GVR.Group || ogvk.Version != req.GVR.Version ||
				strings.ToLower(ogvk.Kind)+"s" != req.GVR.Resource {
				continue
			}
			if req.Namespace != "" && o.GetNamespace() != req.Namespace {
				continue
			}
			if !sel.Matches(labels.Set(o.GetLabels())) {
				continue
			}
			items = append(items, o.Object)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"apiVersion": req.GVR.GroupVersion().String(),
			"kind":       "List",
			"metadata":   map[string]any{"resourceVersion": strconv.FormatInt(s.rv, 10)},
			"items":      items,
		})

	case "create":
		obj := req.Object
		if obj.GetName() == "" && obj.GetGenerateName() != "" {
			obj.SetName(fmt.Sprintf("%s%d", obj.GetGenerateName(), s.rv+1))
		}
		if obj.GetNamespace() == "" {
			obj.SetNamespace(req.Namespace)
		}
		if obj.GetResourceVersion() != "" {
			st := apierrors.NewInvalid(obj.GroupVersionKind().GroupKind(), obj.GetName(), nil).ErrStatus
			st.Message = "resourceVersion should not be set on objects to be created"
			writeStatus(w, &st)
			return
		}
		ck := key(req.GVR, obj.GetNamespace(), obj.GetName())
		if s.objects[ck] != nil {
			st := apierrors.NewAlreadyExists(gr, obj.GetName()).ErrStatus
			writeStatus(w, &st)
			return
		}
		s.writes = append(s.writes, req)
		writeJSON(w, http.StatusCreated, s.store(req.GVR, obj).Object)

	case "update":
		cur := s.objects[k]
		if cur == nil {
			st := apierrors.NewNotFound(gr, req.Name).ErrStatus
			writeStatus(w, &st)
			return
		}
		obj := req.Object
		if rv := obj.GetResourceVersion(); rv != "" && rv != cur.GetResourceVersion() {
			st := apierrors.NewConflict(gr, req.Name, fmt.Errorf("the object has been modified; please apply your changes to the latest version and try again")).ErrStatus
			writeStatus(w, &st)
			return
		}
		if req.Subresource == "status" {
			merged := cur.DeepCopy()
			merged.Object["status"] = obj.Object["status"]
			obj = merged
		} else if st, ok := cur.Object["status"]; ok {
			// The main resource never writes status.
			obj.Object["status"] = st
		}
		obj.SetUID(cur.GetUID())
		s.writes = append(s.writes, req)
		writeJSON(w, http.StatusOK, s.store(req.GVR, obj).Object)

	case "patch":
		cur := s.objects[k]
		if cur == nil {
			st := apierrors.NewNotFound(gr, req.Name).ErrStatus
			writeStatus(w, &st)
			return
		}
		if !isMergePatch(r) {
			st := apierrors.NewBadRequest("only application/merge-patch+json is supported by tenanttest").ErrStatus
			writeStatus(w, &st)
			return
		}
		patch := req.Object.Object
		if req.Subresource == "status" {
			patch = map[string]any{"status": patch["status"]}
		} else {
			delete(patch, "status")
		}
		merged := &unstructured.Unstructured{Object: mergePatch(cur.DeepCopy().Object, patch)}
		s.writes = append(s.writes, req)
		writeJSON(w, http.StatusOK, s.store(req.GVR, merged).Object)

	case "delete":
		if s.objects[k] == nil {
			st := apierrors.NewNotFound(gr, req.Name).ErrStatus
			writeStatus(w, &st)
			return
		}
		delete(s.objects, k)
		writeJSON(w, http.StatusOK, metav1.Status{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
			Status:   metav1.StatusSuccess,
			Details:  &metav1.StatusDetails{Name: req.Name, Group: gr.Group, Kind: gr.Resource},
		})
	}
}

// mergePatch applies an RFC 7386 JSON merge patch to target in place: nested
// objects merge recursively, null deletes, anything else replaces.
func mergePatch(target, patch map[string]any) map[string]any {
	for k, v := range patch {
		if v == nil {
			delete(target, k)
			continue
		}
		pm, pok := v.(map[string]any)
		tm, tok := target[k].(map[string]any)
		if pok && tok {
			target[k] = mergePatch(tm, pm)
			continue
		}
		target[k] = v
	}
	return target
}

func isMergePatch(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), string(types.MergePatchType))
}

func writeStatus(w http.ResponseWriter, st *metav1.Status) {
	st.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}
	if st.Status == "" {
		st.Status = metav1.StatusFailure
	}
	code := int(st.Code)
	if code == 0 {
		code = http.StatusInternalServerError
	}
	writeJSON(w, code, st)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
