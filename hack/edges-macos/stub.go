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

// Command macos-stub is a localhost-only health endpoint used to validate the
// Edges Service transport on a MacOSServer. It is deliberately a health stub:
// it does not execute commands or enroll itself as a worker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const defaultAddr = "127.0.0.1:17873"

type healthResponse struct {
	Status           string `json:"status"`
	ExecutionEnabled bool   `json:"executionEnabled"`
}

func localhostAddr(raw string) (string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("parse listen address %q: %w", raw, err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("listen address %q must use localhost or a loopback IP", raw)
		}
	}
	if port == "" {
		return "", fmt.Errorf("listen address %q has no port", raw)
	}
	return net.JoinHostPort(host, port), nil
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok", ExecutionEnabled: false})
}

func main() {
	addr := flag.String("addr", os.Getenv("FAROS_MACOS_STUB_ADDR"), "localhost listen address (default 127.0.0.1:17873)")
	flag.Parse()
	if *addr == "" {
		*addr = defaultAddr
	}
	listenAddr, err := localhostAddr(*addr)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", health)
	mux.HandleFunc("/healthz", health)
	server := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	log.Printf("macOS runner health stub listening on http://%s (executionEnabled=false)", listenAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
