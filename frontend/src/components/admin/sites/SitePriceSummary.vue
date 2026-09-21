<template>
  <div class="inline-flex items-center gap-3 whitespace-nowrap text-xs" data-testid="site-price-summary">
    <span v-for="tier in tiers" :key="tier.key" :title="details(tier)" class="inline-flex items-center gap-1"
      :class="tier.status === 'ready' ? 'text-green-700 dark:text-green-400' : 'text-amber-700 dark:text-amber-400'">
      <span class="font-medium">{{ tier.key === 'default' ? t('admin.sites.defaultTier') : tier.key }}</span>
      <span>{{ amount(tier.prices) }}</span>
      <span>{{ tier.status === 'ready' ? '✓' : '×' }}</span>
      <span class="sr-only">{{ t(`admin.sites.status.${tier.status}`) }}</span>
    </span>
    <span v-if="!tiers.length" class="text-amber-700">{{ t('admin.sites.status.unknown') }}</span>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { SiteSchedulingTier } from '@/api/admin/upstreamSites'
defineProps<{ tiers: SiteSchedulingTier[] }>()
const { t } = useI18n()
const amount = (prices?: Record<string, number>) => {
  const entries = Object.entries(prices || {})
  if (!entries.length) return '—'
  if (entries.length === 1 && entries[0]![0] === 'request') return `$${Number(entries[0]![1].toPrecision(6))}`
  return t('admin.sites.tokenPricing')
}
const components = (prices?: Record<string, number>) => Object.entries(prices || {})
  .map(([key, value]) => `${t(`admin.sites.components.${key}`)} $${Number(value.toPrecision(8))}`).join(' / ') || '—'
const details = (tier: SiteSchedulingTier) => `${t('admin.sites.price')}: ${components(tier.prices)} ${tier.unit}\n${t('admin.sites.selling')}: ${components(tier.selling)}\n${t('admin.sites.limit')}: ${components(tier.ceiling)}\n${t(`admin.sites.status.${tier.status}`)}${tier.reason ? ` · ${tier.reason}` : ''}`
</script>
