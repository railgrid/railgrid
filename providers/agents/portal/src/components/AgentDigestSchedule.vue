<script setup lang="ts">
import { computed, ref } from 'vue'

const props = defineProps<{ modelValue: string; defaultValue: string; disabled: boolean; error?: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const timeMissing = defineModel<boolean>('timeMissing', { required: true })
const weekdays = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday']
const dayNumbers = [1, 2, 3, 4, 5, 6, 0]
const range = (start: number, end: number) => Array.from({ length: end - start + 1 }, (_, index) => start + index)

const schedule = computed(() => {
  const expression = props.modelValue.trim() || props.defaultValue
  const [minute, hour, monthDay, month, days, extra] = expression.trim().split(/\s+/)
  if (extra || monthDay !== '*' || month !== '*' || !/^\d{1,2}$/.test(minute) ||
    !/^\d{1,2}$/.test(hour) || Number(minute) > 59 || Number(hour) > 23 || !days) return null
  const selected = new Set<number>()
  for (const part of days === '*' ? ['0-6'] : days.split(',')) {
    if (!/^[0-6](-[0-6])?$/.test(part)) return null
    const [start, end = start] = part.split('-').map(Number)
    if (end < start) return null
    range(start, end).forEach(day => selected.add(day))
  }
  return { time: `${hour.padStart(2, '0')}:${minute.padStart(2, '0')}`, days: selected }
})
const editingCron = ref(!schedule.value)

function setSchedule(time: string, days: Set<number>): void {
  if (!/^\d{2}:\d{2}$/.test(time)) return
  const [hour, minute] = time.split(':').map(Number)
  const sorted = [...days].sort((a, b) => a - b)
  const cronDays = sorted.length === 7 ? '*' : sorted.join(',')
  emit('update:modelValue', `${minute} ${hour} * * ${cronDays}`)
}

function updateTime(event: Event): void {
  if (!schedule.value) return
  const time = (event.target as HTMLInputElement).value
  timeMissing.value = !time
  if (time) setSchedule(time, schedule.value.days)
}

function updateCron(event: Event): void {
  const expression = (event.target as HTMLInputElement).value
  if (timeMissing.value && !expression.trim()) return
  timeMissing.value = false
  emit('update:modelValue', expression)
}

function toggleDay(day: number): void {
  if (!schedule.value) return
  const selected = new Set(schedule.value.days)
  if (selected.has(day)) {
    if (selected.size === 1) return
    selected.delete(day)
  } else selected.add(day)
  setSchedule(schedule.value.time, selected)
}
</script>

<template>
  <div v-if="editingCron || !schedule" class="agents-schedule-editor">
    <label>
      When to post the digest
      <input id="agent-create-param-digestTime" class="k-input k-cell-mono" :value="timeMissing ? '' : modelValue" :placeholder="defaultValue"
        :disabled="disabled" autocomplete="off" :aria-invalid="!!error || undefined"
        :aria-describedby="error ? 'agent-digest-cron-hint agent-digest-cron-error' : 'agent-digest-cron-hint'"
        @input="updateCron" />
      <span id="agent-digest-cron-hint" class="agents-hint">Five-field cron expression. The template default uses UTC.</span>
      <span v-if="error" id="agent-digest-cron-error" class="agents-fielderr" role="alert">{{ error }}</span>
    </label>
    <button v-if="schedule" type="button" class="k-btn k-btn--text" :disabled="disabled" @click="editingCron = false">Use time and weekdays</button>
  </div>
  <div v-else class="agents-schedule-editor">
    <div class="agents-schedule-controls">
      <label>
        Post at (UTC)
        <input type="time" class="k-input" :value="timeMissing ? '' : schedule.time" :disabled="disabled" required
          :aria-invalid="!!error || undefined" :aria-describedby="error ? 'agent-digest-cron-error' : undefined"
          @input="updateTime" />
        <span v-if="error" id="agent-digest-cron-error" class="agents-fielderr" role="alert">{{ error }}</span>
      </label>
      <fieldset class="agents-template-choice">
        <legend>Repeat on</legend>
        <div class="agents-schedule-days">
          <button v-for="(day, index) in dayNumbers" :key="day" class="k-btn k-btn--ghost" type="button"
            :aria-label="weekdays[index]" :aria-pressed="schedule.days.has(day)"
            :disabled="disabled || timeMissing || (schedule.days.size === 1 && schedule.days.has(day))" @click="toggleDay(day)">
            {{ weekdays[index].slice(0, 2) }}
          </button>
        </div>
      </fieldset>
    </div>
    <button type="button" class="k-btn k-btn--text" :disabled="disabled" @click="editingCron = true">Edit cron</button>
  </div>
</template>
