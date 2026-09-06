/*
Copyright 2026 The Faros Authors.

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
	"time"

	"github.com/go-logr/logr"
)

// Provider bundles execute as fully trusted code inside the portal document
// (they are classic scripts registering custom elements, not iframes). The
// portal pins each bundle with Subresource Integrity so the browser refuses a
// /main.js that differs from the one the hub hashed here — the pin is computed
// from the same source the UI proxy serves, so a bundle swapped behind
// spec.ui.url after registration cannot execute until the hub has re-admitted
// it by re-hashing.

const (
	// uiIntegrityFetchTimeout bounds one hash fetch of a provider bundle. It is
	// longer than the health probe's because bundles are megabytes, not bytes.
	uiIntegrityFetchTimeout = 15 * time.Second
	// uiIntegrityMaxBytes caps how much bundle the hub is willing to read for a
	// hash; a larger response is treated as a fetch failure.
	uiIntegrityMaxBytes = 64 << 20
	// UIIntegrityResync bounds how long a pin is reused without re-reading the
	// bundle when neither spec.version nor status.reportedVersion changed. A
	// version change (chart upgrade or a heartbeat reporting a new version)
	// forces an immediate re-hash on the next reconcile.
	UIIntegrityResync = 10 * time.Minute
)

// uiIntegrityRecord is one cached pin: the version it was computed for and
// when, so the reconciler can skip the fetch on the (frequent) reconciles a
// heartbeat status write triggers.
type uiIntegrityRecord struct {
	version   string
	integrity string
	hashedAt  time.Time
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
	if ok && cached.version == version && now.Sub(cached.hashedAt) < UIIntegrityResync {
		return cached.integrity, cached.integrity != ""
	}

	client := r.uiClient
	if client == nil {
		client = defaultUIAssetClient()
	}
	integrity, err := hashProviderMainJS(ctx, client, prov)
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

	r.uiIntegrityMu.Lock()
	if r.uiIntegrity == nil {
		r.uiIntegrity = map[providerKey]uiIntegrityRecord{}
	}
	r.uiIntegrity[key] = uiIntegrityRecord{version: version, integrity: integrity, hashedAt: now}
	r.uiIntegrityMu.Unlock()
	if !ok || cached.integrity != integrity {
		logger.Info("Pinned provider UI bundle", "version", version, "integrity", integrity)
	}
	return integrity, true
}

// hashProviderMainJS reads the bundle exactly as the UI proxy would serve it —
// from the embedded assets of a first-party provider, or from
// <spec.ui.url>/main.js — and returns its SRI metadata.
func hashProviderMainJS(ctx context.Context, client httpDoer, prov Provider) (string, error) {
	var (
		data []byte
		err  error
	)
	switch {
	case prov.LocalUIAssets != nil:
		data, err = fs.ReadFile(prov.LocalUIAssets, "main.js")
	case prov.UIURL != nil:
		data, err = fetchProviderMainJS(ctx, client, prov.UIURL)
	default:
		return "", errors.New("provider has no hub-served UI")
	}
	if err != nil {
		return "", err
	}
	return sriSHA384(data), nil
}

// fetchProviderMainJS GETs <ui>/main.js with the same path join the UI proxy
// uses for the browser's request, bounded in time and size.
func fetchProviderMainJS(ctx context.Context, client httpDoer, ui *url.URL) ([]byte, error) {
	if ui.Scheme != "http" && ui.Scheme != "https" {
		return nil, fmt.Errorf("ui URL must use http or https")
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
		return nil, fmt.Errorf("build main.js request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch main.js: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch main.js: returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, uiIntegrityMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read main.js: %w", err)
	}
	if len(data) > uiIntegrityMaxBytes {
		return nil, fmt.Errorf("main.js exceeds %d bytes", uiIntegrityMaxBytes)
	}
	return data, nil
}

// sriSHA384 renders data's SRI metadata in the form the browser's integrity
// attribute expects. sha384 is the strongest digest every SRI-capable browser
// supports.
func sriSHA384(data []byte) string {
	sum := sha512.Sum384(data)
	return "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
}
