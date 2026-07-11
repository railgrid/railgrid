/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package template

// retiredTemplates names platform templates that have been RETIRED: removed
// from the seed catalog and no longer supported by the platform. Seeding only
// upserts the CURRENT catalog, so a live workspace initialized before a
// retirement keeps serving the old Template forever — visible in the portal,
// selectable by agents, backed by code paths that no longer exist. The
// reconciler deletes any Template on this list on sight; the normal finalize
// chain (backend teardown → APIExport entry removal → per-template CRD
// deletion) then dismantles what it authored, exactly as if an operator had
// deleted it by hand. Because this runs in the watch loop, a re-applied
// retired template is removed again — retirement is enforced, not one-shot.
//
// The value is the reason, logged when the deletion happens. Names on this
// list are reserved: an operator's hand-authored Template with a retired name
// will be deleted too, so never reuse a retired name for something new.
//
// Operators who fully manage their own catalog (INFRASTRUCTURE_SKIP_SEED_TEMPLATES)
// still get retirement — the list names platform templates only, which such
// catalogs should not carry.
var retiredTemplates = map[string]string{
	// The dedicated sandbox workload was replaced by template-native
	// development mode: any workload template with a spec.development block
	// (docs/app-studio-template-sandboxes.md). App Studio refuses projects
	// without a template since #394, so instances of this can no longer work.
	"sandbox-runner": "replaced by template-native development mode (spec.development on workload templates)",

	// Preview routing is declared per-template (each dev-capable template's
	// own HTTPRoute serves the preview) since the Gateway API exposure work;
	// the standalone preview-route template has no consumer.
	"sandbox-preview-httproute": "preview routing is declared by each template's own HTTPRoute",
}
