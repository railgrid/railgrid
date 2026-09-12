// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/faroshq/provider-linear/internal/authority"
	"github.com/faroshq/provider-linear/internal/server"
	"github.com/faroshq/provider-sdk/hubclient"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "init" {
		if err := runInitCmd(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := serve(ctx); err != nil {
		log.Fatal(err)
	}
}
func serve(ctx context.Context) error {
	path := os.Getenv("FAROS_PROVIDER_KUBECONFIG")
	if path == "" {
		return errors.New("FAROS_PROVIDER_KUBECONFIG must point to the runtime provider identity")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return err
	}
	cfg.Timeout = 20 * time.Second
	auth := authority.Authority{Config: cfg}
	controller := &authority.Controller{Authority: auth}
	go controller.Run(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !controller.Ready.Load() {
			http.Error(w, "provider API reconciliation unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	api := server.Server{Authority: auth, HubURL: os.Getenv("FAROS_HUB_URL"), Insecure: os.Getenv("FAROS_HUB_INSECURE") == "true"}
	api.Routes(mux)
	mux.Handle("/mcp", http.MaxBytesHandler(api.MCP(), 32768))
	files, _, err := portalHandler()
	if err != nil {
		return err
	}
	mux.Handle("/", files)
	hb, err := hubclient.ConfigFromEnv("linear", "0.1.0")
	if err != nil {
		return err
	}
	hb.CanSend = controller.Ready.Load
	go hubclient.RunHeartbeat(ctx, hb)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8092"
	}
	httpServer := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- httpServer.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	return nil
}
