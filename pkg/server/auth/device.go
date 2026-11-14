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

// Package auth provides server-side OIDC token verification.
package auth

import (
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/railgrid/provider-sdk/statuspage"
	"golang.org/x/oauth2"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/sharedstore"
	"github.com/railgrid/railgrid/pkg/server/proxy"
)

const (
	deviceLoginTTL          = 10 * time.Minute
	devicePollInterval      = 5
	deviceStatePrefix       = "device."
	deviceRequestsPerMinute = 120
	deviceStartsPerMinute   = 6
)

var deviceUserCodes = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)

type deviceStore interface {
	CreateIfAbsent(context.Context, string, []byte, time.Time) (bool, error)
	Get(context.Context, string) ([]byte, error)
	Take(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
	AllowInterval(context.Context, string, time.Duration) (bool, error)
}

type deviceRequest struct {
	PollKey      string
	StartedAt    time.Time
	Network      string
	ExpiresAt    time.Time
	RecipientKey []byte
}

type deviceBrowserState struct {
	deviceRequest
	Verifier    string
	BrowserHash string
}

type deviceResult struct {
	tenancyv1alpha1.LoginResponse
	Error    string `json:"error,omitempty"`
	AckNonce string `json:"ack_nonce,omitempty"`
}

type storedDeviceResult struct {
	Ciphertext []byte
	AckDigest  string
}

// SetDeviceLoginStore wires the shared, expiring store used by OIDC device login.
func (h *Handler) SetDeviceLoginStore(store *sharedstore.Store) {
	h.deviceStore = store
}

func (h *Handler) registerDeviceLoginLimits() {
	h.deviceLimiter = newRateLimiter(deviceRequestsPerMinute, time.Minute, klog.Background().WithName("device-auth-rate-limit"))
	h.deviceLimiter.trustedProxies = h.rateLimiter.trustedProxies
	h.deviceStartLimiter = newRateLimiter(deviceStartsPerMinute, time.Minute, klog.Background().WithName("device-start-rate-limit"))
	h.deviceStartLimiter.trustedProxies = h.rateLimiter.trustedProxies
}

func deviceRequesterNetwork(r *http.Request, trusted []netip.Prefix) string {
	if len(trusted) == 0 {
		return "Unavailable"
	}
	clientIP := proxy.ClientIP(r, trusted)
	addr, err := netip.ParseAddr(clientIP)
	if err != nil {
		return "Unavailable"
	}
	addr = addr.Unmap()
	// ClientIP falls back to the peer if no forwarded client can be resolved.
	if slices.ContainsFunc(trusted, func(prefix netip.Prefix) bool { return prefix.Contains(addr) }) {
		return "Unavailable"
	}
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(addr, bits).Masked().String()
}

func deviceDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (h *Handler) startDeviceLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !strings.HasPrefix(h.hubExternalURL, "https://") {
		http.Error(w, "Device sign-in requires an HTTPS external hub URL.", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid device sign-in request.", http.StatusBadRequest)
		return
	}
	key, err := base64.RawURLEncoding.DecodeString(r.PostForm.Get("recipient_key"))
	if err != nil || len(key) != 32 {
		http.Error(w, "Invalid device recipient key.", http.StatusBadRequest)
		return
	}
	deviceCode := oauth2.GenerateVerifier()
	now := time.Now()
	request := deviceRequest{
		PollKey: deviceDigest(deviceCode), StartedAt: now, ExpiresAt: now.Add(deviceLoginTTL), RecipientKey: key,
		Network: deviceRequesterNetwork(r, h.deviceStartLimiter.trustedProxies),
	}
	payload, err := json.Marshal(request)
	if err != nil {
		h.deviceUnavailable(w, err)
		return
	}
	pendingKey := "pending:" + request.PollKey
	ok, err := h.deviceStore.CreateIfAbsent(ctx, pendingKey, nil, request.ExpiresAt)
	if err != nil || !ok {
		h.deviceUnavailable(w, err)
		return
	}

	issued := false
	defer func() {
		if issued {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := h.deviceStore.Delete(cleanupCtx, pendingKey); err != nil {
			h.logger.Error(err, "failed to remove incomplete CLI sign-in")
		}
	}()
	for range 3 {
		var randomCode [5]byte
		rand.Read(randomCode[:])
		userCode := deviceUserCodes.EncodeToString(randomCode[:])
		created, err := h.deviceStore.CreateIfAbsent(ctx, "user:"+userCode, payload, request.ExpiresAt)
		if err != nil {
			h.deviceUnavailable(w, err)
			return
		}
		if created {
			issued = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(oauth2.DeviceAuthResponse{
				DeviceCode:      deviceCode,
				UserCode:        userCode[:4] + "-" + userCode[4:],
				VerificationURI: strings.TrimRight(h.hubExternalURL, "/") + apiurl.PathAuthDeviceVerify,
				Expiry:          request.ExpiresAt,
				Interval:        devicePollInterval,
			})
			return
		}
	}
	h.deviceUnavailable(w, nil)
}

var deviceCodePage = template.Must(template.New("device-code").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Sign in to Railgrid CLI</title>
</head>
<body>
  <main>
    <h1>Sign in to Railgrid CLI</h1>
    <p>Enter the code shown in the terminal where you ran <code>railgrid login --device</code>.</p>
    {{if .}}<p role="alert">{{.}}</p>{{end}}
    <form method="post">
      <label for="user_code">Device code</label>
      <input id="user_code" name="user_code" type="text" autocapitalize="characters" autocorrect="off" spellcheck="false" autocomplete="off" maxlength="9" required>
      <button type="submit">Continue</button>
    </form>
  </main>
</body>
</html>`))

var deviceConfirmPage = template.Must(template.New("device-confirm").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Confirm Railgrid CLI sign-in</title>
</head>
<body>
  <main>
    <h1>Confirm Railgrid CLI sign-in</h1>
    <dl>
      <dt>Request network</dt><dd>{{.Network}}</dd>
      <dt>Requested at</dt><dd>{{.StartedAt}}</dd>
      <dt>Device code</dt><dd>{{.DisplayCode}}</dd>
    </dl>
    <p>Approving gives the CLI that requested this code access to Railgrid as you.</p>
    <p>Continue only if you ran <code>railgrid login --device</code> yourself and the code matches your terminal. If someone sent you the code, cancel.</p>
    <form method="post">
      <input type="hidden" name="user_code" value="{{.Code}}">
      <input type="hidden" name="confirmation" value="{{.Confirmation}}">
      <button type="submit" name="confirm" value="1">Approve CLI sign-in</button>
      <a href="/auth/device">Cancel</a>
    </form>
  </main>
</body>
</html>`))

func deviceBrowserHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}

func (h *Handler) showDeviceCodePage(w http.ResponseWriter, _ *http.Request) {
	deviceBrowserHeaders(w)
	_ = deviceCodePage.Execute(w, "")
}

func (h *Handler) approveDeviceLogin(w http.ResponseWriter, r *http.Request) {
	deviceBrowserHeaders(w)
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid device sign-in request.", http.StatusBadRequest)
		return
	}
	code := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(r.PostForm.Get("user_code"))), "-", "")
	if len(code) == 8 {
		confirm := r.PostForm.Get("confirm") == "1"
		load := h.deviceStore.Get
		if confirm {
			cookie, err := r.Cookie(deviceBrowserCookie("confirm:"+code, "", 0).Name)
			confirmation := r.PostForm.Get("confirmation")
			if err != nil || len(confirmation) != 43 || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(confirmation)) != 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = deviceCodePage.Execute(w, "Enter the code again to review the sign-in request.")
				return
			}
			load = h.deviceStore.Take
		}
		payload, err := load(r.Context(), "user:"+code)
		switch {
		case err == nil:
			var request deviceRequest
			if err := json.Unmarshal(payload, &request); err != nil {
				h.deviceUnavailable(w, err)
				return
			}
			if confirm {
				http.SetCookie(w, deviceBrowserCookie("confirm:"+code, "", -1))
				if !h.authorizeDeviceBrowser(w, r, request) {
					h.recordDeviceLoginFailure(r.Context(), request)
				}
			} else {
				confirmation := oauth2.GenerateVerifier()
				http.SetCookie(w, deviceBrowserCookie("confirm:"+code, confirmation, int(time.Until(request.ExpiresAt).Seconds())+1))
				_ = deviceConfirmPage.Execute(w, struct {
					Code, DisplayCode, Network, StartedAt, Confirmation string
				}{
					Code: code, DisplayCode: code[:4] + "-" + code[4:], Network: request.Network,
					StartedAt: request.StartedAt.UTC().Format("2006-01-02 15:04:05 UTC"), Confirmation: confirmation,
				})
			}
			return
		case !errors.Is(err, sharedstore.ErrNotFound):
			h.deviceUnavailable(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusBadRequest)
	_ = deviceCodePage.Execute(w, "That code is invalid, expired, or already used. Check your terminal or start a new sign-in.")
}

func (h *Handler) authorizeDeviceBrowser(w http.ResponseWriter, r *http.Request, request deviceRequest) bool {
	state := deviceStatePrefix + oauth2.GenerateVerifier()
	browserSecret := oauth2.GenerateVerifier()
	record := deviceBrowserState{
		deviceRequest: request,
		Verifier:      oauth2.GenerateVerifier(),
		BrowserHash:   deviceDigest(browserSecret),
	}
	value, err := json.Marshal(record)
	if err != nil {
		h.deviceUnavailable(w, err)
		return false
	}
	ok, err := h.deviceStore.CreateIfAbsent(r.Context(), "oidc:"+state, value, request.ExpiresAt)
	if err != nil || !ok {
		h.deviceUnavailable(w, err)
		return false
	}
	http.SetCookie(w, deviceBrowserCookie(state, browserSecret, int(time.Until(request.ExpiresAt).Seconds())+1))
	http.Redirect(w, r, h.oauth2Config.AuthCodeURL(state, oauth2.S256ChallengeOption(record.Verifier)), http.StatusSeeOther)
	return true
}

func deviceBrowserCookie(state, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     "__Host-railgrid-device-" + deviceDigest(state),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
}

// takeDeviceBrowserState checks browser binding before consuming state, so a
// callback from another browser cannot invalidate the legitimate sign-in.
func (h *Handler) takeDeviceBrowserState(w http.ResponseWriter, r *http.Request, state string) (deviceBrowserState, bool) {
	deviceBrowserHeaders(w)
	var record deviceBrowserState
	cookie, err := r.Cookie(deviceBrowserCookie(state, "", 0).Name)
	if err != nil || h.deviceStore == nil || len(state) != len(deviceStatePrefix)+43 {
		http.Error(w, "Invalid or expired device sign-in.", http.StatusBadRequest)
		return record, false
	}
	key := "oidc:" + state
	payload, err := h.deviceStore.Get(r.Context(), key)
	if err == nil {
		err = json.Unmarshal(payload, &record)
	}
	if errors.Is(err, sharedstore.ErrNotFound) || (err == nil && subtle.ConstantTimeCompare([]byte(record.BrowserHash), []byte(deviceDigest(cookie.Value))) != 1) {
		http.Error(w, "Invalid or expired device sign-in.", http.StatusBadRequest)
		return record, false
	}
	if err != nil {
		h.deviceUnavailable(w, err)
		return record, false
	}
	if _, err := h.deviceStore.Take(r.Context(), key); err != nil {
		if errors.Is(err, sharedstore.ErrNotFound) {
			http.Error(w, "Invalid or expired device sign-in.", http.StatusBadRequest)
		} else {
			h.deviceUnavailable(w, err)
		}
		return record, false
	}
	http.SetCookie(w, deviceBrowserCookie(state, "", -1))
	return record, true
}

func (h *Handler) completeDeviceLogin(w http.ResponseWriter, r *http.Request, request deviceRequest, result deviceResult) bool {
	err := h.storeDeviceResult(r.Context(), request, result)
	if errors.Is(err, sharedstore.ErrNotFound) {
		http.Error(w, "Device sign-in expired. Start a new sign-in from the terminal.", http.StatusGone)
		return true
	}
	if err != nil {
		h.deviceUnavailable(w, err)
		return false
	}
	page := statuspage.Page{
		Title:   "CLI sign-in approved",
		Heading: "CLI sign-in approved",
		Message: "Return to the terminal to finish signing in.",
	}
	if result.Error != "" {
		page.Title = "CLI sign-in declined"
		page.Heading = page.Title
		page.Message = "Return to the terminal to start a new sign-in."
		page.State = statuspage.Error
	}
	_ = statuspage.Render(w, page)
	return true
}

func (h *Handler) recordDeviceLoginFailure(ctx context.Context, request deviceRequest) {
	fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.storeDeviceResult(fallbackCtx, request, deviceResult{Error: "server_error"}); err != nil && !errors.Is(err, sharedstore.ErrNotFound) {
		h.logger.Error(err, "failed to record CLI sign-in failure")
	}
}

func (h *Handler) storeDeviceResult(ctx context.Context, request deviceRequest, result deviceResult) error {
	if !time.Now().Before(request.ExpiresAt) {
		return sharedstore.ErrNotFound
	}
	result.AckNonce = oauth2.GenerateVerifier()
	value, err := json.Marshal(result)
	if err != nil {
		return err
	}
	recipientKey, err := ecdh.X25519().NewPublicKey(request.RecipientKey)
	if err != nil {
		return err
	}
	recipient, err := hpke.NewDHKEMPublicKey(recipientKey)
	if err != nil {
		return err
	}
	// Persist only ciphertext. The CLI generated the recipient private key
	// and keeps it in memory for this invocation.
	sealed, err := hpke.Seal(recipient, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(apiurl.DeviceLoginHPKEInfo), value)
	if err != nil {
		return err
	}
	stored, err := json.Marshal(storedDeviceResult{Ciphertext: sealed, AckDigest: deviceDigest(result.AckNonce)})
	if err != nil {
		return err
	}
	created, err := h.deviceStore.CreateIfAbsent(ctx, "result:"+request.PollKey, stored, request.ExpiresAt)
	if err == nil && !created {
		return fmt.Errorf("device sign-in result already exists")
	}
	return err
}

func (h *Handler) pollDeviceLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil || len(r.PostForm.Get("device_code")) != 43 {
		writeDevicePollError(w, "invalid_request")
		return
	}
	ctx := r.Context()
	pollKey := deviceDigest(r.PostForm.Get("device_code"))
	pendingKey := "pending:" + pollKey
	allowed, err := h.deviceStore.AllowInterval(ctx, pendingKey, devicePollInterval*time.Second)
	switch {
	case errors.Is(err, sharedstore.ErrNotFound):
		writeDevicePollError(w, "expired_token")
		return
	case err != nil:
		h.deviceUnavailable(w, err)
		return
	case !allowed:
		writeDevicePollError(w, "slow_down")
		return
	}
	result, err := h.deviceStore.Get(ctx, "result:"+pollKey)
	switch {
	case err == nil:
		var stored storedDeviceResult
		if err := json.Unmarshal(result, &stored); err != nil {
			h.deviceUnavailable(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(stored.Ciphertext)
	case errors.Is(err, sharedstore.ErrNotFound):
		writeDevicePollError(w, "authorization_pending")
	default:
		h.deviceUnavailable(w, err)
	}
}

func (h *Handler) ackDeviceLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil || len(r.PostForm.Get("device_code")) != 43 || len(r.PostForm.Get("ack_nonce")) != 43 {
		writeDevicePollError(w, "invalid_request")
		return
	}
	ctx := r.Context()
	pollKey := deviceDigest(r.PostForm.Get("device_code"))
	// Acknowledge only an existing result so an early request cannot stop
	// the CLI from polling while the browser is still signing in.
	value, err := h.deviceStore.Get(ctx, "result:"+pollKey)
	if errors.Is(err, sharedstore.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	} else if err != nil {
		h.deviceUnavailable(w, err)
		return
	}
	var stored storedDeviceResult
	if err := json.Unmarshal(value, &stored); err != nil {
		h.deviceUnavailable(w, err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(stored.AckDigest), []byte(deviceDigest(r.PostForm.Get("ack_nonce")))) != 1 {
		writeDevicePollError(w, "invalid_request")
		return
	}
	if err := h.deviceStore.Delete(ctx, "pending:"+pollKey); err != nil {
		h.deviceUnavailable(w, err)
		return
	}
	if err := h.deviceStore.Delete(ctx, "result:"+pollKey); err != nil {
		h.deviceUnavailable(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeDevicePollError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(deviceResult{Error: code})
}

func (h *Handler) deviceUnavailable(w http.ResponseWriter, err error) {
	if err != nil {
		h.logger.Error(err, "device sign-in store operation failed")
	}
	http.Error(w, "Device sign-in unavailable. Start a new sign-in from the terminal.", http.StatusServiceUnavailable)
}
