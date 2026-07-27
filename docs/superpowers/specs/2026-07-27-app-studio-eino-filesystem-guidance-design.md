# App Studio Eino Filesystem Guidance Design

## Goal

Restore Eino v0.9.9's detailed filesystem-tool guidance for App Studio while
preserving App Studio's project-relative, read-only filesystem boundary.

## Decision

Keep the existing Eino filesystem middleware and scoped backend. Replace the
short custom descriptions for `ls`, `read_file`, `glob`, and `grep` with
adaptations of Eino's defaults that:

- require project-relative paths instead of absolute host paths;
- retain Eino's search-first exploration guidance;
- document `read_file`'s 2,000-line default and one-based offset/limit
  pagination;
- encourage batching independent reads in one model response;
- tell the model to read existing files before proposing or applying edits; and
- continue to describe the tools as read-only and limited to the current App
  Studio project.

Eino's `write_file` and `edit_file` tools remain disabled. App Studio's
approval-aware mutation tools and phase lifecycle are unchanged.

## Alternatives

Using Eino's descriptions unchanged was rejected because they claim access to
absolute paths and any file on the machine, which is false for App Studio.
Keeping the current short descriptions was rejected because it removes the
reference implementation's practical guidance and encourages unnecessary
small, repeated reads.

## Verification

A focused middleware test will assert that the exposed `read_file` description
preserves the project-relative boundary and includes Eino's default limit,
pagination, search-first, batching, and read-before-edit guidance. Existing
inventory tests will continue to prove that absolute-host claims and Eino
mutation tools are absent.

## Non-goals

- Adding duplicate-read detection or read-coverage tracking
- Changing phase transitions or iteration limits
- Enabling shell execution, Eino mutations, or general subagents
- Changing the scoped filesystem backend
