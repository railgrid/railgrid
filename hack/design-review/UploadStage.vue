<script setup>
import { ref } from 'vue'
import { Upload } from 'lucide-vue-next'
import AttachmentProductStage from './AttachmentProductStage.vue'

const input = ref(null)
const filename = ref('')
const error = ref('')
const dragging = ref(false)

function select(file) {
  if (!file) return
  filename.value = file.name
  error.value = !file.name.toLowerCase().endsWith('.txt')
    ? 'Choose a .txt file.'
    : file.size === 0 ? 'This file is empty. Choose a .txt file with content.' : ''
}

function drop(event) {
  dragging.value = false
  select(event.dataTransfer.files[0])
}
</script>

<template>
  <div class="spec-stack">
    <section class="spec-section spec-stack">
      <h3>Upload a text file</h3>
      <input ref="input" type="file" accept=".txt" hidden @change="select($event.target.files[0]); $event.target.value = ''">
      <button class="k-dropzone" :class="{ 'is-dragover': dragging, 'is-error': error }" type="button" aria-describedby="text-upload-help text-upload-result" @click="input.click()" @dragover.prevent="dragging = true" @dragleave="dragging = false" @drop.prevent="drop">
        <Upload :size="18" aria-hidden="true" /> <strong>Choose a file</strong> or drag it here
      </button>
      <p class="spec-note" id="text-upload-help">Accepted: nonempty .txt files. This example checks the name and size.</p>
      <p class="spec-note" id="text-upload-result" role="status">{{ error || (filename ? filename + ' selected' : '') }}</p>
    </section>
    <AttachmentProductStage />
  </div>
</template>
