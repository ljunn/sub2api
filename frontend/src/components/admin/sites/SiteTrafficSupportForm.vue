<template>
  <fieldset class="space-y-3 rounded-xl border border-primary-200 bg-primary-50/40 p-4 dark:border-primary-900 dark:bg-primary-950/20" :disabled="saving">
    <legend class="px-1 text-sm font-medium">{{ t('admin.sites.support.title') }}</legend>
    <p class="text-xs text-gray-500">{{ t('admin.sites.support.hint') }}</p>
    <label class="flex items-center gap-2 text-sm"><input v-model="form.enabled" data-testid="support-enabled" type="checkbox" />{{ t('admin.sites.support.enabled') }}</label>
    <div class="grid gap-3 sm:grid-cols-2">
      <label class="text-sm">{{ t('admin.sites.support.percent') }}
        <input v-model.number="form.percent" data-testid="support-percent" class="input mt-1" type="number" min="1" max="95" step="1" :disabled="!form.enabled" />
      </label>
      <label class="text-sm">{{ t('admin.sites.support.expires') }}
        <input v-model="expires" data-testid="support-expires" class="input mt-1" type="datetime-local" :disabled="!form.enabled" />
      </label>
    </div>
    <p class="text-xs text-gray-500">{{ t('admin.sites.support.recovery') }}</p>
    <p v-if="error" class="text-sm text-red-600" role="alert">{{ error }}</p>
    <button class="btn btn-secondary" type="button" data-testid="support-save" @click="save">{{ t('admin.sites.support.save') }}</button>
  </fieldset>
</template>
<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { upstreamSitesApi, type SiteTrafficSupport } from '@/api/admin/upstreamSites'
const props = defineProps<{ siteId: string; bindingId: string; config?: SiteTrafficSupport }>()
const emit = defineEmits<{ saved: [config: SiteTrafficSupport] }>()
const { t } = useI18n()
const form = ref<SiteTrafficSupport>({ enabled: false, percent: 20 })
const expires = ref('')
const saving = ref(false)
const error = ref('')
watch(() => [props.bindingId, props.config] as const, () => {
  form.value = { ...(props.config || { enabled: false, percent: 20 }) }
  const date = props.config?.expires_at ? new Date(props.config.expires_at) : null
  expires.value = date ? new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16) : ''
}, { immediate: true })
async function save() {
  error.value = ''
  const percent = Number(form.value.percent)
  const deadline = expires.value ? new Date(expires.value) : null
  if (!Number.isFinite(percent) || percent < 1 || percent > 95 || (form.value.enabled && deadline && (!Number.isFinite(deadline.getTime()) || deadline.getTime() <= Date.now()))) {
    error.value = t('admin.sites.support.invalid'); return
  }
  saving.value = true
  try {
    const config = await upstreamSitesApi.saveTrafficSupport(props.siteId, props.bindingId, { enabled: form.value.enabled, percent, expires_at: deadline?.toISOString() })
    emit('saved', config)
  } catch (e: unknown) {
    error.value = (e as { response?: { data?: { message?: string } } })?.response?.data?.message || t('admin.sites.support.failed')
  } finally { saving.value = false }
}
</script>
