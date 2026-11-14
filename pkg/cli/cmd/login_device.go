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

package cmd

import (
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
)

func runDeviceLogin(ctx context.Context, out io.Writer, hubURL string, insecure bool) error {
	if !strings.HasPrefix(hubURL, "https://") {
		return fmt.Errorf("device sign-in requires an HTTPS hub URL")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit CLI flag
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating device sign-in key: %w", err)
	}
	recipient, err := hpke.NewDHKEMPrivateKey(key)
	if err != nil {
		return fmt.Errorf("preparing device sign-in key: %w", err)
	}
	form := url.Values{"recipient_key": {base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}}
	status, _, body, err := deviceLoginPOST(ctx, client, hubURL+apiurl.PathAuthDeviceStart, form)
	switch {
	case err != nil:
		return fmt.Errorf("starting device sign-in: %w", err)
	case status == http.StatusNotFound:
		return fmt.Errorf("hub at %s does not support device sign-in; update the hub or run login without --device", hubURL)
	case status != http.StatusOK:
		return fmt.Errorf("starting device sign-in: hub returned HTTP %d", status)
	}
	var device oauth2.DeviceAuthResponse
	if err := json.Unmarshal(body, &device); err != nil {
		return fmt.Errorf("decoding device sign-in response: %w", err)
	}
	if device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" || device.Expiry.IsZero() || device.Interval < 0 || device.Interval > 60 {
		return fmt.Errorf("hub returned an invalid device sign-in response")
	}
	verificationURL, err := url.Parse(device.VerificationURI)
	if err != nil || verificationURL.Scheme != "https" || verificationURL.Hostname() == "" {
		return fmt.Errorf("hub returned an invalid HTTPS verification URL")
	}

	_, _ = fmt.Fprintf(out, "Open %s in your browser and enter code %s.\nWaiting for sign-in approval…\n", device.VerificationURI, device.UserCode)

	pollCtx, cancel := context.WithDeadline(ctx, device.Expiry)
	defer cancel()
	response, err := pollDeviceLogin(pollCtx, client, hubURL, device, recipient)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("device sign-in expired; run railgrid login --device again")
	}
	if err != nil {
		return fmt.Errorf("waiting for device sign-in: %w", err)
	}
	if err := saveOIDCLogin(out, response.LoginResponse); err != nil {
		return err
	}
	// The hub retains the ciphertext until expiry if cleanup cannot be sent.
	ackCtx, cancelAck := context.WithTimeout(pollCtx, 2*time.Second)
	defer cancelAck()
	_, _, _, _ = deviceLoginPOST(ackCtx, client, hubURL+apiurl.PathAuthDeviceAck, url.Values{"device_code": {device.DeviceCode}, "ack_nonce": {response.AckNonce}})
	return nil
}

func pollDeviceLogin(ctx context.Context, client *http.Client, hubURL string, device oauth2.DeviceAuthResponse, recipient hpke.PrivateKey) (deviceLoginResult, error) {
	interval := 5 * time.Second
	if device.Interval > 0 {
		interval = time.Duration(device.Interval) * time.Second
	}
	delay := interval
	form := url.Values{"device_code": {device.DeviceCode}}
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return deviceLoginResult{}, ctx.Err()
		case <-timer.C:
		}
		status, retryAfter, body, err := deviceLoginPOST(ctx, client, hubURL+apiurl.PathAuthDevicePoll, form)
		if ctx.Err() != nil {
			return deviceLoginResult{}, ctx.Err()
		}
		if err != nil || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout {
			// The result is safe to fetch again even if a preceding response
			// was interrupted after the hub started sending it.
			delay = min(2*delay, max(interval, 30*time.Second))
			continue
		}
		delay = interval
		switch status {
		case http.StatusOK:
			return openDeviceLoginResult(recipient, body)
		case http.StatusTooManyRequests:
			delay = max(interval, retryAfter)
		case http.StatusBadRequest:
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &response); err != nil {
				return deviceLoginResult{}, fmt.Errorf("hub rejected the device sign-in")
			}
			switch response.Error {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				delay = interval
			case "expired_token":
				return deviceLoginResult{}, context.DeadlineExceeded
			default:
				return deviceLoginResult{}, fmt.Errorf("hub rejected the device sign-in")
			}
		default:
			return deviceLoginResult{}, fmt.Errorf("hub returned HTTP %d", status)
		}
	}
}

type deviceLoginResult struct {
	tenancyv1alpha1.LoginResponse
	Error    string `json:"error"`
	AckNonce string `json:"ack_nonce"`
}

func openDeviceLoginResult(recipient hpke.PrivateKey, sealed []byte) (deviceLoginResult, error) {
	var result deviceLoginResult
	data, err := hpke.Open(recipient, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(apiurl.DeviceLoginHPKEInfo), sealed)
	if err != nil {
		return result, fmt.Errorf("decrypting device sign-in result: %w", err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decoding device sign-in result: %w", err)
	}
	switch result.Error {
	case "access_denied":
		return result, fmt.Errorf("sign-in was declined")
	case "server_error":
		return result, fmt.Errorf("the hub could not complete sign-in; check the browser for details")
	}
	if len(result.Kubeconfig) == 0 || result.IDToken == "" || result.IssuerURL == "" || result.ClientID == "" || result.UserID == "" || len(result.AckNonce) != 43 {
		return result, fmt.Errorf("hub returned incomplete device login credentials")
	}
	return result, nil
}

func deviceLoginPOST(ctx context.Context, client *http.Client, endpoint string, form url.Values) (int, time.Duration, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json, application/octet-stream")
	req.Header.Set("User-Agent", cliUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	var retryAfter time.Duration
	var body []byte
	switch resp.StatusCode {
	case http.StatusOK, http.StatusBadRequest:
		body, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	case http.StatusTooManyRequests:
		if seconds, parseErr := strconv.ParseUint(resp.Header.Get("Retry-After"), 10, 32); parseErr == nil {
			retryAfter = time.Duration(seconds) * time.Second
		}
	}
	return resp.StatusCode, retryAfter, body, err
}
