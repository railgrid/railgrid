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
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"hash"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
)

// The reconcile loop pins a provider's bundle by fetching it (ui_integrity.go),
// which leaves one window open: between a rebuild behind spec.ui.url and the
// next reconcile, the pin the portal holds describes bytes the upstream no
// longer serves, and every browser refuses the bundle with "Failed to find a
// valid digest in the 'integrity' attribute". The proxy is the one component
// that sees exactly what the browser sees, so it hashes the body it forwards
// and reports a pin that disagrees. The hash is taken as the body streams —
// nothing is buffered, so a megabyte bundle still reaches the browser at the
// same time it otherwise would — and is reported only on a clean EOF, because a
// partial body's hash is not the bundle's.
//
// This never relaxes SRI: the browser still enforces whatever pin the portal
// was given for THIS load. It only shortens how long the hub keeps handing out
// a pin it can see is wrong.

// observeMainJS returns a ModifyResponse that hashes a proxied /main.js body
// and hands the result to the integrity observer when it differs from the pin
// the provider currently advertises.
func (p *ProviderProxy) observeMainJS(prov Provider) func(*http.Response) error {
	name := prov.Name
	current := prov.MainJSIntegrity
	observer := p.integrityObserver
	return func(resp *http.Response) error {
		// Only a 200 carries the bundle itself. A 304, a redirect or an error
		// body says nothing about what the bundle hashes to.
		if resp.StatusCode != http.StatusOK || !isJavaScriptContentType(resp.Header.Get("Content-Type")) {
			return nil
		}
		resp.Body = &hashingReadCloser{
			rc:  resp.Body,
			sum: sha512.New384(),
			max: uiIntegrityMaxBytes,
			report: func(digest []byte) {
				integrity := "sha384-" + base64.StdEncoding.EncodeToString(digest)
				if integrity == current {
					return
				}
				p.log.Info("Correcting the provider UI pin from the bundle actually served",
					"provider", name, "was", current, "now", integrity)
				observer.ObserveMainJSIntegrity(name, integrity)
			},
		}
		return nil
	}
}

// hashingReadCloser digests a response body as it is copied to the client and
// calls report exactly once, on a clean EOF and only when the whole body fit
// within max. A truncated or aborted body is silently dropped: the point of the
// hash is that it describes the complete bundle.
type hashingReadCloser struct {
	rc        io.ReadCloser
	sum       hash.Hash
	max       int64
	n         int64
	truncated bool
	reported  bool
	report    func(digest []byte)
}

func (h *hashingReadCloser) Read(b []byte) (int, error) {
	n, err := h.rc.Read(b)
	if n > 0 && !h.truncated {
		h.n += int64(n)
		if h.n > h.max {
			h.truncated = true
		} else {
			_, _ = h.sum.Write(b[:n])
		}
	}
	if errors.Is(err, io.EOF) && !h.truncated && !h.reported {
		h.reported = true
		h.report(h.sum.Sum(nil))
	}
	return n, err
}

func (h *hashingReadCloser) Close() error { return h.rc.Close() }

// isMainJSPath reports whether rest (the path after /ui/providers/{name})
// addresses the bundle the portal pins. Cleaned first so /assets/../main.js
// cannot reach the bundle without being recognised as it.
func isMainJSPath(rest string) bool {
	return path.Clean("/"+strings.TrimPrefix(rest, "/")) == "/main.js"
}

// isJavaScriptContentType reports whether a response claims to be the script
// the portal will execute. A provider dev server that answers /main.js with an
// HTML error page is not a bundle, and its hash must not become the pin.
func isJavaScriptContentType(value string) bool {
	if value == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		// Unparseable parameters are common enough not to be fatal; judge the
		// bare type.
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	}
	switch mediaType {
	case "application/javascript", "text/javascript", "application/x-javascript",
		"application/ecmascript", "text/ecmascript", "module":
		return true
	}
	return false
}
