<template>
  <div class="mt-3 min-w-0 [overflow-wrap:anywhere] rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-900/40" data-testid="site-balance">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
      <span class="text-gray-500">{{ t('admin.siteBalance.balance') }}</span>
      <strong class="text-base" :class="fresh && low ? 'text-red-600' : ''" data-testid="balance-amount">{{ amount }} USD</strong>
      <span :class="fresh && low ? 'text-red-600' : 'text-gray-500'">{{ t(`admin.siteBalance.${site.balance?.conversion_error ? 'conversionNeeded' : fresh ? (low ? 'low' : 'normal') : 'stale'}`) }}</span>
      <span class="text-gray-500">{{ t('admin.siteBalance.lastCheck') }} {{ date(site.balance?.last_success) }}</span>
      <button class="ml-auto min-h-11 shrink-0 text-primary-600 sm:min-h-0 disabled:opacity-50" type="button" :disabled="disabled || querying" @click="refresh">{{ t(querying ? 'common.loading' : 'admin.siteBalance.query') }}</button>
    </div>
    <p v-if="site.balance?.units_per_usd" class="mt-1 text-xs text-gray-500">{{ t('admin.siteBalance.conversion') }} 1 USD = {{ site.balance.units_per_usd }} {{ site.balance.currency }}</p>
    <p v-if="site.balance?.conversion_error" class="mt-1 text-xs text-amber-700" role="alert">{{ site.balance.conversion_error }}</p>
    <p v-if="site.balance?.last_notified" class="mt-1 text-xs text-gray-500">{{ t('admin.siteBalance.lastNotified') }} {{ date(site.balance.last_notified) }}</p>
    <p v-if="site.balance?.error || queryError" class="mt-2 text-sm text-amber-700" role="alert">{{ site.balance?.error || queryError }}</p>
    <p v-if="site.balance?.notify_error" class="mt-2 text-sm text-amber-700" role="alert">{{ site.balance.notify_error }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { upstreamSiteBalanceApi, type SiteBalanceSettings } from '@/api/admin/upstreamSiteBalance'
import type { UpstreamSite } from '@/api/admin/upstreamSites'

const props = defineProps<{ site: UpstreamSite; settings: SiteBalanceSettings | null; disabled?: boolean }>()
const emit = defineEmits<{ updated: [site: UpstreamSite]; busy: [value: boolean] }>()
const { t } = useI18n()
const querying = ref(false)
const queryError = ref('')
const now = ref(Date.now())
const timer = setInterval(() => { now.value = Date.now() }, 30000)
onUnmounted(() => clearInterval(timer))
const known = computed(() => typeof props.site.balance?.amount_usd === 'number' && Number.isFinite(props.site.balance.amount_usd))
const amount = computed(() => known.value ? props.site.balance!.amount_usd!.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '—')
const fresh = computed(() => known.value && !props.site.balance?.error && now.value - Date.parse(props.site.balance?.last_success || '') < 600000)
const low = computed(() => known.value && props.site.balance!.amount_usd! < (props.settings?.threshold ?? 20))
const date = (value?: string) => value ? new Date(value).toLocaleString() : '—'

async function refresh() {
  querying.value = true
  queryError.value = ''
  emit('busy', true)
  try {
    const site = await upstreamSiteBalanceApi.refresh(props.site.id)
    now.value = Date.now()
    emit('updated', site)
  } catch {
    queryError.value = t('admin.siteBalance.queryFailed')
  } finally {
    querying.value = false
    emit('busy', false)
  }
}
</script>
