<template>
  <BaseDialog :show="true" :title="t('admin.sites.manualPrice.title')" @close="!saving && emit('close')">
    <form id="site-manual-price-form" class="space-y-4" @submit.prevent="save(false)">
      <div class="text-sm"><p class="font-medium">{{ model.model }}</p><p class="text-gray-500">{{ model.group_name }}</p></div>
      <p class="text-sm text-gray-500">{{ t('admin.sites.manualPrice.hint') }}</p>
      <label class="block text-sm">{{ t('admin.sites.manualPrice.mode') }}
        <select v-model="mode" class="input mt-1" data-testid="manual-price-mode" @change="values = {}">
          <option value="image">{{ t('admin.sites.manualPrice.image') }}</option>
          <option value="per_request">{{ t('admin.sites.manualPrice.request') }}</option>
          <option value="token">{{ t('admin.sites.manualPrice.token') }}</option>
        </select>
      </label>
      <p v-if="mode === 'image'" class="text-xs text-gray-500">{{ t('admin.sites.manualPrice.imageHint') }}</p>
      <div class="grid gap-3 sm:grid-cols-3">
        <label v-for="key in fields" :key="key" class="block text-sm">
          {{ mode === 'image' ? key : t(`admin.sites.components.${key}`) }}
          <input v-model="values[key]" class="input mt-1" type="number" min="0" step="any" :required="mode !== 'image'"
            :data-testid="`manual-price-${key}`" :placeholder="reference(key)" />
        </label>
      </div>
      <details v-if="mode === 'token'" class="text-sm">
        <summary class="cursor-pointer text-gray-500">{{ t('admin.sites.manualPrice.optional') }}</summary>
        <p class="my-2 text-xs text-gray-500">{{ t('admin.sites.manualPrice.tokenHint') }}</p>
        <div class="grid gap-3 sm:grid-cols-2">
          <label v-for="key in optionalFields" :key="key">{{ t(`admin.sites.components.${key}`) }}
            <input v-model="values[key]" class="input mt-1" type="number" min="0" step="any" :data-testid="`manual-price-${key}`" />
          </label>
        </div>
      </details>
      <p v-if="error" class="text-sm text-red-600" role="alert">{{ error }}</p>
    </form>
    <template #footer>
      <button v-if="model.manual_price" class="btn btn-secondary mr-auto" :disabled="saving" @click="save(true)">{{ t('admin.sites.manualPrice.auto') }}</button>
      <button class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button class="btn btn-primary" type="submit" form="site-manual-price-form" :disabled="saving">{{ t('common.save') }}</button>
    </template>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { upstreamSitesApi, type SiteModel, type SiteManualPrice, type UpstreamSite } from '@/api/admin/upstreamSites'
const props = defineProps<{ siteId: string; model: SiteModel }>()
const emit = defineEmits<{ close: []; saved: [site: UpstreamSite] }>()
const { t } = useI18n()
const mode = ref<SiteManualPrice['billing_mode']>(props.model.manual_price?.billing_mode || (props.model.image || props.model.tiers.some(t => t.unit === 'USD/image') ? 'image' : props.model.tiers.some(t => t.unit === 'USD/request') ? 'per_request' : 'token'))
const values = ref<Record<string, number | string>>({ ...props.model.manual_price?.prices })
const fields = computed(() => mode.value === 'image' ? ['1K', '2K', '4K'] : mode.value === 'per_request' ? ['request'] : ['input_price', 'output_price'])
const optionalFields = ['cache_read_price', 'cache_write_price', 'cache_write_1h_price', 'image_input_price', 'image_output_price']
const saving = ref(false)
const error = ref('')
function reference(key: string) {
  const value = mode.value === 'image' ? props.model.tiers.find(t => t.key === key)?.prices.request : props.model.tiers.find(t => t.key === 'default')?.prices[key]
  return value === undefined ? '' : t('admin.sites.manualPrice.reference', { price: value })
}
async function save(automatic: boolean) {
  error.value = ''
  const prices: Record<string, number> = {}
  if (!automatic) {
    for (const key of [...fields.value, ...(mode.value === 'token' ? optionalFields : [])]) {
      const value = values.value[key]
      if (value === '' || value === undefined) continue
      const number = Number(value)
      if (!Number.isFinite(number) || number < 0) { error.value = t('admin.sites.manualPrice.invalid'); return }
      prices[key] = number
    }
    if (!Object.keys(prices).length || (mode.value !== 'image' && fields.value.some(key => prices[key] === undefined))) {
      error.value = t('admin.sites.manualPrice.required'); return
    }
  }
  saving.value = true
  try {
    emit('saved', await upstreamSitesApi.saveModelPrice(props.siteId, {
      group_id: props.model.group_id, model: props.model.model, billing_mode: mode.value, prices, automatic,
    }))
  } catch (e) {
    const response = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
    error.value = response || (e instanceof Error ? e.message : t('admin.sites.failed'))
  } finally { saving.value = false }
}
</script>
