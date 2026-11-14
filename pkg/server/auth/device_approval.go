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

package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/sharedstore"
)

type deviceApproval struct {
	Request               deviceRequest
	Identity              oidcIdentity
	IDToken, RefreshToken string
	TokenExpiresAt        int64
}

// The decryption key lives only in a short-lived, HttpOnly browser cookie.
// The shared store sees ciphertext and can serve approval on any hub replica.
func deviceApprovalCipher(secret string) (cipher.AEAD, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (h *Handler) showDeviceApproval(w http.ResponseWriter, r *http.Request, state deviceBrowserState, approval deviceApproval) bool {
	approval.Request = state.deviceRequest
	if !time.Now().Before(approval.Request.ExpiresAt) {
		http.Error(w, "Device sign-in expired. Start a new sign-in from the terminal.", http.StatusGone)
		return false
	}
	id, secret := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	key := "approval:" + id
	value, err := json.Marshal(approval)
	if err != nil {
		h.deviceBrowserUnavailable(w, err)
		return false
	}
	box, err := deviceApprovalCipher(secret)
	if err != nil {
		h.deviceBrowserUnavailable(w, err)
		return false
	}
	sealed := box.Seal(nil, nil, value, []byte(key))
	created, err := h.deviceStore.CreateIfAbsent(r.Context(), key, sealed, approval.Request.ExpiresAt)
	if err != nil || !created {
		h.deviceBrowserUnavailable(w, err)
		return false
	}
	http.SetCookie(w, deviceBrowserCookie(key, secret, int(time.Until(approval.Request.ExpiresAt).Seconds())+1))
	account := approval.Identity.Email
	if account == "" {
		account = approval.Identity.Subject
	}
	renderDevicePage(w, http.StatusOK, devicePage{
		Approval: id, Account: account, AccountName: approval.Identity.Name,
		DisplayCode: state.DisplayCode, Network: state.Network,
		StartedAt:  state.StartedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
		CanRefresh: approval.RefreshToken != "",
	})
	return true
}

func (h *Handler) decideDeviceLogin(w http.ResponseWriter, r *http.Request) {
	deviceBrowserHeaders(w)
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid device approval.", http.StatusBadRequest)
		return
	}
	id, decision := r.PostForm.Get("approval"), r.PostForm.Get("decision")
	key := "approval:" + id
	cookie, err := r.Cookie(deviceBrowserCookie(key, "", 0).Name)
	if err != nil || len(id) != 43 || len(cookie.Value) != 43 || (decision != "approve" && decision != "decline") {
		http.Error(w, "Invalid device approval. Start a new sign-in from the terminal.", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	sealed, err := h.deviceStore.Get(ctx, key)
	if errors.Is(err, sharedstore.ErrNotFound) {
		http.Error(w, "Device approval expired or was already used. Check your terminal.", http.StatusGone)
		return
	}
	if err != nil {
		h.deviceBrowserUnavailable(w, err)
		return
	}
	box, err := deviceApprovalCipher(cookie.Value)
	if err != nil {
		h.deviceBrowserUnavailable(w, err)
		return
	}
	value, err := box.Open(nil, nil, sealed, []byte(key))
	if err != nil {
		http.Error(w, "Invalid device approval. Start a new sign-in from the terminal.", http.StatusBadRequest)
		return
	}
	var approval deviceApproval
	if err := json.Unmarshal(value, &approval); err != nil {
		h.deviceBrowserUnavailable(w, err)
		return
	}
	// Verify browser ownership before the atomic take so another browser
	// cannot consume the approval. Only the winning request may issue a result.
	if _, err := h.deviceStore.Take(ctx, key); errors.Is(err, sharedstore.ErrNotFound) {
		http.Error(w, "Device approval expired or was already used. Check your terminal.", http.StatusGone)
		return
	} else if err != nil {
		h.deviceBrowserUnavailable(w, err)
		return
	}
	http.SetCookie(w, deviceBrowserCookie(key, "", -1))
	result := deviceResult{Error: "access_denied"}
	if decision == "approve" {
		response, err := h.approvedDeviceCredentials(r, approval)
		if err != nil {
			h.deviceBrowserUnavailable(w, err)
			h.recordDeviceLoginFailure(ctx, approval.Request)
			return
		}
		result = deviceResult{LoginResponse: response}
	}
	if !h.completeDeviceLogin(w, r, approval.Request, result) {
		h.recordDeviceLoginFailure(ctx, approval.Request)
	}
}

func (h *Handler) approvedDeviceCredentials(r *http.Request, approval deviceApproval) (tenancyv1alpha1.LoginResponse, error) {
	var resp tenancyv1alpha1.LoginResponse
	if time.Now().Unix() >= approval.TokenExpiresAt {
		return resp, fmt.Errorf("verified device identity has expired")
	}
	ctx := r.Context()
	userID, firstLogin, err := h.seedUser(ctx, approval.Identity)
	if err != nil {
		return resp, err
	}
	cluster := h.lookupDefaultCluster(ctx, userID)
	kubeconfig, err := h.generateKubeconfig(userID, cluster, approval.Identity.Email)
	if err != nil {
		return resp, err
	}
	return tenancyv1alpha1.LoginResponse{
		Kubeconfig: kubeconfig, UserID: userID, FirstLogin: firstLogin, Email: approval.Identity.Email,
		IDToken: approval.IDToken, RefreshToken: approval.RefreshToken, ExpiresAt: approval.TokenExpiresAt,
		IssuerURL: h.oidcConfig.IssuerURL, ClientID: h.oidcConfig.ClientID,
	}, nil
}
