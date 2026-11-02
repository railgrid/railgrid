// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package restapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// Lock acquisition and all subsequent authority writes share this bounded
// request context. Call only after tenant/role checks and request decoding.
func (h *Handler) lockOrgProviderLifecycle(w http.ResponseWriter, r *http.Request, org string, required bool) (*http.Request, func(), bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	if h.mgr.orgProviderLifecycleLock == nil {
		if required {
			cancel()
			writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "organization provider lifecycle containment is not configured")
			return r, nil, false
		}
		return r.WithContext(ctx), cancel, true
	}
	unlock, err := h.mgr.orgProviderLifecycleLock(ctx, org)
	if err != nil || unlock == nil {
		cancel()
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "organization provider lifecycle is unavailable")
		return r, nil, false
	}
	return r.WithContext(ctx), func() { unlock(); cancel() }, true
}

func (h *Handler) requireOrgProviderUnbound(w http.ResponseWriter, r *http.Request, org, name string) bool {
	if h.mgr.orgProviderCheckDelete == nil {
		writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "organization provider binding audit is not configured")
		return false
	}
	if err := h.mgr.orgProviderCheckDelete(r.Context(), org, name, ""); err != nil {
		if errors.Is(err, providers.ErrOrgProviderInUse) {
			writeStatus(w, http.StatusConflict, "Conflict", "organization provider still has referencing APIBindings; disable them and wait for deletion before reusing or deleting the provider workspace")
		} else {
			writeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "organization provider binding audit unavailable")
		}
		return false
	}
	return true
}
