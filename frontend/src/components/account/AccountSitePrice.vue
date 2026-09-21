<template>
  <RouterLink v-if="policy" :to="`/admin/upstream-sites?site=${policy.site_id}`"
    class="mt-1 inline-flex items-center gap-2 whitespace-nowrap text-xs" :title="t('admin.sites.managed')">
    <span class="text-primary-700 dark:text-primary-300">{{ t('admin.sites.managed') }}</span>
    <SitePriceSummary :tiers="scheduling?.tiers || []" />
  </RouterLink>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SiteAccountPolicy, SiteAccountScheduling } from '@/api/admin/upstreamSites'
import SitePriceSummary from '@/components/admin/sites/SitePriceSummary.vue'
const props = defineProps<{ value: unknown; scheduling?: SiteAccountScheduling }>()
const { t } = useI18n()
const policy = computed(() => props.value && typeof props.value === 'object' ? props.value as SiteAccountPolicy : null)
</script>
