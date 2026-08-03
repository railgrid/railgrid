// Copyright 2026 The Faros Authors.
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

package store

const assistantReviewModeSchemaVersion = "assistant-review-mode-v1"

func assistantRunModeValid(mode AssistantRunMode) bool {
	return mode == AssistantRunModeDefault || mode == AssistantRunModePlan || mode == AssistantRunModeReview
}

// assistantReviewModeSchemaStatements widens only the collaboration-mode
// constraints. It locates those constraints through the mode column so it
// cannot accidentally drop the separate approval_mode checks.
func assistantReviewModeSchemaStatements() []string {
	return []string{
		`DO $$
		DECLARE constraint_name text;
		BEGIN
			FOR constraint_name IN
				SELECT DISTINCT c.conname
				FROM pg_constraint c
				JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
				WHERE c.conrelid = 'app_studio_assistant_runs'::regclass AND c.contype = 'c' AND a.attname = 'mode'
			LOOP
				EXECUTE format('ALTER TABLE app_studio_assistant_runs DROP CONSTRAINT %I', constraint_name);
			END LOOP;
			ALTER TABLE app_studio_assistant_runs ADD CONSTRAINT app_studio_assistant_runs_mode_check
				CHECK (mode IN ('default','plan','review'));
		END $$`,
		`DO $$
		DECLARE constraint_name text;
		BEGIN
			FOR constraint_name IN
				SELECT DISTINCT c.conname
				FROM pg_constraint c
				JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
				WHERE c.conrelid = 'app_studio_assistant_turns'::regclass AND c.contype = 'c' AND a.attname = 'mode'
			LOOP
				EXECUTE format('ALTER TABLE app_studio_assistant_turns DROP CONSTRAINT %I', constraint_name);
			END LOOP;
			ALTER TABLE app_studio_assistant_turns ADD CONSTRAINT app_studio_assistant_turns_mode_check
				CHECK (mode IN ('default','plan','review'));
		END $$`,
	}
}
