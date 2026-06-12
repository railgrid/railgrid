// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package core wires the embedded kuery engine: SQL store, query engine,
// multi-cluster sync controller, and the stale-cluster garbage collector.
// The engagement controller feeds clusters in; the query API and MCP tools
// read out. See docs/kuery-provider-architecture.md (kedge repo).
package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/faroshq/kuery/pkg/engine"
	"github.com/faroshq/kuery/pkg/gc"
	"github.com/faroshq/kuery/pkg/store"
	kuerysync "github.com/faroshq/kuery/pkg/sync"
)

// Config selects the store backend and the resources excluded from sync.
type Config struct {
	// Driver is "sqlite" (default) or "postgres".
	Driver string
	// DSN is the SQLite file path or the PostgreSQL connection string.
	DSN string
	// Blacklist is a comma-separated list of "resource.group" entries
	// excluded from sync (group empty for core resources). Defaults to
	// kuery's own default (secrets, events) when empty.
	Blacklist string
	// Whitelist, when non-empty, restricts sync to exactly these
	// "resource.group" entries (the blacklist still applies on top).
	// Non-whitelisted types stay discoverable in resource_types but sync
	// no objects — edge links are bandwidth-constrained, so the chart
	// defaults this to workloads/config/RBAC/networking.
	Whitelist string
	// GCInterval is how often the stale-cluster GC runs. Default 5m.
	GCInterval time.Duration
}

// Core bundles the embedded kuery components the rest of the provider uses.
type Core struct {
	Store  store.Store
	Engine *engine.Engine
	Sync   *kuerysync.SyncController
	gc     *gc.GarbageCollector
}

// New creates the store (running migrations), engine, and sync controller.
func New(cfg Config) (*Core, error) {
	if cfg.Driver == "" {
		cfg.Driver = "sqlite"
	}
	if cfg.DSN == "" {
		cfg.DSN = "kuery.db"
	}
	if cfg.GCInterval == 0 {
		cfg.GCInterval = 5 * time.Minute
	}

	s, err := store.NewStore(store.Config{Driver: cfg.Driver, DSN: cfg.DSN})
	if err != nil {
		return nil, fmt.Errorf("creating kuery store (%s): %w", cfg.Driver, err)
	}
	if err := s.AutoMigrate(); err != nil {
		return nil, fmt.Errorf("migrating kuery store: %w", err)
	}

	blacklist, err := parseBlacklist(cfg.Blacklist)
	if err != nil {
		return nil, err
	}
	whitelist, err := parseWhitelist(cfg.Whitelist)
	if err != nil {
		return nil, err
	}

	return &Core{
		Store:  s,
		Engine: engine.NewEngine(s),
		Sync: kuerysync.NewSyncController(kuerysync.Config{
			Store:     s,
			Blacklist: blacklist,
			Whitelist: whitelist,
		}),
		gc: gc.NewGarbageCollector(s, cfg.GCInterval),
	}, nil
}

// StartGC runs the stale-cluster garbage collector until ctx is done.
// Blocking; run in a goroutine.
func (c *Core) StartGC(ctx context.Context) {
	c.gc.Run(ctx)
}

// parseBlacklist converts a comma-separated "resource.group" list into
// kuery's Blacklist. Empty input yields kuery's defaults (secrets, events).
func parseBlacklist(raw string) (*kuerysync.Blacklist, error) {
	gvrs, err := parseGVRList(raw)
	if err != nil {
		return nil, err
	}
	if gvrs == nil {
		return kuerysync.NewBlacklist(kuerysync.DefaultBlacklist), nil
	}
	return kuerysync.NewBlacklist(gvrs), nil
}

// parseWhitelist converts a comma-separated "resource.group" list into
// kuery's Whitelist. Empty input yields nil — sync everything watchable.
func parseWhitelist(raw string) (*kuerysync.Whitelist, error) {
	gvrs, err := parseGVRList(raw)
	if err != nil {
		return nil, err
	}
	if gvrs == nil {
		return nil, nil
	}
	return kuerysync.NewWhitelist(gvrs), nil
}

// parseGVRList parses comma-separated "resource" / "resource.group"
// entries. Empty input returns nil.
func parseGVRList(raw string) ([]schema.GroupVersionResource, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var gvrs []schema.GroupVersionResource
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		resource, group, _ := strings.Cut(entry, ".")
		if resource == "" {
			return nil, fmt.Errorf("invalid resource list entry %q", entry)
		}
		gvrs = append(gvrs, schema.GroupVersionResource{Group: group, Resource: resource})
	}
	return gvrs, nil
}
