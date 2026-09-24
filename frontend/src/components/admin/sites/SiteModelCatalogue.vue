<template>
  <div class="min-w-0 space-y-3 p-4 sm:p-5">
    <div class="flex flex-wrap items-center gap-3">
      <input v-model="search" class="input min-w-0 basis-full sm:flex-1 sm:basis-auto" :placeholder="t('admin.sites.searchModels')" :aria-label="t('admin.sites.searchModels')" />
      <label class="flex items-center gap-2 whitespace-nowrap text-sm"><input v-model="onlyNew" type="checkbox" />{{ t('admin.sites.discovery.onlyNew') }}</label>
    </div>
    <p class="text-xs text-gray-500">{{ t('admin.sites.discovery.hint') }}</p>
    <p v-if="readError" class="text-sm text-amber-700" role="alert">{{ t('admin.sites.discovery.readFailed') }} <button class="underline" @click="flushRead">{{ t('admin.sites.discovery.retry') }}</button></p>
    <div ref="list" class="rounded-lg border border-gray-100 dark:border-dark-700 lg:max-h-[520px] lg:overflow-auto">
      <div v-for="model in filteredModels" :key="modelKey(model)" :data-discovery-id="model.discovery_id || ''" data-testid="catalogue-model"
        class="flex min-w-0 flex-col gap-3 border-b border-gray-100 px-3 py-4 last:border-0 dark:border-dark-700 sm:px-4 lg:flex-row lg:items-center lg:justify-between lg:gap-4 lg:py-3">
        <div class="min-w-0">
          <div class="flex items-start gap-2 text-sm font-medium">
            <span class="min-w-0 [overflow-wrap:anywhere] lg:truncate" :title="model.model">{{ model.model }}</span>
            <span v-if="visitDiscoveries.has(model.discovery_id || '')" class="shrink-0 rounded bg-blue-50 px-1.5 text-xs text-blue-700 dark:bg-blue-900/30 dark:text-blue-300">{{ t('admin.sites.discovery.new') }}</span>
          </div>
          <div class="mt-1 text-xs text-gray-500 [overflow-wrap:anywhere] lg:truncate" :title="model.group_name">{{ model.group_name }} · {{ model.platform }}</div>
          <div class="mt-1 flex flex-wrap gap-x-3 text-xs text-gray-500">
            <span v-for="tier in model.tiers" :key="tier.key" class="min-w-0 [overflow-wrap:anywhere]" :title="tier.note || tier.reason">{{ tier.key }} {{ prices(tier) }}</span>
          </div>
          <p v-for="note in notes(model)" :key="note"
            class="mt-1 text-xs text-gray-500 [overflow-wrap:anywhere]">{{ note }}</p>
          <p v-if="model.reason" class="mt-1 text-xs text-amber-700">{{ model.reason }}</p>
        </div>
        <div class="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-t border-gray-100 pt-2 text-xs dark:border-dark-700 lg:justify-end lg:border-0 lg:pt-0">
          <button v-if="!model.longxia && model.vividai?.kind !== 'video'" class="min-h-11 text-primary-600 lg:min-h-0" :disabled="busy" data-testid="edit-model-price" @click="$emit('price', model)">{{ t(model.manual_price ? 'admin.sites.manualPrice.edit' : 'admin.sites.manualPrice.fill') }}</button>
          <span v-if="isBound(model)" class="text-gray-500">{{ t('admin.sites.discovery.bound') }}</span>
          <button class="min-h-11 text-primary-600 lg:min-h-0" :disabled="busy" data-testid="bind-model" @click="$emit('bind', model)">{{ t('admin.sites.bind') }}</button>
        </div>
      </div>
      <p v-if="!filteredModels.length" class="p-6 text-center text-sm text-gray-500">{{ t('admin.sites.overview.noResults') }}</p>
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { upstreamSitesApi, type UpstreamSite, type SiteModel, type SitePriceTier } from '@/api/admin/upstreamSites'
import { sitePriceComponent } from './sitePriceFormat'
const props = defineProps<{ site: UpstreamSite; busy: boolean; initialOnlyNew: boolean }>()
const emit = defineEmits<{ price: [model: SiteModel]; bind: [model: SiteModel]; read: [siteId: string, ids: string[]] }>()
const { t } = useI18n()
const search = ref('')
const onlyNew = ref(props.initialOnlyNew)
watch(() => props.initialOnlyNew, value => { onlyNew.value = value })
const list = ref<HTMLElement>()
const visitDiscoveries = ref(new Set<string>())
const readError = ref(false)
const pending = new Set<string>()
const observed = new Set<string>()
let observer: IntersectionObserver | undefined
let sending = false
let disposed = false
const siteId = props.site.id
const modelKey = (model: SiteModel) => JSON.stringify([model.group_id, model.model])
watch(() => props.site.models, models => {
  for (const model of models) if (model.unread && model.discovery_id) visitDiscoveries.value.add(model.discovery_id)
}, { immediate: true })
const filteredModels = computed(() => props.site.models.filter(model =>
  (!onlyNew.value || visitDiscoveries.value.has(model.discovery_id || '')) &&
  `${model.model} ${model.group_name}`.toLowerCase().includes(search.value.trim().toLowerCase()),
).slice().sort((a, b) => Number(visitDiscoveries.value.has(b.discovery_id || '')) - Number(visitDiscoveries.value.has(a.discovery_id || ''))))
const isBound = (model: SiteModel) => props.site.bindings.some(binding => binding.group_id === model.group_id && binding.model === model.model)
const prices = (tier: SitePriceTier) => Object.entries(tier.prices).map(([key, value]) => `${t(`admin.sites.components.${sitePriceComponent(key, tier.unit)}`)} $${Number(value.toPrecision(6))}`).join(' / ') || '—'
const notes = (model: SiteModel) => [...new Set(model.tiers.flatMap(tier => tier.note ? [tier.note] : []))]

async function flushRead() {
  if (sending || !pending.size) return
  sending = true
  readError.value = false
  try {
    while (pending.size) {
      const ids = [...pending].slice(0, 100)
      const result = await upstreamSitesApi.markModelsRead(siteId, ids)
      for (const id of ids) pending.delete(id)
      emit('read', siteId, result.discovery_ids)
    }
  } catch {
    readError.value = true
  } finally {
    sending = false
  }
}
function observeRows() {
  if (disposed || !observer) return
  observer.disconnect()
  for (const row of list.value?.querySelectorAll<HTMLElement>('[data-discovery-id]') || []) {
    const id = row.dataset.discoveryId!
    if (id && !observed.has(id) && props.site.models.some(model => model.discovery_id === id && model.unread)) observer.observe(row)
  }
}
onMounted(() => {
  observer = new IntersectionObserver(entries => {
    // Background tabs and rows below the scroll viewport are never marked read.
    if (document.visibilityState !== 'visible') return
    for (const entry of entries) {
      if (!entry.isIntersecting || entry.intersectionRatio < 0.5) continue
      const id = (entry.target as HTMLElement).dataset.discoveryId!
      observed.add(id)
      pending.add(id)
      observer?.unobserve(entry.target)
    }
    if (!readError.value) void flushRead()
  }, { threshold: 0.5 })
  observeRows()
  document.addEventListener('visibilitychange', observeRows)
})
watch(filteredModels, () => { void nextTick(observeRows) })
onUnmounted(() => {
  disposed = true
  observer?.disconnect()
  document.removeEventListener('visibilitychange', observeRows)
})
</script>
