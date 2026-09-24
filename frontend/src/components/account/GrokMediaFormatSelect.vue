<template>
  <div class="mt-3">
    <label class="input-label">{{ t('admin.accounts.grok.mediaFormat') }}</label>
    <select :value="modelValue" class="input" data-testid="grok-media-format" @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)">
      <option value="auto">{{ t('admin.accounts.grok.mediaFormatAuto') }}</option>
      <option value="xai">{{ t('admin.accounts.grok.mediaFormatXAI') }}</option>
      <option value="openai">{{ t('admin.accounts.grok.mediaFormatOpenAI') }}</option>
    </select>
    <p class="input-hint">{{ t('admin.accounts.grok.mediaFormatHint') }}</p>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
defineProps<{ modelValue: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
</script>
