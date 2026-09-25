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
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
)

// Provider bundles execute as fully trusted code inside the portal document
// (they are classic scripts registering custom elements, not iframes). The
// portal pins each bundle with Subresource Integrity so the browser refuses a
// /main.js that differs from the one the hub hashed here — the pin is computed
// from the same source the UI proxy serves, so a bundle swapped behind
// spec.serving.ui.url after registration cannot execute until the hub has re-admitted
// it by re-hashing.

const (
	// uiIntegrityFetchTimeout bounds one hash fetch of a provider bundle. It is
	// longer than the health probe's because bundles are megabytes, not bytes.
	uiIntegrityFetchTimeout = 15 * time.Second
	// uiIntegrityMaxBytes caps how much bundle the hub is willing to read for a
	// hash; a larger response is treated as a fetch failure.
	uiIntegrityMaxBytes = 64 << 20
	// UIIntegrityResync bounds how long a pin is reused without re-reading the
	// bundle, and is the FALLBACK path only: an upstream that returns an ETag
	// on /main.js is revalidated with a conditional GET on every reconcile, so
	// a bundle rebuilt at an unchanged version is re-pinned within one
	// reconcile instead of within this interval. Embedded assets and upstreams
	// that send no ETag keep the interval. A version change (chart upgrade or a
	// heartbeat reporting a new version) forces an immediate re-hash either way.
	UIIntegrityResync = 10 * time.Minute
)

// uiIntegrityRecord is one cached pin: the version it was computed for and
// when, so the reconciler can skip the fetch on the (frequent) reconciles a
// heartbeat status write triggers.
//
// etag is the upstream's validator for the bytes this pin was computed from,
// empty when the upstream sent none (or for embedded assets, which are read
// from the hub binary and cannot change while it runs). A non-empty etag is
// what turns the next reconcile's re-read into a conditional GET: the common
// answer is 304 and no body, which is cheap enough to do every reconcile.
type uiIntegrityRecord struct {
	version   string
	integrity string
	hashedAt  time.Time
	etag      string
}

func defaultUIAssetClient() *http.Client {
	return &http.Client{
		Timeout: uiIntegrityFetchTimeout,
		// A provider-controlled redirect cannot move the hub's fetch to another
		// authority; the proxy does not follow one for the browser either.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// uiIntegrityVersion is the version a pin is keyed on: what the running pod
// reports, falling back to what the chart declared.
func uiIntegrityVersion(specVersion, reportedVersion string) string {
	if reportedVersion != "" {
		return reportedVersion
	}
	return specVersion
}

// pinUIIntegrity returns the SRI pin for prov's /main.js at version, fetching
// and hashing the bundle when there is no fresh cached pin for that version.
//
// A pin cached WITH an upstream ETag is revalidated on every reconcile with a
// conditional GET: 304 keeps the pin (and refreshes hashedAt), 200 means the
// bundle behind the URL is a different one and is re-pinned immediately. That
// is what makes a rebuild at an unchanged version — every Tilt rebuild, every
// image rebuild at the same chart version — visible to the hub within one
// reconcile instead of within UIIntegrityResync, which is how a browser came to
// be handed a pin the bundle could no longer match. Without an upstream ETag
// (and for embedded assets, which cannot change in-process) the resync interval
// is still the only trigger.
//
// The second return is false when the hub does not serve this provider's
// bundle at all (org-owned providers go over the edge tunnel and are never
// dialled; builtinRoute and UI-less entries have no /main.js) or when the
// fetch failed and there is no pin to keep. A failure at an unchanged version
// keeps the previous pin: a transient upstream error must not silently unpin a
// bundle. A failure after a version change drops the pin so the portal can
// still load the new bundle, unpinned, until the next reconcile succeeds.
//
// Safe to call concurrently for one provider. The lock is dropped across the
// fetch, so the error path decides on the cache entry it re-reads rather than
// the snapshot it took beforehand: a record another reconcile wrote meanwhile
// is left alone, and only this call's own superseded pin is dropped.
func (r *CatalogReconciler) pinUIIntegrity(ctx context.Context, logger logr.Logger, prov Provider, version string) (string, bool) {
	if prov.OrgUUID != "" || (prov.LocalUIAssets == nil && prov.UIURL == nil) {
		return "", false
	}
	key := providerKey{Org: prov.OrgUUID, Name: prov.Name}
	now := time.Now()

	r.uiIntegrityMu.Lock()
	cached, ok := r.uiIntegrity[key]
	r.uiIntegrityMu.Unlock()
	fresh := ok && cached.version == version
	if fresh && cached.etag == "" && now.Sub(cached.hashedAt) < UIIntegrityResync {
		// Nothing to revalidate against, so the interval is the only guard.
		return cached.integrity, cached.integrity != ""
	}

	// Offer the cached validator only when it describes a pin still in force
	// for this version: a 304 is read as "the pin you hold is still right", so
	// revalidating against anything else would confirm the wrong bundle.
	ifNoneMatch := ""
	if fresh && cached.integrity != "" {
		ifNoneMatch = cached.etag
	}

	client := r.uiClient
	if client == nil {
		client = defaultUIAssetClient()
	}
	read, err := readProviderMainJS(ctx, client, prov, ifNoneMatch)
	if err != nil {
		logger.Info("WARNING could not pin provider UI bundle; portal loads it unpinned until the next successful reconcile", "err", err.Error(), "version", version)
		r.uiIntegrityMu.Lock()
		defer r.uiIntegrityMu.Unlock()
		// Re-read the entry: the pre-fetch snapshot is stale by however long
		// the fetch took, and another reconcile may have written a newer
		// record meanwhile. Deciding on the snapshot would delete that record.
		current, currentOK := r.uiIntegrity[key]
		if currentOK && current.version == version {
			// Keep the pin, but do not refresh hashedAt: the next reconcile
			// retries the fetch instead of trusting this pin for another
			// resync interval.
			return current.integrity, current.integrity != ""
		}
		if currentOK != ok || current != cached {
			// A concurrent reconcile wrote this entry while the fetch was in
			// flight, so it is not the stale pin this call set out to replace.
			// Leave it and report only this attempt's own failure.
			return "", false
		}
		// The entry is unchanged since the snapshot: a pin for a superseded
		// version. Drop it so the portal loads the new bundle unpinned rather
		// than with a hash that cannot match it.
		delete(r.uiIntegrity, key)
		return "", false
	}

	if read.notModified {
		// The upstream confirmed the bytes behind the URL are the ones this pin
		// was computed from. Refresh hashedAt so the no-ETag fallback window
		// restarts, but only on the record this call revalidated: a concurrent
		// reconcile that wrote a different pin meanwhile owns the entry now.
		r.uiIntegrityMu.Lock()
		if current, currentOK := r.uiIntegrity[key]; currentOK && current == cached {
			current.hashedAt = now
			r.uiIntegrity[key] = current
		}
		r.uiIntegrityMu.Unlock()
		return cached.integrity, cached.integrity != ""
	}

	r.uiIntegrityMu.Lock()
	if r.uiIntegrity == nil {
		r.uiIntegrity = map[providerKey]uiIntegrityRecord{}
	}
	r.uiIntegrity[key] = uiIntegrityRecord{version: version, integrity: read.integrity, hashedAt: now, etag: read.etag}
	r.uiIntegrityMu.Unlock()
	if !ok || cached.integrity != read.integrity {
		logger.Info("Pinned provider UI bundle", "version", version, "integrity", read.integrity)
	}
	return read.integrity, true
}

// ObserveMainJSIntegrity records the SRI pin of the /main.js body the UI proxy
// actually streamed to a browser (see ProviderProxy.SetMainJSIntegrityObserver).
//
// It is the second half of the answer to a bundle that changes at an unchanged
// version: the reconcile loop revalidates every reconcile, but a browser can
// still ask for the bundle in the window between the rebuild and the next
// reconcile, and what it receives is authoritative — that body is what the
// integrity attribute has to match. So the pin is corrected from the bytes
// served, for the provider's CURRENT version, and the next reconcile writes it
// through to the CatalogEntry status like any other pin.
//
// The cached record deliberately loses its ETag here: that validator described
// the body the hub hashed, not the one it just served, and keeping it would let
// the next conditional GET confirm the superseded pin with a 304.
//
// Only platform providers with a proxied UI are corrected. Org-owned bundles
// travel the edge tunnel and are pinned per grant (ui_grant.go), and embedded
// assets are read from this binary and cannot disagree with it.
func (r *CatalogReconciler) ObserveMainJSIntegrity(name, integrity string) {
	if r == nil || r.reg == nil || name == "" || integrity == "" {
		return
	}
	prov, ok := r.reg.Get(name)
	if !ok || prov.OrgUUID != "" || prov.UIURL == nil {
		return
	}
	if prov.MainJSIntegrity == integrity {
		return
	}
	version := uiIntegrityVersion(prov.Version, prov.ReportedVersion)

	r.uiIntegrityMu.Lock()
	if r.uiIntegrity == nil {
		r.uiIntegrity = map[providerKey]uiIntegrityRecord{}
	}
	r.uiIntegrity[providerKey{Name: name}] = uiIntegrityRecord{version: version, integrity: integrity, hashedAt: time.Now()}
	r.uiIntegrityMu.Unlock()

	prov.MainJSIntegrity = integrity
	r.reg.Upsert(prov)
}

// uiBundleRead is one read of a provider's /main.js: either its SRI pin plus
// the upstream validator to revalidate with next time, or notModified when a
// conditional GET confirmed the caller's existing pin and returned no body.
type uiBundleRead struct {
	integrity   string
	etag        string
	notModified bool
}

// hashProviderMainJS reads the bundle exactly as the UI proxy would serve it —
// from the embedded assets of a first-party provider, or from
// <spec.serving.ui.url>/main.js — and returns its SRI metadata.
func hashProviderMainJS(ctx context.Context, client httpDoer, prov Provider) (string, error) {
	read, err := readProviderMainJS(ctx, client, prov, "")
	if err != nil {
		return "", err
	}
	return read.integrity, nil
}

// readProviderMainJS is hashProviderMainJS plus revalidation: ifNoneMatch, when
// non-empty, is sent as If-None-Match so an unchanged upstream can answer 304
// without a body. Embedded assets ignore it — they are read from this binary,
// so there is nothing to revalidate against.
func readProviderMainJS(ctx context.Context, client httpDoer, prov Provider, ifNoneMatch string) (uiBundleRead, error) {
	switch {
	case prov.LocalUIAssets != nil:
		data, err := fs.ReadFile(prov.LocalUIAssets, "main.js")
		if err != nil {
			return uiBundleRead{}, err
		}
		return uiBundleRead{integrity: sriSHA384(data)}, nil
	case prov.UIURL != nil:
		return fetchProviderMainJS(ctx, client, prov.UIURL, ifNoneMatch)
	default:
		return uiBundleRead{}, errors.New("provider has no hub-served UI")
	}
}

// fetchProviderMainJS GETs <ui>/main.js with the same path join the UI proxy
// uses for the browser's request, bounded in time and size, and reports the
// upstream's ETag so the next fetch can be conditional.
func fetchProviderMainJS(ctx context.Context, client httpDoer, ui *url.URL, ifNoneMatch string) (uiBundleRead, error) {
	if ui.Scheme != "http" && ui.Scheme != "https" {
		return uiBundleRead{}, fmt.Errorf("ui URL must use http or https")
	}
	target := *ui
	target.Path = singleJoiningSlash(ui.Path, "/main.js")
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""

	fetchCtx, cancel := context.WithTimeout(ctx, uiIntegrityFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return uiBundleRead{}, fmt.Errorf("build main.js request: %w", err)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := client.Do(req)
	if err != nil {
		return uiBundleRead{}, fmt.Errorf("fetch main.js: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// 304 is only an answer to a question we asked. An unsolicited one is an
	// upstream fault, not a confirmation of a pin we did not offer.
	if resp.StatusCode == http.StatusNotModified && ifNoneMatch != "" {
		return uiBundleRead{notModified: true, etag: firstNonEmpty(resp.Header.Get("ETag"), ifNoneMatch)}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return uiBundleRead{}, fmt.Errorf("fetch main.js: returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, uiIntegrityMaxBytes+1))
	if err != nil {
		return uiBundleRead{}, fmt.Errorf("read main.js: %w", err)
	}
	if len(data) > uiIntegrityMaxBytes {
		return uiBundleRead{}, fmt.Errorf("main.js exceeds %d bytes", uiIntegrityMaxBytes)
	}
	// A weak validator ("W/\"…\"") does not promise byte equality, which is
	// exactly what an SRI pin needs, so it is not kept: the provider then falls
	// back to the resync interval rather than to a 304 that means less than it
	// appears to.
	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	if strings.HasPrefix(etag, "W/") {
		etag = ""
	}
	return uiBundleRead{integrity: sriSHA384(data), etag: etag}, nil
}

// sriSHA384 renders data's SRI metadata in the form the browser's integrity
// attribute expects. sha384 is the strongest digest every SRI-capable browser
// supports.
func sriSHA384(data []byte) string {
	sum := sha512.Sum384(data)
	return "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
}
