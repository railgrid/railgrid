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

// faros-access-proxy fronts one published template instance. Templates render
// it as a component of their own graph; every knob arrives via environment
// variables (overridable by flags). The process holds no credentials and has
// no Kubernetes client.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/faroshq/provider-infrastructure/accessproxy"
)

func main() {
	listen := flag.String("listen-address", envOr("FAROS_ACCESS_PROXY_LISTEN_ADDRESS", ":8080"), "HTTP listen address")
	mode := flag.String("mode", envOr("FAROS_ACCESS_PROXY_MODE", "public"), "access mode: public or private")
	host := flag.String("host", envOr("FAROS_ACCESS_PROXY_HOST", ""), "exact external host this proxy serves")
	routes := flag.String("routes", envOr("FAROS_ACCESS_PROXY_ROUTES", ""), "comma-separated prefix=target route list")
	publicScheme := flag.String("public-scheme", envOr("FAROS_ACCESS_PROXY_PUBLIC_SCHEME", "https"), "external scheme for the app callback URL")
	cluster := flag.String("instance-cluster", envOr("FAROS_ACCESS_PROXY_INSTANCE_CLUSTER", ""), "logical cluster ID of the tenant workspace")
	group := flag.String("instance-group", envOr("FAROS_ACCESS_PROXY_INSTANCE_GROUP", ""), "API group of the instance CRD")
	resource := flag.String("instance-resource", envOr("FAROS_ACCESS_PROXY_INSTANCE_RESOURCE", ""), "REST plural of the instance CRD")
	name := flag.String("instance-name", envOr("FAROS_ACCESS_PROXY_INSTANCE_NAME", ""), "instance name")
	hubURL := flag.String("hub-url", envOr("FAROS_HUB_URL", ""), "in-cluster hub origin for the code exchange (private mode)")
	hubPublicURL := flag.String("hub-public-url", envOr("FAROS_HUB_PUBLIC_URL", ""), "browser-reachable hub origin for authorize redirects (defaults to --hub-url)")
	hubInsecure := flag.Bool("hub-insecure-skip-tls-verify", envBool("FAROS_HUB_INSECURE_SKIP_TLS_VERIFY"), "skip TLS verification for hub calls (development only)")
	flag.Parse()

	config := accessproxy.Config{
		ListenAddress: *listen,
		Mode:          accessproxy.Mode(strings.ToLower(strings.TrimSpace(*mode))),
		Host:          *host,
		Routes:        parseRoutes(*routes),
		Instance: accessproxy.InstanceRef{
			Cluster:  *cluster,
			Group:    *group,
			Resource: *resource,
			Name:     *name,
		},
		HubURL:       *hubURL,
		HubPublicURL: *hubPublicURL,
		HubInsecure:  *hubInsecure,
		PublicScheme: *publicScheme,
	}

	proxy, err := accessproxy.New(config)
	if err != nil {
		log.Fatalf("access proxy configuration invalid: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("faros-access-proxy: serving %s mode=%s routes=%d listen=%s", *host, config.Mode, len(config.Routes), *listen)
	if err := proxy.Serve(ctx); err != nil {
		log.Fatalf("access proxy exited: %v", err)
	}
}

func parseRoutes(raw string) []accessproxy.Route {
	var routes []accessproxy.Route
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		prefix, target, ok := strings.Cut(entry, "=")
		if !ok {
			// Let normalizeConfig produce the error with full context.
			routes = append(routes, accessproxy.Route{Prefix: entry})
			continue
		}
		routes = append(routes, accessproxy.Route{Prefix: strings.TrimSpace(prefix), Target: strings.TrimSpace(target)})
	}
	return routes
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(key)), "true")
}
