<template>
  <section class="space-y-3" data-testid="site-overview">
    <div class="grid grid-cols-2 gap-2 lg:grid-cols-4">
      <button v-for="item in filters" :key="item.key" class="rounded-lg border px-4 py-3 text-left"
        :class="filter === item.key ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20' : 'border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800'"
        :data-filter="item.key" :aria-pressed="filter === item.key" @click="filter = item.key">
        <span class="text-xs text-gray-500">{{ t(`admin.sites.overview.${item.key}`) }}</span>
        <strong class="ml-3 text-lg" :class="item.key === 'low' && item.count ? 'text-red-600' : ''">{{ item.count }}</strong>
      </button>
    </div>
    <input v-model="search" class="input" :placeholder="t('admin.sites.overview.search')" :aria-label="t('admin.sites.overview.search')" />
    <div class="max-h-[65vh] overflow-auto rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
      <table class="w-full text-left text-sm">
        <thead class="sticky top-0 z-10 bg-gray-50 text-xs text-gray-500 dark:bg-dark-900"><tr>
          <th class="px-4 py-2">{{ t('admin.sites.name') }}</th>
          <th class="px-4 py-2">{{ t('admin.sites.overview.updates') }}</th>
          <th class="px-4 py-2">{{ t('admin.siteBalance.balance') }}</th>
          <th class="px-4 py-2">{{ t('admin.sites.overview.connection') }}</th>
          <th class="px-4 py-2 text-right">{{ t('admin.sites.bindings') }}</th>
        </tr></thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
          <tr v-for="site in filteredSites" :key="site.id" :data-site="site.id"
            tabindex="0" :aria-current="selectedId === site.id ? 'true' : undefined"
            class="cursor-pointer outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary-500"
            :class="selectedId === site.id ? 'bg-primary-50/70 dark:bg-primary-900/10' : 'hover:bg-gray-50 dark:hover:bg-dark-700/50'"
            @click="$emit('select', site.id)"
            @keydown.enter.self.prevent="$emit('select', site.id)"
            @keydown.space.self.prevent="$emit('select', site.id)">
            <td class="px-4 py-2">
              <button type="button" class="block max-w-56 truncate font-semibold text-primary-700 dark:text-primary-400" :title="site.name" @click.stop="$emit('select', site.id)">{{ site.name }}</button>
              <div class="mt-0.5 max-w-56 truncate text-xs text-gray-500" :title="site.base_url">{{ site.kind === 'kongfang' ? t('admin.sites.kongfang') : site.kind === 'newapi' ? 'New API' : 'Sub2API' }} · {{ site.base_url }}</div>
              <a :href="site.base_url" target="_blank" rel="noopener noreferrer"
                class="mt-1 inline-flex items-center gap-1 text-xs text-primary-600 hover:underline"
                :aria-label="t('admin.sites.overview.openSiteLabel', { name: site.name })"
                data-testid="open-upstream-site" @click.stop>
                {{ t('admin.sites.overview.openSite') }} <span aria-hidden="true">↗</span>
              </a>
            </td>
            <td class="px-4 py-2 whitespace-nowrap">
              <button v-if="unreadCount(site)" type="button" class="rounded-full bg-blue-50 px-2 py-1 text-xs font-medium text-blue-700 dark:bg-blue-900/30 dark:text-blue-300" @click.stop="$emit('models', site.id)">{{ t('admin.sites.overview.newCount', { count: unreadCount(site) }) }}</button>
              <span v-else class="text-xs text-gray-400">{{ t('admin.sites.overview.noNew') }}</span>
            </td>
            <td class="px-4 py-2 whitespace-nowrap">
              <button type="button" class="text-left" :title="site.balance?.error || site.balance?.last_success" @click.stop="$emit('select', site.id)">
                <span class="font-medium" :class="isLow(site) ? 'text-red-600' : ''">{{ amount(site) }} USD</span>
                <span v-if="isLow(site)" class="ml-2 text-xs text-red-600">{{ t('admin.siteBalance.low') }}</span>
                <span v-if="site.balance?.conversion_error" class="ml-2 text-xs text-amber-600">{{ t('admin.siteBalance.conversionNeeded') }}</span>
                <span v-else-if="!isFresh(site)" class="ml-2 text-xs text-amber-600">{{ t('admin.sites.overview.balancePending') }}</span>
              </button>
            </td>
            <td class="px-4 py-2 text-xs" :title="site.error || site.balance?.error">
              <span :class="hasError(site) ? 'text-amber-700' : 'text-gray-500'">{{ t(`admin.sites.status.${!site.enabled ? 'disabled' : hasError(site) ? 'error' : site.status}`) }}</span>
            </td>
            <td class="px-4 py-2 text-right text-gray-500">{{ site.bindings.length }}</td>
          </tr>
          <tr v-if="!filteredSites.length"><td colspan="5" class="p-5 text-center text-sm text-gray-500">{{ t('admin.sites.overview.noResults') }}</td></tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UpstreamSite } from '@/api/admin/upstreamSites'
const props = defineProps<{ sites: UpstreamSite[]; selectedId: string; threshold: number; now: number }>()
defineEmits<{ select: [id: string]; models: [id: string] }>()
const { t } = useI18n()
const filter = ref('all')
const search = ref('')
const unreadCount = (site: UpstreamSite) => site.models.filter(model => model.unread).length
const isLow = (site: UpstreamSite) => site.enabled && typeof site.balance?.amount_usd === 'number' && site.balance.amount_usd < props.threshold
const isFresh = (site: UpstreamSite) => !!site.balance?.last_success && !site.balance.error && props.now - Date.parse(site.balance.last_success) < 600000
const hasError = (site: UpstreamSite) => site.enabled && (site.status === 'error' || !!site.balance?.error)
const amount = (site: UpstreamSite) => typeof site.balance?.amount_usd === 'number' ? site.balance.amount_usd.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '—'
const filters = computed(() => [
  { key: 'all', count: props.sites.length },
  { key: 'new', count: props.sites.filter(site => unreadCount(site) > 0).length },
  { key: 'low', count: props.sites.filter(isLow).length },
  { key: 'error', count: props.sites.filter(hasError).length },
])
const filteredSites = computed(() => props.sites.filter(site => {
  if (!`${site.name} ${site.base_url}`.toLowerCase().includes(search.value.trim().toLowerCase())) return false
  return filter.value === 'all' || (filter.value === 'new' && unreadCount(site) > 0) || (filter.value === 'low' && isLow(site)) || (filter.value === 'error' && hasError(site))
}))
</script>
