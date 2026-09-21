<template>
  <form class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800" @submit.prevent="save">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2 class="font-semibold">{{ t('admin.siteBalance.settings') }}</h2>
      <label class="flex items-center gap-2 text-sm">
        <input v-model="form.enabled" type="checkbox" :disabled="!loaded || saving" />
        {{ t('admin.siteBalance.enabled') }}
      </label>
    </div>
    <p class="mt-2 text-sm text-gray-500">{{ t('admin.siteBalance.hint') }}</p>
    <div class="mt-4 flex flex-wrap items-end gap-4">
      <label class="min-w-52 flex-1 text-sm">
        {{ t('admin.siteBalance.adminEmail') }}
        <input v-model="form.admin_email" type="email" class="input mt-1" :required="form.enabled" :disabled="!loaded || saving" autocomplete="email" />
      </label>
      <label class="text-sm">
        {{ t('admin.siteBalance.threshold') }}
        <input v-model.number="form.threshold" type="number" min="0.000001" max="1000000000000" step="any" required class="input mt-1" :disabled="!loaded || saving" />
      </label>
      <button class="btn btn-primary" type="submit" :disabled="!loaded || saving">{{ t('common.save') }}</button>
    </div>
    <p v-if="message" class="mt-3 text-sm text-red-600" role="alert">{{ message }}</p>
    <p v-if="loaded && !form.smtp_configured" class="mt-3 text-sm text-amber-700">{{ t('admin.siteBalance.smtpMissing') }}</p>
  </form>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { upstreamSiteBalanceApi, type SiteBalanceSettings } from '@/api/admin/upstreamSiteBalance'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const app = useAppStore()
const emit = defineEmits<{ updated: [settings: SiteBalanceSettings] }>()
const loaded = ref(false)
const saving = ref(false)
const message = ref('')
const form = ref<SiteBalanceSettings>({ enabled: false, threshold: 20, admin_email: '', smtp_configured: false })

onMounted(async () => {
  try {
    form.value = await upstreamSiteBalanceApi.settings()
    loaded.value = true
    emit('updated', { ...form.value })
  } catch {
    message.value = t('admin.siteBalance.settingsFailed')
  }
})

async function save() {
  saving.value = true
  message.value = ''
  try {
    form.value = await upstreamSiteBalanceApi.saveSettings({ ...form.value })
    emit('updated', { ...form.value })
    app.showSuccess(t('admin.siteBalance.saved'))
  } catch {
    message.value = t('admin.siteBalance.saveFailed')
  } finally {
    saving.value = false
  }
}
</script>
