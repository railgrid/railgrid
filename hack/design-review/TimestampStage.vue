<script setup>
import ResourceTimestamp from '../../provider-sdk/portalkit-vue/ResourceTimestamp.vue'
import AITimestamp from '../../provider-sdk/agentkit-vue/AITimestamp.vue'

const now = Date.now()
const started = new Date(now - 2 * 60 * 60 * 1000).toISOString()
const recorded = new Date(now - 119 * 60 * 1000).toISOString()
const next = new Date(now + 24 * 60 * 60 * 1000).toISOString()
</script>

<template>
  <div class="spec-stack">
    <p class="spec-note">Anatomy: a relative label, an event-specific accessible name, and exact date and timezone on demand. Hover or focus to reveal it; activate to keep it visible inline.</p>
    <section class="spec-section spec-stack" aria-labelledby="resource-times-title">
      <h3 id="resource-times-title">Resource and run events</h3>
      <div class="spec-row"><span>Run started <ResourceTimestamp :value="started" label="Run started" /></span><span>Step recorded <ResourceTimestamp :value="recorded" label="Step recorded" /></span><span>Scheduled <ResourceTimestamp :value="next" label="Scheduled" /></span></div>
      <div class="spec-row"><span>Missing <ResourceTimestamp :value="null" label="Run finished" /></span><span>Invalid <ResourceTimestamp value="not-a-date" label="Invalid timestamp" /></span></div>
      <p class="spec-note">The resource component shows an accessible unavailable marker for missing and invalid values. Future timestamps are shown here to review formatting, not to define a scheduling interface.</p>
    </section>
    <section class="spec-section spec-stack" aria-labelledby="message-times-title">
      <h3 id="message-times-title">Conversation messages</h3>
      <div class="spec-row"><span>Message <AITimestamp :value="started" aria-label="Message sent" /></span></div>
      <p class="spec-note">The conversation component omits a missing or invalid timestamp; it does not render the resource component’s unavailable marker.</p>
    </section>
    <p class="spec-note">The <a href="http://127.0.0.1:5200/" target="_blank" rel="noopener">Agents run fixture</a> uses sample data and the real run component.</p>
  </div>
</template>
