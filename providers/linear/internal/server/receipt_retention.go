// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"errors"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

const receiptTenantLabel = "linear.internal.faros.sh/tenant"
const receiptIssuedAnnotation = "linear.internal.faros.sh/issued"
const receiptLimit = 2000

var receiptAdmission sync.Mutex

// admitReceipt runs under the single-replica admission lock. Only confirmed
// outcomes whose request keys have expired can be removed. Uncertain writes
// remain evidence, even when retaining them causes new writes to be rejected.
func admitReceipt(ctx context.Context, records dynamic.ResourceInterface, tenant string, now time.Time) error {
	count := 0
	cursor := ""
	for {
		page, err := records.List(ctx, metav1.ListOptions{LabelSelector: receiptTenantLabel + "=" + tenant, Limit: 100, Continue: cursor})
		if err != nil {
			return err
		}
		for i := range page.Items {
			u := &page.Items[i]
			phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
			completed, _, _ := unstructured.NestedString(u.Object, "status", "completedAt")
			done, doneErr := time.Parse(time.RFC3339, completed)
			issued, issuedErr := time.Parse(time.RFC3339, u.GetAnnotations()[receiptIssuedAnnotation])
			cutoff := now.Add(-30 * 24 * time.Hour)
			if (phase == "Succeeded" || phase == "Failed") && doneErr == nil && issuedErr == nil && done.Before(cutoff) && issued.Before(cutoff) {
				finalizers := []string{}
				for _, f := range u.GetFinalizers() {
					if f != "linear.internal.faros.sh/receipt-history" {
						finalizers = append(finalizers, f)
					}
				}
				u.SetFinalizers(finalizers)
				updated, err := records.Update(ctx, u, metav1.UpdateOptions{})
				if err != nil {
					return err
				}
				uid, version := updated.GetUID(), updated.GetResourceVersion()
				if err = records.Delete(ctx, u.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}); err != nil {
					return err
				}
			} else {
				count++
			}
		}
		next := page.GetContinue()
		if next == "" {
			break
		}
		if next == cursor || count >= receiptLimit {
			return errors.New("write receipt capacity reached; resolve retained uncertain writes")
		}
		cursor = next
	}
	if count >= receiptLimit {
		return errors.New("write receipt capacity reached; resolve retained uncertain writes")
	}
	return nil
}
