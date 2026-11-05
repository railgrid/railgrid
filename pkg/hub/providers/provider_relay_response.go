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

package providers

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
)

// orgProviderModifyResponse removes browser-controlled response state from an
// organization-owned upstream. A valid 101 handshake keeps only the headers
// needed to complete an upgrade; ReverseProxy validates the negotiated protocol
// against the request before handing off the connection.
func orgProviderModifyResponse(resp *http.Response, requestHeaders http.Header) error {
	filtered := make(http.Header)
	CopyOrgProviderResponseHeaders(filtered, resp.Header)
	copyOrgProviderLocation(filtered, resp.Header, resp.StatusCode)
	if resp.StatusCode == http.StatusSwitchingProtocols {
		requestUpgrade := strings.TrimSpace(requestHeaders.Get("Upgrade"))
		responseUpgrade := strings.TrimSpace(resp.Header.Get("Upgrade"))
		if requestUpgrade == "" || !headerHasToken(requestHeaders, "Connection", "upgrade") ||
			!strings.EqualFold(requestUpgrade, responseUpgrade) || !headerHasToken(resp.Header, "Connection", "upgrade") {
			return errors.New("invalid organization provider protocol upgrade")
		}
		filtered.Set("Connection", "Upgrade")
		filtered.Set("Upgrade", responseUpgrade)
		for _, name := range []string{"Sec-WebSocket-Accept", "Sec-WebSocket-Protocol", "Sec-WebSocket-Extensions"} {
			if values := resp.Header.Values(name); len(values) != 0 {
				filtered[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
			}
		}
	}
	resp.Header = filtered
	// ReverseProxy otherwise forwards values announced in Trailer after the
	// final response headers, outside ModifyResponse's filtering point.
	resp.Trailer = nil
	return nil
}

func headerHasToken(header http.Header, name, wanted string) bool {
	for _, value := range header.Values(name) {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), wanted) {
				return true
			}
		}
	}
	return false
}

// orgProviderResponseWriter drops informational headers before ReverseProxy
// writes them. net/http/httputil forwards 1xx responses through httptrace and
// does not call ModifyResponse for them.
type orgProviderResponseWriter struct {
	underlying http.ResponseWriter
	upstream   http.Header
	committed  bool
}

func newOrgProviderResponseWriter(w http.ResponseWriter) *orgProviderResponseWriter {
	return &orgProviderResponseWriter{underlying: w, upstream: make(http.Header)}
}

// Header returns a private staging map. ReverseProxy writes announced and late
// trailers through this map after the final headers have been committed, so
// they must never alias the browser-facing header map.
func (w *orgProviderResponseWriter) Header() http.Header { return w.upstream }

func (w *orgProviderResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		clear(w.upstream)
		return
	}
	w.commitHeaders()
	w.underlying.WriteHeader(status)
}

func (w *orgProviderResponseWriter) commitHeaders() {
	if w.committed {
		return
	}
	w.committed = true
	dst := w.underlying.Header()
	for key, values := range w.upstream {
		if _, exists := dst[key]; exists {
			continue // retain headers installed by trusted hub middleware
		}
		dst[key] = append([]string(nil), values...)
	}
}

func (w *orgProviderResponseWriter) Write(p []byte) (int, error) {
	w.commitHeaders()
	return w.underlying.Write(p)
}

func (w *orgProviderResponseWriter) Flush() {
	w.commitHeaders()
	if f, ok := w.underlying.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *orgProviderResponseWriter) FlushError() error {
	w.commitHeaders()
	if f, ok := w.underlying.(interface{ FlushError() error }); ok {
		return f.FlushError()
	}
	if f, ok := w.underlying.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func (w *orgProviderResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.underlying.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support protocol upgrades")
	}
	return h.Hijack()
}

func (w *orgProviderResponseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.underlying.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}
