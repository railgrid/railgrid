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
	"context"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

const (
	// instanceReportDelay is how long a hub runs before its first report: a
	// hub that is crash-looping or was started for a minute reports nothing.
	instanceReportDelay = 5 * time.Minute
	// instanceReportInterval paces the report after that. The backend is an
	// external system, so a timer is the only way to reach it.
	instanceReportInterval = 24 * time.Hour

	instanceReportEvent = "instance_report"
)

// Report is what a hub knows about itself that the instance report may carry.
// There is deliberately no field for a hostname, a name of anything, or an
// exact count.
type Report struct {
	// KCP is "embedded" or "external".
	KCP string
	// Auth is "oidc", "static" or "oidc+static".
	Auth string
	// Organizations and Users are counts, reported as a Bucket. Negative
	// means unknown and the label is left out.
	Organizations int
	Users         int
	// Providers are the names of the platform providers installed. Only
	// Railgrid's own are reported by name; the rest are counted.
	Providers []string
}

// Reporter tells the built-in destination, once a day, that this installation
// exists and which version it runs. It is how Railgrid knows how many
// self-hosted hubs there are. See docs/telemetry.md for the full payload.
type Reporter struct {
	sink       Sink
	instanceID string
	version    string
	collect    func(context.Context) Report

	delay, interval time.Duration
}

// NewReporter returns a reporter delivering to sink. collect is called for
// each report and must not fail: what it cannot find out it leaves unknown.
func NewReporter(sink Sink, instanceID, version string, collect func(context.Context) Report) *Reporter {
	return &Reporter{
		sink: sink, instanceID: instanceID, version: version, collect: collect,
		// Jitter, so a fleet restarted together does not report together.
		delay:    instanceReportDelay + rand.N(instanceReportDelay),
		interval: instanceReportInterval,
	}
}

// Run reports until ctx is cancelled. Delivery is best effort and is not
// retried: a missed day is a gap in a chart, nothing more. A scaled hub runs
// this on its leader only.
func (r *Reporter) Run(ctx context.Context) {
	logger := klog.FromContext(ctx).WithName("telemetry")
	timer := time.NewTimer(r.delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		sendCtx, cancel := context.WithTimeout(ctx, time.Minute)
		client, event := r.build(r.collect(sendCtx))
		if err := r.sink.Send(sendCtx, client, event); err != nil && ctx.Err() == nil {
			logger.V(2).Info("Instance report was not delivered", "err", err.Error())
		}
		cancel()
		timer.Reset(r.interval)
	}
}

// build is the whole report: everything that leaves the hub is assembled here.
func (r *Reporter) build(report Report) (Client, Event) {
	props := map[string]string{"version": r.version}
	if report.KCP != "" {
		props["kcp"] = report.KCP
	}
	if report.Auth != "" {
		props["auth"] = report.Auth
	}
	if report.Organizations >= 0 {
		props["organizations"] = Bucket(report.Organizations)
	}
	if report.Users >= 0 {
		props["users"] = Bucket(report.Users)
	}
	var known []string
	other := 0
	for _, name := range report.Providers {
		if railgridProviders[name] && name != "org-provider" {
			known = append(known, name)
		} else {
			other++
		}
	}
	sort.Strings(known)
	if len(known) > 0 {
		props["providers"] = strings.Join(known, ",")
	}
	props["other_providers"] = Bucket(other)

	// The session is the installation: one visitor on the instances property
	// is one hub. The user agent is browser-shaped because Umami discards
	// anything it takes for a bot, and Go's default is one.
	client := Client{ID: r.instanceID, UserAgent: "Mozilla/5.0 (X11; Linux x86_64) railgrid-hub/" + r.version}
	return client, Event{Type: "event", Name: instanceReportEvent, Route: serverRoute, Props: props}
}
