<template>
  <div class="inline-flex max-w-full flex-wrap items-center gap-x-3 gap-y-2 text-xs" data-testid="site-price-summary">
    <span v-for="tier in tiers" :key="tier.key" :title="details(tier)" class="inline-flex min-w-0 flex-wrap items-center gap-1 [overflow-wrap:anywhere]"
      :class="tier.status === 'ready' ? 'text-green-700 dark:text-green-400' : 'text-amber-700 dark:text-amber-400'">
      <span class="font-medium">{{ tier.key === 'default' ? t('admin.sites.defaultTier') : tier.key }}</span>
      <span>{{ amount(tier.prices) }}{{ tier.unit === 'USD/second' ? '/s' : '' }}</span>
      <span>{{ tier.status === 'ready' ? '✓' : '×' }}</span>
      <span class="sr-only">{{ t(`admin.sites.status.${tier.status}`) }}</span>
    </span>
    <span v-if="!tiers.length" class="text-amber-700">{{ t('admin.sites.status.unknown') }}</span>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { SiteSchedulingTier } from '@/api/admin/upstreamSites'
import { sitePriceComponent } from './sitePriceFormat'
defineProps<{ tiers: SiteSchedulingTier[] }>()
const { t } = useI18n()
const amount = (prices?: Record<string, number>) => {
  const entries = Object.entries(prices || {})
  if (!entries.length) return '—'
  if (entries.length === 1 && ['request', 'second'].includes(entries[0]![0])) return `$${Number(entries[0]![1].toPrecision(6))}`
  return t('admin.sites.tokenPricing')
}
const components = (prices: Record<string, number> | undefined, unit: string) => Object.entries(prices || {})
  .map(([key, value]) => `${t(`admin.sites.components.${sitePriceComponent(key, unit)}`)} $${Number(value.toPrecision(8))}`).join(' / ') || '—'
const details = (tier: SiteSchedulingTier) => `${t('admin.sites.price')}: ${components(tier.prices, tier.unit)} ${tier.unit}\n${t('admin.sites.selling')}: ${components(tier.selling, tier.unit)}\n${t('admin.sites.limit')}: ${components(tier.ceiling, tier.unit)}\n${t(`admin.sites.status.${tier.status}`)}${tier.reason ? ` · ${tier.reason}` : ''}`
</script>
