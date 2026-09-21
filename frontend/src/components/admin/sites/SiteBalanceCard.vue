<template>
  <div class="mt-4 rounded-lg border border-gray-200 p-4 dark:border-dark-700" data-testid="site-balance">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <p class="text-sm text-gray-500">{{ t('admin.siteBalance.balance') }}</p>
        <p class="mt-1 text-xl font-semibold" :class="fresh && low ? 'text-red-600' : ''" data-testid="balance-amount">
          {{ amount }} <span class="text-sm">{{ site.balance?.currency }}</span>
        </p>
        <p class="mt-1 text-xs" :class="fresh && low ? 'text-red-600' : 'text-gray-500'">
          {{ t(`admin.siteBalance.${fresh ? (low ? 'low' : 'normal') : 'stale'}`) }}
        </p>
      </div>
      <button class="btn btn-secondary" type="button" :disabled="disabled || querying" @click="refresh">
        {{ t(querying ? 'common.loading' : 'admin.siteBalance.query') }}
      </button>
    </div>
    <p class="mt-3 text-xs text-gray-500">{{ t('admin.siteBalance.lastCheck') }} {{ date(site.balance?.last_success) }}</p>
    <p v-if="site.balance?.last_notified" class="mt-1 text-xs text-gray-500">
      {{ t('admin.siteBalance.lastNotified') }} {{ date(site.balance.last_notified) }}
    </p>
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
const known = computed(() => typeof props.site.balance?.amount === 'number' && Number.isFinite(props.site.balance.amount))
const amount = computed(() => known.value ? props.site.balance!.amount!.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '—')
const fresh = computed(() => known.value && !props.site.balance?.error && now.value - Date.parse(props.site.balance?.last_success || '') < 600000)
const low = computed(() => known.value && props.site.balance!.amount! < (props.settings?.threshold ?? 20))
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
