<!-- The form for a HARNESS IDENTITY credential (claude-code, codex).
     
     It is a sibling of agentkit's ModelConnectionForm rather than a mode of it,
     because the two families do not share a field set: a harness identity has
     no endpoint, nothing to probe and no chat model, so base URL, "Find models"
     and "Test connection" are ABSENT here rather than present and inert. The
     classes, ids and sections are the model form's, so it is the same control
     surface with a different set of controls — not a second visual idiom.
     
     No value typed here is ever logged, put in a URL or written to the object's
     spec: it goes to one key of one Secret, and the parent clears the input on
     unmount. -->
<script setup lang="ts">
import { Check, Loader2 } from 'lucide-vue-next'
import {
  HARNESS_SECRET_KEY_API_KEY,
  HARNESS_SECRET_KEY_CODEX_AUTH,
  HARNESS_SECRET_KEY_OAUTH_TOKEN,
  type HarnessSecretKey,
} from '../types'

const props = defineProps<{
  name: string
  nameDisabled?: boolean
  namePattern?: string
  providerPreset: string
  providerOptions: readonly { value: string; label: string }[]
  providerLabelId: string
  providerGuidance: string
  /** Where the value comes from — one line, from the preset table. */
  origin?: string
  /**
   * The Secret keys this provider accepts. Two for claude-code, which is a
   * CHOICE the person makes (the key is the kind declaration); one for codex,
   * which is then stated rather than asked.
   */
  secretKeys: readonly HarnessSecretKey[]
  secretKey: HarnessSecretKey
  secret: string
  secretRequired?: boolean
  secretHint: string
  nameError?: string | null
  secretError?: string | null
  formError?: string | null
  saveDisabled?: boolean
  busy?: boolean
  editing?: boolean
  wide?: boolean
}>()

const emit = defineEmits<{
  save: []
  cancel: []
  'update:name': [value: string]
  'update:provider': [value: string]
  'update:secretKey': [value: HarnessSecretKey]
  'update:secret': [value: string]
}>()

// What each key IS, in the words the harness docs use. The key name is the
// kind, so this table is the label for a kind and not decoration.
const KEY_COPY: Record<HarnessSecretKey, { label: string; blurb: string }> = {
  [HARNESS_SECRET_KEY_OAUTH_TOKEN]: {
    label: 'Setup token',
    blurb: 'A long-lived token from the Claude Code setup-token command, tied to a Claude subscription.',
  },
  [HARNESS_SECRET_KEY_API_KEY]: {
    label: 'Anthropic API key',
    blurb: 'Turns bill to the Anthropic account this key belongs to.',
  },
  [HARNESS_SECRET_KEY_CODEX_AUTH]: {
    label: 'Codex auth.json',
    blurb: 'The login session file created when you run codex login on a machine you control.',
  },
}

const CODEX = HARNESS_SECRET_KEY_CODEX_AUTH

function copyFor(key: HarnessSecretKey) {
  return KEY_COPY[key]
}
function isLocked(): boolean {
  return Boolean(props.busy)
}
</script>

<template>
  <form
    class="k-create-surface k-model-form"
    :class="{ 'k-create-surface--wide': wide }"
    aria-label="Harness identity form"
    :aria-busy="isLocked()"
    novalidate
    @submit.prevent="emit('save')"
  >
    <div class="k-create-body">
      <label for="model-display-name" class="k-model-form-field">
        Name
        <input
          id="model-display-name"
          :value="name"
          class="k-input"
          placeholder="e.g. my-claude"
          :pattern="namePattern"
          name="name"
          aria-label="Name"
          required
          :disabled="isLocked() || nameDisabled"
          aria-required="true"
          :aria-invalid="Boolean(nameError)"
          :aria-describedby="nameError ? 'model-display-name-error' : 'model-display-name-hint'"
          @input="emit('update:name', ($event.target as HTMLInputElement).value)"
        >
        <span v-if="nameError" id="model-display-name-error" class="k-model-form-hint text-danger" role="alert">{{ nameError }}</span>
        <span v-else id="model-display-name-hint" class="k-model-form-hint">Choose a recognizable lowercase name for this identity.</span>
      </label>

      <section class="k-model-form-section" aria-labelledby="model-provider-heading">
        <h5 id="model-provider-heading" class="k-model-form-heading">Harness</h5>
        <label class="k-model-form-field">
          <span :id="providerLabelId">Provider</span>
          <select
            id="model-provider"
            :value="providerPreset"
            class="k-input k-form-select__trigger"
            :disabled="isLocked()"
            :aria-labelledby="providerLabelId"
            :aria-describedby="`${providerLabelId}-hint`"
            required
            @change="emit('update:provider', ($event.target as HTMLSelectElement).value)"
          >
            <option v-for="option in providerOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
          <span :id="`${providerLabelId}-hint`" class="k-model-form-hint">{{ providerGuidance }}</span>
        </label>
      </section>

      <section class="k-model-form-section" aria-labelledby="model-credential-heading">
        <h5 id="model-credential-heading" class="k-model-form-heading">Credential</h5>

        <!-- Two keys is a real choice: claude-code carries EXACTLY ONE of them,
             and which one it carries is what the harness is handed. -->
        <fieldset v-if="secretKeys.length > 1" class="agents-cap-fs">
          <legend id="harness-secret-key-label" class="agents-fieldset-legend">Credential type</legend>
          <div class="agents-radiocards">
            <label
              v-for="key in secretKeys"
              :key="key"
              class="agents-radiocard k-checkbox-hit"
              :class="{ sel: key === secretKey }"
            >
              <input
                type="radio"
                name="harness-secret-key"
                :value="key"
                :checked="key === secretKey"
                :disabled="isLocked()"
                @change="emit('update:secretKey', key)"
              >
              <span class="agents-radiocard-t">{{ copyFor(key).label }}</span>
              <span class="agents-radiocard-b">{{ copyFor(key).blurb }}</span>
            </label>
          </div>
        </fieldset>

        <label for="harness-secret" class="k-model-form-field">
          {{ copyFor(secretKey).label }}
          <textarea
            v-if="secretKey === CODEX"
            id="harness-secret"
            :name="secretKey"
            :aria-label="copyFor(secretKey).label"
            :value="secret"
            :class="['k-input', 'font-mono', 'text-[12px]', { 'border-danger': secretError }]"
            rows="6"
            spellcheck="false"
            autocomplete="off"
            :placeholder="editing ? '{ … }  (leave blank to keep the stored login)' : '{ &quot;OPENAI_API_KEY&quot;: …, &quot;tokens&quot;: { … } }'"
            :required="secretRequired"
            :disabled="isLocked()"
            :aria-required="secretRequired"
            :aria-invalid="Boolean(secretError)"
            :aria-describedby="secretError ? 'harness-secret-error' : 'model-credential-help'"
            @input="emit('update:secret', ($event.target as HTMLTextAreaElement).value)"
          ></textarea>
          <input
            v-else
            id="harness-secret"
            :name="secretKey"
            :aria-label="copyFor(secretKey).label"
            :value="secret"
            :class="['k-input', 'h-10', { 'border-danger': secretError }]"
            type="password"
            autocomplete="new-password"
            :placeholder="editing ? `${copyFor(secretKey).label} (leave blank to keep current)` : copyFor(secretKey).label"
            :required="secretRequired"
            :disabled="isLocked()"
            :aria-required="secretRequired"
            :aria-invalid="Boolean(secretError)"
            :aria-describedby="secretError ? 'harness-secret-error' : 'model-credential-help'"
            @input="emit('update:secret', ($event.target as HTMLInputElement).value)"
          >
          <p v-if="secretError" id="harness-secret-error" class="k-model-form-hint text-danger" role="alert">{{ secretError }}</p>
          <p id="model-credential-help" class="k-model-form-hint">{{ secretHint }}</p>
        </label>
        <p v-if="origin" class="k-model-form-hint">{{ origin }}</p>
      </section>

      <p v-if="formError" class="k-inline-notification k-inline-notification--error" role="alert">{{ formError }}</p>
      <p v-else class="k-inline-notification k-inline-notification--info" role="status" aria-live="polite">
        This login has no endpoint to test. After saving, we check its format. A successful first run confirms that the runner can use it.
      </p>
    </div>

    <footer class="k-create-actions">
      <button type="button" class="k-btn k-btn--ghost" :disabled="isLocked()" @click="emit('cancel')">Cancel</button>
      <button type="submit" class="k-btn k-btn--primary" :disabled="isLocked() || saveDisabled">
        <Loader2 v-if="busy" :size="14" class="animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />
        <Check v-else :size="14" :stroke-width="1.75" aria-hidden="true" />
        {{ busy ? (editing ? 'Saving changes…' : 'Saving identity…') : editing ? 'Save changes' : 'Add identity' }}
      </button>
    </footer>
  </form>
</template>
