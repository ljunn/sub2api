<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
const { t } = useI18n()
</script>

<template>
  <div class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
    <label class="flex cursor-pointer items-center gap-2 text-sm font-medium">
      <input
        type="checkbox"
        data-testid="images-transparent-background-supported"
        :checked="modelValue"
        class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
        @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)"
      />
      {{ t('admin.accounts.transparentBackgroundSupport') }}
    </label>
    <p class="input-hint mt-2">{{ t('admin.accounts.transparentBackgroundSupportHint') }}</p>
  </div>
</template>
