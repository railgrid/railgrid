<script setup lang="ts">
import { computed } from 'vue'
import aws from '../assets/deployment-targets/aws.svg?inline'
import azure from '../assets/deployment-targets/microsoft-azure.svg?inline'
import googleCloud from '../assets/deployment-targets/google-cloud.svg?inline'
import kubernetes from '../assets/deployment-targets/kubernetes.svg?inline'

const props = defineProps<{ label: string }>()
const marks = new Map([
  ['AWS', aws],
  ['Microsoft Azure', azure],
  ['Google Cloud', googleCloud],
  ['Kubernetes', kubernetes],
])
const mark = computed(() => marks.get(props.label))
</script>

<template>
  <img
    v-if="mark"
    class="deployment-target-mark"
    :class="{ 'deployment-target-mark--aws': label === 'AWS' }"
    :src="mark"
    :alt="label"
    :data-k-tip="label"
    tabindex="0"
  >
  <span v-else>{{ label }}</span>
</template>
