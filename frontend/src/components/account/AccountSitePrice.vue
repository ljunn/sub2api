<template>
  <RouterLink
    v-if="policy"
    :to="`/admin/upstream-sites?site=${policy.site_id}`"
    class="mt-1 flex max-w-sm flex-wrap gap-1 text-xs"
    :title="t('admin.sites.managed')"
  >
    <span
      class="rounded bg-primary-50 px-1.5 py-0.5 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
      >{{ t('admin.sites.managed') }}</span
    >
    <span
      v-for="tier in policy.tiers"
      :key="tier.key"
      class="rounded px-1.5 py-0.5"
      :class="
        !policy.local_group_id && siteTierStatus(policy, tier) === 'ready'
          ? 'bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-300'
          : 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-300'
      "
    >
      {{ tier.key === 'default' ? '' : tier.key }}
      {{
        Object.entries(tier.prices)
          .map(
            ([key, value]) => `${t(`admin.sites.components.${key}`)} $${value}`,
          )
          .join(' / ') || t('admin.sites.unknown')
      }}
      · {{ tier.unit }} · {{ policy.local_group_id ? t('admin.sites.automatic') : t(`admin.sites.status.${siteTierStatus(policy, tier)}`) }}
    </span>
    <span v-if="!policy.tiers.length">{{ t('admin.sites.unknown') }}</span>
  </RouterLink>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  siteTierStatus,
  type SiteAccountPolicy,
} from '@/api/admin/upstreamSites'
const props = defineProps<{ value: unknown }>()
const { t } = useI18n()
const policy = computed(() =>
  props.value && typeof props.value === 'object'
    ? (props.value as SiteAccountPolicy)
    : null,
)
</script>
