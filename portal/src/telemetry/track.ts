// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The one function application code calls to record something that is not a
// click: an outcome (created, failed) or a step in a flow. It has no imports
// so a store can use it without pulling the router and the other stores in.
//
// Labels are FIXED strings chosen by an engineer ("limit", "checkout"). Never
// pass an error message, a name or anything a user typed: docs/telemetry.md.

type Track = (name: string, props?: Record<string, string>) => void

let sink: Track = () => {}

// trackEvent does nothing unless the operator enabled telemetry.
export function trackEvent(name: string, props?: Record<string, string>): void {
  try { sink(name, props) } catch { /* telemetry never affects the app */ }
}

// setTrack is called once by installTelemetry.
export function setTrack(track: Track): void {
  sink = track
}
