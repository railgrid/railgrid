// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/authority"
	"github.com/faroshq/provider-linear/internal/engine"
	"github.com/faroshq/provider-sdk/tenantaccess"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Server struct {
	Authority authority.Authority
	HubURL    string
	Insecure  bool
}
type Submit struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Spec      api.OperationSpec `json:"spec"`
}

func (s Server) Caller(r *http.Request) (dynamic.Interface, error) {
	cluster := r.Header.Get("X-Faros-Cluster")
	auth := r.Header.Get("Authorization")
	if !namePattern.MatchString(cluster) || !strings.HasPrefix(auth, "Bearer ") || len(auth) < 8 {
		return nil, errors.New("tenant bearer identity required")
	}
	return tenantaccess.NewDynamicClient(s.HubURL, cluster, strings.TrimPrefix(auth, "Bearer "), s.Insecure)
}
func (s Server) Submit(ctx context.Context, r *http.Request, input Submit) (any, error) {
	if !namePattern.MatchString(input.Namespace) || !namePattern.MatchString(input.Name) {
		return nil, errors.New("namespace and stable operation name required")
	}
	client, err := s.Caller(r)
	if err != nil {
		return nil, err
	}
	op := api.Operation{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupName + "/" + api.Version, Kind: "Operation"}, ObjectMeta: metav1.ObjectMeta{Name: input.Name, Namespace: input.Namespace}, Spec: input.Spec}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&op)
	if err != nil {
		return nil, err
	}
	return client.Resource(engine.Operations).Namespace(input.Namespace).Create(ctx, &unstructured.Unstructured{Object: obj}, metav1.CreateOptions{})
}
func (s Server) Routes(mux *http.ServeMux) {
	var webhookMu sync.Mutex
	mux.HandleFunc("POST /api/operations", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 32768)
		var input Submit
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid operation", 400)
			return
		}
		out, err := s.Submit(r.Context(), r, input)
		if err != nil {
			http.Error(w, "operation rejected; check namespace permissions and unique operation name", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /webhooks/{cluster}/{namespace}/{connection}", func(w http.ResponseWriter, r *http.Request) {
		cluster, ns, conn := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("connection")
		if !namePattern.MatchString(cluster) || !namePattern.MatchString(ns) || !namePattern.MatchString(conn) {
			http.Error(w, "invalid route", 400)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
		if err != nil {
			http.Error(w, "invalid payload", http.StatusRequestEntityTooLarge)
			return
		}
		client, err := s.Authority.Tenant(r.Context(), cluster, ns, conn)
		if err != nil {
			http.Error(w, "subscription unavailable", http.StatusServiceUnavailable)
			return
		}
		e := engine.Engine{Client: client}
		webhookMu.Lock()
		defer webhookMu.Unlock()
		if err = e.Webhook(r.Context(), ns, conn, r.Header.Get("Linear-Signature"), r.Header.Get("Linear-Delivery"), raw); err != nil {
			http.Error(w, "delivery not accepted", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(204)
	})
}
