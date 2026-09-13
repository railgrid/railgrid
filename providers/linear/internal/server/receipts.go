// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/actionapi"
	"github.com/faroshq/provider-linear/internal/engine"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var activeWrites sync.Map

func (s Server) write(ctx context.Context, r *http.Request, caller dynamic.Interface, team api.Team, conn api.Connection, e engine.Engine, action string, input actionapi.Input, req ActionRequest, inspect bool) (actionapi.Outcome, error) {
	var out actionapi.Outcome
	if len(req.RequestID) < 1 || len(req.RequestID) > 160 || strings.ContainsAny(req.RequestID, "/\\ \t\n\r") {
		return out, errors.New("stable requestId or Idempotency-Key required")
	}
	parts := strings.SplitN(req.RequestID, ".", 2)
	issued, parseErr := time.Parse("20060102T150405Z", parts[0])
	// Opaque keys never expire: forgetting one would permit an old write to replay.
	// They remain subject to the same bounded tenant receipt quota.
	issuedAnnotation := ""
	if parseErr == nil && len(parts) == 2 {
		issuedAnnotation = issued.Format(time.RFC3339)
		if (!inspect && time.Since(issued) > 30*24*time.Hour) || time.Until(issued) > 5*time.Minute {
			return out, errors.New("requestId expired or invalid; inspect the original outcome before starting a new intent")
		}
	}
	identity, err := caller.Resource(schema.GroupVersionResource{Group: "authentication.k8s.io", Version: "v1", Resource: "selfsubjectreviews"}).Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "SelfSubjectReview"}}, metav1.CreateOptions{})
	if err != nil {
		return out, errors.New("caller identity unavailable")
	}
	username, _, _ := unstructured.NestedString(identity.Object, "status", "userInfo", "username")
	uid, _, _ := unstructured.NestedString(identity.Object, "status", "userInfo", "uid")
	if username == "" {
		return out, errors.New("caller identity unavailable")
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{r.Header.Get("X-Faros-Cluster"), username, uid, action, "v1", req.RequestID}, "\x00")))
	name := "receipt-" + hex.EncodeToString(digest[:])
	store := s.PrivateReceipts
	if store == nil {
		store, err = dynamic.NewForConfig(s.Authority.Config)
		if err != nil {
			return out, err
		}
	}
	e.ReceiptClient = store
	receipts := store.Resource(engine.Receipts)
	existing, err := receipts.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && !inspect {
		partition := sha256.Sum256([]byte(r.Header.Get("X-Faros-Cluster")))
		tenantLabel := hex.EncodeToString(partition[:16])
		canonicalInput, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			return out, marshalErr
		}
		inputHash := sha256.Sum256(canonicalInput)
		receiptAdmission.Lock()
		if capacityErr := admitReceipt(ctx, receipts, tenantLabel, time.Now()); capacityErr != nil {
			receiptAdmission.Unlock()
			return out, capacityErr
		}
		record := actionapi.Receipt{TypeMeta: metav1.TypeMeta{APIVersion: "linear.internal.faros.sh/v1alpha1", Kind: "ActionReceipt"}, ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{receiptTenantLabel: tenantLabel}, Annotations: map[string]string{"linear.internal.faros.sh/input-digest": hex.EncodeToString(inputHash[:]), receiptIssuedAnnotation: issuedAnnotation, "linear.internal.faros.sh/owner": s.InstanceID, "linear.internal.faros.sh/team-uid": string(team.UID), "linear.internal.faros.sh/connection-uid": string(conn.UID)}}, Spec: input, Status: actionapi.Outcome{ConnectionUID: string(conn.UID)}}
		data, convertErr := runtime.DefaultUnstructuredConverter.ToUnstructured(&record)
		if convertErr != nil {
			receiptAdmission.Unlock()
			return out, convertErr
		}
		existing, err = receipts.Create(ctx, &unstructured.Unstructured{Object: data}, metav1.CreateOptions{})
		receiptAdmission.Unlock()
		if err == nil {
			// Only the create winner dispatches. A concurrent duplicate only observes.
			activeWrites.Store(name, true)
			defer activeWrites.Delete(name)
			if err = e.Reconcile(ctx, existing); err != nil {
				return out, fmt.Errorf("outcome recording interrupted; inspect requestId: %w", err)
			}
		} else if apierrors.IsAlreadyExists(err) {
			existing, err = receipts.Get(ctx, name, metav1.GetOptions{})
		}
	}
	if err != nil {
		return out, errors.New("write outcome unavailable; retain requestId")
	}
	if existing.GetAnnotations()["linear.internal.faros.sh/team-uid"] != string(team.UID) || existing.GetAnnotations()["linear.internal.faros.sh/connection-uid"] != string(conn.UID) {
		return out, fmt.Errorf("%w: requestId belongs to a replaced Team or Connection; inspect Linear", errActionConflict)
	}
	var receipt actionapi.Receipt
	if err = engine.Decode(existing, &receipt); err != nil {
		return out, err
	}
	canonicalInput, marshalErr := json.Marshal(input)
	if marshalErr != nil {
		return out, marshalErr
	}
	inputHash := sha256.Sum256(canonicalInput)
	if !inspect && existing.GetAnnotations()["linear.internal.faros.sh/input-digest"] != hex.EncodeToString(inputHash[:]) {
		return out, fmt.Errorf("%w: requestId already used with different input", errActionConflict)
	}
	_, active := activeWrites.Load(name)
	if receipt.Status.Phase == "Running" && (!active || existing.GetAnnotations()["linear.internal.faros.sh/owner"] != s.InstanceID) {
		if err = e.Reconcile(ctx, existing); err != nil {
			return out, err
		}
		if err = engine.Decode(existing, &receipt); err != nil {
			return out, err
		}
	}
	out = receipt.Status
	if out.Phase == "" {
		out.Phase = "Uncertain"
		out.Message = "Dispatch was interrupted. Inspect Linear before starting a new intent."
	}
	return out, nil
}
