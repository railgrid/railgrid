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

package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// The built-in destination: Railgrid's own analytics backend, which a
// released hub reports to unless the operator says otherwise. Two properties
// on it, so the number of sessions on the second is the number of
// installations and nothing a console user does is mixed into that.
const (
	BuiltinURL = "https://telemetry.railgrid.ai"
	// BuiltinConsoleWebsiteID records console page views and clicks from
	// self-hosted hubs.
	BuiltinConsoleWebsiteID = "c0bf8219-b1c5-4ee4-9ab2-e07298355b10"
	// BuiltinInstancesWebsiteID records one report a day per hub.
	BuiltinInstancesWebsiteID = "159f61d1-0ea4-460a-8afa-390fdca1ffb1"
)

var releaseRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$`)

// IsRelease reports whether version is a tagged release (v1.2.3, v1.2.3-rc.1).
// "dev", and what git describe prints between tags (v1.2.3-4-gabc123,
// v1.2.3-dirty), are not: a hub built from a checkout, by Tilt, by an e2e
// suite or by CI must never report to the built-in destination.
func IsRelease(version string) bool { return releaseRE.MatchString(version) }

// InstanceID derives the installation's anonymous identifier from seed, a
// value that is stable for the life of the installation and unique to it
// (the hub passes the UID of one of its own kcp logical clusters). It is a
// one-way hash formatted as a UUID: it says "the same installation as
// yesterday" and nothing else, and nothing has to be stored to keep it stable
// across restarts and replicas.
func InstanceID(seed string) string {
	sum := sha256.Sum256([]byte("railgrid-instance\n" + seed))
	h := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// sessionID is the session key for a console event on the built-in
// destination: the installation and the browser version, hashed. It stands in
// for the browser's address, which is not sent there.
func sessionID(instanceID, userAgent string) string {
	sum := sha256.Sum256([]byte(instanceID + "\n" + userAgent))
	return hex.EncodeToString(sum[:16])
}

// railgridProviders are the provider names Railgrid itself publishes, plus
// the fixed label the portal reports an organization's own provider under.
// Any other name was chosen by whoever runs the installation and is reported
// to the built-in destination as otherProvider.
var railgridProviders = map[string]bool{
	"agents": true, "app-studio": true, "code": true, "databricks": true,
	"edges": true, "factory": true, "infrastructure": true, "kuery": true,
	"linear": true, "planner": true, "quickstart": true,
	"org-provider": true,
}

const otherProvider = "other"

// anonymize removes from a sanitized event what the operator's own backend
// may see and Railgrid's may not. See Relay.Anonymous.
func anonymize(e Event) Event {
	e.Referrer = ""
	if name, ok := e.Props["provider"]; ok && !railgridProviders[name] {
		props := make(map[string]string, len(e.Props))
		for k, v := range e.Props {
			props[k] = v
		}
		props["provider"] = otherProvider
		e.Props = props
	}
	// /:orgID/:workspaceID/providers/<name>: the one literal route segment
	// that is a name somebody chose.
	segments := strings.Split(e.Route, "/")
	for i := 1; i < len(segments); i++ {
		if segments[i-1] == "providers" && !strings.HasPrefix(segments[i], ":") && !railgridProviders[segments[i]] {
			segments[i] = otherProvider
		}
	}
	e.Route = strings.Join(segments, "/")
	return e
}

// Bucket turns a count into a size class. The instance report carries these
// and never the count itself.
func Bucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n == 1:
		return "1"
	case n <= 5:
		return "2-5"
	case n <= 20:
		return "6-20"
	case n <= 100:
		return "21-100"
	case n <= 500:
		return "101-500"
	}
	return "500+"
}
