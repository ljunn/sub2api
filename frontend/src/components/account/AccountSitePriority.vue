<template>
  <details v-if="scores.length" class="max-w-72 text-xs text-gray-700 dark:text-gray-300">
    <summary class="cursor-pointer space-y-1" :aria-label="t('admin.sites.priority.details')">
      <span v-for="tier in scores" :key="tier.key" class="block whitespace-nowrap">
        {{ tier.key === 'default' ? t('admin.sites.priority.automatic') : tier.key }} · {{ tier.priority_score!.priority }}
      </span>
    </summary>
    <div class="mt-2 space-y-3 rounded-lg bg-gray-50 p-3 dark:bg-dark-800">
      <p>{{ t('admin.sites.priority.hint') }}</p>
      <div v-for="tier in scores" :key="tier.key" class="space-y-1">
        <p class="font-medium">{{ tier.key }} · {{ t('admin.sites.priority.score', { score: tier.priority_score!.score.toFixed(2) }) }}</p>
        <p>{{ t('admin.sites.priority.success', { rate: (tier.priority_score!.success_rate * 100).toFixed(1), successes: tier.priority_score!.successes, samples: tier.priority_score!.samples }) }}</p>
        <p>{{ t('admin.sites.priority.speed', { seconds: tier.priority_score!.speed_seconds.toFixed(2) }) }}<span v-if="tier.priority_score!.speed_estimated"> · {{ t('admin.sites.priority.estimated') }}</span></p>
        <p>{{ t('admin.sites.priority.cost', { ratio: (tier.priority_score!.cost_ratio * 100).toFixed(1) }) }}</p>
      </div>
      <p>{{ t('admin.sites.priority.window') }}</p>
    </div>
  </details>
  <span v-else class="text-sm text-gray-700 dark:text-gray-300">{{ fallback }}</span>
  <RouterLink v-if="supportTiers.length && scheduling?.site_id" class="mt-1 block text-xs text-primary-600"
    :to="{ path: '/admin/upstream-sites', query: { site: scheduling.site_id, binding: scheduling.binding_id } }">
    <span v-for="tier in supportTiers" :key="tier.key" class="block">
      {{ tier.key }} · {{ t('admin.sites.support.target', { percent: tier.traffic_support!.target }) }} · {{ t('admin.sites.support.actual', { percent: tier.traffic_support!.actual.toFixed(1) }) }}
      <span class="block text-gray-500">{{ t(`admin.sites.support.states.${tier.traffic_support!.state}`) }} · {{ t('admin.sites.support.effective', { percent: tier.traffic_support!.effective }) }}</span>
    </span>
  </RouterLink>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SiteAccountScheduling } from '@/api/admin/upstreamSites'

const props = defineProps<{ scheduling?: SiteAccountScheduling; fallback: number }>()
const { t } = useI18n()
const supportTiers = computed(() => props.scheduling?.tiers.filter(tier => tier.traffic_support) || [])
const scores = computed(() => props.scheduling?.tiers.filter(tier => tier.priority_score?.ready) || [])
</script>
