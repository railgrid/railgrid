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

// Package dispatch is the runner/v1 attempt lifecycle, written once.
//
// A runner executes attempts, and pkg/runner/client reaches one. Everything
// that happens between a dispatch and its answer — following the attempt to
// the end, telling a park from a finish, reading a harness's stream into text,
// tools and cost, answering a question or a permission prompt, stopping an
// attempt and SEEING it stop — had been written twice, once per product, and
// the two copies drifted. The agents provider learned that a terminal event
// can arrive before the terminal receipt; Factory's copy still believed the
// event. Factory refused a receipt whose session id had changed; the agents
// copy knew a harness forks its session on a resume. Each bug was fixed in one
// place and stayed in the other.
//
// So the rules live here, and the products keep only what is theirs: an
// agent's transcript and inbox, Factory's approved envelope and its delivery.
// This package knows about neither. It knows the protocol.
//
// The rules it holds:
//
//   - The RECEIPT is the authority on how an attempt ended. A terminal event is
//     a reason to go and read it, never a conclusion in itself.
//   - The receipt's session id is authoritative, because a harness may fork its
//     session on a resume. Whatever is chained next is chained onto what the
//     receipt said, never onto what was sent.
//   - A cursor the runner no longer holds is reconciled through Inspect, not
//     failed over: the turn is still working, our view of it is what is stale.
//   - A park is one of two things — a QUESTION answered with words, or a
//     PERMISSION prompt answered with a verdict on a named call — and they
//     resume through different fields that are mutually exclusive on the wire.
//   - A cancel is not done until the runner's receipt says cancelled. Reporting
//     it earlier describes something nobody has observed.
//   - "attempt not found" is the ONE answer that permits a first start. Every
//     other failure to read an attempt may be hiding accepted work.
//
// What this package deliberately does not do: it never persists anything, it
// decides nothing about who may answer a park, and it holds no credential past
// the call it was given for. A caller that needs durability writes what
// Outcome and Position tell it; a caller that needs an answer asks its own
// users. Observer is how the stream reaches a caller as it happens, and every
// method on it is optional.
package dispatch
