// This is the generated application's server-side entrypoint used by the E2E.
// The server package manifest installs @faros/actions-node through the exact
// public-artifact npm alias; the app never receives a Databricks URL, PAT, SQL
// text, or provider credential.
import { createActionsClient } from '@faros/actions-node';

const actionsBaseURL = String(process.env.FAROS_ACTIONS_BASE_URL ?? '').trim();
const actionsTokenFile = String(process.env.FAROS_ACTIONS_TOKEN_FILE ?? '').trim();
const project = String(process.env.FAROS_PROJECT ?? '').trim();
const alias = String(process.env.FAROS_ACTION_ALIAS ?? '').trim();
const input = JSON.parse(process.env.FAROS_ACTION_INPUT_JSON ?? '{"limit":2}');
const action = String(process.env.FAROS_ACTION ?? 'query_table/v1').trim();
const headers = process.env.FAROS_ACTION_HEADERS_JSON
  ? JSON.parse(process.env.FAROS_ACTION_HEADERS_JSON)
  : undefined;

const client = createActionsClient({
  baseURL: actionsBaseURL,
  project,
  // Pass the injected workload credential path explicitly. The generated
  // application never receives a caller token or provider credential.
  tokenFile: actionsTokenFile,
  allowInsecureLoopback: true,
  headers,
});
try {
  const result = await client.integration(alias).invoke(action, input);
  // stdout is the generated app's bounded result contract. Keep it free of
  // configuration and credentials so the suite can assert safe app output.
  process.stdout.write(JSON.stringify(result));
} catch (error) {
  // Keep negative-path evidence deterministic without dumping a stack or
  // request configuration into generated-app output.
  const status = Number.isInteger(error?.status) ? error.status : 0;
  console.error(`${error?.name ?? 'Error'}: status=${status}: ${error?.message ?? String(error)}`);
  process.exitCode = 1;
}
