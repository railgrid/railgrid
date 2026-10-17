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

package leaderelection

import "sigs.k8s.io/controller-runtime/pkg/config"

// ControllerOptions permits rebuilding controllers across sequential leadership
// terms. controller-runtime's default name registry is process-global and never
// forgets stopped controllers. Use only for managers owned by Run's callback:
// Run waits for the previous callback to finish before starting the next term.
// Callers must keep controller names distinct within each term. Skipping
// validation disables that check even within one manager; duplicates would
// silently share metrics and log labels.
func ControllerOptions() config.Controller {
	skip := true
	return config.Controller{SkipNameValidation: &skip}
}
