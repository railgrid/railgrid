<script setup lang="ts">
import { ref, computed } from 'vue'
import { ArrowLeft, Save, KeyRound } from 'lucide-vue-next'
import { updateEdgeService, connectEdgeService } from './api'
import type { CatalogEntry, CatalogCredentialField, EdgeServiceEdit } from './api'
import type { EdgeService, Edge, ErrorResponse } from './types'

// A dedicated per-service page: provider info (from the catalog) + editable
// configuration + credentials + status. Reached from the Services list via the
// Edit button; emits back/saved so the list can close it and refresh.
const props = defineProps<{ service: EdgeService; catalog: CatalogEntry[]; edges: Edge[] }>()
const emit = defineEmits<{ back: []; saved: [] }>()

const busy = ref(false)
const error = ref<string | null>(null)

// Editable config, seeded from the service.
const form = ref<EdgeServiceEdit>({
  serviceType: props.service.serviceType,
  scheme: props.service.scheme || 'http',
  port: props.service.port,
  host: props.service.host ?? '',
  targetNamespace: props.service.targetNamespace ?? '',
  targetName: props.service.targetName ?? '',
})
const instructions = ref(props.service.instructions ?? '')
const targetMode = ref<'host' | 'kube'>(props.service.host ? 'host' : props.service.targetName ? 'kube' : 'host')

// The catalog entry for the currently-selected type drives the info panel,
// scheme lock, host hints and credential fields.
const entry = computed(() => props.catalog.find((c) => c.type === form.value.serviceType))
const schemeLocked = computed(() => !!entry.value?.schemeLocked)
const edgeIsServer = computed(() => props.edges.find((e) => e.name === props.service.edgeName)?.type === 'server')

function onTypeChange() {
  const c = entry.value
  if (!c) return
  if (c.defaultPort) form.value.port = c.defaultPort
  if (c.defaultScheme) form.value.scheme = c.defaultScheme
  if (c.hostRequired) targetMode.value = 'host'
}

// Human labels for the auth badge.
const AUTH_LABELS: Record<string, string> = {
  bearer: 'Bearer token',
  apiKeyHeader: 'API key (header)',
  apiKeyQuery: 'API key (query)',
  basic: 'Basic auth',
  proxmox: 'Proxmox API token',
  pihole: 'Session login',
  qbittorrent: 'Session login',
  none: 'No auth',
}
function authLabel(a?: string): string {
  return AUTH_LABELS[a ?? ''] ?? a ?? '—'
}

async function onSaveConfig() {
  busy.value = true
  error.value = null
  try {
    const byHost = targetMode.value === 'host'
    await updateEdgeService(props.service.name, {
      serviceType: form.value.serviceType,
      scheme: form.value.scheme,
      port: Number(form.value.port) || props.service.port,
      host: byHost ? form.value.host?.trim() || undefined : undefined,
      targetNamespace: form.value.targetNamespace,
      targetName: byHost ? '' : form.value.targetName,
      instructions: instructions.value,
    })
    emit('saved')
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Save failed'
  } finally {
    busy.value = false
  }
}

// ── Credentials ────────────────────────────────────────────────────
// Inputs keyed by field.key; packed into the single Secret "token" value per the
// type's packing (mirrors Services.vue's create-form logic).
const credInputs = ref<Record<string, string>>({})
const credFields = computed<CatalogCredentialField[]>(
  () => entry.value?.credential.fields ?? [{ key: 'token', label: 'token', secret: true }],
)
function packedCredential(): string {
  const cred = entry.value?.credential
  if (cred?.packing === 'userpass') {
    const u = (credInputs.value['username'] ?? '').trim()
    const p = credInputs.value['password'] ?? ''
    return `${u}:${p}`
  }
  const key = credFields.value[0]?.key ?? 'token'
  return (credInputs.value[key] ?? '').trim()
}
const credFilled = computed(() => {
  const cred = entry.value?.credential
  if (cred?.packing === 'userpass') return !!(credInputs.value['username']?.trim() && credInputs.value['password'])
  return !!packedCredential()
})
async function onSaveCreds() {
  const token = packedCredential()
  if (!token) return
  busy.value = true
  error.value = null
  try {
    await connectEdgeService(props.service.name, token)
    credInputs.value = {}
    emit('saved')
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Connect failed'
  } finally {
    busy.value = false
  }
}

function statusClass(v?: string): string {
  return v === 'True' ? 'ok' : v === 'False' ? 'down' : 'pending'
}
function phaseClass(p?: string): string {
  return p === 'Ready' ? 'ok' : p === 'Unreachable' ? 'down' : 'pending'
}
function age(ts?: string): string {
  if (!ts) return ''
  const secs = Math.max(0, Math.floor((Date.now() - new Date(ts).getTime()) / 1000))
  if (secs < 60) return `${secs}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`
  return `${Math.floor(secs / 86400)}d`
}
</script>

<template>
  <div class="edges-app">
    <header class="edges-header">
      <div style="display: flex; align-items: center; gap: 10px;">
        <button class="icon" title="Back to services" @click="emit('back')"><ArrowLeft :size="16" /></button>
        <div>
          <h1>{{ service.name }} <span class="status" :class="phaseClass(service.phase)">{{ service.phase || 'Pending' }}</span></h1>
          <p>{{ entry?.displayName ?? service.serviceType }}<span v-if="entry?.description"> — {{ entry.description }}</span></p>
        </div>
      </div>
    </header>

    <div v-if="error" class="banner error">{{ error }}</div>

    <!-- Provider info (from the catalog) -->
    <div class="wiz-card" style="margin-bottom: 16px;">
      <div class="es-head">Provider info</div>
      <div class="row" style="gap: 28px; flex-wrap: wrap;">
        <div>
          <span class="lbl">Auth</span>
          <div><span class="pill">{{ authLabel(entry?.auth) }}</span></div>
        </div>
        <div>
          <span class="lbl">Default port</span>
          <div class="mono">{{ entry?.defaultPort ?? '—' }}</div>
        </div>
        <div>
          <span class="lbl">Scheme</span>
          <div class="mono">{{ entry?.defaultScheme ?? 'http' }}{{ entry?.schemeLocked ? ' (fixed)' : '' }}</div>
        </div>
        <div>
          <span class="lbl">Reached via</span>
          <div class="mono">{{ entry?.hostRequired ? 'LAN host (required)' : 'Agent loopback' }}</div>
        </div>
      </div>
      <div v-if="entry?.tools?.length" style="margin-top: 14px;">
        <span class="lbl">Exposed AI tools</span>
        <ul style="margin: 6px 0 0; padding-left: 18px;">
          <li v-for="t in entry.tools" :key="t.name" style="margin: 2px 0;">
            <span class="mono">{{ t.name }}</span><span v-if="t.description" class="muted"> — {{ t.description }}</span>
          </li>
        </ul>
      </div>
      <div v-else class="muted" style="margin-top: 8px; font-size: 12px;">Proxy-only — this service exposes no AI tools.</div>
    </div>

    <!-- Configuration -->
    <div class="wiz-card" style="margin-bottom: 16px;">
      <div class="es-head">Configuration</div>
      <div class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Type</span>
          <select v-model="form.serviceType" class="input" @change="onTypeChange">
            <option v-for="c in catalog" :key="c.type" :value="c.type">{{ c.displayName }}</option>
          </select>
        </label>
        <label class="fld" style="flex: 0 0 120px;">
          <span class="lbl">Scheme</span>
          <select v-model="form.scheme" class="input" :disabled="schemeLocked" :title="schemeLocked ? 'Fixed by the service type' : ''">
            <option value="http">http</option>
            <option value="https">https</option>
          </select>
        </label>
        <label class="fld" style="flex: 0 0 120px;">
          <span class="lbl">Port</span>
          <input v-model="form.port" type="number" min="1" max="65535" class="input" />
        </label>
      </div>
      <div class="row" style="gap: 16px; margin: 6px 0;">
        <label style="display: flex; align-items: center; gap: 6px; cursor: pointer;">
          <input type="radio" value="host" v-model="targetMode" /> Host / IP
        </label>
        <label style="display: flex; align-items: center; gap: 6px; cursor: pointer;" :style="{ opacity: edgeIsServer ? 0.5 : 1 }">
          <input type="radio" value="kube" v-model="targetMode" :disabled="edgeIsServer" /> Kubernetes Service
        </label>
      </div>
      <div v-if="targetMode === 'host'" class="row" style="gap: 12px;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Host {{ entry?.hostRequired ? '(required)' : '(blank = agent loopback)' }}</span>
          <input v-model="form.host" class="input" placeholder="192.168.1.1, myui.example.com" />
          <span v-if="entry?.hostHelp" class="muted" style="font-size: 12px; margin-top: 4px;">{{ entry.hostHelp }}</span>
        </label>
      </div>
      <div v-else class="row" style="gap: 12px;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Target namespace</span>
          <input v-model="form.targetNamespace" class="input" placeholder="home" />
        </label>
        <label class="fld" style="flex: 1;">
          <span class="lbl">Target service name</span>
          <input v-model="form.targetName" class="input" placeholder="home-assistant" />
        </label>
      </div>
      <label class="fld" style="margin-top: 8px;">
        <span class="lbl">AI instructions (optional)</span>
        <textarea v-model="instructions" class="input" rows="3" placeholder="Describe this service's entities/rooms so the AI knows your setup."></textarea>
      </label>
      <div class="wiz-actions">
        <button class="btn primary" :disabled="busy" @click="onSaveConfig"><Save :size="14" /> Save configuration</button>
      </div>
    </div>

    <!-- Credentials -->
    <div class="wiz-card" style="margin-bottom: 16px;">
      <div class="es-head">Credentials</div>
      <div class="muted" style="margin-bottom: 8px;">{{ entry?.credential.hint ?? 'Credential' }} — makes the service Ready. Stored as a Secret, never on the agent host.</div>
      <div class="row" style="gap: 8px; align-items: flex-end;">
        <label v-for="f in credFields" :key="f.key" class="fld" style="flex: 1;">
          <span class="lbl">{{ f.label }}</span>
          <input v-model="credInputs[f.key]" :type="f.secret ? 'password' : 'text'" class="input" :placeholder="f.label" />
          <span v-if="f.help" class="muted" style="font-size: 12px; margin-top: 4px;">{{ f.help }}</span>
        </label>
        <button class="btn" :disabled="busy || !credFilled" @click="onSaveCreds"><KeyRound :size="14" /> {{ service.hasCredentials ? 'Update' : 'Set' }} credentials</button>
      </div>
    </div>

    <!-- Status -->
    <div class="wiz-card">
      <div class="es-head">Status <span class="status" :class="phaseClass(service.phase)">{{ service.phase || 'Pending' }}</span></div>
      <div v-if="service.url" class="muted mono" style="margin-bottom: 8px; font-size: 12px;">{{ service.url }}</div>
      <table v-if="service.conditions.length" class="edges-table" style="font-size: 12px;">
        <thead><tr><th>Condition</th><th>Status</th><th>Reason</th><th>Message</th><th>Age</th></tr></thead>
        <tbody>
          <tr v-for="c in service.conditions" :key="c.type">
            <td class="name">{{ c.type }}</td>
            <td><span class="status" :class="statusClass(c.status)">{{ c.status }}</span></td>
            <td class="mono muted">{{ c.reason || '—' }}</td>
            <td class="muted">{{ c.message || '—' }}</td>
            <td class="mono muted">{{ age(c.lastTransitionTime) }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="muted">No conditions reported yet.</div>
    </div>
  </div>
</template>
