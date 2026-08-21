# Faros telemetry catalog

This file is generated from platform/provider event roots, central metrics, and the checked-in JSON Schemas.

## Events

| Action | Owner | Source | Product group | Categories | Lifecycle | Tiers | Identifiers | Privacy | Retention | Properties |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | ---: | --- |
| `agents_agent_created` | agents | `providers/agents/telemetry/events/agents_agent_created.yaml` | agents | agents | active @ 0.1 | free, premium, ultimate | org, workspace, actor, resource | pseudonymous; pseudonymize=org,workspace,actor,resource; no_raw_content | 90 | outcome (string)=success |
| `agents_run_terminal` | agents | `providers/agents/telemetry/events/agents_run_terminal.yaml` | agents | runs | active @ 0.1 | free, premium, ultimate | org, workspace, resource, run | pseudonymous; pseudonymize=org,workspace,resource,run; no_raw_content | 90 | outcome (string)=succeeded,failed,aborted |
| `app_studio_preview_ready` | app-studio | `providers/app-studio/telemetry/events/app_studio_preview_ready.yaml` | app_studio | preview | active @ 0.1 | free, premium, ultimate | org, workspace, project | pseudonymous; pseudonymize=org,workspace,project; no_raw_content | 90 | outcome (string)=ready, preview_kind (string)=development |
| `app_studio_project_created` | app-studio | `providers/app-studio/telemetry/events/app_studio_project_created.yaml` | app_studio | projects | active @ 0.1 | free, premium, ultimate | org, workspace, project, actor | pseudonymous; pseudonymize=org,workspace,project,actor; no_raw_content | 90 | outcome (string)=success |
| `app_studio_project_published` | app-studio | `providers/app-studio/telemetry/events/app_studio_project_published.yaml` | app_studio | publishing | active @ 0.1 | free, premium, ultimate | org, workspace, project, actor | pseudonymous; pseudonymize=org,workspace,project,actor; no_raw_content | 90 | outcome (string)=published,promoted |
| `edge_first_ready` | edges | `providers/edges/telemetry/events/edge_first_ready.yaml` | edges | connectivity | active @ 0.1 | free, premium, ultimate | scope, resource | pseudonymous; pseudonymize=scope,resource; no_raw_content | 90 | edge_type (string)=kubernetes_cluster,linux_server, outcome (string)=ready |
| `organization_created` | platform | `telemetry/events/platform/organization_created.yaml` | platform | tenancy | active @ 0.1 | free, premium, ultimate | org, actor | pseudonymous; pseudonymize=org,actor; no_raw_content | 90 | outcome (string)=success |
| `provider_enabled` | platform | `telemetry/events/platform/provider_enabled.yaml` | platform | providers | active @ 0.1 | free, premium, ultimate | org, workspace, actor, resource | pseudonymous; pseudonymize=org,workspace,actor,resource; no_raw_content | 90 | outcome (string)=success, provider (string)=agents,app-studio,code,databricks,edges,infrastructure,kuery,quickstart,vibe-studio |
| `workspace_created` | platform | `telemetry/events/platform/workspace_created.yaml` | platform | tenancy | active @ 0.1 | free, premium, ultimate | org, workspace, actor | pseudonymous; pseudonymize=org,workspace,actor; no_raw_content | 90 | outcome (string)=success |

## Metrics and funnels

| Key path | Kind | Owner | Source | Description | Events | Labels |
| --- | --- | --- | --- | --- | --- | --- |
| `activation_funnel` | funnel | platform | `telemetry/metrics/activation_funnel.yaml` | Independently workspace-deduplicated 28-day workspace creation and provider enablement volumes; not a cohort-conversion funnel. | workspace_created [unique workspace] {outcome=success}; provider_enabled [unique workspace] {outcome=success} |  |
| `agents_activation_funnel` | funnel | agents | `telemetry/metrics/agents_activation_funnel.yaml` | Independently deduplicated 28-day agent creation and successful-run stage volumes for the same resource identity; not an ordered cohort funnel. | agents_agent_created [unique resource] {outcome=success}; agents_run_terminal [unique resource] {outcome=succeeded} |  |
| `agents_agent_created_total` | counter | agents | `telemetry/metrics/agents_agent_created_total.yaml` | Total Agents provider agent creation outcomes. | agents_agent_created [unique resource] | outcome=success |
| `agents_run_terminal_total` | counter | agents | `telemetry/metrics/agents_run_terminal_total.yaml` | Total distinct terminal run outcomes in the Agents provider within each retained daily bucket. | agents_run_terminal [unique run] | outcome=succeeded,failed,aborted |
| `app_studio_activation_funnel` | funnel | app-studio | `telemetry/metrics/app_studio_activation_funnel.yaml` | Independently deduplicated 28-day project creation, preview-ready, and initial-publication stage volumes; re-promotions and runtime readiness are excluded, and this is not an ordered cohort funnel. | app_studio_project_created [unique project] {outcome=success}; app_studio_preview_ready [unique project] {outcome=ready}; app_studio_project_published [unique project] {outcome=published} |  |
| `app_studio_preview_ready_total` | counter | app-studio | `telemetry/metrics/app_studio_preview_ready_total.yaml` | Total App Studio projects whose preview became ready. | app_studio_preview_ready [unique project] | outcome=ready; preview_kind=development |
| `app_studio_project_created_total` | counter | app-studio | `telemetry/metrics/app_studio_project_created_total.yaml` | Total App Studio project creation outcomes. | app_studio_project_created [unique project] | outcome=success |
| `app_studio_project_published_total` | counter | app-studio | `telemetry/metrics/app_studio_project_published_total.yaml` | Total durably accepted App Studio production-binding publication and re-promotion requests; not runtime readiness. | app_studio_project_published [unique project] | outcome=published,promoted |
| `edge_first_ready_total` | counter | edges | `telemetry/metrics/edge_first_ready_total.yaml` | Total opaque provider tenant scopes whose first edge reached readiness. | edge_first_ready [unique scope] | edge_type=kubernetes_cluster,linux_server; outcome=ready |
| `organization_created_total` | counter | platform | `telemetry/metrics/organization_created_total.yaml` | Total organization creation outcomes. | organization_created [unique org] | outcome=success |
| `provider_enabled_total` | counter | platform | `telemetry/metrics/provider_enabled_total.yaml` | Total provider enablement outcomes. | provider_enabled [unique workspace] | outcome=success; provider=agents,app-studio,code,databricks,edges,infrastructure,kuery,quickstart,vibe-studio |
| `workspace_created_total` | counter | platform | `telemetry/metrics/workspace_created_total.yaml` | Total workspace creation outcomes. | workspace_created [unique workspace] | outcome=success |
